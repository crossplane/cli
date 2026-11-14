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

	// A type that already has a MarshalJSON (oapi-codegen generates one for
	// types with additionalProperties or unions) is left alone: a second
	// method would not compile.
	existing := collectExistingMethods(f)

	var b strings.Builder
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
			if existing[ts.Name.Name]["MarshalJSON"] {
				continue
			}
			writeManagedFieldsMarshaler(&b, ts.Name.Name)
		}
	}

	if b.Len() == 0 {
		return code, nil
	}

	combined, err := ensureImports(code+"\n"+b.String(), []importSpec{{path: "encoding/json"}})
	if err != nil {
		return "", err
	}
	formatted, err := format.Source([]byte(combined))
	if err != nil {
		return "", errors.Wrap(err, "failed to format generated MarshalJSON code")
	}
	return string(formatted), nil
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
