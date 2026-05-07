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
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	devv1alpha1 "github.com/crossplane/cli/v2/apis/dev/v1alpha1"
)

func TestPusherPush(t *testing.T) {
	t.Parallel()

	regSrv, err := registry.TLS("localhost")
	if err != nil {
		t.Fatalf("failed to start test registry: %v", err)
	}
	t.Cleanup(regSrv.Close)
	transport := regSrv.Client().Transport
	regHost := strings.TrimPrefix(regSrv.URL, "https://")

	cases := map[string]struct {
		repo          string
		functionNames []string
		archs         []string
	}{
		"ConfigurationOnly": {
			repo:          regHost + "/unittest/demo-project",
			functionNames: nil,
			archs:         []string{"amd64", "arm64"},
		},
		"EmbeddedFunctions": {
			repo:          regHost + "/unittest/embedded-functions",
			functionNames: []string{"fn"},
			archs:         []string{"amd64", "arm64"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			imgMap := make(ImageTagMap)

			cfgTag := mustTag(t, fmt.Sprintf("%s:%s", tc.repo, ConfigurationTag))
			imgMap[cfgTag] = mustRandomImage(t, "amd64", "linux")

			for _, fnName := range tc.functionNames {
				fnRepo := fmt.Sprintf("%s_%s", tc.repo, fnName)
				for _, arch := range tc.archs {
					imgMap[mustTag(t, fmt.Sprintf("%s:%s", fnRepo, arch))] = mustRandomImage(t, arch, "linux")
				}
			}

			proj := &devv1alpha1.Project{
				Spec: devv1alpha1.ProjectSpec{
					Repository: tc.repo,
				},
			}

			pusher := NewPusher(
				PushWithTransport(transport),
				PushWithAuthKeychain(anonymousKeychain{}),
				PushWithMaxConcurrency(2),
			)

			gotTag, err := pusher.Push(t.Context(), proj, imgMap, PushWithTag("v0.0.3"))
			if err != nil {
				t.Fatalf("Push: %v", err)
			}

			wantTag := mustTag(t, fmt.Sprintf("%s:%s", tc.repo, "v0.0.3"))
			if diff := cmp.Diff(wantTag.String(), gotTag.String()); diff != "" {
				t.Errorf("Push returned unexpected tag (-want +got):\n%s", diff)
			}

			if _, err := remote.Image(wantTag, remote.WithTransport(transport)); err != nil {
				t.Errorf("failed to pull configuration tag %s: %v", wantTag, err)
			}

			for _, fnName := range tc.functionNames {
				fnTag := mustTag(t, fmt.Sprintf("%s_%s:%s", tc.repo, fnName, "v0.0.3"))
				idx, err := remote.Index(fnTag, remote.WithTransport(transport))
				if err != nil {
					t.Errorf("failed to pull function index %s: %v", fnTag, err)
					continue
				}
				mfst, err := idx.IndexManifest()
				if err != nil {
					t.Errorf("failed to read index manifest for %s: %v", fnTag, err)
					continue
				}
				if diff := cmp.Diff(len(tc.archs), len(mfst.Manifests)); diff != "" {
					t.Errorf("unexpected manifest count for %s (-want +got):\n%s", fnTag, diff)
				}
			}
		})
	}
}

// anonymousKeychain returns authn.Anonymous for every request. Using a real
// keychain in tests would consult the host's docker config; we want to avoid
// that.
type anonymousKeychain struct{}

func (anonymousKeychain) Resolve(authn.Resource) (authn.Authenticator, error) {
	return authn.Anonymous, nil
}

func mustTag(t *testing.T, ref string) name.Tag {
	t.Helper()
	tag, err := name.NewTag(ref, name.StrictValidation)
	if err != nil {
		t.Fatalf("failed to parse tag %q: %v", ref, err)
	}
	return tag
}

func mustRandomImage(t *testing.T, arch, os string) v1.Image {
	t.Helper()
	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatalf("failed to build random image: %v", err)
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		t.Fatalf("failed to read config file: %v", err)
	}
	cfg = cfg.DeepCopy()
	cfg.Architecture = arch
	cfg.OS = os
	img, err = mutate.ConfigFile(img, cfg)
	if err != nil {
		t.Fatalf("failed to set config file: %v", err)
	}
	return img
}
