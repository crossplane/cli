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
	"strings"
	"testing"
)

// roMethods parses src and returns the set of "recv.method" names declared.
func roMethods(t *testing.T, src string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("failed to parse generated source: %v\n%s", err, src)
	}
	out := map[string]bool{}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
			continue
		}
		recv := fn.Recv.List[0].Type
		if star, ok := recv.(*ast.StarExpr); ok {
			recv = star.X
		}
		id, ok := recv.(*ast.Ident)
		if !ok {
			continue
		}
		out[id.Name+"."+fn.Name.Name] = true
	}
	return out
}

func TestAddRuntimeObjects(t *testing.T) {
	cases := map[string]struct {
		reason       string
		input        string
		wantHasRoots bool
		wantMethods  []string
		notMethods   []string
	}{
		"DeepCopyOnly": {
			reason: "non-root structs get DeepCopy methods but not runtime.Object methods",
			input: `package v1alpha1

type Bar struct {
	Count *int64 ` + "`json:\"count,omitempty\"`" + `
}

type Foo struct {
	Name   *string             ` + "`json:\"name,omitempty\"`" + `
	Items  *[]string           ` + "`json:\"items,omitempty\"`" + `
	Bars   *[]Bar              ` + "`json:\"bars,omitempty\"`" + `
	Labels *map[string]string  ` + "`json:\"labels,omitempty\"`" + `
	Bar    *Bar                ` + "`json:\"bar,omitempty\"`" + `
}
`,
			wantHasRoots: false,
			wantMethods: []string{
				"Foo.DeepCopyInto", "Foo.DeepCopy",
				"Bar.DeepCopyInto", "Bar.DeepCopy",
			},
			notMethods: []string{
				"Foo.DeepCopyObject", "Foo.GetObjectKind",
				"Bar.DeepCopyObject", "Bar.GetObjectKind",
			},
		},
		"RootType": {
			reason: "structs with APIVersion+Kind+Metadata get runtime.Object methods; supporting structs do not",
			input: `package v1alpha1

type FooAPIVersion string
type FooKind string

type FooSpec struct {
	Replicas *int64 ` + "`json:\"replicas,omitempty\"`" + `
}

type ObjectMeta struct {
	Name *string ` + "`json:\"name,omitempty\"`" + `
}

type ListMeta struct {
	ResourceVersion *string ` + "`json:\"resourceVersion,omitempty\"`" + `
}

type Foo struct {
	APIVersion *FooAPIVersion ` + "`json:\"apiVersion,omitempty\"`" + `
	Kind       *FooKind       ` + "`json:\"kind,omitempty\"`" + `
	Metadata   *ObjectMeta    ` + "`json:\"metadata,omitempty\"`" + `
	Spec       *FooSpec       ` + "`json:\"spec,omitempty\"`" + `
}

type FooList struct {
	APIVersion *string      ` + "`json:\"apiVersion,omitempty\"`" + `
	Kind       *string      ` + "`json:\"kind,omitempty\"`" + `
	Metadata   *ListMeta    ` + "`json:\"metadata,omitempty\"`" + `
	Items      *[]Foo       ` + "`json:\"items,omitempty\"`" + `
}
`,
			wantHasRoots: true,
			wantMethods: []string{
				"Foo.DeepCopyObject", "Foo.GetObjectKind", "Foo.GroupVersionKind", "Foo.SetGroupVersionKind",
				"FooList.DeepCopyObject", "FooList.GetObjectKind", "FooList.GroupVersionKind", "FooList.SetGroupVersionKind",
			},
			notMethods: []string{
				"FooSpec.DeepCopyObject",
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, hasRoots, err := addRuntimeObjects(tc.input)
			if err != nil {
				t.Fatalf("addRuntimeObjects returned error: %v", err)
			}
			if hasRoots != tc.wantHasRoots {
				t.Errorf("hasRoots = %v, want %v (%s)", hasRoots, tc.wantHasRoots, tc.reason)
			}

			methods := roMethods(t, got)
			for _, m := range tc.wantMethods {
				if !methods[m] {
					t.Errorf("expected method %s to be generated (%s)", m, tc.reason)
				}
			}
			for _, m := range tc.notMethods {
				if methods[m] {
					t.Errorf("did not expect method %s (%s)", m, tc.reason)
				}
			}
		})
	}
}

// TestRuntimeObjectsThenAccessorsDeduplicates pins the order the generator
// applies the two Go features in. Both emit methods onto the same structs, and
// only addAccessors skips names that already exist, so runtime.Object has to run
// first. A field named objectKind is the case that shows it: accessors would
// derive GetObjectKind from the field, which is also what root types get from
// schema.ObjectKind. Applied the other way round the struct ends up with two
// GetObjectKind methods and the generated module stops compiling.
func TestRuntimeObjectsThenAccessorsDeduplicates(t *testing.T) {
	const src = `package v1alpha1

type FooAPIVersion string
type FooKind string

type ObjectMeta struct {
	Name *string ` + "`json:\"name,omitempty\"`" + `
}

type Foo struct {
	APIVersion *FooAPIVersion ` + "`json:\"apiVersion,omitempty\"`" + `
	Kind       *FooKind       ` + "`json:\"kind,omitempty\"`" + `
	Metadata   *ObjectMeta    ` + "`json:\"metadata,omitempty\"`" + `
	ObjectKind *string        ` + "`json:\"objectKind,omitempty\"`" + `
}
`

	code, err := applyRuntimeObjects(src, true)
	if err != nil {
		t.Fatalf("applyRuntimeObjects: %v", err)
	}
	code, err = applyAccessors(code, true)
	if err != nil {
		t.Fatalf("applyAccessors: %v", err)
	}

	if got := countMethod(t, code, "Foo", "GetObjectKind"); got != 1 {
		t.Errorf("Foo.GetObjectKind declared %d times, want 1:\n%s", got, code)
	}
	// The accessor pair is still generated for everything that doesn't collide.
	if got := countMethod(t, code, "Foo", "SetObjectKind"); got != 1 {
		t.Errorf("Foo.SetObjectKind declared %d times, want 1", got)
	}
	if got := countMethod(t, code, "Foo", "GetMetadata"); got != 1 {
		t.Errorf("Foo.GetMetadata declared %d times, want 1", got)
	}
}

func TestObjectMetaAccessors(t *testing.T) {
	const src = `package v1alpha1

type FooAPIVersion string
type FooKind string

type ObjectMeta struct {
	Name *string ` + "`json:\"name,omitempty\"`" + `
}

type ListMeta struct {
	ResourceVersion *string ` + "`json:\"resourceVersion,omitempty\"`" + `
}

type Foo struct {
	APIVersion *FooAPIVersion ` + "`json:\"apiVersion,omitempty\"`" + `
	Kind       *FooKind       ` + "`json:\"kind,omitempty\"`" + `
	Metadata   *ObjectMeta    ` + "`json:\"metadata,omitempty\"`" + `
}

type FooList struct {
	APIVersion *FooAPIVersion ` + "`json:\"apiVersion,omitempty\"`" + `
	Kind       *FooKind       ` + "`json:\"kind,omitempty\"`" + `
	Metadata   *ListMeta      ` + "`json:\"metadata,omitempty\"`" + `
	Items      *[]Foo         ` + "`json:\"items,omitempty\"`" + `
}
`

	code, _, err := addRuntimeObjects(src)
	if err != nil {
		t.Fatalf("addRuntimeObjects: %v", err)
	}

	methods := roMethods(t, code)
	for _, m := range []string{
		"Foo.GetName", "Foo.SetName",
		"Foo.GetNamespace", "Foo.SetNamespace",
		"Foo.GetUID", "Foo.SetUID",
		"Foo.GetLabels", "Foo.SetLabels",
		"Foo.GetOwnerReferences", "Foo.SetOwnerReferences",
		"Foo.GetManagedFields", "Foo.SetManagedFields",
	} {
		if !methods[m] {
			t.Errorf("expected %s to be generated for an ObjectMeta-shaped root", m)
		}
	}

	// A ListMeta-shaped root must not get ObjectMeta's methods.
	for _, m := range []string{"FooList.GetName", "FooList.SetName", "FooList.GetLabels"} {
		if methods[m] {
			t.Errorf("did not expect %s on a ListMeta-shaped root", m)
		}
	}

	if !strings.Contains(code, "k8stypes.UID") {
		t.Errorf("expected GetUID to return k8stypes.UID, got:\n%s", code)
	}
	if !strings.Contains(code, `k8stypes "k8s.io/apimachinery/pkg/types"`) {
		t.Errorf("expected k8stypes import, got:\n%s", code)
	}
}

func TestListInterfaceAccessors(t *testing.T) {
	const src = `package v1alpha1

type FooAPIVersion string
type FooKind string

type ObjectMeta struct {
	Name *string ` + "`json:\"name,omitempty\"`" + `
}

type ListMeta struct {
	ResourceVersion *string ` + "`json:\"resourceVersion,omitempty\"`" + `
}

type Foo struct {
	APIVersion *FooAPIVersion ` + "`json:\"apiVersion,omitempty\"`" + `
	Kind       *FooKind       ` + "`json:\"kind,omitempty\"`" + `
	Metadata   *ObjectMeta    ` + "`json:\"metadata,omitempty\"`" + `
}

type FooList struct {
	APIVersion *FooAPIVersion ` + "`json:\"apiVersion,omitempty\"`" + `
	Kind       *FooKind       ` + "`json:\"kind,omitempty\"`" + `
	Metadata   *ListMeta      ` + "`json:\"metadata,omitempty\"`" + `
	Items      *[]Foo         ` + "`json:\"items,omitempty\"`" + `
}
`

	code, _, err := addRuntimeObjects(src)
	if err != nil {
		t.Fatalf("addRuntimeObjects: %v", err)
	}

	methods := roMethods(t, code)
	for _, m := range []string{
		"FooList.GetResourceVersion", "FooList.SetResourceVersion",
		"FooList.GetContinue", "FooList.SetContinue",
		"FooList.GetRemainingItemCount", "FooList.SetRemainingItemCount",
	} {
		if !methods[m] {
			t.Errorf("expected %s to be generated for a ListMeta-shaped root", m)
		}
	}

	// An ObjectMeta-shaped root must not get ListInterface's methods.
	if methods["Foo.GetContinue"] {
		t.Error("did not expect Foo.GetContinue on an ObjectMeta-shaped root")
	}
}

func TestFixListItemsFields(t *testing.T) {
	const src = `package v1alpha1

type FooAPIVersion string
type FooKind string

type ObjectMeta struct {
	Name *string ` + "`json:\"name,omitempty\"`" + `
}

type ListMeta struct {
	ResourceVersion *string ` + "`json:\"resourceVersion,omitempty\"`" + `
}

type Foo struct {
	APIVersion *FooAPIVersion ` + "`json:\"apiVersion,omitempty\"`" + `
	Kind       *FooKind       ` + "`json:\"kind,omitempty\"`" + `
	Metadata   *ObjectMeta    ` + "`json:\"metadata,omitempty\"`" + `
}

type FooList struct {
	APIVersion *FooAPIVersion ` + "`json:\"apiVersion,omitempty\"`" + `
	Kind       *FooKind       ` + "`json:\"kind,omitempty\"`" + `
	Metadata   *ListMeta      ` + "`json:\"metadata,omitempty\"`" + `
	Items      *[]Foo         ` + "`json:\"items,omitempty\"`" + `
}
`

	code, err := applyRuntimeObjects(src, true)
	if err != nil {
		t.Fatalf("applyRuntimeObjects: %v", err)
	}

	if strings.Contains(code, "Items      *[]Foo") || strings.Contains(code, "Items *[]Foo") {
		t.Errorf("expected Items to be rewritten from *[]Foo to []Foo, got:\n%s", code)
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", code, parser.ParseComments)
	if err != nil {
		t.Fatalf("rewritten code does not parse: %v\n%s", err, code)
	}
	found := false
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != "FooList" {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			for _, field := range st.Fields.List {
				if !hasFieldName(field, "Items") {
					continue
				}
				if _, ok := field.Type.(*ast.ArrayType); !ok {
					t.Errorf("expected FooList.Items to be a plain slice, got %T", field.Type)
				}
				found = true
			}
		}
	}
	if !found {
		t.Fatal("did not find FooList.Items field in rewritten code")
	}
}

// TestItemsFieldDeepCopyIndependenceWithAliasedElement pins the shape the
// plain fixture above doesn't cover: oapi-codegen aliases every root type to
// a qualified name, and a List's Items field uses that alias. It must
// resolve back to the struct, or DeepCopy shares item pointers with the
// original instead of copying them.
func TestItemsFieldDeepCopyIndependenceWithAliasedElement(t *testing.T) {
	const src = `package v1alpha1

type FooAPIVersion string
type FooKind string

type ObjectMeta struct {
	Name *string ` + "`json:\"name,omitempty\"`" + `
}

type ListMeta struct {
	ResourceVersion *string ` + "`json:\"resourceVersion,omitempty\"`" + `
}

type Foo struct {
	APIVersion *FooAPIVersion ` + "`json:\"apiVersion,omitempty\"`" + `
	Kind       *FooKind       ` + "`json:\"kind,omitempty\"`" + `
	Metadata   *ObjectMeta    ` + "`json:\"metadata,omitempty\"`" + `
	Name       *string        ` + "`json:\"name,omitempty\"`" + `
}

type QualifiedFoo = Foo

type FooList struct {
	APIVersion *FooAPIVersion  ` + "`json:\"apiVersion,omitempty\"`" + `
	Kind       *FooKind        ` + "`json:\"kind,omitempty\"`" + `
	Metadata   *ListMeta       ` + "`json:\"metadata,omitempty\"`" + `
	Items      *[]QualifiedFoo ` + "`json:\"items,omitempty\"`" + `
}
`
	code, err := applyRuntimeObjects(src, true)
	if err != nil {
		t.Fatalf("applyRuntimeObjects: %v", err)
	}
	if strings.Contains(code, "copy(out.Items, in.Items)") {
		t.Errorf("expected element-wise DeepCopy for an aliased Items element, got a shallow copy:\n%s", code)
	}
	if !strings.Contains(code, "in.Items[i].DeepCopyInto(&out.Items[i])") {
		t.Errorf("expected FooList's DeepCopyInto to deep-copy each aliased Items element, got:\n%s", code)
	}
}

func TestItemsFieldDeepCopyIndependence(t *testing.T) {
	const src = `package v1alpha1

type FooAPIVersion string
type FooKind string

type ObjectMeta struct {
	Name *string ` + "`json:\"name,omitempty\"`" + `
}

type ListMeta struct {
	ResourceVersion *string ` + "`json:\"resourceVersion,omitempty\"`" + `
}

type Foo struct {
	APIVersion *FooAPIVersion ` + "`json:\"apiVersion,omitempty\"`" + `
	Kind       *FooKind       ` + "`json:\"kind,omitempty\"`" + `
	Metadata   *ObjectMeta    ` + "`json:\"metadata,omitempty\"`" + `
	Name       *string        ` + "`json:\"name,omitempty\"`" + `
}

type FooList struct {
	APIVersion *FooAPIVersion ` + "`json:\"apiVersion,omitempty\"`" + `
	Kind       *FooKind       ` + "`json:\"kind,omitempty\"`" + `
	Metadata   *ListMeta      ` + "`json:\"metadata,omitempty\"`" + `
	Items      *[]Foo         ` + "`json:\"items,omitempty\"`" + `
}
`
	code, err := applyRuntimeObjects(src, true)
	if err != nil {
		t.Fatalf("applyRuntimeObjects: %v", err)
	}
	if !strings.Contains(code, "in.Items[i].DeepCopyInto(&out.Items[i])") {
		t.Errorf("expected FooList's DeepCopyInto to deep-copy each Items element, got:\n%s", code)
	}
}

// countMethod returns how many times recv.method is declared in src.
func countMethod(t *testing.T, src, recv, method string) int {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("failed to parse generated source: %v\n%s", err, src)
	}
	n := 0
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 || fn.Name.Name != method {
			continue
		}
		r := fn.Recv.List[0].Type
		if star, ok := r.(*ast.StarExpr); ok {
			r = star.X
		}
		if id, ok := r.(*ast.Ident); ok && id.Name == recv {
			n++
		}
	}
	return n
}
