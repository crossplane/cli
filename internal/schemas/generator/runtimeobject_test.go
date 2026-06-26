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

func TestAddRuntimeObjectsDeepCopy(t *testing.T) {
	input := `package v1alpha1

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
`
	got, _, err := addRuntimeObjects(input)
	if err != nil {
		t.Fatalf("addRuntimeObjects returned error: %v", err)
	}

	methods := roMethods(t, got)
	for _, m := range []string{
		"Foo.DeepCopyInto", "Foo.DeepCopy",
		"Bar.DeepCopyInto", "Bar.DeepCopy",
	} {
		if !methods[m] {
			t.Errorf("expected method %s to be generated", m)
		}
	}

	// Non-root structs must NOT get runtime.Object methods.
	for _, m := range []string{
		"Foo.DeepCopyObject", "Foo.GetObjectKind",
		"Bar.DeepCopyObject", "Bar.GetObjectKind",
	} {
		if methods[m] {
			t.Errorf("non-root struct should not declare %s", m)
		}
	}
}

func TestAddRuntimeObjectsRootType(t *testing.T) {
	input := `package v1alpha1

type FooAPIVersion string
type FooKind string

type FooSpec struct {
	Replicas *int64 ` + "`json:\"replicas,omitempty\"`" + `
}

type ObjectMeta struct {
	Name *string ` + "`json:\"name,omitempty\"`" + `
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
	Metadata   *ObjectMeta  ` + "`json:\"metadata,omitempty\"`" + `
	Items      *[]Foo       ` + "`json:\"items,omitempty\"`" + `
}
`
	got, hasRoots, err := addRuntimeObjects(input)
	if err != nil {
		t.Fatalf("addRuntimeObjects returned error: %v", err)
	}
	if !hasRoots {
		t.Error("expected addRuntimeObjects to report root types present")
	}

	methods := roMethods(t, got)
	for _, m := range []string{
		"Foo.DeepCopyObject", "Foo.GetObjectKind", "Foo.GroupVersionKind", "Foo.SetGroupVersionKind",
		"FooList.DeepCopyObject", "FooList.GetObjectKind", "FooList.GroupVersionKind", "FooList.SetGroupVersionKind",
	} {
		if !methods[m] {
			t.Errorf("expected root-type method %s to be generated", m)
		}
	}
	// FooSpec is not a root type.
	if methods["FooSpec.DeepCopyObject"] {
		t.Error("FooSpec should not be a runtime.Object")
	}
}
