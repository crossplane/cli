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
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/spf13/afero"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"

	"github.com/crossplane/cli/v2/internal/terminal"
	"github.com/crossplane/cli/v2/internal/testutils/xpkgtest"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"
)

// stalledRegistry returns the host of a server that accepts every request and
// never writes response headers, like a registry connection gone silent.
func stalledRegistry(t *testing.T) string {
	t.Helper()
	stop := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-stop:
		case <-r.Context().Done():
		}
	}))
	// Cleanups run last-in first-out: unblock the handlers before Close waits
	// for them.
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(stop) })
	return strings.TrimPrefix(srv.URL, "http://")
}

func TestPushStalledRegistry(t *testing.T) {
	t.Parallel()

	type args struct {
		packages              int
		responseHeaderTimeout time.Duration
		timeout               time.Duration // Run's ctx deadline; 0 means none.
	}
	type want struct {
		errContains string
		errIs       error
		within      time.Duration
	}
	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"SinglePackageResponseHeaderTimeout": {
			reason: "Pushing one package to a registry that never sends response headers should fail once the transport's header timeout expires.",
			args:   args{packages: 1, responseHeaderTimeout: 100 * time.Millisecond},
			want: want{
				errContains: "timeout awaiting response headers",
				// httptest serves HTTP/1.1, where ggcr does not retry the
				// timeout; allow for its backoff anyway.
				within: 10 * time.Second,
			},
		},
		"MultiPackageResponseHeaderTimeout": {
			reason: "Pushing several packages to a registry that never sends response headers should fail once the transport's header timeout expires.",
			args:   args{packages: 2, responseHeaderTimeout: 100 * time.Millisecond},
			want: want{
				errContains: "timeout awaiting response headers",
				within:      10 * time.Second,
			},
		},
		"SinglePackageContextDeadline": {
			reason: "A deadline on Run's context should end a stalled single-package push before the transport's header timeout.",
			args:   args{packages: 1, responseHeaderTimeout: time.Hour, timeout: 200 * time.Millisecond},
			want:   want{errIs: context.DeadlineExceeded, within: 2 * time.Second},
		},
		"MultiPackageContextDeadline": {
			reason: "A deadline on Run's context should end a stalled multi-package push before the transport's header timeout.",
			args:   args{packages: 2, responseHeaderTimeout: time.Hour, timeout: 200 * time.Millisecond},
			want:   want{errIs: context.DeadlineExceeded, within: 2 * time.Second},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			files := make([]string, tc.args.packages)
			for i := range files {
				files[i] = writePackage(t)
			}

			c := &pushCmd{
				Package:               stalledRegistry(t) + "/pkg:v1",
				PackageFiles:          files,
				fs:                    afero.NewOsFs(),
				responseHeaderTimeout: tc.args.responseHeaderTimeout,
			}

			ctx := t.Context()
			if tc.args.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.args.timeout)
				defer cancel()
			}

			start := time.Now()
			err := c.Run(ctx, logging.NewNopLogger(), terminal.NewNopSpinnerPrinter())
			elapsed := time.Since(start)

			if err == nil || !strings.Contains(err.Error(), tc.want.errContains) {
				t.Errorf("\n%s\nRun(...): want error containing %q, got %v", tc.reason, tc.want.errContains, err)
			}
			if tc.want.errIs != nil && !errors.Is(err, tc.want.errIs) {
				t.Errorf("\n%s\nRun(...): want errors.Is(err, %v), got %v", tc.reason, tc.want.errIs, err)
			}
			if elapsed > tc.want.within {
				t.Errorf("\n%s\nRun(...): took %s, want within %s", tc.reason, elapsed, tc.want.within)
			}
		})
	}
}

// writePackage writes a random single-layer image to a tarball and returns
// its path.
func writePackage(t *testing.T) string {
	t.Helper()
	img, err := random.Image(64, 1)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "pkg.xpkg")
	if err := tarball.WriteToFile(path, name.MustParseReference("example.com/pkg:v1"), img); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestPushImagesDefaultTransport swaps defaultPushTransport, so it can't run
// in parallel with other tests that push with no options.
func TestPushImagesDefaultTransport(t *testing.T) { //nolint:paralleltest // Mutates defaultPushTransport.
	type args struct {
		responseHeaderTimeout time.Duration
	}
	type want struct {
		errContains string
		within      time.Duration
	}
	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"ResponseHeaderTimeout": {
			reason: "pushImages with no options (xpkg batch) should push with defaultPushTransport, so a registry that never sends response headers fails once its header timeout expires.",
			args:   args{responseHeaderTimeout: 100 * time.Millisecond},
			want: want{
				errContains: "timeout awaiting response headers",
				within:      10 * time.Second,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			orig := defaultPushTransport
			defaultPushTransport = clixpkg.NewRegistryTransport(tc.args.responseHeaderTimeout, time.Second)
			t.Cleanup(func() { defaultPushTransport = orig })

			img, err := random.Image(64, 1)
			if err != nil {
				t.Fatal(err)
			}

			start := time.Now()
			err = pushImages(t.Context(), logging.NewNopLogger(), nil, []packageImage{{Image: img}}, stalledRegistry(t)+"/pkg:v1", nil)
			elapsed := time.Since(start)

			if err == nil || !strings.Contains(err.Error(), tc.want.errContains) {
				t.Errorf("\n%s\npushImages(...): want error containing %q, got %v", tc.reason, tc.want.errContains, err)
			}
			if elapsed > tc.want.within {
				t.Errorf("\n%s\npushImages(...): took %s, want within %s", tc.reason, elapsed, tc.want.within)
			}
		})
	}
}

// newTestRegistry serves an in-memory OCI registry on a loopback address, which
// go-containerregistry talks to over plain HTTP. The optional middleware can
// intercept requests before they reach the registry.
func newTestRegistry(t *testing.T, middleware func(http.ResponseWriter, *http.Request) bool) string {
	t.Helper()
	reg := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if middleware != nil && middleware(w, r) {
			return
		}
		reg.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// pushed describes what a test found in the registry under a tag.
type pushed struct {
	// Annotations on the manifest (or index) the tag points to.
	Annotations map[string]string
	// Platforms of the index manifests; empty when the tag is a single image.
	Platforms []string
	// LayerAnnotations of every layer of every pushed image manifest, in
	// order.
	LayerAnnotations [][]map[string]string
}

func inspectTag(t *testing.T, ref string) pushed {
	t.Helper()
	tag, err := name.NewTag(ref)
	if err != nil {
		t.Fatal(err)
	}
	desc, err := remote.Get(tag)
	if err != nil {
		t.Fatalf("remote.Get(%s): %v", ref, err)
	}

	layerAnns := func(img v1.Image) []map[string]string {
		m, err := img.Manifest()
		if err != nil {
			t.Fatal(err)
		}
		anns := make([]map[string]string, 0, len(m.Layers))
		for _, l := range m.Layers {
			anns = append(anns, l.Annotations)
		}
		return anns
	}

	if !desc.MediaType.IsIndex() {
		img, err := desc.Image()
		if err != nil {
			t.Fatal(err)
		}
		m, err := img.Manifest()
		if err != nil {
			t.Fatal(err)
		}
		return pushed{Annotations: m.Annotations, LayerAnnotations: [][]map[string]string{layerAnns(img)}}
	}

	idx, err := desc.ImageIndex()
	if err != nil {
		t.Fatal(err)
	}
	im, err := idx.IndexManifest()
	if err != nil {
		t.Fatal(err)
	}
	got := pushed{Annotations: im.Annotations}
	for _, d := range im.Manifests {
		got.Platforms = append(got.Platforms, d.Platform.OS+"/"+d.Platform.Architecture)
		img, err := idx.Image(d.Digest)
		if err != nil {
			t.Fatal(err)
		}
		got.LayerAnnotations = append(got.LayerAnnotations, layerAnns(img))
	}
	return got
}

func TestPushImages(t *testing.T) {
	host := newTestRegistry(t, nil)
	pkgLayer := map[string]string{xpkg.AnnotationKey: xpkg.PackageAnnotation}

	type args struct {
		platforms   [][2]string
		url         string
		annotations map[string]string
	}
	type want struct {
		pushed pushed
		err    error
	}

	_, errBadTag := name.NewTag("not a tag", name.StrictValidation)

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"SingleImage": {
			reason: "A single package should be pushed as an image manifest at the tag, with layer and manifest annotations.",
			args: args{
				platforms:   [][2]string{{"linux", "amd64"}},
				url:         host + "/org/single:v1",
				annotations: map[string]string{"a": "b"},
			},
			want: want{pushed: pushed{
				Annotations:      map[string]string{"a": "b"},
				LayerAnnotations: [][]map[string]string{{pkgLayer}},
			}},
		},
		"MultiPlatform": {
			reason: "Multiple packages should be pushed by digest and tagged as an index carrying each platform and the annotations.",
			args: args{
				platforms:   [][2]string{{"linux", "amd64"}, {"linux", "arm64"}},
				url:         host + "/org/multi:v1",
				annotations: map[string]string{"a": "b"},
			},
			want: want{pushed: pushed{
				Annotations:      map[string]string{"a": "b"},
				Platforms:        []string{"linux/amd64", "linux/arm64"},
				LayerAnnotations: [][]map[string]string{{pkgLayer}, {pkgLayer}},
			}},
		},
		"InvalidTag": {
			reason: "A URL that isn't a fully qualified tag should be rejected before pushing.",
			args: args{
				platforms: [][2]string{{"linux", "amd64"}},
				url:       "not a tag",
			},
			want: want{err: errors.Wrapf(errBadTag, errFmtNewTag, "not a tag")},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			imgs := make([]packageImage, 0, len(tc.args.platforms))
			for _, p := range tc.args.platforms {
				imgs = append(imgs, packageImage{Image: xpkgtest.Image(t, xpkgtest.WithPlatform(p[0], p[1]), xpkgtest.WithPackageLabel()), Path: p[1]})
			}

			err := pushImages(t.Context(), logging.NewNopLogger(), terminal.NewNopSpinnerPrinter(), imgs, tc.args.url, tc.args.annotations, remote.WithAuth(authn.Anonymous))
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Fatalf("\n%s\npushImages(...): -want error, +got error:\n%s", tc.reason, diff)
			}
			if tc.want.err != nil {
				return
			}
			if diff := cmp.Diff(tc.want.pushed, inspectTag(t, tc.args.url)); diff != "" {
				t.Errorf("\n%s\npushImages(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestPushImagesRegistryError(t *testing.T) {
	// Reject the first upload of each image manifest, which is pushed by digest
	// for a multi-platform package, and every manifest upload to org/single.
	// A later upload of the same digest succeeds, so if pushImages went on to
	// write the index after a per-platform failure the index would land and the
	// Head check below would catch it. 403 is not retried by go-containerregistry.
	var rejected sync.Map
	host := newTestRegistry(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != http.MethodPut {
			return false
		}
		if strings.HasPrefix(r.URL.Path, "/v2/org/single/manifests/") {
			w.WriteHeader(http.StatusForbidden)
			return true
		}
		if strings.Contains(r.URL.Path, "/manifests/sha256:") {
			if _, seen := rejected.LoadOrStore(r.URL.Path, true); !seen {
				w.WriteHeader(http.StatusForbidden)
				return true
			}
		}
		return false
	})

	cases := map[string]struct {
		reason    string
		platforms [][2]string
		repo      string
		want      string
	}{
		"SingleImage": {
			reason:    "A failed single-image push should name the package path.",
			platforms: [][2]string{{"linux", "amd64"}},
			repo:      "single",
			want:      "failed to push package file amd64",
		},
		"MultiPlatform": {
			reason:    "A failed per-platform push should fail the whole push without writing an index, even though a retry of the same manifests would succeed.",
			platforms: [][2]string{{"linux", "amd64"}, {"linux", "arm64"}},
			repo:      "multi",
			want:      "failed to push package file",
		},
	}
	for caseName, tc := range cases {
		t.Run(caseName, func(t *testing.T) {
			imgs := make([]packageImage, 0, len(tc.platforms))
			for _, p := range tc.platforms {
				imgs = append(imgs, packageImage{Image: xpkgtest.Image(t, xpkgtest.WithPlatform(p[0], p[1]), xpkgtest.WithPackageLabel()), Path: p[1]})
			}
			ref := host + "/org/" + tc.repo + ":v1"
			err := pushImages(t.Context(), logging.NewNopLogger(), nil, imgs, ref, nil, remote.WithAuth(authn.Anonymous))
			// The cause is ggcr's transport error, which embeds the registry's
			// random port and, for multi-platform, the manifest digest.
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("\n%s\npushImages(...): want error containing %q, got %v", tc.reason, tc.want, err)
			}
			r, err := name.ParseReference(ref)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := remote.Head(r); err == nil {
				t.Errorf("\n%s\npushImages(...): want nothing at %s, but a manifest was written", tc.reason, ref)
			}
		})
	}
}

func TestPushCmdRun(t *testing.T) {
	// Use an empty Docker config so the default keychain doesn't consult the
	// host's credential helpers.
	t.Setenv("DOCKER_CONFIG", t.TempDir())

	host := newTestRegistry(t, nil)

	type args struct {
		// files to write to the working directory, by platform arch.
		files       []string
		packageArgs []string
		annotations []string
	}
	type want struct {
		pushed pushed
		err    error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"FindsPackageInWorkingDir": {
			reason: "With no --package-files the single xpkg in the working directory should be pushed.",
			args: args{
				files:       []string{"amd64"},
				annotations: []string{"org.opencontainers.image.source=https://example.org"},
			},
			want: want{pushed: pushed{
				Annotations:      map[string]string{"org.opencontainers.image.source": "https://example.org"},
				LayerAnnotations: [][]map[string]string{{{xpkg.AnnotationKey: xpkg.PackageAnnotation}}},
			}},
		},
		"MultiplePackageFiles": {
			reason: "Explicit package files should be pushed together as a multi-platform index.",
			args: args{
				files:       []string{"amd64", "arm64"},
				packageArgs: []string{"amd64.xpkg", "arm64.xpkg"},
			},
			want: want{pushed: pushed{
				Platforms: []string{"linux/amd64", "linux/arm64"},
				LayerAnnotations: [][]map[string]string{
					{{xpkg.AnnotationKey: xpkg.PackageAnnotation}},
					{{xpkg.AnnotationKey: xpkg.PackageAnnotation}},
				},
			}},
		},
		"InvalidAnnotation": {
			reason: "A malformed --oci-annotation should fail before anything is read.",
			args:   args{annotations: []string{"novalue"}},
			want:   want{err: errors.Wrap(errors.New(`invalid annotation "novalue": must be in key=value format`), errParseAnnotations)},
		},
		"NoPackageInWorkingDir": {
			reason: "With no --package-files and no xpkg in the working directory the command should fail.",
			want:   want{err: errors.Wrap(errors.New("directory does not contain a compiled crossplane package"), errFindPackageinWd)},
		},
		"UnreadablePackageFile": {
			reason: "A package file that isn't an image tarball should fail to read, naming the file.",
			args:   args{packageArgs: []string{"crossplane.yaml"}},
			want:   want{err: errors.Wrapf(io.ErrUnexpectedEOF, errFmtReadPackage, "crossplane.yaml")},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			for _, arch := range tc.args.files {
				if err := tarball.WriteToFile(filepath.Join(dir, arch+".xpkg"), nil, xpkgtest.Image(t, xpkgtest.WithPlatform("linux", arch), xpkgtest.WithPackageLabel())); err != nil {
					t.Fatal(err)
				}
			}
			if err := afero.WriteFile(afero.NewOsFs(), filepath.Join(dir, "crossplane.yaml"), []byte("not a tarball"), 0o600); err != nil {
				t.Fatal(err)
			}

			ref := host + "/org/" + strings.ToLower(name) + ":v1"
			c := &pushCmd{Package: ref, PackageFiles: tc.args.packageArgs, OCIAnnotation: tc.args.annotations}
			if err := c.AfterApply(); err != nil {
				t.Fatal(err)
			}

			err := c.Run(t.Context(), logging.NewNopLogger(), terminal.NewNopSpinnerPrinter())
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Fatalf("\n%s\nRun(...): -want error, +got error:\n%s", tc.reason, diff)
			}
			if tc.want.err != nil {
				return
			}
			if diff := cmp.Diff(tc.want.pushed, inspectTag(t, ref)); diff != "" {
				t.Errorf("\n%s\nRun(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}
