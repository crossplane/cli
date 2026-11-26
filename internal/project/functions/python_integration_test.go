//go:build integration

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

package functions

import (
	"context"
	"crypto/rand"
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/moby/moby/client"
	"github.com/spf13/afero"
	"golang.org/x/sync/errgroup"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"

	"github.com/crossplane/cli/v2/internal/docker"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"
	"github.com/crossplane/cli/v2/pkg/images"
)

// TestPythonBuildDeterministic builds the same python function twice, in two
// separate build containers, and requires bit-identical images. A build whose
// output varies for unchanged inputs changes the function's digest, which
// rolls out a new function revision for a no-op rebuild (the venv tar's
// wall-clock mtimes and the mtime the compiled bytecode embeds did exactly
// that).
func TestPythonBuildDeterministic(t *testing.T) {
	t.Parallel()

	if err := docker.Check(t.Context()); err != nil {
		t.Skipf("python builds need a Docker-compatible runtime: %v", err)
	}

	baseImageRef, transport := pushPythonTestBaseImage(t)
	b := &pythonBuilder{
		buildImage:     images.Image(images.Python, images.ImageBuild),
		runtimeImage:   baseImageRef.String(),
		transport:      transport,
		configStore:    clixpkg.NewStaticImageConfigStore(nil),
		pipCacheVolume: testPipCacheVolume(t),
	}
	bc := BuildContext{
		ProjectFS:     afero.NewBasePathFs(afero.NewOsFs(), "testdata"),
		FunctionPath:  "python-function",
		Architectures: []string{"amd64"},
	}

	digests := make([]string, 2)
	for i := range digests {
		d, err := pythonBuildDigest(context.Background(), b, bc)
		if err != nil {
			t.Fatalf("Build() %d failed: %v", i, err)
		}
		digests[i] = d
	}

	if digests[0] != digests[1] {
		t.Errorf("Build() is not deterministic: rebuilding an unchanged function produced %s, then %s", digests[0], digests[1])
	}
}

// TestPythonBuildPipCache builds a python function that depends on a package
// from PyPI, first twice at once on an empty pip cache volume, then again once
// the cache is warm. Every build must succeed and produce the same image: the
// cache may save downloads, but never change what gets installed.
func TestPythonBuildPipCache(t *testing.T) {
	t.Parallel()

	if err := docker.Check(t.Context()); err != nil {
		t.Skipf("python builds need a Docker-compatible runtime: %v", err)
	}

	baseImageRef, transport := pushPythonTestBaseImage(t)
	b := &pythonBuilder{
		buildImage:     images.Image(images.Python, images.ImageBuild),
		runtimeImage:   baseImageRef.String(),
		transport:      transport,
		configStore:    clixpkg.NewStaticImageConfigStore(nil),
		pipCacheVolume: testPipCacheVolume(t),
	}
	bc := BuildContext{
		ProjectFS:     afero.NewBasePathFs(afero.NewOsFs(), "testdata"),
		FunctionPath:  "python-function-deps",
		Architectures: []string{"amd64"},
	}

	// A project's functions build concurrently, so their containers can all
	// mount the cache volume before it exists.
	cold := make([]string, 2)
	eg, ctx := errgroup.WithContext(t.Context())
	for i := range cold {
		eg.Go(func() error {
			d, err := pythonBuildDigest(ctx, b, bc)
			cold[i] = d
			return err
		})
	}
	if err := eg.Wait(); err != nil {
		t.Fatalf("concurrent Build() on an empty pip cache failed: %v", err)
	}

	// Unless pip wrote to the volume, the build below isn't a warm one.
	cid, err := docker.StartContainer(t.Context(), "", b.buildImage,
		docker.StartWithVolumeMount(b.pipCacheVolume, pythonPipCacheDir),
		docker.StartWithCommand([]string{"sh", "-c", `find "$0" -type f | grep -q .`, pythonPipCacheDir}),
	)
	if err != nil {
		t.Fatal(err)
	}
	err = docker.WaitForContainerByID(t.Context(), cid)
	_ = docker.StopContainerByID(context.Background(), cid)
	if err != nil {
		t.Fatalf("pip cache volume is empty after building: %v", err)
	}

	warm, err := pythonBuildDigest(t.Context(), b, bc)
	if err != nil {
		t.Fatalf("Build() with a warm pip cache failed: %v", err)
	}

	if cold[0] != cold[1] || cold[0] != warm {
		t.Errorf("Build() output depends on the pip cache: concurrent cold builds produced %s and %s, then a warm build produced %s", cold[0], cold[1], warm)
	}
}

// pushPythonTestBaseImage pushes a stand-in for the distroless python runtime
// base to a throwaway registry, and returns its reference and the transport to
// reach it with. Its content doesn't matter, only that the venv layer appended
// onto it is reproducible.
func pushPythonTestBaseImage(t *testing.T) (name.Reference, http.RoundTripper) {
	t.Helper()

	regSrv, err := registry.TLS("localhost")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(regSrv.Close)
	testRegistry, err := name.NewRegistry(strings.TrimPrefix(regSrv.URL, "https://"))
	if err != nil {
		t.Fatal(err)
	}

	baseImageRef := testRegistry.Repo("unittest-base-image").Tag("latest")
	baseImage, err := mutate.ConfigFile(empty.Image, &v1.ConfigFile{Architecture: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	baseLayer, err := random.Layer(1, types.OCILayer)
	if err != nil {
		t.Fatal(err)
	}
	baseImage, err = mutate.Append(baseImage, mutate.Addendum{
		Layer:       baseLayer,
		Annotations: map[string]string{xpkg.AnnotationKey: xpkg.PackageAnnotation},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Push(baseImageRef, baseImage, remote.WithTransport(regSrv.Client().Transport)); err != nil {
		t.Fatal(err)
	}

	return baseImageRef, regSrv.Client().Transport
}

// testPipCacheVolume returns the name of a pip cache volume private to the
// test, so the test starts from an empty cache and leaves the user's alone.
// Docker creates the volume on first mount; it's removed when the test ends.
func testPipCacheVolume(t *testing.T) string {
	t.Helper()

	vol := "crossplane-test-pip-cache-" + strings.ToLower(rand.Text())
	t.Cleanup(func() {
		cli, err := docker.NewClient()
		if err != nil {
			t.Logf("cannot remove pip cache volume %q: %v", vol, err)
			return
		}
		if _, err := cli.VolumeRemove(context.Background(), vol, client.VolumeRemoveOptions{Force: true}); err != nil {
			t.Logf("cannot remove pip cache volume %q: %v", vol, err)
		}
	})

	return vol
}

// pythonBuildDigest builds the function for a single architecture and returns
// the digest of the resulting image.
func pythonBuildDigest(ctx context.Context, b *pythonBuilder, bc BuildContext) (string, error) {
	imgs, err := b.Build(ctx, bc)
	if err != nil {
		return "", err
	}
	if len(imgs) != 1 {
		return "", errors.Errorf("Build() returned %d images, want 1", len(imgs))
	}
	d, err := imgs[0].Digest()
	if err != nil {
		return "", err
	}
	return d.String(), nil
}
