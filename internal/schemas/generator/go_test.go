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

package generator

import (
	"embed"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/afero"
	"golang.org/x/mod/modfile"
)

//go:embed testdata/*.yaml
var testdataFS embed.FS

func TestGenerateFromCRDGo(t *testing.T) {
	inputFS := afero.NewBasePathFs(afero.FromIOFS{FS: testdataFS}, "testdata")
	schemaFS, err := goGenerator{}.GenerateFromCRD(t.Context(), inputFS, nil)
	if err != nil {
		t.Fatal(err)
	}

	expectedFiles := []string{
		"models/go.mod",
		"models/io/k8s/meta/v1/meta.go",
		"models/co/acme/platform/v1alpha1/accountscaffold.go",
		"models/co/acme/platform/v1alpha1/xaccountscaffold.go",
		"models/io/upbound/azure/web/v1beta1/linuxfunctionapp.go",
	}

	files := token.NewFileSet()
	for _, path := range expectedFiles {
		exists, err := afero.Exists(schemaFS, path)
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("expected model file %s does not exist", path)
		}

		contents, err := afero.ReadFile(schemaFS, path)
		if err != nil {
			t.Fatal(err)
		}

		switch filepath.Ext(path) {
		case ".go":
			f, err := parser.ParseFile(files, path, contents, parser.ParseComments)
			if err != nil {
				t.Fatal(err)
			}
			expectedPackage := filepath.Base(filepath.Dir(path))
			if diff := cmp.Diff(expectedPackage, f.Name.Name); diff != "" {
				t.Errorf("package name (-want +got):\n%s", diff)
			}

		case ".mod":
			mod, err := modfile.Parse(path, contents, nil)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff("dev.crossplane.io/models", mod.Module.Mod.Path); diff != "" {
				t.Errorf("module path (-want +got):\n%s", diff)
			}
		}
	}
}

func TestGenerateFromOpenAPIGo(t *testing.T) {
	inputFS := afero.NewBasePathFs(afero.FromIOFS{FS: testdataJSONFS}, "testdata")
	schemaFS, err := goGenerator{}.GenerateFromOpenAPI(t.Context(), inputFS, nil)
	if err != nil {
		t.Fatal(err)
	}

	expectedFiles := []string{
		"models/go.mod",
		"models/io/k8s/util/v1/intstr.go",
		"models/io/k8s/runtime/v1/runtime.go",
		"models/io/k8s/core/v1/core.go",
		"models/io/k8s/policy/v1/policy.go",
		"models/io/k8s/autoscaling/v1/autoscaling.go",
		"models/io/k8s/resource/v1/resource.go",
		"models/io/k8s/authentication/v1/authentication.go",
	}

	files := token.NewFileSet()
	for _, path := range expectedFiles {
		exists, err := afero.Exists(schemaFS, path)
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("expected model file %s does not exist", path)
		}

		contents, err := afero.ReadFile(schemaFS, path)
		if err != nil {
			t.Fatal(err)
		}

		switch filepath.Ext(path) {
		case ".go":
			f, err := parser.ParseFile(files, path, contents, parser.ParseComments)
			if err != nil {
				t.Fatal(err)
			}
			expectedPackage := filepath.Base(filepath.Dir(path))
			if diff := cmp.Diff(expectedPackage, f.Name.Name); diff != "" {
				t.Errorf("package name (-want +got):\n%s", diff)
			}

		case ".mod":
			mod, err := modfile.Parse(path, contents, nil)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff("dev.crossplane.io/models", mod.Module.Mod.Path); diff != "" {
				t.Errorf("module path (-want +got):\n%s", diff)
			}
		}
	}
}
