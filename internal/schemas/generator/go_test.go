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
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/afero"
	"golang.org/x/mod/modfile"
)

//go:embed testdata/*.yaml
var testdataFS embed.FS

// assertValidTypeDecls fails the test if the given parsed Go file declares the
// same type twice or declares a self-referential alias (`type X = X`), both of
// which don't compile but slip through syntax-only parsing.
func assertValidTypeDecls(t *testing.T, f *ast.File, path string) {
	t.Helper()

	seen := make(map[string]bool)
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, s := range gd.Specs {
			ts, ok := s.(*ast.TypeSpec)
			if !ok {
				continue
			}
			if seen[ts.Name.Name] {
				t.Errorf("duplicate type %s declared in %s", ts.Name.Name, path)
			}
			seen[ts.Name.Name] = true

			if ident, ok := ts.Type.(*ast.Ident); ok && ident.Name == ts.Name.Name {
				t.Errorf("self-referential type %s in %s", ts.Name.Name, path)
			}
		}
	}
}

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
		"models/io/cilium/v2/ciliumclusterwidenetworkpolicy.go",
		"models/com/example/v1/widget.go",
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
			assertValidTypeDecls(t, f, path)

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

// TestGenerateFromCRDGoScaleSubresource ensures CRDs with a scale subresource
// generate a model for the resource itself and don't pull the autoscaling/v1
// Scale schemas into the generated models. crd.ToOpenAPI drops the scale
// subresource before building the OpenAPI spec, so no shared autoscaling
// package must appear in the CRD flow.
func TestGenerateFromCRDGoScaleSubresource(t *testing.T) {
	inputFS := afero.NewBasePathFs(afero.FromIOFS{FS: testdataFS}, "testdata")
	schemaFS, err := goGenerator{}.GenerateFromCRD(t.Context(), inputFS, nil)
	if err != nil {
		t.Fatal(err)
	}

	contents, err := afero.ReadFile(schemaFS, "models/com/example/v1/widget.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "type Widget struct") {
		t.Error("generated code doesn't define Widget")
	}

	exists, err := afero.Exists(schemaFS, "models/io/k8s/autoscaling/v1/autoscaling.go")
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Error("scale subresource leaked the autoscaling/v1 Scale schemas into the generated models")
	}
}

// TestGenerateFromCRDGoValidationOnlyCombinators ensures we can generate
// models for CRDs that use anyOf/oneOf purely for validation (e.g. Cilium's
// "exactly one of endpointSelector and nodeSelector must be set"). Kubernetes
// structural schemas allow only `required` constraints and empty property
// schemas inside these junctors, but oapi-codegen would generate a union
// member type per variant, and a schema with both anyOf and oneOf produces
// colliding type names (two <TypeName>0). We strip such combinators; typed
// ones like x-kubernetes-int-or-string's anyOf must be kept.
func TestGenerateFromCRDGoValidationOnlyCombinators(t *testing.T) {
	inputFS := afero.NewBasePathFs(afero.FromIOFS{FS: testdataFS}, "testdata")
	schemaFS, err := goGenerator{}.GenerateFromCRD(t.Context(), inputFS, nil)
	if err != nil {
		t.Fatal(err)
	}

	contents, err := afero.ReadFile(schemaFS, "models/io/cilium/v2/ciliumclusterwidenetworkpolicy.go")
	if err != nil {
		t.Fatal(err)
	}
	code := string(contents)

	// The fields referenced by the validation-only combinators must still be
	// generated from the schema's regular properties.
	for _, field := range []string{"EndpointSelector", "NodeSelector", "Ingress", "Egress"} {
		if !strings.Contains(code, field) {
			t.Errorf("generated code missing field %s", field)
		}
	}

	// No union types must be generated for the validation-only combinators on
	// the spec schema.
	if strings.Contains(code, "type IoCiliumV2CiliumClusterwideNetworkPolicySpec0") {
		t.Error("generated code contains a union type for a validation-only combinator")
	}

	// Typed anyOf variants (x-kubernetes-int-or-string) must still generate
	// union member types.
	if !strings.Contains(code, "IoCiliumV2CiliumClusterwideNetworkPolicySpecEgressIcmpsFieldsType0") {
		t.Error("generated code is missing the int-or-string union member type")
	}
}

// TestGenerateFromOpenAPIGoSharedK8sPackages ensures models generated for
// real k8s API groups don't overwrite the shared k8s packages:
//
//   - The resource.k8s.io group (dynamic resource allocation, GA in k8s 1.34)
//     reverses to the same io/k8s/resource/v1 path as the shared apimachinery
//     Quantity package. It used to clobber it, leaving a package that imported
//     itself and creating a core/v1 -> resource/v1 -> core/v1 import cycle.
//     It now lives at io/k8s/api/resource instead.
//   - GVK groups whose schemas are all shared k8s package schemas (e.g.
//     autoscaling/v1's Scale) used to generate an empty file over the shared
//     package. They're skipped now.
func TestGenerateFromOpenAPIGoSharedK8sPackages(t *testing.T) {
	inputFS := afero.NewBasePathFs(afero.FromIOFS{FS: testdataJSONFS}, "testdata")
	schemaFS, err := goGenerator{}.GenerateFromOpenAPI(t.Context(), inputFS, nil)
	if err != nil {
		t.Fatal(err)
	}

	// The shared apimachinery resource package must define Quantity and must
	// not import itself.
	contents, err := afero.ReadFile(schemaFS, "models/io/k8s/resource/v1/resource.go")
	if err != nil {
		t.Fatal(err)
	}
	shared := string(contents)
	if !strings.Contains(shared, "type Quantity struct") {
		t.Error("shared resource package no longer defines Quantity")
	}
	if strings.Contains(shared, "type Quantity = Quantity") {
		t.Error("shared resource package contains a self-referential Quantity alias")
	}
	if strings.Contains(shared, `"dev.crossplane.io/models/io/k8s/resource/v1"`) {
		t.Error("shared resource package imports itself")
	}

	// The resource.k8s.io group models live at io/k8s/api/resource and
	// reference Quantity from the shared package instead of importing
	// themselves.
	contents, err = afero.ReadFile(schemaFS, "models/io/k8s/api/resource/v1/resource.go")
	if err != nil {
		t.Fatal(err)
	}
	dra := string(contents)
	if !strings.Contains(dra, "DeviceClass") {
		t.Error("resource.k8s.io models are missing DRA types")
	}
	if !strings.Contains(dra, "resourcev1.Quantity") {
		t.Error("resource.k8s.io models don't reference the shared Quantity")
	}

	// The shared autoscaling package must not be overwritten by the empty
	// autoscaling/v1 GVK group.
	contents, err = afero.ReadFile(schemaFS, "models/io/k8s/autoscaling/v1/autoscaling.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "type Scale") {
		t.Error("shared autoscaling package no longer defines Scale")
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
		"models/io/k8s/api/resource/v1/resource.go",
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
			assertValidTypeDecls(t, f, path)

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
