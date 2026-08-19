/*
Copyright 2024 The Crossplane Authors.

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

package validate

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/crane"
)

// anonymousKeychain returns authn.Anonymous for every request, and records that
// it was asked. The default keychain would consult the host's docker config: if
// that sets a credsStore, resolving credentials shells out to
// docker-credential-<store>, which is not on PATH under `nix run .#test`
// (nix/apps.nix sets inheritPath = false), so the tag lookup fails on a
// developer machine while passing in CI. internal/project/push_test.go carries
// the same helper for the push path.
//
// The used flag is what keeps this honest: if the option stops being threaded
// through to crane, this keychain is never consulted and the test fails on any
// host, with or without a docker config.
type anonymousKeychain struct {
	used atomic.Bool
}

func (k *anonymousKeychain) Resolve(authn.Resource) (authn.Authenticator, error) {
	k.used.Store(true)

	return authn.Anonymous, nil
}

func TestFindImageTagForVersionConstraint(t *testing.T) {
	repoName := "ubuntu"
	responseTags := []byte(`{"tags":["1.2.3","4.5.6"]}`)
	cases := map[string]struct {
		responseBody  []byte
		host          string
		constraint    string
		expectedImage string
		expectError   bool
	}{
		"NoConstraint": {
			responseBody:  responseTags,
			constraint:    "1.2.3",
			expectedImage: "ubuntu:1.2.3",
		},
		"Constraint": {
			responseBody:  responseTags,
			constraint:    ">=1.2.3",
			expectedImage: "ubuntu:4.5.6",
		},
		"ConstraintV": {
			responseBody:  responseTags,
			constraint:    ">=v1.2.3",
			expectedImage: "ubuntu:4.5.6",
		},
		"ConstraintPreRelease": {
			responseBody:  responseTags,
			constraint:    ">v4.5.6-rc.0.100.g658deda0.dirty",
			expectedImage: "ubuntu:4.5.6",
		},
		"CannotFetchTags": {
			responseBody: responseTags,
			host:         "wrong.host",
			constraint:   ">=4.5.6",
			expectError:  true,
		},
		"NoMatchingTag": {
			responseBody: responseTags,
			constraint:   ">4.5.6",
			expectError:  true,
		},
		"RangedConstraint": {
			responseBody:  responseTags,
			constraint:    ">=v2.0.0 <v5.0.0",
			expectedImage: "ubuntu:4.5.6",
		},
		"CommaSeparatedRangedConstraint": {
			responseBody:  responseTags,
			constraint:    ">=v2.0.0,<v5.0.0",
			expectedImage: "ubuntu:4.5.6",
		},
	}

	keychain := &anonymousKeychain{}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tagsPath := fmt.Sprintf("/v2/%s/tags/list", repoName)

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v2/":
					w.WriteHeader(http.StatusOK)
				case tagsPath:
					if r.Method != http.MethodGet {
						t.Errorf("Method; got %v, want %v", r.Method, http.MethodGet)
					}

					w.Write(tc.responseBody)
				default:
					t.Fatalf("Unexpected path: %v", r.URL.Path)
				}
			}))
			defer server.Close()

			u, err := url.Parse(server.URL)
			if err != nil {
				t.Fatalf("url.Parse(%v) = %v", server.URL, err)
			}

			host := u.Host
			if tc.host != "" {
				host = tc.host
			}

			image, err := findImageTagForVersionConstraint(
				fmt.Sprintf("%s/%s:%s", host, repoName, tc.constraint),
				crane.WithAuthFromKeychain(keychain),
			)

			expectedImage := ""
			if !tc.expectError {
				expectedImage = fmt.Sprintf("%s/%s", host, tc.expectedImage)
			}

			switch {
			case tc.expectError && err == nil:
				t.Errorf("[%s] expected: error\n", name)
			case !tc.expectError && err != nil:
				// Report the error rather than only the empty result: a
				// credential lookup that never reached the registry used to
				// read here as an unreachable registry.
				t.Errorf("[%s] unexpected error: %v\n", name, err)
			case expectedImage != image:
				t.Errorf("[%s] expected: %s, got: %s\n", name, expectedImage, image)
			}
		})
	}

	if !keychain.used.Load() {
		t.Error("tags were listed without the injected keychain; the lookup fell back to the host's docker config")
	}
}

func TestIsErrBaseLayerNotFound(t *testing.T) {
	type args struct {
		err error
	}

	tests := map[string]struct {
		reason string
		args   args
		want   bool
	}{
		"BaseLayerNotFound": {
			reason: "Should return true if the error is a BaseLayerNotFound error",
			args: args{
				err: NewBaseLayerNotFoundError("foo"),
			},
			want: true,
		},
		"NotBaseLayerNotFound": {
			reason: "Should return false if the error is not a BaseLayerNotFound error",
			args: args{
				err: fmt.Errorf("foo"),
			},
			want: false,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := IsErrBaseLayerNotFound(tt.args.err); got != tt.want {
				t.Errorf("IsErrBaseLayerNotFound() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSeparateImageTag(t *testing.T) {
	type args struct {
		image string
	}

	type want struct {
		imageBase string
		imageTag  string
	}

	tests := map[string]struct {
		args args
		want want
	}{
		"ImageWithDigest": {
			args: args{
				image: "my-registry:1234/crossplane/crossplane:v2.0.0@sha256:abc1234",
			},
			want: want{
				imageBase: "my-registry:1234/crossplane/crossplane:v2.0.0@sha256",
				imageTag:  "abc1234",
			},
		},
		"RegistryWithPort": {
			args: args{
				image: "my-registry:1234/crossplane/crossplane:v2.0.0",
			},
			want: want{
				imageBase: "my-registry:1234/crossplane/crossplane",
				imageTag:  "v2.0.0",
			},
		},
		"ColonSeparatedImage": {
			args: args{
				image: "ghcr.io/crossplane/crossplane:v2.0.0",
			},
			want: want{
				imageBase: "ghcr.io/crossplane/crossplane",
				imageTag:  "v2.0.0",
			},
		},
		"EmptyTag": {
			args: args{
				image: "ghcr.io/crossplane/crossplane:",
			},
			want: want{
				imageBase: "ghcr.io/crossplane/crossplane",
				imageTag:  "",
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			imageBase, imageTag := separateImageTag(tt.args.image)
			if imageBase != tt.want.imageBase || imageTag != tt.want.imageTag {
				t.Errorf("separateImageTag() want %v got %v", tt.want, want{
					imageBase: imageBase,
					imageTag:  imageTag,
				})
			}
		})
	}
}
