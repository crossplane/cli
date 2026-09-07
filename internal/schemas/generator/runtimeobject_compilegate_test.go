//go:build compilegate

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

// The tests in this file are the real correctness gate for the generated
// DeepCopy / runtime.Object code: they materialize the generated models module
// on disk and shell out to the Go toolchain to build and test it, which catches
// codegen bugs that parse cleanly but don't type-check.
//
// They need to resolve the generated module's dependencies, so they need network
// access and a writable module cache. Our unit tests run inside the Nix sandbox
// (see nix/checks.nix), which has neither: modules come from gomod2nix, and the
// generated module pins the apimachinery version the Go function template uses,
// which is not the version this repository depends on. Rather than skip at
// runtime — which reports as a test that ran — they are behind a build tag, so
// it's explicit that a plain `go test ./...` does not cover them:
//
//	go test -tags compilegate ./internal/schemas/generator/...
//
// golangci-lint builds this file (see build-tags in .golangci.yml) so it can't
// rot silently.

package generator

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
)

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

// resolveGeneratedModuleDeps runs `go mod download` in the generated module, so
// the compile gate fails rather than silently passing when the go.mod and go.sum
// we generate are broken or have drifted.
func resolveGeneratedModuleDeps(t *testing.T, modelsDir string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "go", "mod", "download")
	cmd.Dir = modelsDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to resolve generated module dependencies: %v\n%s", err, out)
	}
}

// TestGeneratedRuntimeObjectsCompile materializes the generated module (flag on),
// adds a consumer that registers the types in a runtime.Scheme and exercises an
// accessor through the runtime.Object interface, and compiles the whole module.
func TestGeneratedRuntimeObjectsCompile(t *testing.T) {
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
// with the feature on, compiles the result, and registers every generated
// built-in package in one scheme.
func TestGenerateFromOpenAPIRuntimeObjectsCompile(t *testing.T) {
	inputFS := afero.NewBasePathFs(afero.FromIOFS{FS: testdataJSONFS}, "testdata")
	schemaFS, err := goGenerator{runtimeObjects: true}.GenerateFromOpenAPI(t.Context(), inputFS, nil)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	roMaterialize(t, schemaFS, dir)

	// Registering every built-in package in a single scheme is the check that
	// the API groups we write into groupversion_info.go are right:
	// AddKnownTypes panics if two Go types claim the same GVK, which is what
	// would happen if two packages sharing the core (empty) group also shared a
	// kind name.
	consumer := `package consumer

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	authnv1 "dev.crossplane.io/models/io/k8s/authentication/v1"
	autoscalingv1 "dev.crossplane.io/models/io/k8s/autoscaling/v1"
	metav1 "dev.crossplane.io/models/io/k8s/core/meta/v1"
	corev1 "dev.crossplane.io/models/io/k8s/core/v1"
	policyv1 "dev.crossplane.io/models/io/k8s/policy/v1"
)

func TestBuiltInGroupVersions(t *testing.T) {
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		corev1.AddToScheme,
		metav1.AddToScheme,
		authnv1.AddToScheme,
		autoscalingv1.AddToScheme,
		policyv1.AddToScheme,
	} {
		if err := add(s); err != nil {
			t.Fatal(err)
		}
	}

	// Built-in types must be known by the GVK their own apiVersion reports, not
	// by the synthetic group label the generator uses for the directory layout.
	cases := map[string]struct {
		obj  runtime.Object
		want schema.GroupVersionKind
	}{
		"CoreV1":      {obj: &corev1.Pod{}, want: schema.GroupVersionKind{Version: "v1", Kind: "Pod"}},
		"MetaV1":      {obj: &metav1.Status{}, want: schema.GroupVersionKind{Version: "v1", Kind: "Status"}},
		"Autoscaling": {obj: &autoscalingv1.Scale{}, want: schema.GroupVersionKind{Group: "autoscaling", Version: "v1", Kind: "Scale"}},
		"Authn":       {obj: &authnv1.TokenRequest{}, want: schema.GroupVersionKind{Group: "authentication.k8s.io", Version: "v1", Kind: "TokenRequest"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gvks, _, err := s.ObjectKinds(tc.obj)
			if err != nil {
				t.Fatalf("ObjectKinds: %v", err)
			}
			if len(gvks) != 1 || gvks[0] != tc.want {
				t.Errorf("ObjectKinds = %v, want [%v]", gvks, tc.want)
			}
		})
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

	cmd := exec.CommandContext(t.Context(), "go", "test", "./...")
	cmd.Dir = modelsDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated OpenAPI runtime.Object models failed to build/test: %v\n%s", err, out)
	}
}

// TestGeneratedModelsCompileWithoutRuntimeObjects is the flag-off counterpart:
// we write the same go.mod and go.sum either way, so the generated module must
// still build when the runtime.Object code isn't there to use apimachinery.
func TestGeneratedModelsCompileWithoutRuntimeObjects(t *testing.T) {
	inputFS := afero.NewBasePathFs(afero.FromIOFS{FS: testdataFS}, "testdata")
	schemaFS, err := goGenerator{}.GenerateFromCRD(t.Context(), inputFS, nil)
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
		t.Fatalf("generated models failed to compile with the feature off: %v\n%s", err, out)
	}
}

// TestGeneratedModelsCompileWithAccessorsAndRuntimeObjects builds the output
// with both generator features on. They emit methods onto the same structs, so
// a name they both claim — GetObjectKind against a field named objectKind, say —
// would be a duplicate method that only a real build catches. Neither feature's
// own gate covers the combination.
func TestGeneratedModelsCompileWithAccessorsAndRuntimeObjects(t *testing.T) {
	inputFS := afero.NewBasePathFs(afero.FromIOFS{FS: testdataFS}, "testdata")
	schemaFS, err := goGenerator{accessors: true, runtimeObjects: true}.GenerateFromCRD(t.Context(), inputFS, nil)
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
		t.Fatalf("generated models failed to compile with both features on: %v\n%s", err, out)
	}
}

// TestGenerateFromOpenAPIWithAccessorsAndRuntimeObjects is the same check for
// the OpenAPI path, which generates the far larger built-in Kubernetes packages.
func TestGenerateFromOpenAPIWithAccessorsAndRuntimeObjects(t *testing.T) {
	inputFS := afero.NewBasePathFs(afero.FromIOFS{FS: testdataJSONFS}, "testdata")
	schemaFS, err := goGenerator{accessors: true, runtimeObjects: true}.GenerateFromOpenAPI(t.Context(), inputFS, nil)
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
		t.Fatalf("generated OpenAPI models failed to compile with both features on: %v\n%s", err, out)
	}
}
