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
	"go/format"
	"go/parser"
	"go/token"
	"strings"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

// accessorReceiver is the receiver variable name used by generated accessor
// methods. A single letter cannot collide with any generated package import
// alias, which are all multi-letter.
const accessorReceiver = "o"

// applyAccessors runs addAccessors when enabled. It is called by the generation
// loops after all other Go post-processing, so the accessors reference the
// final, fixed-up type names.
func applyAccessors(code string, enabled bool) (string, error) {
	if !enabled {
		return code, nil
	}
	return addAccessors(code)
}

// addAccessors generates GetX/SetX accessor methods for every field of every
// struct type declared in the given Go source. Getters return the field's
// (pointer) type as-is and setters take the same type, so the generated methods
// reference only types already present in the file and never require new
// imports. Type aliases are skipped: their Type is not a struct literal, so they
// share the underlying struct's method set for free.
func addAccessors(code string) (string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", code, parser.ParseComments)
	if err != nil {
		return "", errors.Wrap(err, "failed to parse Go code for accessors")
	}

	// Collect the methods that already exist on each type, so we never emit a
	// GetX/SetX that collides with a method oapi-codegen already generated
	// (e.g. GetAdditionalProperties, or union As/From/Merge helpers). A
	// duplicate method would make the package fail to compile.
	existing := collectExistingMethods(f)

	var b strings.Builder
	// Walk declarations in source order so the generated output is stable.
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			// Skip type aliases (`type Foo = Bar`); only generate accessors for
			// struct type definitions.
			if ts.Assign.IsValid() {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok || st.Fields == nil {
				continue
			}
			writeStructAccessors(&b, fset, ts.Name.Name, st, existing[ts.Name.Name])
		}
	}

	if b.Len() == 0 {
		return code, nil
	}

	combined := code + "\n" + b.String()
	formatted, err := format.Source([]byte(combined))
	if err != nil {
		return "", errors.Wrap(err, "failed to format generated accessors")
	}
	return string(formatted), nil
}

// collectExistingMethods returns, per receiver type name, the set of method
// names already declared in the file.
func collectExistingMethods(f *ast.File) map[string]map[string]bool {
	existing := map[string]map[string]bool{}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
			continue
		}
		recv := receiverTypeName(fn.Recv.List[0].Type)
		if recv == "" {
			continue
		}
		if existing[recv] == nil {
			existing[recv] = map[string]bool{}
		}
		existing[recv][fn.Name.Name] = true
	}
	return existing
}

// receiverTypeName returns the bare type name of a method receiver, stripping a
// leading pointer if present (e.g. `*Foo` -> `Foo`).
func receiverTypeName(e ast.Expr) string {
	if star, ok := e.(*ast.StarExpr); ok {
		e = star.X
	}
	if id, ok := e.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// writeStructAccessors appends a getter and setter for each named field of the
// given struct to b. Any accessor whose name already exists in skip is omitted
// to avoid colliding with methods oapi-codegen already generated.
func writeStructAccessors(b *strings.Builder, fset *token.FileSet, typeName string, st *ast.StructType, skip map[string]bool) {
	for _, field := range st.Fields.List {
		// Skip embedded/anonymous fields; generated models don't use them.
		if len(field.Names) == 0 {
			continue
		}

		var typ strings.Builder
		if err := format.Node(&typ, fset, field.Type); err != nil {
			// format.Node only fails on malformed nodes, which cannot occur for
			// a node we just parsed; skip defensively rather than panic.
			continue
		}
		fieldType := typ.String()

		for _, name := range field.Names {
			// Skip unexported fields: an accessor for them would be useless to
			// external consumers and could produce oddly-cased method names.
			// Generated models don't currently have any, but guard defensively.
			if !name.IsExported() {
				continue
			}

			fieldName := name.Name

			// Getter.
			if !skip["Get"+fieldName] {
				b.WriteString("\n// Get" + fieldName + " returns the " + fieldName + " field.\n")
				b.WriteString("func (" + accessorReceiver + " *" + typeName + ") Get" + fieldName + "() " + fieldType + " {\n")
				b.WriteString("\treturn " + accessorReceiver + "." + fieldName + "\n")
				b.WriteString("}\n")
			}

			// Setter.
			if !skip["Set"+fieldName] {
				b.WriteString("\n// Set" + fieldName + " sets the " + fieldName + " field.\n")
				b.WriteString("func (" + accessorReceiver + " *" + typeName + ") Set" + fieldName + "(v " + fieldType + ") {\n")
				b.WriteString("\t" + accessorReceiver + "." + fieldName + " = v\n")
				b.WriteString("}\n")
			}
		}
	}
}
