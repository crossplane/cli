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
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
)

// resolveGeneratedModuleDeps runs `go mod download` in the generated module so
// the compile gate fails (rather than silently passing) when generated module
// dependencies are broken or drifting. Set CROSSPLANE_SKIP_COMPILE_GATE=1 to
// opt out for offline/local development; CI leaves it unset so broken deps fail.
func resolveGeneratedModuleDeps(t *testing.T, modelsDir string) {
	t.Helper()
	if os.Getenv("CROSSPLANE_SKIP_COMPILE_GATE") != "" {
		t.Skip("CROSSPLANE_SKIP_COMPILE_GATE set; skipping compile gate")
	}
	cmd := exec.CommandContext(t.Context(), "go", "mod", "download")
	cmd.Dir = modelsDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to resolve generated module dependencies: %v\n%s", err, out)
	}
}

// roMaterialize writes every file in fs out to dir on disk.
func roMaterialize(t *testing.T, fs afero.Fs, dir string) {
	t.Helper()
	err := afero.Walk(fs, "", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		bs, err := afero.ReadFile(fs, path)
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, bs, 0o644)
	})
	if err != nil {
		t.Fatalf("failed to materialize generated FS: %v", err)
	}
}

func TestGenerateFromCRDRuntimeObjectsArtifacts(t *testing.T) {
	inputFS := afero.NewBasePathFs(afero.FromIOFS{FS: testdataFS}, "testdata")
	schemaFS, err := goGenerator{runtimeObjects: true}.GenerateFromCRD(t.Context(), inputFS, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Root types implement runtime.Object + schema.ObjectKind.
	crd, err := afero.ReadFile(schemaFS, "models/co/acme/platform/v1alpha1/xaccountscaffold.go")
	if err != nil {
		t.Fatal(err)
	}
	methods := roMethods(t, string(crd))
	for _, m := range []string{
		"XAccountScaffold.DeepCopyInto", "XAccountScaffold.DeepCopy", "XAccountScaffold.DeepCopyObject",
		"XAccountScaffold.GetObjectKind", "XAccountScaffold.SetGroupVersionKind",
		"XAccountScaffoldSpec.DeepCopyInto", "XAccountScaffoldSpec.DeepCopy",
	} {
		if !methods[m] {
			t.Errorf("expected %s in generated CRD model", m)
		}
	}
	// Nested non-root struct must not be a runtime.Object.
	if methods["XAccountScaffoldSpec.DeepCopyObject"] {
		t.Error("nested struct should not implement runtime.Object")
	}

	// A groupversion_info.go is generated for the package.
	if exists, _ := afero.Exists(schemaFS, "models/co/acme/platform/v1alpha1/groupversion_info.go"); !exists {
		t.Error("expected groupversion_info.go for the package")
	}

	// go.mod gains the apimachinery dependency.
	mod, err := afero.ReadFile(schemaFS, "models/go.mod")
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(mod), "k8s.io/apimachinery") {
		t.Error("expected k8s.io/apimachinery in generated go.mod when feature is on")
	}
}

func TestGenerateFromCRDNoRuntimeObjectsByDefault(t *testing.T) {
	inputFS := afero.NewBasePathFs(afero.FromIOFS{FS: testdataFS}, "testdata")
	schemaFS, err := goGenerator{}.GenerateFromCRD(t.Context(), inputFS, nil)
	if err != nil {
		t.Fatal(err)
	}

	crd, err := afero.ReadFile(schemaFS, "models/co/acme/platform/v1alpha1/xaccountscaffold.go")
	if err != nil {
		t.Fatal(err)
	}
	if roMethods(t, string(crd))["XAccountScaffold.DeepCopyObject"] {
		t.Error("runtime.Object methods must not be generated when feature is disabled")
	}
	if exists, _ := afero.Exists(schemaFS, "models/co/acme/platform/v1alpha1/groupversion_info.go"); exists {
		t.Error("groupversion_info.go must not exist when feature is disabled")
	}
	mod, err := afero.ReadFile(schemaFS, "models/go.mod")
	if err != nil {
		t.Fatal(err)
	}
	if contains(string(mod), "k8s.io/apimachinery") {
		t.Error("go.mod must not reference apimachinery when feature is disabled")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

// TestGeneratedRuntimeObjectsCompile is the real correctness gate: it
// materializes the generated module (flag on), adds a consumer that registers
// the types in a runtime.Scheme and exercises an accessor through the
// runtime.Object interface, and compiles the whole module. This is what catches
// DeepCopyInto codegen bugs that parse cleanly but don't type-check.
func TestGeneratedRuntimeObjectsCompile(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available; skipping compile gate")
	}

	inputFS := afero.NewBasePathFs(afero.FromIOFS{FS: testdataFS}, "testdata")
	schemaFS, err := goGenerator{runtimeObjects: true}.GenerateFromCRD(t.Context(), inputFS, nil)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	roMaterialize(t, schemaFS, dir)

	// A behavioral test inside the generated module: it compiles the whole
	// module (build gate) and asserts runtime.Object satisfaction, AddToScheme
	// GVK round-tripping, SetGroupVersionKind writing the typed fields, and
	// DeepCopy independence.
	consumer := `package consumer

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	v1alpha1 "dev.crossplane.io/models/co/acme/platform/v1alpha1"
)

func TestGeneratedRuntimeObject(t *testing.T) {
	var _ runtime.Object = &v1alpha1.XAccountScaffold{}
	var _ runtime.Object = &v1alpha1.XAccountScaffoldList{}

	s := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	gvks, _, err := s.ObjectKinds(&v1alpha1.XAccountScaffold{})
	if err != nil {
		t.Fatalf("ObjectKinds: %v", err)
	}
	if len(gvks) == 0 || gvks[0].Kind != "XAccountScaffold" {
		t.Fatalf("unexpected GVKs: %v", gvks)
	}

	o := &v1alpha1.XAccountScaffold{}
	o.GetObjectKind().SetGroupVersionKind(schema.GroupVersionKind{
		Group: "platform.acme.co", Version: "v1alpha1", Kind: "XAccountScaffold",
	})
	if o.APIVersion == nil || string(*o.APIVersion) != "platform.acme.co/v1alpha1" {
		t.Fatalf("APIVersion not set by SetGroupVersionKind: %v", o.APIVersion)
	}

	ptr := func(s string) *string { return &s }

	// Scalar pointer independence.
	orig := &v1alpha1.XAccountScaffoldSpecParameters{Name: ptr("a")}
	cp := orig.DeepCopy()
	*cp.Name = "b"
	if *orig.Name != "a" {
		t.Fatalf("DeepCopy (*string) not independent: original mutated to %q", *orig.Name)
	}

	// Slice-of-structs independence (exercises writeSliceCopy).
	spec := &v1alpha1.XAccountScaffoldSpec{
		ResourceRefs: &[]v1alpha1.XAccountScaffoldSpecResourceRefsItem{{Name: ptr("a")}},
	}
	specCopy := spec.DeepCopy()
	*(*specCopy.ResourceRefs)[0].Name = "b"
	if *(*spec.ResourceRefs)[0].Name != "a" {
		t.Fatalf("DeepCopy (*[]struct) not independent: original mutated to %q", *(*spec.ResourceRefs)[0].Name)
	}

	// Map independence (exercises writeMapCopy).
	sel := &v1alpha1.XAccountScaffoldSpecCompositionSelector{
		MatchLabels: &map[string]string{"k": "v"},
	}
	selCopy := sel.DeepCopy()
	(*selCopy.MatchLabels)["k"] = "x"
	if (*sel.MatchLabels)["k"] != "v" {
		t.Fatalf("DeepCopy (*map) not independent: original mutated to %q", (*sel.MatchLabels)["k"])
	}
}
`
	consumerDir := filepath.Join(dir, "models", "consumer")
	if err := os.MkdirAll(consumerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(consumerDir, "consumer_test.go"), []byte(consumer), 0o644); err != nil {
		t.Fatal(err)
	}

	modelsDir := filepath.Join(dir, "models")

	resolveGeneratedModuleDeps(t, modelsDir)

	// `go test ./...` builds every generated package and runs the behavioral
	// test above.
	cmd := exec.CommandContext(t.Context(), "go", "test", "./...")
	cmd.Dir = modelsDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated runtime.Object models failed to build/test: %v\n%s", err, out)
	}
}

// TestGenerateFromOpenAPIRuntimeObjectsCompile exercises the OpenAPI generation
// path (the shared k8s and GVK packages, which include union and intstr types)
// with the feature on, and compiles the result.
func TestGenerateFromOpenAPIRuntimeObjectsCompile(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available; skipping compile gate")
	}

	inputFS := afero.NewBasePathFs(afero.FromIOFS{FS: testdataJSONFS}, "testdata")
	schemaFS, err := goGenerator{runtimeObjects: true}.GenerateFromOpenAPI(t.Context(), inputFS, nil)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	roMaterialize(t, schemaFS, dir)
	modelsDir := filepath.Join(dir, "models")

	resolveGeneratedModuleDeps(t, modelsDir)

	cmd := exec.CommandContext(t.Context(), "go", "build", "./...")
	cmd.Dir = modelsDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated OpenAPI runtime.Object models failed to compile: %v\n%s", err, out)
	}
}
