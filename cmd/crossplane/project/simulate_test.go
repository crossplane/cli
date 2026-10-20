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
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alecthomas/kong"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	un "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8stypes "k8s.io/apimachinery/pkg/types"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"

	dp "github.com/crossplane/cli/v2/internal/diff/diffprocessor"
	"github.com/crossplane/cli/v2/internal/diff/renderer"
)

// parseSimulate parses args as `simulate` flags, running kong's validation.
func parseSimulate(t *testing.T, args ...string) (*simulateCmd, error) {
	t.Helper()

	var cli struct {
		Simulate simulateCmd `cmd:""`
	}
	parser, err := kong.New(&cli, kong.Vars{"project_file": "crossplane-project.yaml"}, kong.Exit(func(int) {}))
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
				Output:         "diff",
				OutputVersion:  "v1",
				MaxNestedDepth: 10,
				DryRunOn:       "all",
			}},
		},
		"AllFlags": {
			reason: "Every flag should land on its field.",
			args: []string{
				"-f", "p.yaml", "-c", "staging", "-n", "team-a", "--name", "db",
				"--include-pinned", "--max-iterations", "3", "--timeout", "2m",
				"--max-concurrency", "2", "-o", "json", "--output-version", "v2", "--crossplane-version", "v2.3.4", "--exit-code",
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
				ExitCode:          true,
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
				Output:                   "diff",
				OutputVersion:            "v1",
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
				Output:         "diff",
				OutputVersion:  "v1",
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
		"TwoInputs": {
			reason: "-f should take exactly one project file.",
			args:   []string{"-f", "a.yaml", "-f", "b.yaml"},
			want:   want{err: true},
		},
		"UnknownOutputVersion": {
			reason: "Structured output schema versions other than v1 and v2 should be rejected.",
			args:   []string{"--output-version", "v3"},
			want:   want{err: true},
		},
		"UnknownOutput": {
			reason: "Output formats other than diff, json and yaml should be rejected.",
			args:   []string{"-o", "plan"},
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
				Output:          "diff",
				OutputVersion:   "v1",
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
		Output:            "yaml",
		OutputVersion:     "v2",
		IncludePinned:     true,
		MaxIterations:     7,
		CrossplaneVersion: "v2.3.4",

		Compact:                  true,
		MaxNestedDepth:           4,
		IgnorePaths:              []string{"a.b"},
		FunctionCredentials:      FunctionCredentials{Secrets: []corev1.Secret{{ObjectMeta: metav1.ObjectMeta{Name: "creds"}}}},
		FunctionRegistryOverride: "reg.example.com",
		EventualState:            true,
		MaxRecvMessageSize:       16,
		MinimizeComposition:      true,
		AnalyzeOn:                "spec-change",
		DryRunOn:                 "existing",
	}

	cfg := dp.ProcessorConfig{}
	for _, o := range c.processorOptions(k, logging.NewNopLogger(), []pkgv1.Function{}) {
		o(&cfg)
	}

	type got struct {
		OutputFormat        renderer.OutputFormat
		OutputVersion       renderer.OutputVersion
		IncludeManual       bool
		MaxRenderIterations int
		CrossplaneVersion   string
		CrossplaneImage     string
		Colorize            bool
		StdoutIsK           bool
		StderrIsK           bool
		HasAdvisories       bool
		HasFunctionProvider bool
		Compact             bool
		MaxNestedDepth      int
		IgnorePaths         []string
		Credentials         []corev1.Secret
		RegistryOverride    string
		EventualState       bool
		MaxRecvMessageSize  int
		MinimizeComposition bool
		AnalyzeOn           dp.AnalyzeOn
		DryRunOn            dp.DryRunOn
	}
	want := got{
		OutputFormat:        renderer.OutputFormatYAML,
		OutputVersion:       renderer.OutputVersionV2,
		IncludeManual:       true,
		MaxRenderIterations: 7,
		CrossplaneVersion:   "v2.3.4",
		// A buffer is not a terminal.
		Colorize:            false,
		StdoutIsK:           true,
		StderrIsK:           true,
		HasAdvisories:       true,
		HasFunctionProvider: true,
		Compact:             true,
		MaxNestedDepth:      4,
		IgnorePaths:         []string{lastAppliedPath, "a.b"},
		Credentials:         c.FunctionCredentials.Secrets,
		RegistryOverride:    "reg.example.com",
		EventualState:       true,
		MaxRecvMessageSize:  16,
		MinimizeComposition: true,
		AnalyzeOn:           dp.AnalyzeOnSpecChange,
		DryRunOn:            dp.DryRunOnExisting,
	}
	g := got{
		OutputFormat:        cfg.OutputFormat,
		OutputVersion:       cfg.OutputVersion,
		IncludeManual:       cfg.IncludeManual,
		MaxRenderIterations: cfg.MaxRenderIterations,
		CrossplaneVersion:   cfg.CrossplaneVersion,
		CrossplaneImage:     cfg.CrossplaneImage,
		Colorize:            cfg.Colorize,
		StdoutIsK:           cfg.Stdout == stdout,
		StderrIsK:           cfg.Stderr == stderr,
		HasAdvisories:       cfg.Advisories != nil,
		HasFunctionProvider: cfg.Factories.FunctionProvider != nil,
		Compact:             cfg.Compact,
		MaxNestedDepth:      cfg.MaxNestedDepth,
		IgnorePaths:         cfg.IgnorePaths,
		Credentials:         cfg.FunctionCredentials,
		RegistryOverride:    cfg.FunctionRegistryOverride,
		EventualState:       cfg.EventualState,
		MaxRecvMessageSize:  cfg.MaxRecvMessageSize,
		MinimizeComposition: cfg.MinimizeComposition,
		AnalyzeOn:           cfg.AnalyzeOn,
		DryRunOn:            cfg.DryRunOn,
	}
	if diff := cmp.Diff(want, g); diff != "" {
		t.Errorf("processorOptions(...): -want, +got:\n%s", diff)
	}
}

// fakeCompProc is a CompDiffProcessor that records its calls.
type fakeCompProc struct {
	initErr error
	diffFn  func(ctx context.Context) (bool, error)

	gotNamespace string
	gotRefs      []k8stypes.NamespacedName
	gotComps     int
	cleanups     int
}

func (f *fakeCompProc) Initialize(context.Context) error { return f.initErr }

func (f *fakeCompProc) DiffComposition(ctx context.Context, comps []*un.Unstructured, ns string, refs []k8stypes.NamespacedName) (bool, error) {
	f.gotNamespace, f.gotRefs, f.gotComps = ns, refs, len(comps)
	if f.diffFn == nil {
		return false, nil
	}
	return f.diffFn(ctx)
}

func (f *fakeCompProc) Cleanup(context.Context) error {
	f.cleanups++
	return nil
}

func exitCode(err error) int {
	if err == nil {
		return 0
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
			reason: "Changes alone should not fail the run.",
			args: args{initClients: noInit, proc: &fakeCompProc{diffFn: func(context.Context) (bool, error) {
				return true, nil
			}}},
			want: want{diffed: true},
		},
		"ChangesWithExitCode": {
			reason: "With --exit-code, changes should exit 3.",
			args: args{cmd: simulateCmd{ExitCode: true}, initClients: noInit, proc: &fakeCompProc{diffFn: func(context.Context) (bool, error) {
				return true, nil
			}}},
			want: want{code: dp.ExitCodeDiffDetected, err: errChangesDetected, diffed: true},
		},
		"NoChangesWithExitCode": {
			reason: "With --exit-code, a run with no changes should still succeed.",
			args:   args{cmd: simulateCmd{ExitCode: true}, initClients: noInit, proc: &fakeCompProc{}},
			want:   want{diffed: true},
		},
		"ErrorWithExitCode": {
			reason: "With --exit-code, a tool error should still exit 1, not 3.",
			args: args{cmd: simulateCmd{ExitCode: true}, initClients: noInit, proc: &fakeCompProc{diffFn: func(context.Context) (bool, error) {
				return true, errBoom
			}}},
			want: want{code: dp.ExitCodeToolError, err: errBoom, diffed: true},
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
			args: args{initClients: noInit, proc: &fakeCompProc{diffFn: func(context.Context) (bool, error) {
				return true, errBoom
			}}},
			want: want{code: dp.ExitCodeToolError, err: errBoom, diffed: true},
		},
		"SchemaValidationError": {
			reason: "A simulation that failed only schema validation should exit 2.",
			args: args{initClients: noInit, proc: &fakeCompProc{diffFn: func(context.Context) (bool, error) {
				return false, dp.NewSchemaValidationError("XR/db", "invalid", errBoom)
			}}},
			want: want{code: dp.ExitCodeSchemaValidation, err: errBoom, diffed: true},
		},
		"Interrupted": {
			reason: "An interrupted run should report the interruption and exit 130, whatever it returned.",
			args: args{cancel: true, initClients: noInit, proc: &fakeCompProc{diffFn: func(ctx context.Context) (bool, error) {
				return false, ctx.Err()
			}}},
			want: want{code: dp.ExitCodeInterrupted, err: dp.ErrInterrupted, diffed: true},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.args.cancel {
				cancel()
			}

			err := tc.args.cmd.simulate(ctx, logging.NewNopLogger(), tc.args.initClients, tc.args.proc, []*un.Unstructured{{}})

			if diff := cmp.Diff(tc.want.code, exitCode(err)); diff != "" {
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
