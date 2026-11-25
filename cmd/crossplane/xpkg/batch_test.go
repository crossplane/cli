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

package xpkg

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/spf13/afero"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"

	"github.com/crossplane/cli/v2/internal/testutils/xpkgtest"
)

// newBatchFixture copies the family provider source tree in
// testdata/batch/provider to a temp dir: a metadata template and CRDs for the
// ec2 and s3 services. It adds an ec2 provider binary for each platform. It returns a batchCmd rooted there.
func newBatchFixture(t *testing.T, platforms []string, urlFormat string) *batchCmd {
	t.Helper()
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS("testdata/batch/provider")); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, p := range platforms {
		files[filepath.Join("bin", p, "ec2")] = "ec2-binary-" + p
	}
	writeFiles(t, root, files)

	return &batchCmd{
		fs:                      afero.NewOsFs(),
		ProviderName:            "provider-test",
		FamilyPackageURLFormat:  urlFormat,
		Platform:                platforms,
		ProviderBinRoot:         filepath.Join(root, "bin"),
		PackageMetadataTemplate: filepath.Join(root, "package/crossplane.yaml.tmpl"),
		CRDRoot:                 filepath.Join(root, "package/crds"),
		ExamplesRoot:            filepath.Join(root, "examples"),
		OutputDir:               filepath.Join(root, "out"),
	}
}

// openErr returns the error os.Open gives for a missing path, so expected
// errors carry the platform's own message.
func openErr(path string) error {
	_, err := os.Open(path)
	return err
}

func testBaseImgMap(t *testing.T, platforms []string) map[string]v1.Image {
	t.Helper()
	m := make(map[string]v1.Image, len(platforms))
	for _, p := range platforms {
		goos, arch, _ := strings.Cut(p, "_")
		m[p] = xpkgtest.Image(t, xpkgtest.WithPlatform(goos, arch))
	}
	return m
}

func TestBatchProcessService(t *testing.T) {
	// Use an empty Docker config so the default keychain doesn't consult the
	// host's credential helpers.
	t.Setenv("DOCKER_CONFIG", t.TempDir())

	host := newTestRegistry(t, nil)
	platforms := []string{"linux_amd64", "linux_arm64"}

	type args struct {
		buildOnly bool
		store     bool
	}
	type want struct {
		pushed pushed
		stored []string
	}

	pkgLayer := map[string]string{xpkg.AnnotationKey: xpkg.PackageAnnotation}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"PushMultiArch": {
			reason: "The service package should be pushed as a multi-arch index whose images each have the unlabelled base layer, exactly one package layer, and the unlabelled provider binary layer. The second platform reuses the first platform's addendum layers and labels.",
			want: want{pushed: pushed{
				Platforms:        []string{"linux/amd64", "linux/arm64"},
				LayerAnnotations: [][]map[string]string{{nil, pkgLayer, nil}, {nil, pkgLayer, nil}},
			}},
		},
		"BuildOnlyAndStore": {
			reason: "With --build-only and --store-packages the package should be written per platform and not pushed.",
			args:   args{buildOnly: true, store: true},
			want: want{stored: []string{
				"linux_amd64/provider-test-ec2-v1.xpkg",
				"linux_arm64/provider-test-ec2-v1.xpkg",
			}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := newBatchFixture(t, platforms, host+"/"+strings.ToLower(name)+"/%s:v1")
			c.BuildOnly = tc.args.buildOnly
			if tc.args.store {
				c.StorePackages = []string{"ec2"}
				for _, p := range platforms {
					if err := os.MkdirAll(filepath.Join(c.OutputDir, p), 0o750); err != nil {
						t.Fatal(err)
					}
				}
			}

			if err := c.processService(t.Context(), logging.NewNopLogger(), testBaseImgMap(t, platforms), "ec2"); err != nil {
				t.Fatalf("\n%s\nprocessService(...): %v", tc.reason, err)
			}

			var stored []string
			for _, p := range platforms {
				m, err := filepath.Glob(filepath.Join(c.OutputDir, p, "*.xpkg"))
				if err != nil {
					t.Fatal(err)
				}
				for _, f := range m {
					rel, err := filepath.Rel(c.OutputDir, f)
					if err != nil {
						t.Fatal(err)
					}
					stored = append(stored, rel)

					// The stored package must hold only the ec2 CRD and the binary.
					files, pkg := packageContents(t, f)
					if !strings.Contains(pkg, "ec2.example.org") || strings.Contains(pkg, "s3.example.org") {
						t.Errorf("\n%s\nprocessService(...): package.yaml should contain only the ec2 CRD:\n%s", tc.reason, pkg)
					}
					if !slices.Contains(files, "/usr/local/bin/provider") {
						t.Errorf("\n%s\nprocessService(...): package should contain the provider binary, got %v", tc.reason, files)
					}
				}
			}
			if diff := cmp.Diff(tc.want.stored, stored); diff != "" {
				t.Errorf("\n%s\nprocessService(...): stored packages -want, +got:\n%s", tc.reason, diff)
			}

			if tc.args.buildOnly {
				return
			}
			if diff := cmp.Diff(tc.want.pushed, inspectTag(t, c.getPackageURL("ec2"))); diff != "" {
				t.Errorf("\n%s\nprocessService(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestBatchProcessServiceErrors(t *testing.T) {
	platforms := []string{"linux_amd64"}

	type args struct {
		service string
		mutate  func(t *testing.T, c *batchCmd)
	}
	type want struct {
		// err builds the expected error from the fixture's batchCmd, whose
		// paths are under a per-test temp dir.
		err func(c *batchCmd) error
		// errPrefix is our own wrap of a cause we don't control.
		errPrefix string
	}
	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"MissingProviderBinary": {
			reason: "A service without a provider binary for the platform should fail.",
			args:   args{service: "s3"},
			want: want{err: func(c *batchCmd) error {
				p := filepath.Join(c.ProviderBinRoot, "linux_amd64", "s3")
				return errors.Wrapf(openErr(p), errReadProviderBinFmt, "s3", "linux_amd64", p)
			}},
		},
		"InvalidTemplate": {
			reason: "A metadata template that can't be read should fail.",
			args: args{
				service: "ec2",
				mutate:  func(_ *testing.T, c *batchCmd) { c.PackageMetadataTemplate = filepath.Join(c.CRDRoot, "missing.tmpl") },
			},
			want: want{err: func(c *batchCmd) error {
				return errors.Wrap(openErr(c.PackageMetadataTemplate), errInvalidTemplate)
			}},
		},
		"LintFailure": {
			reason: "Configuration metadata should make the configuration linter reject the provider's CRDs.",
			args: args{
				service: "ec2",
				mutate: func(t *testing.T, c *batchCmd) {
					t.Helper()
					meta, err := os.ReadFile("testdata/package/configuration/crossplane.yaml")
					if err != nil {
						t.Fatal(err)
					}
					// The backend appends the CRDs with no separator, so end the document.
					meta = append(meta, "---\n"...)
					if err := os.WriteFile(c.PackageMetadataTemplate, meta, 0o600); err != nil {
						t.Fatal(err)
					}
				},
			},
			// The cause lists crossplane-runtime's linter messages.
			want: want{errPrefix: "failed to build smaller provider package: ec2: failed to lint package: "},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := newBatchFixture(t, platforms, "example.org/%s:v1")
			c.BuildOnly = true
			if tc.args.mutate != nil {
				tc.args.mutate(t, c)
			}
			err := c.processService(t.Context(), logging.NewNopLogger(), testBaseImgMap(t, platforms), tc.args.service)
			if tc.want.err != nil {
				if diff := cmp.Diff(tc.want.err(c), err, test.EquateErrors()); diff != "" {
					t.Errorf("\n%s\nprocessService(...): -want error, +got error:\n%s", tc.reason, diff)
				}
				if !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("\n%s\nprocessService(...): want errors.Is(err, fs.ErrNotExist), got %v", tc.reason, err)
				}
				return
			}
			// Only our wrap prefix is ours; see the case for the cause.
			if err == nil || !strings.HasPrefix(err.Error(), tc.want.errPrefix) {
				t.Errorf("\n%s\nprocessService(...): want error starting %q, got %v", tc.reason, tc.want.errPrefix, err)
			}
		})
	}
}

func TestBatchPushWithRetry(t *testing.T) {
	// Use an empty Docker config so the default keychain doesn't consult the
	// host's credential helpers.
	t.Setenv("DOCKER_CONFIG", t.TempDir())

	type args struct {
		failures int32
		retries  uint
	}
	cases := map[string]struct {
		reason string
		args   args
		err    string
	}{
		"SucceedsAfterRetry": {
			reason: "A push that fails fewer times than the retry count should succeed.",
			args:   args{failures: 2, retries: 3},
		},
		"ExhaustsRetries": {
			reason: "A push that keeps failing should return an error after retries are exhausted.",
			args:   args{failures: 100, retries: 1},
			err:    `failed to push smaller provider package: ec2`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var remaining atomic.Int32
			remaining.Store(tc.args.failures)
			var attempts atomic.Int32
			host := newTestRegistry(t, func(w http.ResponseWriter, r *http.Request) bool {
				if r.Method != http.MethodPut || !strings.Contains(r.URL.Path, "/manifests/") {
					return false
				}
				attempts.Add(1)
				if remaining.Add(-1) >= 0 {
					w.WriteHeader(http.StatusForbidden)
					return true
				}
				return false
			})

			c := &batchCmd{ProviderName: "provider-test", FamilyPackageURLFormat: host + "/org/%s:v1", PushRetry: tc.args.retries}
			imgs := []packageImage{{Image: xpkgtest.Image(t, xpkgtest.WithPlatform("linux", "amd64"), xpkgtest.WithPackageLabel()), Path: "ec2-linux_amd64"}}

			err := c.pushWithRetry(t.Context(), logging.NewNopLogger(), imgs, "ec2")
			if tc.err != "" {
				// The cause is ggcr's 403 transport error, which embeds the
				// registry's random port; match only our wrap.
				if err == nil || !strings.HasPrefix(err.Error(), tc.err) {
					t.Fatalf("\n%s\npushWithRetry(...): want error starting %q, got %v", tc.reason, tc.err, err)
				}
				if got, want := attempts.Load(), int32(tc.args.retries+1); got != want {
					t.Errorf("\n%s\npushWithRetry(...): want %d attempts, got %d", tc.reason, want, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("\n%s\npushWithRetry(...): %v", tc.reason, err)
			}
			if got, want := attempts.Load(), tc.args.failures+1; got != want {
				t.Errorf("\n%s\npushWithRetry(...): want %d attempts, got %d", tc.reason, want, got)
			}
			inspectTag(t, host+"/org/provider-test-ec2:v1")
		})
	}
}

func TestBatchGetPackageURL(t *testing.T) {
	cases := map[string]struct {
		reason string
		c      batchCmd
		want   string
	}{
		"Default": {
			reason: "The repository should be <provider>-<service>.",
			c:      batchCmd{ProviderName: "provider-aws", FamilyPackageURLFormat: "xpkg.example.org/org/%s:v1"},
			want:   "xpkg.example.org/org/provider-aws-ec2:v1",
		},
		"Suffix": {
			reason: "A push suffix should go between the provider name and the service.",
			c:      batchCmd{ProviderName: "provider-aws", FamilyPackageURLFormat: "xpkg.example.org/org/%s:v1", ProviderNameSuffixForPush: "dev"},
			want:   "xpkg.example.org/org/provider-aws-dev-ec2:v1",
		},
		"SuffixWithOverride": {
			reason: "A push suffix should be appended to an overridden repository name.",
			c: batchCmd{
				ProviderName: "provider-aws", FamilyPackageURLFormat: "xpkg.example.org/org/%s:v1",
				ProviderNameSuffixForPush: "dev", PackageRepoOverride: map[string]string{"ec2": "provider-family-aws"},
			},
			want: "xpkg.example.org/org/provider-family-aws-dev:v1",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, tc.c.getPackageURL("ec2")); diff != "" {
				t.Errorf("\n%s\ngetPackageURL(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestBatchGetPackageVersion(t *testing.T) {
	cases := map[string]struct {
		reason string
		format string
		want   string
	}{
		"Tagged": {
			reason: "The tag after the last colon should be the version, even with a registry port.",
			format: "localhost:5000/org/%s:v1.2.3",
			want:   "v1.2.3",
		},
		"Untagged": {
			reason: "A URL format without a tag should default to latest.",
			format: "org/%s",
			want:   tagLatest,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := &batchCmd{FamilyPackageURLFormat: tc.format}
			if diff := cmp.Diff(tc.want, c.getPackageVersion()); diff != "" {
				t.Errorf("\n%s\ngetPackageVersion(): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestBatchOverrides(t *testing.T) {
	c := &batchCmd{
		ExamplesRoot:          "examples",
		ExamplesGroupOverride: map[string]string{"all": wildcard, "renamed": "other"},
		CRDGroupOverride:      map[string]string{"all": wildcard, "renamed": "other"},
	}
	cases := map[string]struct {
		reason       string
		service      string
		wantExamples string
		wantCRD      string
	}{
		"Default": {
			reason:       "Without overrides the service name should select examples and CRDs.",
			service:      "ec2",
			wantExamples: filepath.Join("examples", "ec2"),
			wantCRD:      "ec2.",
		},
		"Wildcard": {
			reason:       "A wildcard override should select all examples and CRDs.",
			service:      "all",
			wantExamples: "examples",
			wantCRD:      "",
		},
		"Renamed": {
			reason:       "A named override should replace the service name.",
			service:      "renamed",
			wantExamples: filepath.Join("examples", "other"),
			wantCRD:      "other.",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if diff := cmp.Diff(tc.wantExamples, c.getExamplesGroup(tc.service)); diff != "" {
				t.Errorf("\n%s\ngetExamplesGroup(...): -want, +got:\n%s", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.wantCRD, c.getCRDPrefix(tc.service)); diff != "" {
				t.Errorf("\n%s\ngetCRDPrefix(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestMakeBaseImgMapInvalidPlatform(t *testing.T) {
	// Valid platforms resolve images from the Docker daemon, so only the
	// validation path is tested here.
	_, err := makeBaseImgMap(t.Context(), []string{"linux-amd64"}, "example.org/base")
	want := errors.Errorf(errInvalidPlatformFmt, "linux-amd64")
	if diff := cmp.Diff(want, err, test.EquateErrors()); diff != "" {
		t.Errorf("makeBaseImgMap(...): -want error, +got error:\n%s", diff)
	}
}
