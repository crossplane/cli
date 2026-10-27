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
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"k8s.io/client-go/rest"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	runtimexpkg "github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"

	"github.com/crossplane/cli/v2/internal/diffserver"
	"github.com/crossplane/cli/v2/internal/terminal"
)

func TestLoadProviderInputs(t *testing.T) {
	const ec2 = "xpkg.upbound.io/upbound/provider-aws-ec2:v2.9.0"

	cases := map[string]struct {
		reason   string
		manifest string
		want     []diffserver.Input
		wantErr  string // A substring of the error; empty means no error.
	}{
		"DockerAnnotations": {
			reason: "The Docker runtime is the default; its annotations override the publish and target addresses per provider.",
			manifest: `---
apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: provider-aws-ec2
  annotations:
    simulate.crossplane.io/runtime-docker-publish-address: 0.0.0.0
    simulate.crossplane.io/runtime-docker-target: 192.168.64.2
spec:
  package: ` + ec2 + `
---
apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: provider-aws-s3
spec:
  package: xpkg.upbound.io/upbound/provider-aws-s3:v2.9.0
`,
			want: []diffserver.Input{
				{Image: ec2, PublishAddress: netip.MustParseAddr("0.0.0.0"), Target: "192.168.64.2"},
				{Image: "xpkg.upbound.io/upbound/provider-aws-s3:v2.9.0"},
			},
		},
		"NotAProvider": {
			reason:   "Only Provider manifests can be run as diff servers.",
			manifest: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n",
			wantErr:  "expected only Provider manifests",
		},
		"StopCleanupRejected": {
			reason: "Render's Stop cleanup policy is deliberately unsupported for diff servers.",
			manifest: `---
apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: provider-aws-ec2
  annotations:
    simulate.crossplane.io/runtime-docker-cleanup: Stop
spec:
  package: ` + ec2 + `
`,
			wantErr: "invalid",
		},
		"DevelopmentDefaults": {
			reason: "A Development provider defaults to upjet's diff server address and needs no spec.package.",
			manifest: `---
apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: provider-aws-ec2
  annotations:
    simulate.crossplane.io/runtime: Development
    simulate.crossplane.io/runtime-development-api-groups: ec2.aws.upbound.io, ec2.aws.m.upbound.io
`,
			want: []diffserver.Input{{
				Runtime:           diffserver.RuntimeDevelopment,
				Name:              "provider-aws-ec2",
				DevelopmentTarget: "localhost:9099",
				APIGroups:         []string{"ec2.aws.upbound.io", "ec2.aws.m.upbound.io"},
			}},
		},
		"DevelopmentTargetIgnoresDockerAnnotations": {
			reason: "A Development provider takes its target from the annotation; spec.package and the Docker annotations do not apply to it, as in render.",
			manifest: `---
apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: provider-aws-ec2
  annotations:
    simulate.crossplane.io/runtime: Development
    simulate.crossplane.io/runtime-development-target: localhost:9444
    simulate.crossplane.io/runtime-development-api-groups: ec2.aws.upbound.io
    simulate.crossplane.io/runtime-docker-publish-address: 0.0.0.0
    simulate.crossplane.io/runtime-docker-cleanup: Stop
spec:
  package: ` + ec2 + `
`,
			want: []diffserver.Input{{
				Runtime:           diffserver.RuntimeDevelopment,
				Name:              "provider-aws-ec2",
				DevelopmentTarget: "localhost:9444",
				APIGroups:         []string{"ec2.aws.upbound.io"},
			}},
		},
		"DevelopmentWithoutAPIGroups": {
			reason: "Nothing is fetched for a Development provider, so it must declare the API groups it serves.",
			manifest: `---
apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: provider-aws-ec2
  annotations:
    simulate.crossplane.io/runtime: Development
    simulate.crossplane.io/runtime-development-api-groups: " , "
`,
			wantErr: AnnotationKeyDevelopmentAPIGroups,
		},
		"DevelopmentGroupsOverlap": {
			reason: "An API group can route to only one development diff server.",
			manifest: `---
apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: provider-aws-ec2
  annotations:
    simulate.crossplane.io/runtime: Development
    simulate.crossplane.io/runtime-development-api-groups: ec2.aws.upbound.io
---
apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: provider-aws-ec2-fork
  annotations:
    simulate.crossplane.io/runtime: Development
    simulate.crossplane.io/runtime-development-target: localhost:9100
    simulate.crossplane.io/runtime-development-api-groups: ec2.aws.upbound.io
`,
			wantErr: "both declare the API group",
		},
		"UnknownRuntime": {
			reason: "Runtime values other than Docker and Development are rejected, as in render.",
			manifest: `---
apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: provider-aws-ec2
  annotations:
    simulate.crossplane.io/runtime: Podman
spec:
  package: ` + ec2 + `
`,
			wantErr: "unsupported",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "providers.yaml")
			if err := os.WriteFile(path, []byte(tc.manifest), 0o600); err != nil {
				t.Fatalf("cannot write manifest: %v", err)
			}

			got, err := LoadProviderInputs(path)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("%s\nLoadProviderInputs(...): want an error containing %q, got %v", tc.reason, tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("%s\nLoadProviderInputs(...): unexpected error: %v", tc.reason, err)
			}
			if diff := cmp.Diff(tc.want, got, cmp.Comparer(func(a, b netip.Addr) bool { return a == b })); diff != "" {
				t.Errorf("%s\nLoadProviderInputs(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestNeedsDocker(t *testing.T) {
	cases := map[string]struct {
		reason string
		inputs []diffserver.Input
		want   bool
	}{
		"NoInputs": {
			reason: "Nothing to run means nothing to run in Docker.",
			want:   false,
		},
		"OnlyDevelopment": {
			reason: "Development providers connect to servers the user runs, so Docker is not needed.",
			inputs: []diffserver.Input{{Runtime: diffserver.RuntimeDevelopment, Name: "dev"}},
			want:   false,
		},
		"MixedRuntimes": {
			reason: "One provider whose image runs as a container is enough to need Docker.",
			inputs: []diffserver.Input{{Runtime: diffserver.RuntimeDevelopment, Name: "dev"}, {Image: "xpkg.example.org/provider:v1"}},
			want:   true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := NeedsDocker(tc.inputs); got != tc.want {
				t.Errorf("%s\nNeedsDocker(...): want %t, got %t", tc.reason, tc.want, got)
			}
		})
	}
}

// failingPkgs fails every Get, so each image is skipped.
type failingPkgs struct {
	runtimexpkg.Client

	gets int
}

func (f *failingPkgs) Get(context.Context, string, ...runtimexpkg.GetOption) (*runtimexpkg.Package, error) {
	f.gets++
	return nil, errors.New("boom")
}

func TestServerSetEnsure(t *testing.T) {
	pkgs := &failingPkgs{}
	var out bytes.Buffer
	s := NewServerSet(terminal.NewNopSpinnerPrinter(), logging.NewNopLogger(), &out, pkgs, "s")

	if err := s.Ensure(context.Background(), nil); err != nil || out.Len() != 0 || pkgs.gets != 0 {
		t.Fatalf("empty Ensure: err=%v out=%q gets=%d", err, out.String(), pkgs.gets)
	}
	in := []diffserver.Input{{Image: "xpkg.example/p:v1"}}
	for range 2 {
		if err := s.Ensure(context.Background(), in); err != nil {
			t.Fatal(err)
		}
	}
	if pkgs.gets != 1 {
		t.Errorf("Get called %d times, want 1", pkgs.gets)
	}
	if got := strings.Count(out.String(), "Not running a diff server for xpkg.example/p:v1: "); got != 1 {
		t.Errorf("skip message printed %d times, want 1: %q", got, out.String())
	}
	if n := len(s.mgr.Servers()); n != 0 {
		t.Errorf("%d servers running, want 0", n)
	}
}

func TestServerSetEnsureDevelopmentKeys(t *testing.T) {
	var out bytes.Buffer
	s := NewServerSet(terminal.NewNopSpinnerPrinter(), logging.NewNopLogger(), &out, &failingPkgs{}, "s")
	// Development inputs have no image, so each is keyed by its own name.
	inputs := []diffserver.Input{
		{Runtime: diffserver.RuntimeDevelopment, Name: "a", DevelopmentTarget: "localhost:1", APIGroups: []string{"a.example.org"}},
		{Runtime: diffserver.RuntimeDevelopment, Name: "b", DevelopmentTarget: "localhost:2", APIGroups: []string{"b.example.org"}},
	}
	_ = s.Ensure(context.Background(), inputs)
	for _, n := range []string{"a", "b"} {
		if got := strings.Count(out.String(), "Not running a diff server for "+n+":"); got != 1 {
			t.Errorf("skip message for %q printed %d times, want 1: %q", n, got, out.String())
		}
	}
	before := out.String()
	_ = s.Ensure(context.Background(), inputs)
	if out.String() != before {
		t.Errorf("second Ensure printed more: %q", out.String())
	}
}

func TestNewKubeClientConcurrent(_ *testing.T) {
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			// A bogus host makes client.New succeed lazily; we only care that
			// no shared scheme is mutated.
			_, _, _ = NewKubeClient(&rest.Config{Host: "http://127.0.0.1:1"})
		})
	}
	wg.Wait()
	// ponytail: no assertion; this test exists for -race, to catch shared scheme mutation.
}
