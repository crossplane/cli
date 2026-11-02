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

package render

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/google/go-cmp/cmp"

	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"
)

func TestEngineFlagsDockerNetwork(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "crossplane")
	if err := os.WriteFile(bin, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	annotated := []pkgv1.Function{functionWithAnnotations(map[string]string{AnnotationKeyRuntimeDockerNetwork: "fn-net"})}

	type want struct {
		network string
		local   bool
		stderr  string
	}

	cases := map[string]struct {
		reason string
		env    string
		args   []string
		want   want
	}{
		"AnnotationOnly": {
			reason: "Without the flag and env var the function annotation should name the network.",
			want:   want{network: "fn-net"},
		},
		"EnvOverAnnotation": {
			reason: "CROSSPLANE_DOCKER_NETWORK should win over the function annotation.",
			env:    "env-net",
			want:   want{network: "env-net"},
		},
		"FlagOverEnv": {
			reason: "--crossplane-docker-network should win over CROSSPLANE_DOCKER_NETWORK.",
			env:    "env-net",
			args:   []string{"--crossplane-docker-network", "flag-net"},
			want:   want{network: "flag-net"},
		},
		"EnvWithBinary": {
			reason: "An exported CROSSPLANE_DOCKER_NETWORK should not stop --crossplane-binary, nor warn.",
			env:    "env-net",
			args:   []string{"--crossplane-binary", bin},
			want:   want{network: "env-net", local: true},
		},
		"FlagWithBinary": {
			reason: "Passing --crossplane-docker-network with --crossplane-binary should warn once that it is ignored.",
			args:   []string{"--crossplane-binary", bin, "--crossplane-docker-network", "flag-net"},
			want:   want{network: "flag-net", local: true, stderr: "warning: --crossplane-docker-network is ignored with --crossplane-binary\n"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// t.Setenv restores any outer value; kong treats a set-but-empty variable
			// as set, so unset it for the no-env cases.
			t.Setenv("CROSSPLANE_DOCKER_NETWORK", tc.env)
			if tc.env == "" {
				_ = os.Unsetenv("CROSSPLANE_DOCKER_NETWORK")
			}

			var cli struct {
				Render struct {
					EngineFlags `prefix:""`
				} `cmd:""`
			}
			stderr := &bytes.Buffer{}
			parser, err := kong.New(&cli, kong.Vars{"session_id_help": "Session ID."}, kong.Exit(func(int) {}), kong.Writers(&bytes.Buffer{}, stderr))
			if err != nil {
				t.Fatalf("kong.New(...): %v", err)
			}
			if _, err := parser.Parse(append([]string{"render"}, tc.args...)); err != nil {
				t.Fatalf("\n%s\nParse(%v): %v", tc.reason, tc.args, err)
			}
			f := &cli.Render.EngineFlags
			f.SetDefaultCrossplaneDockerNetwork(annotated)

			if diff := cmp.Diff(tc.want.network, f.CrossplaneDockerNetwork); diff != "" {
				t.Errorf("\n%s\nnetwork: -want, +got:\n%s", tc.reason, diff)
			}
			_, local := NewEngineFromFlags(f, nil).(*localRenderEngine)
			if local != tc.want.local {
				t.Errorf("\n%s\nlocal engine: want %t, got %t", tc.reason, tc.want.local, local)
			}
			if diff := cmp.Diff(tc.want.stderr, stderr.String()); diff != "" {
				t.Errorf("\n%s\nstderr: -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestEngineFlagsAfterApplyIgnoresResolved(t *testing.T) {
	stderr := &bytes.Buffer{}
	flag := &kong.Flag{Value: &kong.Value{Name: "crossplane-docker-network"}}
	k := &kong.Context{Kong: &kong.Kong{Stderr: stderr}, Path: []*kong.Path{{Flag: flag, Resolved: true}}}
	f := &EngineFlags{CrossplaneBinary: "/bin/crossplane", CrossplaneDockerNetwork: "net"}
	if err := f.AfterApply(k); err != nil {
		t.Fatalf("AfterApply(...): %v", err)
	}
	if stderr.Len() != 0 {
		t.Errorf("AfterApply(...) with a resolver-sourced network: want no warning, got %q", stderr.String())
	}
}
