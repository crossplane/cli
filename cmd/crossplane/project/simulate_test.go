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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/kong"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dp "github.com/crossplane/cli/v2/internal/diff/diffprocessor"
	"github.com/crossplane/cli/v2/internal/simulate"
	"github.com/crossplane/cli/v2/pkg/render"
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
	if err := os.WriteFile(creds, []byte("apiVersion: v1\nkind: Secret\nmetadata:\n  name: creds\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	type want struct {
		cmd     *simulateCmd
		secrets []corev1.Secret
		err     bool
	}

	cases := map[string]struct {
		reason string
		env    map[string]string
		args   []string
		want   want
	}{
		"Defaults": {
			reason: "With no flags the defaults should apply.",
			want: want{cmd: &simulateCmd{Flags: simulate.Flags{
				Input:          []string{"crossplane-project.yaml"},
				MaxConcurrency: 8,
				MaxIterations:  dp.DefaultMaxRenderIterations,
				Timeout:        2 * time.Minute,
				Output:         "plan",
				MaxNestedDepth: 10,
				DryRunOn:       "all",
			}}},
		},
		"AllFlags": {
			reason: "Every flag should land on its field.",
			args: []string{
				"-f", "p.yaml", "-c", "staging", "-n", "team-a", "--name", "db",
				"--include-pinned", "--max-iterations", "3", "--timeout", "2m",
				"--max-concurrency", "2", "-o", "json", "--crossplane-version", "v2.3.4",
				"--as", "jane", "--as-group", "admins",
			},
			want: want{cmd: &simulateCmd{Flags: simulate.Flags{
				Input:          []string{"p.yaml"},
				Context:        "staging",
				Namespace:      "team-a",
				Name:           []string{"db"},
				IncludePinned:  true,
				MaxIterations:  3,
				Timeout:        2 * time.Minute,
				MaxConcurrency: 2,
				Output:         "json",
				EngineFlags:    render.EngineFlags{CrossplaneVersion: "v2.3.4"},
				MaxNestedDepth: 10,
				DryRunOn:       "all",
			}}},
		},
		"ParityFlags": {
			reason: "The crossplane-diff parity flags should land on their fields.",
			args: []string{
				"--no-color", "--compact", "--max-nested-depth", "3", "--ignore-paths", "a.b", "--ignore-paths", "c.d",
				"--eventual-state", "--max-recv-message-size", "16",
				"--minimize-composition", "--analyze-on", "always", "--dry-run-on", "existing",
			},
			want: want{cmd: &simulateCmd{Flags: simulate.Flags{
				Input:               []string{"crossplane-project.yaml"},
				MaxConcurrency:      8,
				MaxIterations:       dp.DefaultMaxRenderIterations,
				Timeout:             2 * time.Minute,
				Output:              "plan",
				NoColor:             true,
				Compact:             true,
				MaxNestedDepth:      3,
				IgnorePaths:         []string{"a.b", "c.d"},
				EventualState:       true,
				MaxRecvMessageSize:  16,
				MinimizeComposition: true,
				AnalyzeOn:           "always",
				DryRunOn:            "existing",
			}}},
		},
		"FunctionCredentials": {
			reason: "--function-credentials should load the Secrets in the file at parse time.",
			args:   []string{"--function-credentials", creds},
			want: want{
				cmd: &simulateCmd{Flags: simulate.Flags{
					Input:               []string{"crossplane-project.yaml"},
					MaxConcurrency:      8,
					MaxIterations:       dp.DefaultMaxRenderIterations,
					Timeout:             2 * time.Minute,
					Output:              "plan",
					MaxNestedDepth:      10,
					DryRunOn:            "all",
					FunctionCredentials: creds,
				}},
				secrets: []corev1.Secret{{
					TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
					ObjectMeta: metav1.ObjectMeta{Name: "creds"},
				}},
			},
		},
		"FunctionCredentialsMissing": {
			reason: "A missing credentials path should be rejected at parse time.",
			args:   []string{"--function-credentials", filepath.Join(t.TempDir(), "nope.yaml")},
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
			want: want{cmd: &simulateCmd{Flags: simulate.Flags{
				Input:          []string{"a.yaml", "b,c.yaml"},
				MaxConcurrency: 8,
				MaxIterations:  dp.DefaultMaxRenderIterations,
				Timeout:        2 * time.Minute,
				Output:         "plan",
				MaxNestedDepth: 10,
				DryRunOn:       "all",
			}}},
		},
		"PlanFlags": {
			reason: "The provider plan flags should land on their fields.",
			args:   []string{"--providers", dir, "--image-configs", creds, "--session-id", "s1"},
			want: want{cmd: &simulateCmd{Flags: simulate.Flags{
				Input:          []string{"crossplane-project.yaml"},
				MaxConcurrency: 8,
				MaxIterations:  dp.DefaultMaxRenderIterations,
				Timeout:        2 * time.Minute,
				Output:         "plan",
				MaxNestedDepth: 10,
				DryRunOn:       "all",
				Providers:      dir,
				ImageConfigs:   creds,
				EngineFlags:    render.EngineFlags{SessionID: "s1"},
			}}},
		},
		"RenderEngineFlags": {
			reason: "The render engine flags should land on the embedded EngineFlags.",
			args:   []string{"--crossplane-binary", creds, "--session-id", "s1"},
			want: want{cmd: &simulateCmd{Flags: simulate.Flags{
				Input:          []string{"crossplane-project.yaml"},
				MaxConcurrency: 8,
				MaxIterations:  dp.DefaultMaxRenderIterations,
				Timeout:        2 * time.Minute,
				Output:         "plan",
				MaxNestedDepth: 10,
				DryRunOn:       "all",
				EngineFlags:    render.EngineFlags{CrossplaneBinary: creds, SessionID: "s1"},
			}}},
		},
		"RenderDockerNetwork": {
			reason: "--crossplane-docker-network should land on the embedded EngineFlags.",
			args:   []string{"--crossplane-docker-network", "ci"},
			want: want{cmd: &simulateCmd{Flags: simulate.Flags{
				Input:          []string{"crossplane-project.yaml"},
				MaxConcurrency: 8,
				MaxIterations:  dp.DefaultMaxRenderIterations,
				Timeout:        2 * time.Minute,
				Output:         "plan",
				MaxNestedDepth: 10,
				DryRunOn:       "all",
				EngineFlags:    render.EngineFlags{CrossplaneDockerNetwork: "ci"},
			}}},
		},
		"RenderDockerNetworkEnv": {
			reason: "CROSSPLANE_DOCKER_NETWORK should land on the embedded EngineFlags.",
			env:    map[string]string{"CROSSPLANE_DOCKER_NETWORK": "env-net"},
			want: want{cmd: &simulateCmd{Flags: simulate.Flags{
				Input:          []string{"crossplane-project.yaml"},
				MaxConcurrency: 8,
				MaxIterations:  dp.DefaultMaxRenderIterations,
				Timeout:        2 * time.Minute,
				Output:         "plan",
				MaxNestedDepth: 10,
				DryRunOn:       "all",
				EngineFlags:    render.EngineFlags{CrossplaneDockerNetwork: "env-net"},
			}}},
		},
		"RenderDockerNetworkFlagOverEnv": {
			reason: "--crossplane-docker-network should win over CROSSPLANE_DOCKER_NETWORK.",
			env:    map[string]string{"CROSSPLANE_DOCKER_NETWORK": "env-net"},
			args:   []string{"--crossplane-docker-network", "ci"},
			want: want{cmd: &simulateCmd{Flags: simulate.Flags{
				Input:          []string{"crossplane-project.yaml"},
				MaxConcurrency: 8,
				MaxIterations:  dp.DefaultMaxRenderIterations,
				Timeout:        2 * time.Minute,
				Output:         "plan",
				MaxNestedDepth: 10,
				DryRunOn:       "all",
				EngineFlags:    render.EngineFlags{CrossplaneDockerNetwork: "ci"},
			}}},
		},
		"RenderBinaryWithNetwork": {
			reason: "A local render binary ignores a Docker network rather than rejecting it.",
			args:   []string{"--crossplane-binary", creds, "--crossplane-docker-network", "ci"},
			want: want{cmd: &simulateCmd{Flags: simulate.Flags{
				Input:          []string{"crossplane-project.yaml"},
				MaxConcurrency: 8,
				MaxIterations:  dp.DefaultMaxRenderIterations,
				Timeout:        2 * time.Minute,
				Output:         "plan",
				MaxNestedDepth: 10,
				DryRunOn:       "all",
				EngineFlags:    render.EngineFlags{CrossplaneBinary: creds, CrossplaneDockerNetwork: "ci"},
			}}},
		},
		"SkipPlan": {
			reason: "--skip-plan alone should be accepted.",
			args:   []string{"--skip-plan"},
			want: want{cmd: &simulateCmd{Flags: simulate.Flags{
				Input:          []string{"crossplane-project.yaml"},
				MaxConcurrency: 8,
				MaxIterations:  dp.DefaultMaxRenderIterations,
				Timeout:        2 * time.Minute,
				Output:         "plan",
				MaxNestedDepth: 10,
				DryRunOn:       "all",
				SkipPlan:       true,
			}}},
		},
		"SkipPlanWithProviders": {
			reason: "--providers does nothing with --skip-plan, so Run warns rather than Validate rejecting it.",
			args:   []string{"--skip-plan", "--providers", dir},
			want: want{cmd: &simulateCmd{Flags: simulate.Flags{
				Input:          []string{"crossplane-project.yaml"},
				MaxConcurrency: 8,
				MaxIterations:  dp.DefaultMaxRenderIterations,
				Timeout:        2 * time.Minute,
				Output:         "plan",
				MaxNestedDepth: 10,
				DryRunOn:       "all",
				SkipPlan:       true,
				Providers:      dir,
			}}},
		},
		"SkipPlanWithImageConfigs": {
			reason: "--image-configs also rewrites function images, so it applies with --skip-plan.",
			args:   []string{"--skip-plan", "--image-configs", creds},
			want: want{cmd: &simulateCmd{Flags: simulate.Flags{
				Input:          []string{"crossplane-project.yaml"},
				MaxConcurrency: 8,
				MaxIterations:  dp.DefaultMaxRenderIterations,
				Timeout:        2 * time.Minute,
				Output:         "plan",
				MaxNestedDepth: 10,
				DryRunOn:       "all",
				SkipPlan:       true,
				ImageConfigs:   creds,
			}}},
		},
		"SkipPlanWithSessionID": {
			reason: "--session-id does nothing with --skip-plan, so Run warns rather than Validate rejecting it.",
			args:   []string{"--skip-plan", "--session-id", "s1"},
			want: want{cmd: &simulateCmd{Flags: simulate.Flags{
				Input:          []string{"crossplane-project.yaml"},
				MaxConcurrency: 8,
				MaxIterations:  dp.DefaultMaxRenderIterations,
				Timeout:        2 * time.Minute,
				Output:         "plan",
				MaxNestedDepth: 10,
				DryRunOn:       "all",
				SkipPlan:       true,
				EngineFlags:    render.EngineFlags{SessionID: "s1"},
			}}},
		},
		"UnknownOutput": {
			reason: "Output formats other than plan, diff, json and yaml should be rejected.",
			args:   []string{"-o", "xml"},
			want:   want{err: true},
		},
		"ZeroConcurrency": {
			reason: "Zero build slots should be accepted; the builder runs one build at a time.",
			args:   []string{"--max-concurrency", "0"},
			want: want{cmd: &simulateCmd{Flags: simulate.Flags{
				Input:          []string{"crossplane-project.yaml"},
				MaxIterations:  dp.DefaultMaxRenderIterations,
				Timeout:        2 * time.Minute,
				Output:         "plan",
				MaxNestedDepth: 10,
				DryRunOn:       "all",
			}}},
		},
		"EmptyName": {
			reason: "An empty --name should fail at parse time.",
			args:   []string{"--name=,"},
			want:   want{err: true},
		},
		"NamespacedNameWithNamespace": {
			reason: "ns/name with -n should fail at parse time.",
			args:   []string{"-n", "a", "--name", "b/c"},
			want:   want{err: true},
		},
		"TooManySlashes": {
			reason: "a/b/c should fail at parse time.",
			args:   []string{"--name", "a/b/c"},
			want:   want{err: true},
		},
		"EmptyNamespaceInName": {
			reason: "/name should fail at parse time.",
			args:   []string{"--name", "/name"},
			want:   want{err: true},
		},
		"ZeroIterations": {
			reason: "A render needs at least one iteration.",
			args:   []string{"--max-iterations", "0"},
			want:   want{err: true},
		},
		"ZeroTimeout": {
			reason: "A timeout of zero or less would fail every composite at once.",
			args:   []string{"--timeout", "0s"},
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
			want: want{cmd: &simulateCmd{Flags: simulate.Flags{
				Input:          []string{"crossplane-project.yaml"},
				MaxConcurrency: 8,
				MaxIterations:  dp.DefaultMaxRenderIterations,
				Timeout:        2 * time.Minute,
				Output:         "plan",
				MaxNestedDepth: 10,
				DryRunOn:       "all",
				EngineFlags:    render.EngineFlags{CrossplaneImage: "example.com/crossplane:stable"},
			}}},
		},
		"RenderBackendsExclusive": {
			reason: "--crossplane-version and --crossplane-image should be mutually exclusive.",
			args:   []string{"--crossplane-version", "v2.3.4", "--crossplane-image", "example.com/crossplane:v2.3.4"},
			want:   want{err: true},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got, err := parseSimulate(t, tc.args...)
			if gotErr := err != nil; gotErr != tc.want.err {
				t.Fatalf("\n%s\nParse(%v): want error %t, got %v", tc.reason, tc.args, tc.want.err, err)
			}
			if tc.want.err {
				return
			}
			if diff := cmp.Diff(tc.want.cmd, got, cmpopts.IgnoreFields(simulate.Flags{}, "Impersonation"), cmpopts.IgnoreUnexported(simulate.Flags{})); diff != "" {
				t.Errorf("\n%s\nParse(%v): -want, +got:\n%s", tc.reason, tc.args, diff)
			}
			if diff := cmp.Diff(tc.want.secrets, got.Credentials(), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("\n%s\nParse(%v): function credentials: -want, +got:\n%s", tc.reason, tc.args, diff)
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

	commaXR := write("a,b.yaml", "apiVersion: example.org/v1\nkind: XThing\nmetadata:\n  name: comma\n")

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
	// A step input that isn't an object is the engine's to report, not the
	// loader's.
	listInput := write("listinput.yaml", `apiVersion: apiextensions.crossplane.io/v1
kind: Composition
metadata:
  name: l
spec:
  compositeTypeRef:
    apiVersion: example.org/v1
    kind: XThing
  mode: Pipeline
  pipeline:
  - step: render
    functionRef:
      name: function-go-templating
    input: [1]
`)
	xr := write("xr.yaml", "apiVersion: example.org/v1\nkind: XThing\nmetadata:\n  name: x\n")
	claim := write("claim.yaml", "apiVersion: example.org/v1\nkind: Thing\nmetadata:\n  name: t\n  namespace: default\n")
	// A GoTemplate of its own is a resource, not a pipeline input.
	goTemplate := write("gotemplate.yaml", "apiVersion: gotemplating.fn.crossplane.io/v1beta1\nkind: GoTemplate\nsource: Inline\n")
	comps := write("comps/a.yaml", "apiVersion: apiextensions.crossplane.io/v1\nkind: Composition\nmetadata:\n  name: a\n")
	empty := write("empty.yaml", "# nothing here\n")
	notProject := write("meta.yaml", "apiVersion: meta.pkg.crossplane.io/v1\nkind: Configuration\nmetadata:\n  name: cfg\n")

	type want struct {
		mode        simulate.Mode
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
			want:   want{mode: simulate.ProjectMode, projectFile: proj},
		},
		"Composition": {
			reason: "A Composition should select composition mode; its pipeline inputs are part of it.",
			paths:  []string{comp},
			want:   want{mode: simulate.CompositionMode, names: []string{"c"}},
		},
		"CompositionListInput": {
			reason: "A Composition should be loaded without parsing its pipeline inputs as resources.",
			paths:  []string{listInput},
			want:   want{mode: simulate.CompositionMode, names: []string{"l"}},
		},
		"CompositionDirectory": {
			reason: "A directory of Compositions should select composition mode.",
			paths:  []string{filepath.Dir(comps)},
			want:   want{mode: simulate.CompositionMode, names: []string{"a"}},
		},
		"Compositions": {
			reason: "Several Composition files should select composition mode.",
			paths:  []string{comp, comps},
			want:   want{mode: simulate.CompositionMode, names: []string{"c", "a"}},
		},
		"XRAndClaim": {
			reason: "Composite resources and claims should select XR mode.",
			paths:  []string{xr, claim},
			want:   want{mode: simulate.XRMode, names: []string{"x", "t"}},
		},
		"CommaInPath": {
			reason: "-f never splits on commas, so a,b.yaml is one path.",
			paths:  []string{commaXR},
			want:   want{mode: simulate.XRMode, names: []string{"comma"}},
		},
		"NonProjectSingleFile": {
			reason: "A single file that isn't a project file should be loaded as resources; checkComposites rejects it once XRDs are known.",
			paths:  []string{notProject},
			want:   want{mode: simulate.XRMode, names: []string{"cfg"}},
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

func TestSimulateNameFlagParse(t *testing.T) {
	for name, args := range map[string][]string{
		"Repeated":       {"--name=a/db", "--name=b/db"},
		"CommaSeparated": {"--name=a/db,b/db"},
	} {
		t.Run(name, func(t *testing.T) {
			var c simulateCmd
			k, err := kong.New(&c, kong.Vars{"project_file": "crossplane-project.yaml"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := k.Parse(args); err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff([]string{"a/db", "b/db"}, c.Name); diff != "" {
				t.Errorf("Name: -want, +got:\n%s", diff)
			}
		})
	}
}
