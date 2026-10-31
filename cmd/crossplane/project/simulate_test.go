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
	"context"
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

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	"github.com/crossplane/cli/v2/internal/config"
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
			want: want{cmd: &simulateCmd{ProjectFile: "crossplane-project.yaml", Flags: simulate.Flags{
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
			want: want{cmd: &simulateCmd{ProjectFile: "p.yaml", Flags: simulate.Flags{
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
			want: want{cmd: &simulateCmd{ProjectFile: "crossplane-project.yaml", Flags: simulate.Flags{
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
				cmd: &simulateCmd{ProjectFile: "crossplane-project.yaml", Flags: simulate.Flags{
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
		"PlanFlags": {
			reason: "The provider plan flags should land on their fields.",
			args:   []string{"--providers", dir, "--image-configs", creds, "--session-id", "s1"},
			want: want{cmd: &simulateCmd{ProjectFile: "crossplane-project.yaml", Flags: simulate.Flags{
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
			want: want{cmd: &simulateCmd{ProjectFile: "crossplane-project.yaml", Flags: simulate.Flags{
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
			want: want{cmd: &simulateCmd{ProjectFile: "crossplane-project.yaml", Flags: simulate.Flags{
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
			want: want{cmd: &simulateCmd{ProjectFile: "crossplane-project.yaml", Flags: simulate.Flags{
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
			want: want{cmd: &simulateCmd{ProjectFile: "crossplane-project.yaml", Flags: simulate.Flags{
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
			want: want{cmd: &simulateCmd{ProjectFile: "crossplane-project.yaml", Flags: simulate.Flags{
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
			want: want{cmd: &simulateCmd{ProjectFile: "crossplane-project.yaml", Flags: simulate.Flags{
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
			want: want{cmd: &simulateCmd{ProjectFile: "crossplane-project.yaml", Flags: simulate.Flags{
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
			want: want{cmd: &simulateCmd{ProjectFile: "crossplane-project.yaml", Flags: simulate.Flags{
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
			want: want{cmd: &simulateCmd{ProjectFile: "crossplane-project.yaml", Flags: simulate.Flags{
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
			want: want{cmd: &simulateCmd{ProjectFile: "crossplane-project.yaml", Flags: simulate.Flags{
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
			want: want{cmd: &simulateCmd{ProjectFile: "crossplane-project.yaml", Flags: simulate.Flags{
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

func TestSimulateBuildProjectRejectsComposition(t *testing.T) {
	path := filepath.Join(t.TempDir(), "comp.yaml")
	comp := "apiVersion: apiextensions.crossplane.io/v1\nkind: Composition\nmetadata:\n  name: c\nspec:\n  compositeTypeRef:\n    apiVersion: example.org/v1\n    kind: XThing\n"
	if err := os.WriteFile(path, []byte(comp), 0o600); err != nil {
		t.Fatal(err)
	}

	c := &simulateCmd{ProjectFile: path}
	_, err := c.buildProject(context.Background(), logging.NewNopLogger(), nil, &config.Config{})
	if err == nil || !strings.Contains(err.Error(), "failed to parse project file") {
		t.Errorf("buildProject(-f %s): want a project file parse error, got %v", path, err)
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
