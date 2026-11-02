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

package simulate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-containerregistry/pkg/name"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	un "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"

	xperrors "github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	apiextensionsv1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1"
	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"
	pkgv1beta1 "github.com/crossplane/crossplane/apis/v2/pkg/v1beta1"

	dp "github.com/crossplane/cli/v2/internal/diff/diffprocessor"
	"github.com/crossplane/cli/v2/internal/diff/renderer"
	dt "github.com/crossplane/cli/v2/internal/diff/result"
	"github.com/crossplane/cli/v2/internal/diff/testutils"
	"github.com/crossplane/cli/v2/internal/plan"
	xprender "github.com/crossplane/cli/v2/pkg/render"
)

func TestSimulateScope(t *testing.T) {
	type want struct {
		namespace string
		refs      []k8stypes.NamespacedName
		err       string
	}

	cases := map[string]struct {
		reason string
		cmd    CompositionFlags
		want   want
	}{
		"All": {
			reason: "No scope flags should simulate every namespace.",
		},
		"Namespace": {
			reason: "--namespace alone should scope discovery to the namespace.",
			cmd:    CompositionFlags{Namespace: "team-a"},
			want:   want{namespace: "team-a"},
		},
		"ClusterScopedName": {
			reason: "--name alone should name a cluster-scoped composite resource.",
			cmd:    CompositionFlags{Name: []string{"db"}},
			want:   want{refs: []k8stypes.NamespacedName{{Name: "db"}}},
		},
		"NamespacedName": {
			reason: "--name with --namespace should name a namespaced composite resource, not scope discovery.",
			cmd:    CompositionFlags{Namespace: "team-a", Name: []string{"db"}},
			want:   want{refs: []k8stypes.NamespacedName{{Namespace: "team-a", Name: "db"}}},
		},
		"ManyNames": {
			reason: "Each --name should become a ref, bare names taking --namespace.",
			cmd:    CompositionFlags{Namespace: "team-a", Name: []string{"db", "cache"}},
			want:   want{refs: []k8stypes.NamespacedName{{Namespace: "team-a", Name: "db"}, {Namespace: "team-a", Name: "cache"}}},
		},
		"QualifiedNames": {
			reason: "namespace/name should carry its own namespace, mixed with a cluster-scoped bare name.",
			cmd:    CompositionFlags{Name: []string{"team-a/db", "team-b/db", "global"}},
			want:   want{refs: []k8stypes.NamespacedName{{Namespace: "team-a", Name: "db"}, {Namespace: "team-b", Name: "db"}, {Name: "global"}}},
		},
		"QualifiedWithNamespaceFlag": {
			reason: "namespace/name together with --namespace should be rejected.",
			cmd:    CompositionFlags{Namespace: "team-a", Name: []string{"team-a/db"}},
			want:   want{err: "can't be combined with --namespace"},
		},
		"EmptyName": {
			reason: "An empty --name value should be rejected.",
			cmd:    CompositionFlags{Name: []string{"db", " "}},
			want:   want{err: "--name must not be empty"},
		},
		"MalformedName": {
			reason: "A malformed namespace/name should be rejected naming --name.",
			cmd:    CompositionFlags{Name: []string{"a/b/c"}},
			want:   want{err: `invalid --name "a/b/c"`},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ns, refs, err := tc.cmd.scope()
			if tc.want.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want.err) {
					t.Errorf("\n%s\nscope(): want error containing %q, got %v", tc.reason, tc.want.err, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("\n%s\nscope(): unexpected error: %v", tc.reason, err)
			}
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
	c := &Flags{
		sel:           CompositionFlags{IncludePinned: true, AnalyzeOn: "spec-change"},
		MaxIterations: 7,
		EngineFlags:   xprender.EngineFlags{CrossplaneVersion: "v2.3.4", CrossplaneDockerNetwork: "ci"},

		MaxNestedDepth:     4,
		IgnorePaths:        []string{"a.b"},
		EventualState:      true,
		MaxRecvMessageSize: 16,
		DryRunOn:           "existing",
	}

	creds := []corev1.Secret{{ObjectMeta: metav1.ObjectMeta{Name: "creds"}}}
	cfg := dp.ProcessorConfig{}
	for _, o := range c.processorOptions(k, logging.NewNopLogger(), creds, []pkgv1.Function{{ObjectMeta: metav1.ObjectMeta{Name: "fn"}}}, nil, nil) {
		o(&cfg)
	}

	type got struct {
		IncludeManual       bool
		MaxRenderIterations int
		CrossplaneVersion   string
		CrossplaneImage     string
		DockerNetwork       string
		HasAdvisories       bool
		HasFunctionProvider bool
		MaxNestedDepth      int
		IgnorePaths         []string
		Credentials         []corev1.Secret
		EventualState       bool
		MaxRecvMessageSize  int
		AnalyzeOn           dp.AnalyzeOn
		DryRunOn            dp.DryRunOn
	}
	want := got{
		IncludeManual:       true,
		MaxRenderIterations: 7,
		CrossplaneVersion:   "v2.3.4",
		DockerNetwork:       "ci",
		HasAdvisories:       true,
		HasFunctionProvider: true,
		MaxNestedDepth:      4,
		IgnorePaths:         []string{"a.b"},
		Credentials:         creds,
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
		DockerNetwork:       cfg.CrossplaneDockerNetwork,
		HasAdvisories:       cfg.Advisories != nil,
		HasFunctionProvider: cfg.Factories.FunctionProvider != nil,
		MaxNestedDepth:      cfg.MaxNestedDepth,
		IgnorePaths:         cfg.IgnorePaths,
		Credentials:         cfg.FunctionCredentials,
		EventualState:       cfg.EventualState,
		MaxRecvMessageSize:  cfg.MaxRecvMessageSize,
		AnalyzeOn:           cfg.AnalyzeOn,
		DryRunOn:            cfg.DryRunOn,
	}
	if diff := cmp.Diff(want, g); diff != "" {
		t.Errorf("processorOptions(...): -want, +got:\n%s", diff)
	}

	// Without project functions or ImageConfigs every function comes from the
	// cluster as is, so there is nothing to wrap.
	cfg = dp.ProcessorConfig{}
	for _, o := range c.processorOptions(k, logging.NewNopLogger(), nil, nil, nil, nil) {
		o(&cfg)
	}
	if cfg.Factories.FunctionProvider != nil {
		t.Errorf("processorOptions(..., nil): want no function provider overlay, got one")
	}

	// ImageConfigs alone still wrap the cluster's functions.
	cfg = dp.ProcessorConfig{}
	for _, o := range c.processorOptions(k, logging.NewNopLogger(), nil, nil, nil, []pkgv1beta1.ImageConfig{{}}) {
		o(&cfg)
	}
	if cfg.Factories.FunctionProvider == nil {
		t.Errorf("processorOptions(..., ics): want a function provider that rewrites images, got none")
	}

	// --crossplane-binary renders with a local binary.
	c.EngineFlags = xprender.EngineFlags{CrossplaneBinary: "/bin/crossplane"}
	cfg = dp.ProcessorConfig{}
	for _, o := range c.processorOptions(k, logging.NewNopLogger(), nil, nil, nil, nil) {
		o(&cfg)
	}
	if cfg.CrossplaneRenderBinary != "/bin/crossplane" {
		t.Errorf("processorOptions(...) with --crossplane-binary: want render binary %q, got %q", "/bin/crossplane", cfg.CrossplaneRenderBinary)
	}
}

func TestFunctionProviderFactory(t *testing.T) {
	ics := []pkgv1beta1.ImageConfig{{
		ObjectMeta: metav1.ObjectMeta{Name: "mirror"},
		Spec: pkgv1beta1.ImageConfigSpec{
			MatchImages:  []pkgv1beta1.ImageMatch{{Prefix: "xpkg.crossplane.io/"}},
			RewriteImage: &pkgv1beta1.ImageRewrite{Prefix: "mirror.example.com/"},
		},
	}}
	fn := func(name, pkg string) pkgv1.Function {
		return pkgv1.Function{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: pkgv1.FunctionSpec{PackageSpec: pkgv1.PackageSpec{Package: pkg}}}
	}
	cluster := fn("fn-cluster", "xpkg.crossplane.io/org/fn-cluster:v1")
	projFns := []pkgv1.Function{
		fn("fn-dep", "xpkg.crossplane.io/org/fn-dep:v1"),
		fn("fn-own", "xpkg.crossplane.io/org/proj_fn-own:arm64"),
	}
	embedded := map[string]name.Digest{"fn-own": {}}

	comp := &apiextensionsv1.Composition{
		ObjectMeta: metav1.ObjectMeta{Name: "comp"},
		Spec: apiextensionsv1.CompositionSpec{
			Mode: apiextensionsv1.CompositionModePipeline,
			Pipeline: []apiextensionsv1.PipelineStep{
				{Step: "a", FunctionRef: apiextensionsv1.FunctionReference{Name: "fn-cluster"}},
				{Step: "b", FunctionRef: apiextensionsv1.FunctionReference{Name: "fn-dep"}},
				{Step: "c", FunctionRef: apiextensionsv1.FunctionReference{Name: "fn-own"}},
			},
		},
	}
	fnClient := testutils.NewMockFunctionClient().WithSuccessfulFunctionsFetch([]pkgv1.Function{cluster}).Build()

	p := functionProviderFactory(projFns, embedded, ics)(fnClient, logging.NewNopLogger())
	fns, err := p.GetFunctionsForComposition(context.Background(), comp)
	if err != nil {
		t.Fatalf("GetFunctionsForComposition(...): %v", err)
	}
	got := map[string]string{}
	for _, f := range fns {
		got[f.GetName()] = f.Spec.Package
	}
	want := map[string]string{
		"fn-cluster": "mirror.example.com/org/fn-cluster:v1",
		"fn-dep":     "mirror.example.com/org/fn-dep:v1",
		// Embedded functions run from the images loaded into the daemon.
		"fn-own": "xpkg.crossplane.io/org/proj_fn-own:arm64",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("GetFunctionsForComposition(...): -want, +got:\n%s", diff)
	}
}

func TestSimulateDiffOptions(t *testing.T) {
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	k := &kong.Context{Kong: &kong.Kong{Stdout: stdout, Stderr: stderr}}

	cases := map[string]struct {
		reason string
		cmd    Flags
		want   func(o *renderer.DiffOptions)
	}{
		"UnsetFlagsSelectHumanDiff": {
			reason: "Unset flags should select the human-readable diff, uncolorized off a terminal.",
			want: func(o *renderer.DiffOptions) {
				o.UseColors = false
			},
		},
		"CompactAndNoColorCarryThrough": {
			reason: "The compact and no-color flags should reach the renderer.",
			cmd:    Flags{NoColor: true, Compact: true},
			want: func(o *renderer.DiffOptions) {
				o.UseColors = false
				o.Compact = true
			},
		},
		"HooksColorizeAsTerminal": {
			reason: "The test hooks should colorize as for a terminal.",
			cmd:    Flags{Hooks: &Hooks{Terminal: true}},
			want:   func(*renderer.DiffOptions) {},
		},
		"NoColorWinsOverHooks": {
			reason: "--no-color should disable colors even under the test hooks.",
			cmd:    Flags{NoColor: true, Hooks: &Hooks{Terminal: true}},
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
// both report types: ProjectResult for plan, json and yaml, and the engine's
// human diff for diff.
func TestSimulateRenderReports(t *testing.T) {
	xr := corev1.ObjectReference{APIVersion: "example.org/v1", Kind: "XNetwork", Name: "prod"}
	compReport := &dt.CompDiffOutput{Compositions: []dt.CompositionDiff{{
		Name:           "net",
		RevisionImpact: dt.RevisionImpact{ChangeScope: "spec", CreatesRevision: true, RepointedComposites: 1},
		ImpactAnalysis: []dt.XRImpact{{ObjectReference: xr, Status: dt.XRStatusUnchanged}},
	}}}

	// isPlanResult checks out decodes as a ProjectResult naming the composite.
	isPlanResult := func(unmarshal func([]byte, any) error) func(*testing.T, string) {
		return func(t *testing.T, out string) {
			t.Helper()
			var r ProjectResult
			if err := unmarshal([]byte(out), &r); err != nil {
				t.Fatalf("output is not a ProjectResult: %v\n%s", err, out)
			}
			if r.FormatVersion != plan.ResultFormatVersion || len(r.Composites) != 1 || r.Composites[0].Ref.Name != "prod" {
				t.Errorf("want a ProjectResult for composite prod, got:\n%s", out)
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
		cmd      Flags
		wantComp func(*testing.T, string)
	}{
		"Plan": {
			reason:   "-o plan, the default, should print the plan text, with the revision impact under the Composition.",
			cmd:      Flags{Output: "plan"},
			wantComp: contains("[=] Composition.apiextensions.crossplane.io net (no-op)\n    Applying this creates a new CompositionRevision, which 1 composite would adopt.", "XR: XNetwork.example.org prod", "Note: "),
		},
		"JSON": {
			reason:   "-o json should print ProjectResult as JSON.",
			cmd:      Flags{Output: "json"},
			wantComp: isPlanResult(json.Unmarshal),
		},
		"YAML": {
			reason:   "-o yaml should print ProjectResult as YAML.",
			cmd:      Flags{Output: "yaml"},
			wantComp: isPlanResult(func(b []byte, v any) error { return yaml.Unmarshal(b, v) }),
		},
		"Diff": {
			reason:   "-o diff should print the engine's human diff.",
			cmd:      Flags{Output: "diff"},
			wantComp: contains("=== Composition Changes ===", "=== Affected Composite Resources ==="),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tc.cmd.NoColor = true
			for kind, run := range map[string]struct {
				render func(*kong.Context) error
				want   func(*testing.T, string)
			}{
				"comp": {func(k *kong.Context) error {
					_, err := tc.cmd.renderCompReport(k, logging.NewNopLogger(), compReport, nil)
					return err
				}, tc.wantComp},
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

func TestSimulateRESTConfigNamespace(t *testing.T) {
	kubeconfig := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(kubeconfig, []byte(`apiVersion: v1
kind: Config
clusters:
- name: c
  cluster: {server: "https://127.0.0.1:6443"}
users:
- name: u
contexts:
- name: ctx
  context: {cluster: c, user: u, namespace: team-a}
- name: nons
  context: {cluster: c, user: u}
current-context: ctx
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", kubeconfig)

	cases := map[string]struct {
		reason string
		cmd    Flags
		want   string
	}{
		"ContextNamespace": {
			reason: "The kubeconfig context's namespace should be used for resources without one.",
			want:   "team-a",
		},
		"NoContextNamespace": {
			reason: "A context without a namespace should fall back to default.",
			cmd:    Flags{Context: "nons"},
			want:   "default",
		},
		"Hooks": {
			reason: "The test hooks' cluster should use default.",
			cmd:    Flags{Hooks: &Hooks{RESTConfig: &rest.Config{}}},
			want:   "default",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, ns, err := tc.cmd.restConfig()
			if err != nil {
				t.Fatalf("\n%s\nrestConfig(): %v", tc.reason, err)
			}
			if diff := cmp.Diff(tc.want, ns); diff != "" {
				t.Errorf("\n%s\nrestConfig(): -want namespace, +got:\n%s", tc.reason, diff)
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

// exitCode returns the code main would exit with for err.
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
	// changed is a report of one Composition with an affected composite
	// resource.
	changed := func() *dt.CompDiffOutput {
		return &dt.CompDiffOutput{Compositions: []dt.CompositionDiff{{
			Name:           "comp",
			ImpactAnalysis: []dt.XRImpact{{Status: dt.XRStatusChanged}},
		}}}
	}

	type args struct {
		cmd         Flags
		initClients func(context.Context, logging.Logger) error
		proc        *fakeCompProc
		cancel      bool
	}
	type want struct {
		code      int
		err       error
		errMsg    string
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
		"RenderErrorKeepsDiffError": {
			reason: "A render failure should be joined with the simulation's error, not replace it.",
			args: args{cmd: Flags{Output: "xml"}, initClients: noInit, proc: &fakeCompProc{diffFn: func(context.Context) (*dt.CompDiffOutput, error) {
				return changed(), errBoom
			}}},
			want: want{code: dp.ExitCodeToolError, err: errBoom, errMsg: "failed to render diffs", diffed: true},
		},
		"Namespaced": {
			reason: "The namespace Validate scoped to should reach DiffComposition.",
			args:   args{cmd: Flags{sel: CompositionFlags{ns: "team-a"}}, initClients: noInit, proc: &fakeCompProc{}},
			want:   want{diffed: true, namespace: "team-a"},
		},
		"Scoped": {
			reason: "The refs Validate scoped to should reach DiffComposition.",
			args:   args{cmd: Flags{sel: CompositionFlags{refs: []k8stypes.NamespacedName{{Namespace: "team-a", Name: "db"}}}}, initClients: noInit, proc: &fakeCompProc{}},
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
			diff := tc.args.cmd.diffCompositions(k, logging.NewNopLogger(), tc.args.proc, []*un.Unstructured{{}}, true)
			err := tc.args.cmd.simulate(ctx, logging.NewNopLogger(), tc.args.initClients, tc.args.proc, diff)

			if diff := cmp.Diff(tc.want.code, exitCode(err)); diff != "" {
				t.Errorf("\n%s\nsimulate(...): -want exit code, +got:\n%s", tc.reason, diff)
			}
			// Changes found return a silent ExitError, which main prints nothing for.
			if _, silent := SilentExit(err); tc.want.err == nil && err != nil && !silent {
				t.Errorf("\n%s\nsimulate(...): want no error or a silent exit, got %v", tc.reason, err)
			}
			if tc.want.err != nil && !errors.Is(err, tc.want.err) {
				t.Errorf("\n%s\nsimulate(...): want error %v, got %v", tc.reason, tc.want.err, err)
			}
			if tc.want.errMsg != "" && (err == nil || !strings.Contains(err.Error(), tc.want.errMsg)) {
				t.Errorf("\n%s\nsimulate(...): want error containing %q, got %v", tc.reason, tc.want.errMsg, err)
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

func TestWarnIgnoredFlags(t *testing.T) {
	cases := map[string]struct {
		reason   string
		args     []string
		skipPlan bool
		project  bool
		warn     string
	}{
		"ProjectAcceptsAll": {
			reason:  "A project uses every flag.",
			project: true,
			args:    []string{"-n", "a", "--name", "b", "--include-pinned", "--cache-dir", "/c", "--analyze-on", "always", "--minimize-composition"},
		},
		"CacheDirWithPlan": {
			reason: "--cache-dir also caches provider packages, so it applies whenever planning is on.",
			args:   []string{"--cache-dir", "/c"},
		},
		"CacheDirSkipPlanNoProject": {
			reason:   "With --skip-plan and no project there is nothing to cache, so --cache-dir is ignored with a warning.",
			args:     []string{"--cache-dir", "/c"},
			skipPlan: true,
			warn:     "ignoring --cache-dir",
		},
		"ProjectAcceptsCacheDirSkipPlan": {
			reason:   "A project caches function builds even without planning.",
			project:  true,
			args:     []string{"--cache-dir", "/c"},
			skipPlan: true,
		},
		"ProvidersSkipPlan": {
			reason:   "--skip-plan runs no diff servers, so --providers is ignored with a warning.",
			project:  true,
			args:     []string{"--providers", "/p"},
			skipPlan: true,
			warn:     "ignoring --providers",
		},
		"SessionIDSkipPlan": {
			reason:   "--skip-plan runs no diff servers, so --session-id is ignored with a warning.",
			args:     []string{"--session-id", "s1"},
			skipPlan: true,
			warn:     "ignoring --session-id",
		},
		"DefaultsAreNotSet": {
			reason:   "Flags left at their defaults, or set from the environment, are not warned about.",
			skipPlan: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("CROSSPLANE_XPKG_CACHE", "/env-cache")

			var cli struct {
				Simulate struct {
					Flags            `embed:""`
					CompositionFlags `embed:""`
				} `cmd:""`
			}
			parser, err := kong.New(&cli, kong.Vars{"project_file": "crossplane-project.yaml"}, kong.Exit(func(int) {}))
			if err != nil {
				t.Fatalf("kong.New(...): %v", err)
			}
			k, err := parser.Parse(append([]string{"simulate"}, tc.args...))
			if err != nil {
				t.Fatalf("Parse(%v): %v", tc.args, err)
			}

			var stderr bytes.Buffer
			warnIgnoredFlags(&stderr, setFlags(k), tc.skipPlan, tc.project)
			if tc.warn == "" && stderr.Len() > 0 || !strings.Contains(stderr.String(), tc.warn) {
				t.Errorf("\n%s\nwarnIgnoredFlags(...): want warning containing %q, got %q", tc.reason, tc.warn, stderr.String())
			}
		})
	}
}

func TestPlanNotes(t *testing.T) {
	cases := map[string]struct {
		skipPlan   bool
		planned    bool
		routed     int
		considered int
		want       []string
	}{
		"SkipPlan":       {skipPlan: true, want: []string{NoteProviderPlanSkipped}},
		"NotPlanned":     {},
		"NoneRouted":     {planned: true, considered: 1, want: []string{NoteNoDiffServers}},
		"NothingToPlan":  {planned: true},
		"SomeRouted":     {planned: true, routed: 1},
		"ManyRouted":     {planned: true, routed: 5},
		"SkipPlanRouted": {skipPlan: true, planned: true, routed: 2, want: []string{NoteProviderPlanSkipped}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, planNotes(tc.skipPlan, tc.planned, tc.routed, tc.considered)); diff != "" {
				t.Errorf("planNotes(...): -want, +got:\n%s", diff)
			}
		})
	}
}

// TestExitCodesMatchEngine pins the CLI's exit statuses to the engine's, so
// DetermineExitCode's ranking keeps meaning what the help says.
func TestExitCodesMatchEngine(t *testing.T) {
	if diff := cmp.Diff(dp.ExitCodeDiffDetected, ExitCodeChanges); diff != "" {
		t.Errorf("ExitCodeChanges: -engine, +cli:\n%s", diff)
	}
	if diff := cmp.Diff(dp.ExitCodeSchemaValidation, ExitCodeSchemaValidation); diff != "" {
		t.Errorf("ExitCodeSchemaValidation: -engine, +cli:\n%s", diff)
	}
}

func TestNoteNoServers(t *testing.T) {
	none := []string{NoteNoDiffServers}
	cases := map[string]struct {
		output string
		notes  []string
		want   string
	}{
		"DiffPrintsNote": {output: "diff", notes: none, want: NoteNoDiffServers + "\n"},
		"PlanUsesNotes":  {output: "plan", notes: none},
		"DiffOtherNote":  {output: "diff", notes: []string{NoteProviderPlanSkipped}},
		"DiffNoNotes":    {output: "diff"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			stderr := &bytes.Buffer{}
			k := &kong.Context{Kong: &kong.Kong{Stderr: stderr}}
			c := &Flags{Output: tc.output}
			c.noteNoServers(k, tc.notes)
			c.noteNoServers(k, tc.notes)
			if diff := cmp.Diff(tc.want, stderr.String()); diff != "" {
				t.Errorf("noteNoServers(): -want, +got:\n%s", diff)
			}
		})
	}
}

func TestProviderSource(t *testing.T) {
	cases := map[string]struct {
		reason    string
		providers string
		project   bool
		want      source
	}{
		"Flag": {
			reason:    "--providers wins over the project's provider dependencies.",
			providers: "providers.yaml",
			want:      providersFlag,
		},
		"Project": {
			reason:  "Without --providers the project's provider dependencies start.",
			project: true,
			want:    providersProject,
		},
		"NoProject": {
			reason: "Without --providers or a project, each composite resource's providers start as it is simulated.",
			want:   providersPerXR,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := providerSource(tc.providers, tc.project); got != tc.want {
				t.Errorf("%s\nproviderSource(%q): want %v, got %v", tc.reason, tc.providers, tc.want, got)
			}
		})
	}
}

func TestSimulateProcessorOptionsDockerNetwork(t *testing.T) {
	fns := []pkgv1.Function{{ObjectMeta: metav1.ObjectMeta{Name: "fn", Annotations: map[string]string{xprender.AnnotationKeyRuntimeDockerNetwork: "fn-net"}}}}
	cases := map[string]struct {
		env  string
		args []string
		want string
	}{
		"Annotation":         {want: "fn-net"},
		"EnvBeatsAnnotation": {env: "env-net", want: "env-net"},
		"FlagBeatsEnv":       {env: "env-net", args: []string{"--crossplane-docker-network=flag-net"}, want: "flag-net"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// t.Setenv restores any outer value; kong treats a set-but-empty
			// variable as set, so unset it for the no-env cases.
			t.Setenv("CROSSPLANE_DOCKER_NETWORK", tc.env)
			if tc.env == "" {
				_ = os.Unsetenv("CROSSPLANE_DOCKER_NETWORK")
			}
			c, err := parseFlags(t, tc.args...)
			if err != nil {
				t.Fatalf("parseFlags(%v): %v", tc.args, err)
			}
			k := &kong.Context{Kong: &kong.Kong{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}}
			cfg := dp.ProcessorConfig{}
			for _, o := range c.processorOptions(k, logging.NewNopLogger(), nil, fns, nil, nil) {
				o(&cfg)
			}
			if cfg.CrossplaneDockerNetwork != tc.want {
				t.Errorf("processorOptions(...): want network %q, got %q", tc.want, cfg.CrossplaneDockerNetwork)
			}
		})
	}
}

// parseFlags parses args as simulate flags, running kong's validation.
func parseFlags(t *testing.T, args ...string) (*Flags, error) {
	t.Helper()

	var cli struct {
		Simulate Flags `cmd:""`
	}
	parser, err := kong.New(&cli, kong.Vars{"project_file": "crossplane-project.yaml"}, kong.Exit(func(int) {}))
	if err != nil {
		t.Fatalf("kong.New(...): %v", err)
	}
	_, err = parser.Parse(append([]string{"simulate"}, args...))
	return &cli.Simulate, err
}
