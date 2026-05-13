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
	"archive/tar"
	"context"
	"embed"
	"io/fs"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/spf13/afero"
	"github.com/spf13/afero/tarfs"

	"github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"

	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"
)

var (
	_ Builder = &kclBuilder{}
	_ Builder = &pythonBuilder{}
)

func TestIdentify(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		files           map[string]string
		expectError     bool
		expectedBuilder Builder
	}{
		"KCLOnly": {
			files: map[string]string{
				"kcl.mod": "[package]",
			},
			expectedBuilder: &kclBuilder{},
		},
		"PythonOnly": {
			files: map[string]string{
				"pyproject.toml": "[project]",
				"function/fn.py": "",
			},
			expectedBuilder: &pythonBuilder{},
		},
		"GoOnly": {
			files: map[string]string{
				"go.mod": "module example.com/fake/module",
			},
			expectedBuilder: &goBuilder{},
		},
		"PythonAndKCL": {
			files: map[string]string{
				"pyproject.toml": "[project]",
				"function/fn.py": "",
				"kcl.mod":        "[package]",
			},
			// kclBuilder has precedence.
			expectedBuilder: &kclBuilder{},
		},
		"GoTemplating": {
			files: map[string]string{
				"template1.gotmpl": "",
				"template2.tmpl":   "",
			},
			expectedBuilder: &goTemplatingBuilder{},
		},
		"GoTemplatingInvalidFiles": {
			files: map[string]string{
				"template1.gotmpl": "",
				"template2.tmpl":   "",
				"sourcecode.go":    "package main",
			},
			expectError: true,
		},
		"Empty": {
			files:       make(map[string]string),
			expectError: true,
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fromFS := afero.NewMemMapFs()
			for fname, content := range tc.files {
				if err := afero.WriteFile(fromFS, fname, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			builder, err := DefaultIdentifier.Identify(fromFS, nil)
			if tc.expectError {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if err.Error() != "no suitable builder found" {
					t.Errorf("error = %q, want %q", err.Error(), "no suitable builder found")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			wantType := reflect.TypeOf(tc.expectedBuilder)
			gotType := reflect.TypeOf(builder)
			if wantType != gotType {
				t.Errorf("builder type = %v, want %v", gotType, wantType)
			}
		})
	}
}

//go:embed testdata/kcl-function/**
var kclFunction embed.FS

func TestKCLBuild(t *testing.T) {
	t.Parallel()

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
	baseImage, err := mutate.ConfigFile(empty.Image, &v1.ConfigFile{
		Architecture: "amd64",
	})
	if err != nil {
		t.Fatal(err)
	}
	baseLayer, err := random.Layer(1, types.OCILayer)
	if err != nil {
		t.Fatal(err)
	}
	baseImage, err = mutate.Append(baseImage, mutate.Addendum{
		Layer: baseLayer,
		Annotations: map[string]string{
			xpkg.AnnotationKey: xpkg.PackageAnnotation,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Push(baseImageRef, baseImage, remote.WithTransport(regSrv.Client().Transport)); err != nil {
		t.Fatal(err)
	}

	b := &kclBuilder{
		baseImage:   baseImageRef.String(),
		transport:   regSrv.Client().Transport,
		configStore: clixpkg.NewStaticImageConfigStore(nil),
	}
	projFS := afero.FromIOFS{FS: kclFunction}
	fnImgs, err := b.Build(context.Background(), BuildContext{
		ProjectFS:     projFS,
		FunctionPath:  "testdata/kcl-function",
		Architectures: []string{"amd64"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(fnImgs); got != 1 {
		t.Fatalf("len(fnImgs) = %d, want 1", got)
	}
	fnImg := fnImgs[0]

	cfgFile, err := fnImg.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cfgFile.Config.Env, "FUNCTION_KCL_DEFAULT_SOURCE=/src") {
		t.Errorf("env missing FUNCTION_KCL_DEFAULT_SOURCE=/src; got %v", cfgFile.Config.Env)
	}

	verifyCodeLayer(t, fnImg, afero.NewBasePathFs(projFS, "testdata/kcl-function"), "/src")
}

//go:embed testdata/go-templating-function/**
var goTemplatingFunction embed.FS

func TestGoTemplatingBuild(t *testing.T) {
	t.Parallel()

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
	baseImage, err := mutate.ConfigFile(empty.Image, &v1.ConfigFile{
		Architecture: "amd64",
	})
	if err != nil {
		t.Fatal(err)
	}
	baseLayer, err := random.Layer(1, types.OCILayer)
	if err != nil {
		t.Fatal(err)
	}
	baseImage, err = mutate.Append(baseImage, mutate.Addendum{
		Layer: baseLayer,
		Annotations: map[string]string{
			xpkg.AnnotationKey: xpkg.PackageAnnotation,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Push(baseImageRef, baseImage, remote.WithTransport(regSrv.Client().Transport)); err != nil {
		t.Fatal(err)
	}

	b := &goTemplatingBuilder{
		baseImage:   baseImageRef.String(),
		transport:   regSrv.Client().Transport,
		configStore: clixpkg.NewStaticImageConfigStore(nil),
	}
	projFS := afero.FromIOFS{FS: goTemplatingFunction}
	fnImgs, err := b.Build(context.Background(), BuildContext{
		ProjectFS:     projFS,
		FunctionPath:  "testdata/go-templating-function",
		Architectures: []string{"amd64"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(fnImgs); got != 1 {
		t.Fatalf("len(fnImgs) = %d, want 1", got)
	}
	fnImg := fnImgs[0]

	cfgFile, err := fnImg.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cfgFile.Config.Env, "FUNCTION_GO_TEMPLATING_DEFAULT_SOURCE=/src") {
		t.Errorf("env missing FUNCTION_GO_TEMPLATING_DEFAULT_SOURCE=/src; got %v", cfgFile.Config.Env)
	}

	verifyCodeLayer(t, fnImg, afero.NewBasePathFs(projFS, "testdata/go-templating-function"), "/src")
}

// verifyCodeLayer asserts that the image has exactly one layer, and that the
// layer's tarball contains every file from sourceFS, prefixed with destPrefix.
func verifyCodeLayer(t *testing.T, img v1.Image, sourceFS afero.Fs, destPrefix string) {
	t.Helper()

	layers, err := img.Layers()
	if err != nil {
		t.Fatal(err)
	}
	if got := len(layers); got != 1 {
		t.Fatalf("len(layers) = %d, want 1", got)
	}
	layer := layers[0]
	rc, err := layer.Uncompressed()
	if err != nil {
		t.Fatal(err)
	}

	tr := tar.NewReader(rc)
	tfs := tarfs.New(tr)
	_ = afero.Walk(sourceFS, "/", func(path string, _ fs.FileInfo, err error) error {
		if err != nil {
			t.Fatal(err)
		}

		tpath := filepath.Join(destPrefix, path)
		st, err := tfs.Stat(tpath)
		if err != nil {
			t.Fatal(err)
		}

		if st.IsDir() {
			return nil
		}
		wantContents, err := afero.ReadFile(sourceFS, path)
		if err != nil {
			t.Fatal(err)
		}
		gotContents, err := afero.ReadFile(tfs, tpath)
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(wantContents, gotContents); diff != "" {
			t.Errorf("file %q contents (-want +got):\n%s", path, diff)
		}

		return nil
	})
}
