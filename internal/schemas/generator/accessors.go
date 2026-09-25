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

	// writeStructAccessors needs to know which fields are non-pointer struct
	// values (required object-typed fields, see goRemoveRequired in go.go),
	// so it can still return/accept a pointer for them.
	structs := collectStructTypes(f)

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
			writeStructAccessors(&b, fset, receiverTypeExpr(ts), st, existing[ts.Name.Name], structs)
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

// receiverTypeName returns the bare type name of a method receiver, stripping
// a leading pointer (e.g. `*Foo` -> `Foo`) and any generic instantiation
// (e.g. `*Foo[T]` -> `Foo`).
func receiverTypeName(e ast.Expr) string {
	if star, ok := e.(*ast.StarExpr); ok {
		e = star.X
	}
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return receiverTypeName(t.X)
	case *ast.IndexListExpr:
		return receiverTypeName(t.X)
	default:
		return ""
	}
}

// receiverTypeExpr returns the type expression to use for generated methods'
// receivers on ts, instantiating any type parameters with their own names
// (e.g. `type Foo[T any] struct{}` needs a receiver of `Foo[T]`, since Go
// requires a generic type's methods to repeat its parameter list).
func receiverTypeExpr(ts *ast.TypeSpec) string {
	if ts.TypeParams == nil {
		return ts.Name.Name
	}
	var params []string
	for _, field := range ts.TypeParams.List {
		for _, name := range field.Names {
			params = append(params, name.Name)
		}
	}
	return ts.Name.Name + "[" + strings.Join(params, ", ") + "]"
}

// embeddedFieldName returns the name Go promotes into the struct's namespace
// for an anonymous field, mirroring the Go spec: it's the embedded type's own
// name, ignoring any pointer indirection, package qualification, or generic
// instantiation (e.g. embedding `*Bar`, `pkg.Bar`, or `Bar[string]` all
// promote the name `Bar`). Returns "" for type shapes generated models don't
// use, which simply aren't tracked as potential collisions.
func embeddedFieldName(e ast.Expr) string {
	if star, ok := e.(*ast.StarExpr); ok {
		e = star.X
	}
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return t.Sel.Name
	case *ast.IndexExpr:
		return embeddedFieldName(t.X)
	case *ast.IndexListExpr:
		return embeddedFieldName(t.X)
	default:
		return ""
	}
}

// isNilable reports whether a zero value of the given type is spelled `nil`,
// letting the generated getter return nil directly instead of declaring a zero
// variable. Generated models use pointers throughout, so this is the common
// case; the zero-variable form covers everything else.
func isNilable(e ast.Expr) bool {
	switch t := e.(type) {
	case *ast.StarExpr, *ast.MapType, *ast.InterfaceType, *ast.ChanType, *ast.FuncType:
		return true
	case *ast.ArrayType:
		// Slices are nilable; fixed-size arrays are not.
		return t.Len == nil
	default:
		return false
	}
}

// writeStructAccessors appends a getter and setter for each named field of the
// given struct to b. Any accessor whose name already exists in skip is omitted
// to avoid colliding with methods oapi-codegen already generated. An accessor
// is also omitted if its name collides with another field of the same struct
// (e.g. a field named PasswordData alongside a sibling field named
// GetPasswordData): Go forbids a method and a field sharing a name on the same
// type, and Terraform schemas occasionally produce exactly that pair.
func writeStructAccessors(b *strings.Builder, fset *token.FileSet, typeName string, st *ast.StructType, skip map[string]bool, structs map[string]bool) {
	fieldNames := collectFieldNames(st)

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
			if isValueStructField(field.Type, structs) {
				// A required object-typed field is a non-pointer value (see
				// goRemoveRequired). Still return/accept a pointer here, so
				// callers can chain getters and round-trip SetX(GetX()) like
				// every other field.
				if !skip["Get"+fieldName] && !fieldNames["Get"+fieldName] {
					writeValueStructGetter(b, typeName, fieldName, fieldType)
				}
				if !skip["Set"+fieldName] && !fieldNames["Set"+fieldName] {
					writeValueStructSetter(b, typeName, fieldName, fieldType)
				}
				continue
			}
			if !skip["Get"+fieldName] && !fieldNames["Get"+fieldName] {
				writeGetter(b, typeName, fieldName, fieldType, isNilable(field.Type))
			}
			if !skip["Set"+fieldName] && !fieldNames["Set"+fieldName] {
				writeSetter(b, typeName, fieldName, fieldType)
			}
		}
	}
}

// collectFieldNames returns every name that occupies the struct's field
// namespace, including names promoted by anonymous/embedded fields. Go
// promotes an embedded type's own name into the struct's namespace (e.g.
// embedding GetFoo gives the struct a field effectively named GetFoo), so
// those still count as potential collisions even though they have no
// explicit field.Names entry of their own.
func collectFieldNames(st *ast.StructType) map[string]bool {
	fieldNames := map[string]bool{}
	for _, field := range st.Fields.List {
		if len(field.Names) == 0 {
			if n := embeddedFieldName(field.Type); n != "" {
				fieldNames[n] = true
			}
			continue
		}
		for _, name := range field.Names {
			fieldNames[name.Name] = true
		}
	}
	return fieldNames
}

// writeGetter tolerates a nil receiver so that chained getters are safe on
// partially-populated resources.
func writeGetter(b *strings.Builder, typeName, fieldName, fieldType string, nilable bool) {
	b.WriteString("\n// Get" + fieldName + " returns the " + fieldName + " field.\n")
	b.WriteString("// It returns the zero value if the receiver is nil.\n")
	b.WriteString("func (" + accessorReceiver + " *" + typeName + ") Get" + fieldName + "() " + fieldType + " {\n")
	b.WriteString("\tif " + accessorReceiver + " == nil {\n")
	if nilable {
		b.WriteString("\t\treturn nil\n")
	} else {
		b.WriteString("\t\tvar zero " + fieldType + "\n")
		b.WriteString("\t\treturn zero\n")
	}
	b.WriteString("\t}\n")
	b.WriteString("\treturn " + accessorReceiver + "." + fieldName + "\n")
	b.WriteString("}\n")
}

func writeSetter(b *strings.Builder, typeName, fieldName, fieldType string) {
	b.WriteString("\n// Set" + fieldName + " sets the " + fieldName + " field.\n")
	b.WriteString("func (" + accessorReceiver + " *" + typeName + ") Set" + fieldName + "(v " + fieldType + ") {\n")
	b.WriteString("\t" + accessorReceiver + "." + fieldName + " = v\n")
	b.WriteString("}\n")
}

// isValueStructField reports whether typ is a bare reference to a known
// local struct — the shape a required object-typed field gets (see
// goRemoveRequired), unlike every other field, which is a pointer.
func isValueStructField(typ ast.Expr, structs map[string]bool) bool {
	id, ok := typ.(*ast.Ident)
	return ok && structs[id.Name]
}

// writeValueStructGetter appends a getter for a required object-typed field
// (a non-pointer value, see isValueStructField). Returns a pointer so
// callers can chain getters like every other field, and nil on a nil
// receiver instead of panicking.
func writeValueStructGetter(b *strings.Builder, typeName, fieldName, fieldType string) {
	b.WriteString("\n// Get" + fieldName + " returns a pointer to the " + fieldName + " field.\n")
	b.WriteString("// It returns nil if the receiver is nil.\n")
	b.WriteString("func (" + accessorReceiver + " *" + typeName + ") Get" + fieldName + "() *" + fieldType + " {\n")
	b.WriteString("\tif " + accessorReceiver + " == nil {\n")
	b.WriteString("\t\treturn nil\n")
	b.WriteString("\t}\n")
	b.WriteString("\treturn &" + accessorReceiver + "." + fieldName + "\n")
	b.WriteString("}\n")
}

// writeValueStructSetter appends a setter for a required object-typed
// field. Takes a pointer to mirror the getter so SetX(GetX()) round-trips,
// and no-ops on nil since the field can only be replaced, not unset.
func writeValueStructSetter(b *strings.Builder, typeName, fieldName, fieldType string) {
	b.WriteString("\n// Set" + fieldName + " sets the " + fieldName + " field from v. It does nothing if v is nil.\n")
	b.WriteString("func (" + accessorReceiver + " *" + typeName + ") Set" + fieldName + "(v *" + fieldType + ") {\n")
	b.WriteString("\tif v == nil {\n")
	b.WriteString("\t\treturn\n")
	b.WriteString("\t}\n")
	b.WriteString("\t" + accessorReceiver + "." + fieldName + " = *v\n")
	b.WriteString("}\n")
}
