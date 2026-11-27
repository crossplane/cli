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
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"slices"
	"strings"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

// addManagedFieldsMarshalers gives every root object type in the given Go
// source (a type with APIVersion, Kind and an ObjectMeta Metadata field) a
// MarshalJSON method that omits metadata.managedFields.
//
// managedFields is owned by the API server: a server-side apply request that
// sets it is rejected with "metadata.managedFields must be nil". Composition
// functions routinely decode an observed resource into a generated model and
// encode it back as a desired one, so without this the managedFields the API
// server populated on the observed resource would leak into the request.
// Decoding is unaffected, so managedFields stays readable on the model.
//
// A root type that already has a MarshalJSON (oapi-codegen generates one for
// types with additionalProperties or unions) keeps it, renamed to
// marshalJSONWithManagedFields, and the generated MarshalJSON delegates to it.
//
// encoding/json and the apimachinery unstructured converter (ToUnstructured)
// both honour MarshalJSON. The method is generated on the root type rather
// than on ObjectMeta because with runtime.Object generation on, Metadata is
// the real apimachinery ObjectMeta, which we can't add methods to. It runs in
// every generation mode, so the two modes behave the same.
func addManagedFieldsMarshalers(code string) (string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", code, parser.ParseComments)
	if err != nil {
		return "", errors.Wrap(err, "failed to parse Go code for MarshalJSON generation")
	}

	existing := collectExistingMethods(f)

	var b strings.Builder
	var renames []int
	needJSON := false
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Assign.IsValid() || ts.TypeParams != nil {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok || st.Fields == nil {
				continue
			}
			if !isRootStruct(st) || metadataKind(st) != "ObjectMeta" {
				continue
			}
			name := ts.Name.Name
			if !existing[name]["MarshalJSON"] {
				writeManagedFieldsMarshaler(&b, name)
				needJSON = true
				continue
			}
			if existing[name][managedFieldsDelegate] {
				return "", errors.Errorf("type %s already has both MarshalJSON and %s", name, managedFieldsDelegate)
			}
			renames = append(renames, marshalJSONNameOffsets(fset, f, name)...)
			writeManagedFieldsDelegatingMarshaler(&b, name)
		}
	}

	if b.Len() == 0 {
		return code, nil
	}

	combined := renameMarshalJSON(code, renames) + "\n" + b.String()
	if needJSON {
		combined, err = ensureImports(combined, []importSpec{{path: "encoding/json"}})
		if err != nil {
			return "", err
		}
	}
	formatted, err := format.Source([]byte(combined))
	if err != nil {
		return "", errors.Wrap(err, "failed to format generated MarshalJSON code")
	}
	return string(formatted), nil
}

// managedFieldsDelegate is the name an existing MarshalJSON on a root type is
// renamed to, so that the generated MarshalJSON can delegate to it.
const managedFieldsDelegate = "marshalJSONWithManagedFields"

// marshalJSONNameOffsets returns the source offsets of the names of the
// MarshalJSON methods declared on the named type.
func marshalJSONNameOffsets(fset *token.FileSet, f *ast.File, name string) []int {
	var offsets []int
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 || fn.Name.Name != "MarshalJSON" {
			continue
		}
		if receiverTypeName(fn.Recv.List[0].Type) != name {
			continue
		}
		offsets = append(offsets, fset.Position(fn.Name.Pos()).Offset)
	}
	return offsets
}

// renameMarshalJSON renames the MarshalJSON identifiers at the given source
// offsets to managedFieldsDelegate.
func renameMarshalJSON(code string, offsets []int) string {
	slices.Sort(offsets)
	var b strings.Builder
	last := 0
	for _, off := range offsets {
		b.WriteString(code[last:off])
		b.WriteString(managedFieldsDelegate)
		last = off + len("MarshalJSON")
	}
	b.WriteString(code[last:])
	return b.String()
}

// writeManagedFieldsDelegatingMarshaler appends a MarshalJSON for the named
// root type that clears ManagedFields on a shallow copy of Metadata, then
// encodes through the type's original MarshalJSON, renamed to
// managedFieldsDelegate.
func writeManagedFieldsDelegatingMarshaler(b *strings.Builder, name string) {
	fmt.Fprintf(b, "\n// MarshalJSON encodes the %s without metadata.managedFields, which\n", name)
	b.WriteString("// the API server owns and rejects in a server-side apply request. Decoding\n")
	b.WriteString("// still reads managedFields.\n")
	fmt.Fprintf(b, "func (in %s) MarshalJSON() ([]byte, error) {\n", name)
	b.WriteString("\tif in.Metadata != nil && in.Metadata.ManagedFields != nil {\n")
	b.WriteString("\t\tmeta := *in.Metadata\n")
	b.WriteString("\t\tmeta.ManagedFields = nil\n")
	b.WriteString("\t\tin.Metadata = &meta\n")
	b.WriteString("\t}\n")
	fmt.Fprintf(b, "\treturn in.%s()\n", managedFieldsDelegate)
	b.WriteString("}\n")
}

// writeManagedFieldsMarshaler appends a MarshalJSON for the named root type.
// It has a value receiver so that both T and *T implement json.Marshaler. It
// clears ManagedFields on a shallow copy of Metadata, so the receiver is never
// modified, and marshals through a defined type with no methods to avoid
// recursing into itself.
func writeManagedFieldsMarshaler(b *strings.Builder, name string) {
	fmt.Fprintf(b, "\n// MarshalJSON encodes the %s without metadata.managedFields, which\n", name)
	b.WriteString("// the API server owns and rejects in a server-side apply request. Decoding\n")
	b.WriteString("// still reads managedFields.\n")
	fmt.Fprintf(b, "func (in %s) MarshalJSON() ([]byte, error) {\n", name)
	fmt.Fprintf(b, "\ttype noMethods %s\n", name)
	b.WriteString("\tout := noMethods(in)\n")
	b.WriteString("\tif out.Metadata != nil && out.Metadata.ManagedFields != nil {\n")
	b.WriteString("\t\tmeta := *out.Metadata\n")
	b.WriteString("\t\tmeta.ManagedFields = nil\n")
	b.WriteString("\t\tout.Metadata = &meta\n")
	b.WriteString("\t}\n")
	b.WriteString("\treturn json.Marshal(out)\n")
	b.WriteString("}\n")
}
