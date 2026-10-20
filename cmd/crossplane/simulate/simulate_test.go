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
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/crossplane/cli/v2/internal/diffserver"
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

			got, err := loadProviderInputs(path)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("%s\nloadProviderInputs(...): want an error containing %q, got %v", tc.reason, tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("%s\nloadProviderInputs(...): unexpected error: %v", tc.reason, err)
			}
			if diff := cmp.Diff(tc.want, got, cmp.Comparer(func(a, b netip.Addr) bool { return a == b })); diff != "" {
				t.Errorf("%s\nloadProviderInputs(...): -want, +got:\n%s", tc.reason, diff)
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
			if got := needsDocker(tc.inputs); got != tc.want {
				t.Errorf("%s\nneedsDocker(...): want %t, got %t", tc.reason, tc.want, got)
			}
		})
	}
}
