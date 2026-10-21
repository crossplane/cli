/*
Copyright 2026 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package project

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/kong"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/sergi/go-diff/diffmatchpatch"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	un "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	xperrors "github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"

	"github.com/crossplane/cli/v2/cmd/crossplane/common"
	dp "github.com/crossplane/cli/v2/internal/diff/diffprocessor"
	"github.com/crossplane/cli/v2/internal/diff/renderer"
	dt "github.com/crossplane/cli/v2/internal/diff/result"
	"github.com/crossplane/cli/v2/internal/diff/testutils"
	"github.com/crossplane/cli/v2/internal/plan"
)

// parseSimulate parses args as `simulate` flags, running kong's validation.
func parseSimulate(t *testing.T, args ...string) (*simulateCmd, error) {
	t.Helper()

	var cli struct {
		Simulate simulateCmd `cmd:""`
	}
	parser, err := kong.New(&cli, kong.Vars{"project_file": "crossplane-project.yaml"}, kong.Exit(func(int) {}), kong.Bind(&common.ExitStatus{}))
	if err != nil {
		t.Fatalf("kong.New(...): %v", err)
	}
	_, err = parser.Parse(append([]string{"simulate"}, args...))
	return &cli.Simulate, err
}

func TestSimulateFlags(t *testing.T) {
	dir := t.TempDir()
	creds := filepath.Join(dir, "creds.yaml")
	noSecrets := filepath.Join(dir, "none.yaml")
	if err := os.WriteFile(creds, []byte("apiVersion: v1\nkind: Secret\nmetadata:\n  name: creds\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(noSecrets, []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cm\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	type want struct {
		cmd *simulateCmd
		err bool
	}

	cases := map[string]struct {
		reason string
		args   []string
		want   want
	}{
		"Defaults": {
			reason: "With no flags the defaults should apply.",
			want: want{cmd: &simulateCmd{
				Input:          []string{"crossplane-project.yaml"},
				MaxConcurrency: 8,
				MaxIterations:  dp.DefaultMaxRenderIterations,
				Timeout:        time.Minute,
				Output:         "plan",
				MaxNestedDepth: 10,
				DryRunOn:       "all",
			}},
		},
		"AllFlags": {
			reason: "Every flag should land on its field.",
			args: []string{
				"-f", "p.yaml", "-c", "staging", "-n", "team-a", "--name", "db",
				"--include-pinned", "--max-iterations", "3", "--timeout", "2m",
				"--max-concurrency", "2", "-o", "json", "--output-version", "v2", "--crossplane-version", "v2.3.4",
				"--as", "jane", "--as-group", "admins",
			},
			want: want{cmd: &simulateCmd{
				Input:             []string{"p.yaml"},
				Context:           "staging",
				Namespace:         "team-a",
				Name:              "db",
				IncludePinned:     true,
				MaxIterations:     3,
				Timeout:           2 * time.Minute,
				MaxConcurrency:    2,
				Output:            "json",
				OutputVersion:     "v2",
				CrossplaneVersion: "v2.3.4",
				MaxNestedDepth:    10,
				DryRunOn:          "all",
			}},
		},
		"ParityFlags": {
			reason: "The crossplane-diff parity flags should land on their fields.",
			args: []string{
				"--no-color", "--compact", "--max-nested-depth", "3", "--ignore-paths", "a.b", "--ignore-paths", "c.d",
				"--function-registry-override", "reg.example.com", "--eventual-state", "--max-recv-message-size", "16",
				"--minimize-composition", "--analyze-on", "always", "--dry-run-on", "existing",
			},
			want: want{cmd: &simulateCmd{
				Input:                    []string{"crossplane-project.yaml"},
				MaxConcurrency:           8,
				MaxIterations:            dp.DefaultMaxRenderIterations,
				Timeout:                  time.Minute,
				Output:                   "plan",
				NoColor:                  true,
				Compact:                  true,
				MaxNestedDepth:           3,
				IgnorePaths:              []string{"a.b", "c.d"},
				FunctionRegistryOverride: "reg.example.com",
				EventualState:            true,
				MaxRecvMessageSize:       16,
				MinimizeComposition:      true,
				AnalyzeOn:                "always",
				DryRunOn:                 "existing",
			}},
		},
		"FunctionCredentials": {
			reason: "--function-credentials should load the Secrets in the file at parse time.",
			args:   []string{"--function-credentials", creds},
			want: want{cmd: &simulateCmd{
				Input:          []string{"crossplane-project.yaml"},
				MaxConcurrency: 8,
				MaxIterations:  dp.DefaultMaxRenderIterations,
				Timeout:        time.Minute,
				Output:         "plan",
				MaxNestedDepth: 10,
				DryRunOn:       "all",
				FunctionCredentials: FunctionCredentials{Path: creds, Secrets: []corev1.Secret{{
					TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
					ObjectMeta: metav1.ObjectMeta{Name: "creds"},
				}}},
			}},
		},
		"FunctionCredentialsMissing": {
			reason: "A missing credentials path should be rejected at parse time.",
			args:   []string{"--function-credentials", filepath.Join(t.TempDir(), "nope.yaml")},
			want:   want{err: true},
		},
		"FunctionCredentialsNoSecrets": {
			reason: "A credentials file with no Secrets should be rejected.",
			args:   []string{"--function-credentials", noSecrets},
			want:   want{err: true},
		},
		"UnknownDryRunOn": {
			reason: "Unknown --dry-run-on values should be rejected.",
			args:   []string{"--dry-run-on", "none"},
			want:   want{err: true},
		},
		"UnknownAnalyzeOn": {
			reason: "Unknown --analyze-on values should be rejected.",
			args:   []string{"--analyze-on", "never"},
			want:   want{err: true},
		},
		"RepeatedInput": {
			reason: "-f should be repeatable, and a comma should not split a path.",
			args:   []string{"-f", "a.yaml", "-f", "b,c.yaml"},
			want: want{cmd: &simulateCmd{
				Input:          []string{"a.yaml", "b,c.yaml"},
				MaxConcurrency: 8,
				MaxIterations:  dp.DefaultMaxRenderIterations,
				Timeout:        time.Minute,
				Output:         "plan",
				MaxNestedDepth: 10,
				DryRunOn:       "all",
			}},
		},
		"UnknownOutputVersion": {
			reason: "Structured output schema versions other than v1 and v2 should be rejected.",
			args:   []string{"--output-version", "v3"},
			want:   want{err: true},
		},
		"UnknownOutput": {
			reason: "Output formats other than plan, diff, json and yaml should be rejected.",
			args:   []string{"-o", "xml"},
			want:   want{err: true},
		},
		"OutputVersionWithStructuredOutput": {
			reason: "--output-version should be accepted with -o yaml.",
			args:   []string{"-o", "yaml", "--output-version", "v1"},
			want: want{cmd: &simulateCmd{
				Input:          []string{"crossplane-project.yaml"},
				MaxConcurrency: 8,
				MaxIterations:  dp.DefaultMaxRenderIterations,
				Timeout:        time.Minute,
				Output:         "yaml",
				OutputVersion:  "v1",
				MaxNestedDepth: 10,
				DryRunOn:       "all",
			}},
		},
		"OutputVersionWithDefaultOutput": {
			reason: "--output-version should be rejected with the default -o plan, which has no structured schema.",
			args:   []string{"--output-version", "v1"},
			want:   want{err: true},
		},
		"OutputVersionWithPlan": {
			reason: "--output-version should be rejected with -o plan.",
			args:   []string{"-o", "plan", "--output-version", "v2"},
			want:   want{err: true},
		},
		"OutputVersionWithDiff": {
			reason: "--output-version should be rejected with -o diff.",
			args:   []string{"-o", "diff", "--output-version", "v1"},
			want:   want{err: true},
		},
		"ZeroIterations": {
			reason: "A render needs at least one iteration.",
			args:   []string{"--max-iterations", "0"},
			want:   want{err: true},
		},
		"RenderVersionBelowFloor": {
			reason: "A pinned render version below the floor should be rejected.",
			args:   []string{"--crossplane-version", "v2.2.0"},
			want:   want{err: true},
		},
		"RenderImageBelowFloor": {
			reason: "A render image tagged below the floor should be rejected.",
			args:   []string{"--crossplane-image", "example.com/crossplane:v2.2.0"},
			want:   want{err: true},
		},
		"RenderImageUncomparable": {
			reason: "A render image with no comparable version should be accepted.",
			args:   []string{"--crossplane-image", "example.com/crossplane:stable"},
			want: want{cmd: &simulateCmd{
				Input:           []string{"crossplane-project.yaml"},
				MaxConcurrency:  8,
				MaxIterations:   dp.DefaultMaxRenderIterations,
				Timeout:         time.Minute,
				Output:          "plan",
				MaxNestedDepth:  10,
				DryRunOn:        "all",
				CrossplaneImage: "example.com/crossplane:stable",
			}},
		},
		"RenderBackendsExclusive": {
			reason: "--crossplane-version and --crossplane-image should be mutually exclusive.",
			args:   []string{"--crossplane-version", "v2.3.4", "--crossplane-image", "example.com/crossplane:v2.3.4"},
			want:   want{err: true},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := parseSimulate(t, tc.args...)
			if gotErr := err != nil; gotErr != tc.want.err {
				t.Fatalf("\n%s\nParse(%v): want error %t, got %v", tc.reason, tc.args, tc.want.err, err)
			}
			if tc.want.err {
				return
			}
			if diff := cmp.Diff(tc.want.cmd, got, cmpopts.IgnoreFields(simulateCmd{}, "Impersonation")); diff != "" {
				t.Errorf("\n%s\nParse(%v): -want, +got:\n%s", tc.reason, tc.args, diff)
			}
		})
	}
}

func TestSimulateScope(t *testing.T) {
	type want struct {
		namespace string
		refs      []k8stypes.NamespacedName
	}

	cases := map[string]struct {
		reason string
		cmd    simulateCmd
		want   want
	}{
		"All": {
			reason: "No scope flags should simulate every namespace.",
		},
		"Namespace": {
			reason: "--namespace alone should scope discovery to the namespace.",
			cmd:    simulateCmd{Namespace: "team-a"},
			want:   want{namespace: "team-a"},
		},
		"ClusterScopedName": {
			reason: "--name alone should name a cluster-scoped composite resource.",
			cmd:    simulateCmd{Name: "db"},
			want:   want{refs: []k8stypes.NamespacedName{{Name: "db"}}},
		},
		"NamespacedName": {
			reason: "--name with --namespace should name a namespaced composite resource, not scope discovery.",
			cmd:    simulateCmd{Namespace: "team-a", Name: "db"},
			want:   want{refs: []k8stypes.NamespacedName{{Namespace: "team-a", Name: "db"}}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ns, refs := tc.cmd.scope()
			if diff := cmp.Diff(tc.want.namespace, ns); diff != "" {
				t.Errorf("\n%s\nscope(): -want namespace, +got:\n%s", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.refs, refs); diff != "" {
				t.Errorf("\n%s\nscope(): -want refs, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestSimulateProcessorOptions(t *testing.T) {
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	k := &kong.Context{Kong: &kong.Kong{Stdout: stdout, Stderr: stderr}}
	c := &simulateCmd{
		IncludePinned:     true,
		MaxIterations:     7,
		CrossplaneVersion: "v2.3.4",

		MaxNestedDepth:           4,
		IgnorePaths:              []string{"a.b"},
		FunctionCredentials:      FunctionCredentials{Secrets: []corev1.Secret{{ObjectMeta: metav1.ObjectMeta{Name: "creds"}}}},
		FunctionRegistryOverride: "reg.example.com",
		EventualState:            true,
		MaxRecvMessageSize:       16,
		AnalyzeOn:                "spec-change",
		DryRunOn:                 "existing",
	}

	cfg := dp.ProcessorConfig{}
	for _, o := range c.processorOptions(k, logging.NewNopLogger(), []pkgv1.Function{{ObjectMeta: metav1.ObjectMeta{Name: "fn"}}}) {
		o(&cfg)
	}

	type got struct {
		IncludeManual       bool
		MaxRenderIterations int
		CrossplaneVersion   string
		CrossplaneImage     string
		HasAdvisories       bool
		HasFunctionProvider bool
		MaxNestedDepth      int
		IgnorePaths         []string
		Credentials         []corev1.Secret
		RegistryOverride    string
		EventualState       bool
		MaxRecvMessageSize  int
		AnalyzeOn           dp.AnalyzeOn
		DryRunOn            dp.DryRunOn
	}
	want := got{
		IncludeManual:       true,
		MaxRenderIterations: 7,
		CrossplaneVersion:   "v2.3.4",
		HasAdvisories:       true,
		HasFunctionProvider: true,
		MaxNestedDepth:      4,
		IgnorePaths:         []string{"a.b"},
		Credentials:         c.FunctionCredentials.Secrets,
		RegistryOverride:    "reg.example.com",
		EventualState:       true,
		MaxRecvMessageSize:  16,
		AnalyzeOn:           dp.AnalyzeOnSpecChange,
		DryRunOn:            dp.DryRunOnExisting,
	}
	g := got{
		IncludeManual:       cfg.IncludeManual,
		MaxRenderIterations: cfg.MaxRenderIterations,
		CrossplaneVersion:   cfg.CrossplaneVersion,
		CrossplaneImage:     cfg.CrossplaneImage,
		HasAdvisories:       cfg.Advisories != nil,
		HasFunctionProvider: cfg.Factories.FunctionProvider != nil,
		MaxNestedDepth:      cfg.MaxNestedDepth,
		IgnorePaths:         cfg.IgnorePaths,
		Credentials:         cfg.FunctionCredentials,
		RegistryOverride:    cfg.FunctionRegistryOverride,
		EventualState:       cfg.EventualState,
		MaxRecvMessageSize:  cfg.MaxRecvMessageSize,
		AnalyzeOn:           cfg.AnalyzeOn,
		DryRunOn:            cfg.DryRunOn,
	}
	if diff := cmp.Diff(want, g); diff != "" {
		t.Errorf("processorOptions(...): -want, +got:\n%s", diff)
	}

	// Without project functions every function comes from the cluster, so
	// there is nothing to overlay.
	cfg = dp.ProcessorConfig{}
	for _, o := range c.processorOptions(k, logging.NewNopLogger(), nil) {
		o(&cfg)
	}
	if cfg.Factories.FunctionProvider != nil {
		t.Errorf("processorOptions(..., nil): want no function provider overlay, got one")
	}

	// The test hooks render with a local binary.
	c.Hooks = &SimulateHooks{RenderBinary: "/bin/crossplane"}
	c.CrossplaneVersion = ""
	cfg = dp.ProcessorConfig{}
	for _, o := range c.processorOptions(k, logging.NewNopLogger(), nil) {
		o(&cfg)
	}
	if cfg.CrossplaneRenderBinary != "/bin/crossplane" {
		t.Errorf("processorOptions(...) with hooks: want render binary %q, got %q", "/bin/crossplane", cfg.CrossplaneRenderBinary)
	}
}

func TestSimulateDiffOptions(t *testing.T) {
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	k := &kong.Context{Kong: &kong.Kong{Stdout: stdout, Stderr: stderr}}

	cases := map[string]struct {
		reason string
		cmd    simulateCmd
		want   func(o *renderer.DiffOptions)
	}{
		"UnsetFlagsSelectHumanDiff": {
			reason: "Unset flags should select the human-readable diff and the renderer's default output version, uncolorized off a terminal.",
			want: func(o *renderer.DiffOptions) {
				o.UseColors = false
			},
		},
		"StructuredFlagsCarryThrough": {
			reason: "The format, version and compact flags should reach the renderer.",
			cmd:    simulateCmd{Output: "json", OutputVersion: "v2", NoColor: true, Compact: true},
			want: func(o *renderer.DiffOptions) {
				o.UseColors = false
				o.Compact = true
				o.Format = renderer.OutputFormatJSON
				o.OutputVersion = renderer.OutputVersionV2
			},
		},
		"UnknownFormatFallsBackToHumanDiff": {
			reason: "An unrecognized format should fall back to the human-readable diff.",
			cmd:    simulateCmd{Output: "xml"},
			want: func(o *renderer.DiffOptions) {
				o.UseColors = false
			},
		},
		"HooksColorizeAsTerminal": {
			reason: "The test hooks should colorize as for a terminal.",
			cmd:    simulateCmd{Hooks: &SimulateHooks{Terminal: true}},
			want:   func(*renderer.DiffOptions) {},
		},
		"NoColorWinsOverHooks": {
			reason: "--no-color should disable colors even under the test hooks.",
			cmd:    simulateCmd{NoColor: true, Hooks: &SimulateHooks{Terminal: true}},
			want: func(o *renderer.DiffOptions) {
				o.UseColors = false
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			want := renderer.DefaultDiffOptions()
			tc.want(&want)

			got := tc.cmd.diffOptions(k)
			// Writers are interfaces holding the kong context's streams; compare them by identity.
			if got.Stdout != stdout || got.Stderr != stderr {
				t.Errorf("\n%s\ndiffOptions(...) writers: got (%v, %v), want the kong context's", tc.reason, got.Stdout, got.Stderr)
			}

			got.Stdout, got.Stderr, want.Stdout, want.Stderr = nil, nil, nil, nil
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("\n%s\ndiffOptions(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

// TestSimulateRenderReports pins which renderer each -o value selects, for
// both report types: plan.Result for plan, json and yaml, the engine's human
// diff for diff, and its v1/v2 schema only under --output-version.
func TestSimulateRenderReports(t *testing.T) {
	xr := corev1.ObjectReference{APIVersion: "example.org/v1", Kind: "XNetwork", Name: "prod"}
	cm := &un.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "cm"}, "data": map[string]any{"k": "v"}}}
	added := &dt.ResourceDiff{
		Gvk: cm.GroupVersionKind(), ResourceName: "cm", DiffType: dt.DiffTypeAdded,
		Desired:   dt.ResourceViews{Raw: cm, Clean: cm},
		LineDiffs: []diffmatchpatch.Diff{{Type: diffmatchpatch.DiffInsert, Text: "data:\n  k: v\n"}},
	}
	xrReport := &dt.XRReport{Groups: []dt.XRDiffGroup{{XR: xr, Diffs: map[string]*dt.ResourceDiff{added.GetDiffKey(): added}}}}
	compReport := &dt.CompDiffOutput{Compositions: []dt.CompositionDiff{{
		Name:           "net",
		RevisionImpact: dt.RevisionImpact{ChangeScope: "spec", CreatesRevision: true, RepointedComposites: 1},
		ImpactAnalysis: []dt.XRImpact{{ObjectReference: xr, Status: dt.XRStatusUnchanged}},
	}}}

	// isPlanResult checks out decodes as a plan.Result naming the composite.
	isPlanResult := func(unmarshal func([]byte, any) error) func(*testing.T, string) {
		return func(t *testing.T, out string) {
			t.Helper()
			var r plan.Result
			if err := unmarshal([]byte(out), &r); err != nil {
				t.Fatalf("output is not a plan.Result: %v\n%s", err, out)
			}
			if r.FormatVersion != plan.ResultFormatVersion || len(r.Composites) != 1 || r.Composites[0].Ref.Name != "prod" {
				t.Errorf("want a plan.Result for composite prod, got:\n%s", out)
			}
		}
	}
	contains := func(subs ...string) func(*testing.T, string) {
		return func(t *testing.T, out string) {
			t.Helper()
			for _, sub := range subs {
				if !strings.Contains(out, sub) {
					t.Errorf("want output containing %q, got:\n%s", sub, out)
				}
			}
		}
	}

	cases := map[string]struct {
		reason   string
		cmd      simulateCmd
		wantXR   func(*testing.T, string)
		wantComp func(*testing.T, string)
	}{
		"Plan": {
			reason:   "-o plan, the default, should print the plan text, with the revision impact under the Composition.",
			cmd:      simulateCmd{Output: "plan"},
			wantXR:   contains("XR: XNetwork.example.org prod\n  [+] ConfigMap. cm (create)\n      data:\n        + k: \"v\"", "Plan: 1 to add", "Note: "),
			wantComp: contains("[=] Composition.apiextensions.crossplane.io net (no-op)\n    Applying this creates a new CompositionRevision, which 1 composite would adopt.", "XR: XNetwork.example.org prod", "Note: "),
		},
		"JSON": {
			reason:   "-o json should print plan.Result as JSON.",
			cmd:      simulateCmd{Output: "json"},
			wantXR:   isPlanResult(json.Unmarshal),
			wantComp: isPlanResult(json.Unmarshal),
		},
		"YAML": {
			reason:   "-o yaml should print plan.Result as YAML.",
			cmd:      simulateCmd{Output: "yaml"},
			wantXR:   isPlanResult(func(b []byte, v any) error { return yaml.Unmarshal(b, v) }),
			wantComp: isPlanResult(func(b []byte, v any) error { return yaml.Unmarshal(b, v) }),
		},
		"Diff": {
			reason:   "-o diff should print the engine's human diff.",
			cmd:      simulateCmd{Output: "diff"},
			wantXR:   contains("+++ ConfigMap/cm", "Summary: 1 added"),
			wantComp: contains("=== Composition Changes ===", "=== Affected Composite Resources ==="),
		},
		"JSONWithOutputVersion": {
			reason:   "-o json with --output-version should print the engine's structured schema at that version.",
			cmd:      simulateCmd{Output: "json", OutputVersion: "v2"},
			wantXR:   contains(`"outputVersion": "v2"`),
			wantComp: contains(`"outputVersion": "v2"`),
		},
		"YAMLWithOutputVersion": {
			reason:   "-o yaml with --output-version should print the engine's structured schema at that version.",
			cmd:      simulateCmd{Output: "yaml", OutputVersion: "v1"},
			wantXR:   contains("outputVersion: v1"),
			wantComp: contains("outputVersion: v1"),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tc.cmd.NoColor = true
			for kind, run := range map[string]struct {
				render func(*kong.Context) error
				want   func(*testing.T, string)
			}{
				"xr":   {func(k *kong.Context) error { return tc.cmd.renderXRReport(k, logging.NewNopLogger(), xrReport) }, tc.wantXR},
				"comp": {func(k *kong.Context) error { return tc.cmd.renderCompReport(k, logging.NewNopLogger(), compReport) }, tc.wantComp},
			} {
				stdout := &bytes.Buffer{}
				k := &kong.Context{Kong: &kong.Kong{Stdout: stdout, Stderr: &bytes.Buffer{}}}
				if err := run.render(k); err != nil {
					t.Fatalf("\n%s\nrender %s report: %v", tc.reason, kind, err)
				}
				t.Run(kind, func(t *testing.T) { run.want(t, stdout.String()) })
			}
		})
	}
}

// TestWithRenderError pins that a render failure reads exactly as it did when the processor joined it
// with the run's own errors: one flat list, not a list nested in a list.
func TestWithRenderError(t *testing.T) {
	a, b, render := errors.New("a"), errors.New("b"), errors.New("failed to render diffs: boom")

	tests := map[string]struct {
		err  error
		want string
	}{
		"NoRunError":     {err: nil, want: xperrors.Join(render).Error()},
		"SingleRunError": {err: a, want: xperrors.Join(a, render).Error()},
		"JoinedRunError": {err: xperrors.Join(a, b), want: xperrors.Join(a, b, render).Error()},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := withRenderError(tt.err, render).Error(); got != tt.want {
				t.Errorf("withRenderError() = %q, want %q", got, tt.want)
			}
		})
	}
}

var _ dp.CompDiffProcessor = &fakeCompProc{}

// fakeCompProc is a CompDiffProcessor that records its calls.
type fakeCompProc struct {
	initErr error
	diffFn  func(ctx context.Context) (*dt.CompDiffOutput, error)

	gotNamespace string
	gotRefs      []k8stypes.NamespacedName
	gotComps     int
	cleanups     int
}

func (f *fakeCompProc) Initialize(context.Context) error { return f.initErr }

func (f *fakeCompProc) DiffComposition(ctx context.Context, comps []*un.Unstructured, ns string, refs []k8stypes.NamespacedName) (*dt.CompDiffOutput, error) {
	f.gotNamespace, f.gotRefs, f.gotComps = ns, refs, len(comps)
	if f.diffFn == nil {
		return &dt.CompDiffOutput{}, nil
	}
	return f.diffFn(ctx)
}

func (f *fakeCompProc) Cleanup(context.Context) error {
	f.cleanups++
	return nil
}

// exitCode returns the code main would exit with for err and status.
func exitCode(err error, status *common.ExitStatus) int {
	if err == nil {
		return status.Code
	}
	var ec kong.ExitCoder
	if errors.As(err, &ec) {
		return ec.ExitCode()
	}
	return 1
}

func TestSimulateRun(t *testing.T) {
	errBoom := errors.New("boom")
	noInit := func(context.Context, logging.Logger) error { return nil }
	// changed is a report of one Composition with an affected composite
	// resource.
	changed := func() *dt.CompDiffOutput {
		return &dt.CompDiffOutput{Compositions: []dt.CompositionDiff{{
			Name:           "comp",
			ImpactAnalysis: []dt.XRImpact{{Status: dt.XRStatusChanged}},
		}}}
	}

	type args struct {
		cmd         simulateCmd
		initClients func(context.Context, logging.Logger) error
		proc        *fakeCompProc
		cancel      bool
	}
	type want struct {
		code      int
		err       error
		namespace string
		refs      []k8stypes.NamespacedName
		diffed    bool
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"NoChanges": {
			reason: "A run with no changes should succeed.",
			args:   args{initClients: noInit, proc: &fakeCompProc{}},
			want:   want{diffed: true},
		},
		"Changes": {
			reason: "Changes should exit 3, as a result rather than an error.",
			args: args{initClients: noInit, proc: &fakeCompProc{diffFn: func(context.Context) (*dt.CompDiffOutput, error) {
				return changed(), nil
			}}},
			want: want{code: dp.ExitCodeDiffDetected, diffed: true},
		},
		"ErrorWithChanges": {
			reason: "A tool error should exit 1, not 3, even when there are changes.",
			args: args{initClients: noInit, proc: &fakeCompProc{diffFn: func(context.Context) (*dt.CompDiffOutput, error) {
				return changed(), errBoom
			}}},
			want: want{code: dp.ExitCodeToolError, err: errBoom, diffed: true},
		},
		"SchemaValidationErrorWithChanges": {
			reason: "A schema validation failure should exit 2, not 3, even when there are changes.",
			args: args{initClients: noInit, proc: &fakeCompProc{diffFn: func(context.Context) (*dt.CompDiffOutput, error) {
				return changed(), dp.NewSchemaValidationError("XR/db", "invalid", errBoom)
			}}},
			want: want{code: dp.ExitCodeSchemaValidation, err: errBoom, diffed: true},
		},
		"Namespaced": {
			reason: "--namespace alone should reach DiffComposition as the namespace.",
			args:   args{cmd: simulateCmd{Namespace: "team-a"}, initClients: noInit, proc: &fakeCompProc{}},
			want:   want{diffed: true, namespace: "team-a"},
		},
		"Scoped": {
			reason: "--name and --namespace should reach DiffComposition as a ref.",
			args:   args{cmd: simulateCmd{Namespace: "team-a", Name: "db"}, initClients: noInit, proc: &fakeCompProc{}},
			want:   want{diffed: true, refs: []k8stypes.NamespacedName{{Namespace: "team-a", Name: "db"}}},
		},
		"ClientInitError": {
			reason: "A client initialization failure is a tool error.",
			args: args{
				initClients: func(context.Context, logging.Logger) error { return errBoom },
				proc:        &fakeCompProc{},
			},
			want: want{code: dp.ExitCodeToolError, err: errBoom},
		},
		"ProcessorInitError": {
			reason: "A processor initialization failure is a tool error.",
			args:   args{initClients: noInit, proc: &fakeCompProc{initErr: errBoom}},
			want:   want{code: dp.ExitCodeToolError, err: errBoom},
		},
		"DiffError": {
			reason: "A failed simulation is a tool error.",
			args: args{initClients: noInit, proc: &fakeCompProc{diffFn: func(context.Context) (*dt.CompDiffOutput, error) {
				return changed(), errBoom
			}}},
			want: want{code: dp.ExitCodeToolError, err: errBoom, diffed: true},
		},
		"SchemaValidationError": {
			reason: "A simulation that failed only schema validation should exit 2.",
			args: args{initClients: noInit, proc: &fakeCompProc{diffFn: func(context.Context) (*dt.CompDiffOutput, error) {
				return &dt.CompDiffOutput{}, dp.NewSchemaValidationError("XR/db", "invalid", errBoom)
			}}},
			want: want{code: dp.ExitCodeSchemaValidation, err: errBoom, diffed: true},
		},
		"Interrupted": {
			reason: "An interrupted run should return the interruption, whatever it returned, which main exits 130 for.",
			args: args{cancel: true, initClients: noInit, proc: &fakeCompProc{diffFn: func(ctx context.Context) (*dt.CompDiffOutput, error) {
				return changed(), ctx.Err()
			}}},
			want: want{code: dp.ExitCodeToolError, err: dp.ErrInterrupted, diffed: true},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.args.cancel {
				cancel()
			}

			// kong sets -o's default when it parses the flags.
			if tc.args.cmd.Output == "" {
				tc.args.cmd.Output = "plan"
			}
			k := &kong.Context{Kong: &kong.Kong{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}}
			diff := tc.args.cmd.diffCompositions(k, logging.NewNopLogger(), tc.args.proc, []*un.Unstructured{{}}, compositionMode)
			status := &common.ExitStatus{}
			err := tc.args.cmd.simulate(ctx, logging.NewNopLogger(), status, tc.args.initClients, tc.args.proc, diff)

			if diff := cmp.Diff(tc.want.code, exitCode(err, status)); diff != "" {
				t.Errorf("\n%s\nsimulate(...): -want exit code, +got:\n%s", tc.reason, diff)
			}
			if !errors.Is(err, tc.want.err) {
				t.Errorf("\n%s\nsimulate(...): want error %v, got %v", tc.reason, tc.want.err, err)
			}
			if diff := cmp.Diff(tc.want.namespace, tc.args.proc.gotNamespace); diff != "" {
				t.Errorf("\n%s\nsimulate(...): -want namespace, +got:\n%s", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.refs, tc.args.proc.gotRefs); diff != "" {
				t.Errorf("\n%s\nsimulate(...): -want refs, +got:\n%s", tc.reason, diff)
			}
			if diffed := tc.args.proc.gotComps > 0; diffed != tc.want.diffed {
				t.Errorf("\n%s\nsimulate(...): want DiffComposition called %t, got %t", tc.reason, tc.want.diffed, diffed)
			}
			if tc.args.proc.cleanups == 0 {
				t.Errorf("\n%s\nsimulate(...): processor was not cleaned up", tc.reason)
			}
		})
	}
}

func TestLoadInputs(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	proj := write("crossplane-project.yaml", "apiVersion: dev.crossplane.io/v1alpha1\nkind: Project\nmetadata:\n  name: p\n")
	comp := write("comp.yaml", `apiVersion: apiextensions.crossplane.io/v1
kind: Composition
metadata:
  name: c
spec:
  compositeTypeRef:
    apiVersion: example.org/v1
    kind: XThing
  mode: Pipeline
  pipeline:
  - step: render
    functionRef:
      name: function-go-templating
    input:
      apiVersion: gotemplating.fn.crossplane.io/v1beta1
      kind: GoTemplate
      source: Inline
`)
	xr := write("xr.yaml", "apiVersion: example.org/v1\nkind: XThing\nmetadata:\n  name: x\n")
	claim := write("claim.yaml", "apiVersion: example.org/v1\nkind: Thing\nmetadata:\n  name: t\n  namespace: default\n")
	// A GoTemplate of its own is a resource, not a pipeline input.
	goTemplate := write("gotemplate.yaml", "apiVersion: gotemplating.fn.crossplane.io/v1beta1\nkind: GoTemplate\nsource: Inline\n")
	comps := write("comps/a.yaml", "apiVersion: apiextensions.crossplane.io/v1\nkind: Composition\nmetadata:\n  name: a\n")
	empty := write("empty.yaml", "# nothing here\n")
	notProject := write("meta.yaml", "apiVersion: meta.pkg.crossplane.io/v1\nkind: Configuration\nmetadata:\n  name: cfg\n")

	type want struct {
		mode        inputMode
		projectFile string
		names       []string
		err         string
	}

	cases := map[string]struct {
		reason string
		paths  []string
		want   want
	}{
		"ProjectFile": {
			reason: "A single project file should select project mode.",
			paths:  []string{proj},
			want:   want{mode: projectMode, projectFile: proj},
		},
		"Composition": {
			reason: "A Composition should select composition mode; its pipeline inputs are part of it.",
			paths:  []string{comp},
			want:   want{mode: compositionMode, names: []string{"c"}},
		},
		"CompositionDirectory": {
			reason: "A directory of Compositions should select composition mode.",
			paths:  []string{filepath.Dir(comps)},
			want:   want{mode: compositionMode, names: []string{"a"}},
		},
		"Compositions": {
			reason: "Several Composition files should select composition mode.",
			paths:  []string{comp, comps},
			want:   want{mode: compositionMode, names: []string{"c", "a"}},
		},
		"XRAndClaim": {
			reason: "Composite resources and claims should select XR mode.",
			paths:  []string{xr, claim},
			want:   want{mode: xrMode, names: []string{"x", "t"}},
		},
		"NonProjectSingleFile": {
			reason: "A single file that isn't a project file should be loaded as resources; checkComposites rejects it once XRDs are known.",
			paths:  []string{notProject},
			want:   want{mode: xrMode, names: []string{"cfg"}},
		},
		"ProjectWithResources": {
			reason: "A project file combined with resource files should be rejected.",
			paths:  []string{proj, xr},
			want:   want{err: "a project file must be the only -f input"},
		},
		"ProjectInDirectory": {
			reason: "A directory holding a project file should be rejected the same way.",
			paths:  []string{dir},
			want:   want{err: "a project file must be the only -f input"},
		},
		"CompositionWithXR": {
			reason: "Compositions mixed with composite resources should be rejected.",
			paths:  []string{comp, xr},
			want:   want{err: `-f inputs mix Compositions with other resources (XThing "x")`},
		},
		"CompositionWithStandaloneGoTemplate": {
			reason: "A resource that only looks like a pipeline input should still count as another resource.",
			paths:  []string{comp, goTemplate, goTemplate},
			want:   want{err: "-f inputs mix Compositions with other resources (GoTemplate"},
		},
		"Empty": {
			reason: "Inputs with no resources should be rejected.",
			paths:  []string{empty},
			want:   want{err: "no resources found"},
		},
		"Missing": {
			reason: "A missing input should be rejected.",
			paths:  []string{filepath.Join(dir, "nope.yaml")},
			want:   want{err: "cannot read -f input"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := loadInputs(tc.paths)
			if tc.want.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want.err) {
					t.Fatalf("\n%s\nloadInputs(%v): want error containing %q, got %v", tc.reason, tc.paths, tc.want.err, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("\n%s\nloadInputs(%v): %v", tc.reason, tc.paths, err)
			}
			names := []string{}
			for _, r := range got.resources {
				names = append(names, r.GetName())
			}
			if diff := cmp.Diff(tc.want, want{mode: got.mode, projectFile: got.projectFile, names: names}, cmp.AllowUnexported(want{}), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("\n%s\nloadInputs(%v): -want, +got:\n%s", tc.reason, tc.paths, diff)
			}
		})
	}
}

func TestValidateModeFlags(t *testing.T) {
	cases := map[string]struct {
		reason string
		mode   inputMode
		args   []string
		err    string
	}{
		"ProjectAcceptsAll": {
			reason: "A project accepts every flag.",
			mode:   projectMode,
			args:   []string{"-n", "a", "--name", "b", "--include-pinned", "--max-concurrency", "2", "--cache-dir", "/c", "--analyze-on", "always", "--minimize-composition"},
		},
		"CompositionAcceptsScope": {
			reason: "Compositions accept the flags that scope their impact.",
			mode:   compositionMode,
			args:   []string{"-n", "a", "--name", "b", "--include-pinned", "--analyze-on", "always", "--minimize-composition"},
		},
		"CompositionRejectsBuildFlags": {
			reason: "Compositions use the cluster's functions, so the build flags don't apply.",
			mode:   compositionMode,
			args:   []string{"--max-concurrency", "2"},
			err:    "--max-concurrency configures the project's function builds",
		},
		"XRRejectsCacheDir": {
			reason: "Composite resources use the cluster's functions, so the build flags don't apply.",
			mode:   xrMode,
			args:   []string{"--cache-dir", "/c"},
			err:    "--cache-dir configures the project's function builds",
		},
		"XRRejectsNamespace": {
			reason: "Composite resource files name what to simulate, so -n doesn't apply.",
			mode:   xrMode,
			args:   []string{"-n", "a"},
			err:    "--namespace selects which composite resources a Composition change reaches",
		},
		"XRRejectsIncludePinned": {
			reason: "--include-pinned doesn't apply to composite resource files.",
			mode:   xrMode,
			args:   []string{"--include-pinned"},
			err:    "--include-pinned selects",
		},
		"XRAcceptsCommon": {
			reason: "Composite resources accept the flags shared with crossplane-diff's xr command.",
			mode:   xrMode,
			args:   []string{"-o", "json", "--no-color", "--ignore-paths", "a.b", "--eventual-state", "--timeout", "2m", "--dry-run-on", "existing"},
		},
		"DefaultsAreNotSet": {
			reason: "Flags left at their defaults, or set from the environment, are not rejected.",
			mode:   xrMode,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("CROSSPLANE_XPKG_CACHE", "/env-cache")

			var cli struct {
				Simulate simulateCmd `cmd:""`
			}
			parser, err := kong.New(&cli, kong.Vars{"project_file": "crossplane-project.yaml"}, kong.Exit(func(int) {}), kong.Bind(&common.ExitStatus{}))
			if err != nil {
				t.Fatalf("kong.New(...): %v", err)
			}
			k, err := parser.Parse(append([]string{"simulate"}, tc.args...))
			if err != nil {
				t.Fatalf("Parse(%v): %v", tc.args, err)
			}

			err = validateModeFlags(tc.mode, setFlags(k))
			if tc.err == "" {
				if err != nil {
					t.Errorf("\n%s\nvalidateModeFlags(...): %v", tc.reason, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("\n%s\nvalidateModeFlags(...): want error containing %q, got %v", tc.reason, tc.err, err)
			}
		})
	}
}

func TestCheckComposites(t *testing.T) {
	xrGVK := schema.GroupVersionKind{Group: "example.org", Version: "v1", Kind: "XThing"}
	claimGVK := schema.GroupVersionKind{Group: "example.org", Version: "v1", Kind: "Thing"}
	xrd := &un.Unstructured{}
	lookup := func(want schema.GroupVersionKind) func(context.Context, schema.GroupVersionKind) (*un.Unstructured, error) {
		return func(_ context.Context, gvk schema.GroupVersionKind) (*un.Unstructured, error) {
			if gvk == want {
				return xrd, nil
			}
			return nil, errors.New("no XRD found")
		}
	}
	defs := &testutils.MockDefinitionClient{GetXRDForXRFn: lookup(xrGVK), GetXRDForClaimFn: lookup(claimGVK)}
	res := func(apiVersion, kind, name string) *un.Unstructured {
		return &un.Unstructured{Object: map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": map[string]any{"name": name}}}
	}

	cases := map[string]struct {
		reason    string
		resources []*un.Unstructured
		err       string
	}{
		"XRAndClaim": {
			reason:    "Resources an XRD defines as a composite resource or claim should be accepted.",
			resources: []*un.Unstructured{res("example.org/v1", "XThing", "x"), res("example.org/v1", "Thing", "t")},
		},
		"ConfigurationMetadata": {
			reason:    "A Configuration package metadata file should be rejected.",
			resources: []*un.Unstructured{res("meta.pkg.crossplane.io/v1", "Configuration", "cfg")},
			err:       `-f inputs include Configuration "cfg" (meta.pkg.crossplane.io/v1), which no XRD in the cluster defines as a composite resource or claim`,
		},
		"Provider": {
			reason:    "A Provider should be rejected.",
			resources: []*un.Unstructured{res("example.org/v1", "XThing", "x"), res("pkg.crossplane.io/v1", "Provider", "provider-aws")},
			err:       `-f inputs include Provider "provider-aws" (pkg.crossplane.io/v1)`,
		},
		"ConfigMap": {
			reason:    "A non-Crossplane kind should be rejected.",
			resources: []*un.Unstructured{res("v1", "ConfigMap", "cm")},
			err:       `-f inputs include ConfigMap "cm" (v1)`,
		},
		"WrongVersion": {
			reason:    "A composite resource at a version its XRD doesn't define should be rejected.",
			resources: []*un.Unstructured{res("example.org/v2", "XThing", "x")},
			err:       `-f inputs include XThing "x" (example.org/v2)`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := checkComposites(context.Background(), defs, tc.resources)
			if tc.err == "" {
				if err != nil {
					t.Fatalf("\n%s\ncheckComposites(...): %v", tc.reason, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Fatalf("\n%s\ncheckComposites(...): want error containing %q, got %v", tc.reason, tc.err, err)
			}
		})
	}
}
