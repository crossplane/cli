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
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/afero"
)

// collectMethods parses Go source and returns a set of the methods it declares,
// keyed by "recv.name", with the rendered type of the single param or result.
func collectMethods(t *testing.T, src string) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("failed to parse generated source: %v\n%s", err, src)
	}

	out := map[string]string{}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
			continue
		}
		recv := renderRecv(fn.Recv.List[0].Type)
		var typ string
		switch {
		case fn.Type.Results != nil && len(fn.Type.Results.List) == 1:
			typ = renderExpr(t, fn.Type.Results.List[0].Type)
		case fn.Type.Params != nil && len(fn.Type.Params.List) == 1:
			typ = renderExpr(t, fn.Type.Params.List[0].Type)
		}
		out[recv+"."+fn.Name.Name] = typ
	}
	return out
}

func renderRecv(e ast.Expr) string {
	if star, ok := e.(*ast.StarExpr); ok {
		if id, ok := star.X.(*ast.Ident); ok {
			return id.Name
		}
	}
	if id, ok := e.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

func renderExpr(t *testing.T, e ast.Expr) string {
	t.Helper()
	switch x := e.(type) {
	case *ast.StarExpr:
		return "*" + renderExpr(t, x.X)
	case *ast.Ident:
		return x.Name
	case *ast.ArrayType:
		return "[]" + renderExpr(t, x.Elt)
	case *ast.MapType:
		return "map[" + renderExpr(t, x.Key) + "]" + renderExpr(t, x.Value)
	case *ast.SelectorExpr:
		return renderExpr(t, x.X) + "." + x.Sel.Name
	default:
		return ""
	}
}

// hasMethod reports whether src declares a method recv.name (recv without the
// leading *).
func hasMethod(t *testing.T, src []byte, recv, name string) bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("failed to parse source: %v", err)
	}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
			continue
		}
		if renderRecv(fn.Recv.List[0].Type) == recv && fn.Name.Name == name {
			return true
		}
	}
	return false
}

// TestGenerateFromCRDNoAccessorsByDefault verifies the feature gate: with the
// default (disabled) generator, no accessor methods are emitted.
func TestGenerateFromCRDNoAccessorsByDefault(t *testing.T) {
	inputFS := afero.NewBasePathFs(afero.FromIOFS{FS: testdataFS}, "testdata")
	schemaFS, err := goGenerator{}.GenerateFromCRD(t.Context(), inputFS, nil)
	if err != nil {
		t.Fatal(err)
	}

	crd, err := afero.ReadFile(schemaFS, "models/co/acme/platform/v1alpha1/xaccountscaffold.go")
	if err != nil {
		t.Fatal(err)
	}
	if hasMethod(t, crd, "XAccountScaffold", "GetSpec") {
		t.Error("accessors must not be generated when the feature is disabled")
	}
}

func TestGenerateFromCRDIncludesAccessors(t *testing.T) {
	inputFS := afero.NewBasePathFs(afero.FromIOFS{FS: testdataFS}, "testdata")
	schemaFS, err := goGenerator{accessors: true}.GenerateFromCRD(t.Context(), inputFS, nil)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		file   string
		recv   string
		method string
		reason string
	}{
		{
			name:   "TopLevelResourceGetter",
			file:   "models/co/acme/platform/v1alpha1/xaccountscaffold.go",
			recv:   "XAccountScaffold",
			method: "GetSpec",
			reason: "top-level resource structs get getters",
		},
		{
			name:   "TopLevelResourceSetter",
			file:   "models/co/acme/platform/v1alpha1/xaccountscaffold.go",
			recv:   "XAccountScaffold",
			method: "SetSpec",
			reason: "top-level resource structs get setters",
		},
		{
			name:   "TopLevelK8sFieldAccessor",
			file:   "models/co/acme/platform/v1alpha1/xaccountscaffold.go",
			recv:   "XAccountScaffold",
			method: "GetMetadata",
			reason: "fields of imported k8s types are still accessible",
		},
		{
			name:   "NestedStructGetter",
			file:   "models/co/acme/platform/v1alpha1/xaccountscaffold.go",
			recv:   "XAccountScaffoldSpec",
			method: "GetParameters",
			reason: "nested structs get accessors, not just the top-level resource",
		},
		{
			name:   "NestedStructSetter",
			file:   "models/co/acme/platform/v1alpha1/xaccountscaffold.go",
			recv:   "XAccountScaffoldSpec",
			method: "SetParameters",
			reason: "nested structs get setters too",
		},
		{
			name:   "SharedK8sBuildingBlock",
			file:   "models/io/k8s/meta/v1/meta.go",
			recv:   "ObjectMeta",
			method: "GetName",
			reason: "accessors everywhere includes the shared k8s building-block types",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src, err := afero.ReadFile(schemaFS, tc.file)
			if err != nil {
				t.Fatal(err)
			}
			if !hasMethod(t, src, tc.recv, tc.method) {
				t.Errorf("expected %s to declare %s.%s (%s)", tc.file, tc.recv, tc.method, tc.reason)
			}
		})
	}
}

// materialize writes every file in the afero filesystem out to dir on disk.
func materialize(t *testing.T, fs afero.Fs, dir string) {
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

// TestGeneratedModelsCompileWithAccessors is the real verification: it writes
// the generated models to disk, adds a consumer that exercises an accessor
// *through an interface*, and compiles the whole module. Parsing alone would
// pass on output that fails to compile (malformed signatures, name collisions);
// this catches that, and the interface usage proves the actual goal.
func TestGeneratedModelsCompileWithAccessors(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available; skipping compile gate")
	}

	inputFS := afero.NewBasePathFs(afero.FromIOFS{FS: testdataFS}, "testdata")
	schemaFS, err := goGenerator{accessors: true}.GenerateFromCRD(t.Context(), inputFS, nil)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	materialize(t, schemaFS, dir)

	// A consumer that abstracts over the generated resource via an interface,
	// satisfied structurally by the generated accessors.
	consumer := `package consumer

import v1alpha1 "dev.crossplane.io/models/co/acme/platform/v1alpha1"

type specAccessor interface {
	GetSpec() *v1alpha1.XAccountScaffoldSpec
	SetSpec(*v1alpha1.XAccountScaffoldSpec)
}

var _ specAccessor = &v1alpha1.XAccountScaffold{}

// useAccessors round-trips a value through the interface to prove the methods
// are usable, not just present.
func useAccessors(a specAccessor) *v1alpha1.XAccountScaffoldSpec {
	a.SetSpec(a.GetSpec())
	return a.GetSpec()
}

var _ = useAccessors
`
	consumerDir := filepath.Join(dir, "models", "consumer")
	if err := os.MkdirAll(consumerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(consumerDir, "consumer.go"), []byte(consumer), 0o644); err != nil {
		t.Fatal(err)
	}

	modelsDir := filepath.Join(dir, "models")

	// The generated module depends on github.com/oapi-codegen/runtime, which is
	// not a dependency of this repository, so building it requires resolving
	// that module from the proxy or module cache. Probe with `go mod download`
	// first: if modules can't be resolved (e.g. an offline runner with
	// GOPROXY=off and a cold cache), skip rather than report a false failure.
	// Internal dev.crossplane.io/models/... imports resolve within the module
	// itself, so no replace directive is needed for them.
	probe := exec.CommandContext(t.Context(), "go", "mod", "download")
	probe.Dir = modelsDir
	if out, err := probe.CombinedOutput(); err != nil {
		t.Skipf("cannot resolve generated module dependencies (offline?); skipping compile gate: %v\n%s", err, out)
	}

	cmd := exec.CommandContext(t.Context(), "go", "build", "./...")
	cmd.Dir = modelsDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated models failed to compile: %v\n%s", err, out)
	}
}

func TestAddAccessors(t *testing.T) {
	input := `package v1alpha1

type Foo struct {
	Name  *string            ` + "`json:\"name,omitempty\"`" + `
	Items *[]string          ` + "`json:\"items,omitempty\"`" + `
	Labels *map[string]string ` + "`json:\"labels,omitempty\"`" + `
	Bar   *Bar               ` + "`json:\"bar,omitempty\"`" + `
}

type Bar struct {
	Count *int64 ` + "`json:\"count,omitempty\"`" + `
}

// FooAlias is a type alias and must NOT receive its own accessors.
type FooAlias = Foo
`

	got, err := addAccessors(input)
	if err != nil {
		t.Fatalf("addAccessors returned error: %v", err)
	}

	// collectMethods returns every method in the output, so an exact diff
	// against want verifies both that the expected accessors exist with the
	// right pointer signatures and that nothing extra was generated — in
	// particular, that the FooAlias type alias did not get its own methods.
	want := map[string]string{
		"Foo.GetName":   "*string",
		"Foo.SetName":   "*string",
		"Foo.GetItems":  "*[]string",
		"Foo.SetItems":  "*[]string",
		"Foo.GetLabels": "*map[string]string",
		"Foo.SetLabels": "*map[string]string",
		"Foo.GetBar":    "*Bar",
		"Foo.SetBar":    "*Bar",
		"Bar.GetCount":  "*int64",
		"Bar.SetCount":  "*int64",
	}

	if diff := cmp.Diff(want, collectMethods(t, got)); diff != "" {
		t.Errorf("generated accessors (-want +got):\n%s", diff)
	}
}

// countMethods returns how many times recv.name is declared in src.
func countMethods(t *testing.T, src string, recv, name string) int {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("failed to parse source: %v\n%s", err, src)
	}
	n := 0
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
			continue
		}
		if renderRecv(fn.Recv.List[0].Type) == recv && fn.Name.Name == name {
			n++
		}
	}
	return n
}

// TestAddAccessorsSkipsExistingMethods guards against colliding with methods
// oapi-codegen already emits (e.g. GetAdditionalProperties, union As/From
// helpers). A GetX/SetX whose name already exists on the type must not be
// re-emitted, or the package would fail to compile with a duplicate method.
func TestAddAccessorsSkipsExistingMethods(t *testing.T) {
	input := `package v1alpha1

type Foo struct {
	AdditionalProperties *map[string]string ` + "`json:\"-\"`" + `
}

// Pre-existing accessor, as oapi-codegen would generate for additionalProperties.
func (o *Foo) GetAdditionalProperties() *map[string]string {
	return o.AdditionalProperties
}
`

	got, err := addAccessors(input)
	if err != nil {
		t.Fatalf("addAccessors returned error: %v", err)
	}

	// The pre-existing getter must remain the only GetAdditionalProperties.
	if n := countMethods(t, got, "Foo", "GetAdditionalProperties"); n != 1 {
		t.Errorf("expected exactly 1 GetAdditionalProperties (no duplicate), got %d", n)
	}
	// The setter doesn't exist yet, so it should still be generated.
	if n := countMethods(t, got, "Foo", "SetAdditionalProperties"); n != 1 {
		t.Errorf("expected SetAdditionalProperties to be generated, got %d", n)
	}
}
