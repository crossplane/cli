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
	"testing"
	"time"

	"github.com/alecthomas/kong"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
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
				ProjectFile:    "crossplane-project.yaml",
				MaxConcurrency: 8,
				MaxIterations:  dp.DefaultMaxRenderIterations,
				Timeout:        time.Minute,
				Output:         "diff",
			}},
		},
		"AllFlags": {
			reason: "Every flag should land on its field.",
			args: []string{
				"-f", "p.yaml", "-c", "staging", "-n", "team-a", "--name", "db",
				"--include-pinned", "--max-iterations", "3", "--timeout", "2m",
				"--max-concurrency", "2", "-o", "json", "--crossplane-version", "v2.3.4",
				"--as", "jane", "--as-group", "admins",
			},
			want: want{cmd: &simulateCmd{
				ProjectFile:       "p.yaml",
				Context:           "staging",
				Namespace:         "team-a",
				Name:              "db",
				IncludePinned:     true,
				MaxIterations:     3,
				Timeout:           2 * time.Minute,
				MaxConcurrency:    2,
				Output:            "json",
				CrossplaneVersion: "v2.3.4",
			}},
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
				ProjectFile:     "crossplane-project.yaml",
				MaxConcurrency:  8,
				MaxIterations:   dp.DefaultMaxRenderIterations,
				Timeout:         time.Minute,
				Output:          "diff",
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
		IncludePinned:     true,
		MaxIterations:     7,
		CrossplaneVersion: "v2.3.4",
	}

	cfg := dp.ProcessorConfig{}
	for _, o := range c.processorOptions(k, logging.NewNopLogger(), []pkgv1.Function{}) {
		o(&cfg)
	}

	type got struct {
		OutputFormat        renderer.OutputFormat
		IncludeManual       bool
		MaxRenderIterations int
		CrossplaneVersion   string
		CrossplaneImage     string
		Colorize            bool
		StdoutIsK           bool
		StderrIsK           bool
		HasAdvisories       bool
		HasFunctionProvider bool
	}
	want := got{
		OutputFormat:        renderer.OutputFormatYAML,
		IncludeManual:       true,
		MaxRenderIterations: 7,
		CrossplaneVersion:   "v2.3.4",
		// A buffer is not a terminal.
		Colorize:            false,
		StdoutIsK:           true,
		StderrIsK:           true,
		HasAdvisories:       true,
		HasFunctionProvider: true,
	}
	g := got{
		OutputFormat:        cfg.OutputFormat,
		IncludeManual:       cfg.IncludeManual,
		MaxRenderIterations: cfg.MaxRenderIterations,
		CrossplaneVersion:   cfg.CrossplaneVersion,
		CrossplaneImage:     cfg.CrossplaneImage,
		Colorize:            cfg.Colorize,
		StdoutIsK:           cfg.Stdout == stdout,
		StderrIsK:           cfg.Stderr == stderr,
		HasAdvisories:       cfg.Advisories != nil,
		HasFunctionProvider: cfg.Factories.FunctionProvider != nil,
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
