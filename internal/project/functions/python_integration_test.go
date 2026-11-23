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
	"github.com/spf13/afero"

	"github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"

	"github.com/crossplane/cli/v2/internal/docker"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"
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

	regSrv, err := registry.TLS("localhost")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(regSrv.Close)
	testRegistry, err := name.NewRegistry(strings.TrimPrefix(regSrv.URL, "https://"))
	if err != nil {
		t.Fatal(err)
	}

	// A stand-in for the distroless python runtime base: its content doesn't
	// matter, only that the venv layer appended onto it is reproducible.
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

	b := &pythonBuilder{
		buildImage:   pythonBuildImage,
		runtimeImage: baseImageRef.String(),
		transport:    regSrv.Client().Transport,
		configStore:  clixpkg.NewStaticImageConfigStore(nil),
	}
	bc := BuildContext{
		ProjectFS:     afero.NewBasePathFs(afero.NewOsFs(), "testdata"),
		FunctionPath:  "python-function",
		Architectures: []string{"amd64"},
	}

	digests := make([]string, 2)
	for i := range digests {
		imgs, err := b.Build(context.Background(), bc)
		if err != nil {
			t.Fatalf("Build() %d failed: %v", i, err)
		}
		if len(imgs) != 1 {
			t.Fatalf("Build() %d returned %d images, want 1", i, len(imgs))
		}
		d, err := imgs[0].Digest()
		if err != nil {
			t.Fatal(err)
		}
		digests[i] = d.String()
	}

	if digests[0] != digests[1] {
		t.Errorf("Build() is not deterministic: rebuilding an unchanged function produced %s, then %s", digests[0], digests[1])
	}
}
