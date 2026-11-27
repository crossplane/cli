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
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	devv1alpha1 "github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/internal/testutils/xpkgtest"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"
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
			imgMap[cfgTag] = xpkgtest.Image(t, xpkgtest.WithPlatform("linux", "amd64"))

			for _, fnName := range tc.functionNames {
				fnRepo := fmt.Sprintf("%s_%s", tc.repo, fnName)
				for _, arch := range tc.archs {
					imgMap[mustTag(t, fmt.Sprintf("%s:%s", fnRepo, arch))] = xpkgtest.Image(t, xpkgtest.WithPlatform("linux", arch))
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

func TestNewPusherTransport(t *testing.T) {
	t.Parallel()

	type want struct {
		responseHeaderTimeout time.Duration
		sendPingTimeout       time.Duration
	}
	cases := map[string]struct {
		reason string
		want   want
	}{
		"Default": {
			reason: "NewPusher should default to a transport with the registry header timeout and HTTP/2 ping check, not http.DefaultTransport.",
			want: want{
				responseHeaderTimeout: clixpkg.RegistryResponseHeaderTimeout,
				sendPingTimeout:       clixpkg.RegistrySendPingTimeout,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			p, ok := NewPusher().(*realPusher)
			if !ok {
				t.Fatalf("NewPusher(): want *realPusher")
			}
			tr, ok := p.transport.(*http.Transport)
			if !ok {
				t.Fatalf("\n%s\nNewPusher(): want *http.Transport, got %T", tc.reason, p.transport)
			}
			if tr.ResponseHeaderTimeout != tc.want.responseHeaderTimeout {
				t.Errorf("\n%s\nResponseHeaderTimeout: want %s, got %s", tc.reason, tc.want.responseHeaderTimeout, tr.ResponseHeaderTimeout)
			}
			if tr.HTTP2 == nil || tr.HTTP2.SendPingTimeout != tc.want.sendPingTimeout {
				t.Errorf("\n%s\nHTTP2: want SendPingTimeout %s, got %+v", tc.reason, tc.want.sendPingTimeout, tr.HTTP2)
			}
		})
	}
}
