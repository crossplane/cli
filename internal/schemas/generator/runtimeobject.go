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
	"sort"
	"strings"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

// Generated root types reference k8s.io/apimachinery. The runtime package is
// imported under the k8sruntime alias to avoid colliding with the
// github.com/oapi-codegen/runtime import the model files already carry.
const (
	roRuntimeAlias  = "k8sruntime"
	roRuntimeImport = "k8s.io/apimachinery/pkg/runtime"
	roSchemaImport  = "k8s.io/apimachinery/pkg/runtime/schema"

	// roOpenAPITypesAlias is the alias oapi-codegen uses for its formatted
	// string types (Email, Date, File, UUID).
	roOpenAPITypesAlias = "openapi_types"
)

// roScalarSelectorTypes are k8s types referenced via a package selector that are
// aliases to non-struct types (time.Time, maps) and therefore have no
// DeepCopyInto method; they must be copied by value, not deep-copied.
var roScalarSelectorTypes = map[string]bool{ //nolint:gochecknoglobals // Lookup table.
	"Time":         true, // metav1.Time / MicroTime alias time.Time
	"MicroTime":    true,
	"FieldsV1":     true, // alias for map[string]interface{}
	"RawExtension": true, // alias for a JSON-ish type, no DeepCopyInto
}

// applyRuntimeObjects runs addRuntimeObjects when enabled, discarding the
// root-types flag. It is called by the generation loops after all other Go
// post-processing so it operates on the final type names.
func applyRuntimeObjects(code string, enabled bool) (string, error) {
	if !enabled {
		return code, nil
	}
	code, err := fixListItemsFields(code)
	if err != nil {
		return "", err
	}
	out, _, err := addRuntimeObjects(code)
	return out, err
}

// fixListItemsFields rewrites a <Kind>List's Items field from *[]Kind to
// []Kind. goRemoveRequired clears "required" from every schema before
// codegen, so oapi-codegen makes Items a pointer like everything else, but
// k8s.io/apimachinery/pkg/api/meta's GetItemsPtr (used by client.List, the
// fake client, and the informer cache) requires a literal slice. Runs before
// addRuntimeObjects parses the code, so writeDeepCopy sees the fixed type.
func fixListItemsFields(code string) (string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", code, parser.ParseComments)
	if err != nil {
		return "", errors.Wrap(err, "failed to parse Go code for Items field fix")
	}

	var edits []textEdit
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Assign.IsValid() {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok || st.Fields == nil || metadataKind(st) != "ListMeta" {
				continue
			}
			edits = append(edits, listItemsFieldEdits(fset, st)...)
		}
	}

	// Apply in descending offset order so earlier edits don't invalidate the
	// byte offsets computed for later ones.
	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	for _, e := range edits {
		code = code[:e.start] + e.newText + code[e.end:]
	}
	return code, nil
}

// textEdit replaces code[start:end] with newText.
type textEdit struct {
	start, end int
	newText    string
}

// listItemsFieldEdits returns the edits that rewrite a List struct's Items
// field from *[]Kind to []Kind. It returns nothing if Items is absent or isn't
// a pointer-to-slice.
func listItemsFieldEdits(fset *token.FileSet, st *ast.StructType) []textEdit {
	var edits []textEdit
	for _, field := range st.Fields.List {
		if !hasFieldName(field, "Items") {
			continue
		}
		star, ok := field.Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		arr, ok := star.X.(*ast.ArrayType)
		if !ok {
			continue
		}
		edits = append(edits, textEdit{
			start:   fset.Position(field.Type.Pos()).Offset,
			end:     fset.Position(field.Type.End()).Offset,
			newText: "[]" + renderType(fset, arr.Elt),
		})
	}
	return edits
}

// addRuntimeObjects generates controller-gen-style DeepCopy methods for every
// struct in the given Go source, plus runtime.Object + schema.ObjectKind
// methods and a scheme-registering init() for every root type (a struct with
// both APIVersion and Kind fields). It returns the augmented source and whether
// any root types were found (so the caller can emit a groupversion_info.go for
// the package).
func addRuntimeObjects(code string) (string, bool, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", code, parser.ParseComments)
	if err != nil {
		return "", false, errors.Wrap(err, "failed to parse Go code for runtime.Object generation")
	}

	structs := collectStructTypes(f)
	aliases := collectCollectionAliases(f)

	// Deterministic order: walk declarations in source order.
	var b strings.Builder
	hasRoots := false
	usesObjectMetaAccessors := false
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Assign.IsValid() {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok || st.Fields == nil {
				continue
			}
			name := ts.Name.Name
			writeDeepCopy(&b, fset, name, st, structs, aliases)
			if isRootStruct(st) {
				hasRoots = true
				kind := metadataKind(st)
				writeRuntimeObject(&b, name, st, kind)
				if kind == "ObjectMeta" {
					usesObjectMetaAccessors = true
				}
			}
		}
	}

	if b.Len() == 0 {
		return code, false, nil
	}

	combined := code + "\n" + b.String()
	// Add each import only if something generated needs it, so DeepCopy-only
	// files (e.g. shared k8s packages that aren't roots) don't get unused
	// imports.
	var extraImports []importSpec
	if hasRoots {
		extraImports = append(extraImports,
			importSpec{alias: roRuntimeAlias, path: roRuntimeImport},
			importSpec{path: roSchemaImport},
		)
	}
	if usesObjectMetaAccessors {
		extraImports = append(extraImports,
			importSpec{alias: "k8stypes", path: "k8s.io/apimachinery/pkg/types"},
		)
	}
	if len(extraImports) > 0 {
		combined, err = ensureImports(combined, extraImports)
		if err != nil {
			return "", false, err
		}
	}

	formatted, err := format.Source([]byte(combined))
	if err != nil {
		return "", false, errors.Wrap(err, "failed to format generated runtime.Object code")
	}
	return string(formatted), hasRoots, nil
}

// collectStructTypes returns the set of names that are, or alias, a struct
// type declared in the file. oapi-codegen emits `type ComponentName = GoName`
// for a schema whose x-go-type-name overrides its default derived name (see
// goRenameSchemaType in go.go) — e.g. a $ref field is typed by the component
// name, which is a true Go alias (fully interchangeable, same method set) for
// the actual struct. Without following that alias, a field using the
// component name wouldn't be recognized as the local struct it actually is.
func collectStructTypes(f *ast.File) map[string]bool {
	out := map[string]bool{}
	// aliasOf maps a type-alias name to its target ("type A = B" records
	// aliasOf["A"] = "B"). oapi-codegen aliases every root type to a
	// qualified name, and fields like a <Kind>List's Items use that alias.
	// It must resolve to a struct too, or classifyElem treats it as a
	// scalar and DeepCopy degrades to a shallow copy that shares pointers.
	aliasOf := map[string]string{}
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
			if ts.Assign.IsValid() {
				if id, ok := ts.Type.(*ast.Ident); ok {
					aliasOf[ts.Name.Name] = id.Name
				}
				continue
			}
			if _, ok := ts.Type.(*ast.StructType); ok {
				out[ts.Name.Name] = true
			}
		}
	}
	// Resolve alias chains: an alias may point at another alias before
	// reaching the underlying struct name.
	markStructAliases(out, aliasOf)
	return out
}

// markStructAliases adds name to out for every name in aliasOf whose alias
// chain (following `type A = B` links) terminates at a name already in out.
func markStructAliases(out map[string]bool, aliasOf map[string]string) {
	for name := range aliasOf {
		if !resolvesToStruct(name, aliasOf, out, map[string]bool{}) {
			continue
		}
		out[name] = true
	}
}

// resolvesToStruct follows cur's alias chain in aliasOf and reports whether
// it terminates at a name already in out. seen guards against a cycle.
func resolvesToStruct(cur string, aliasOf map[string]string, out, seen map[string]bool) bool {
	if seen[cur] {
		return false
	}
	seen[cur] = true
	target, isAlias := aliasOf[cur]
	if !isAlias {
		return out[cur]
	}
	return resolvesToStruct(target, aliasOf, out, seen)
}

// collectCollectionAliases returns local named types whose underlying type is a
// map or slice (both `type X map[..]` and `type X = map[..]`). Fields of these
// types must be deep-copied like a literal map/slice, not shallow-copied as a
// scalar.
func collectCollectionAliases(f *ast.File) map[string]ast.Expr {
	out := map[string]ast.Expr{}
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
			switch ts.Type.(type) {
			case *ast.MapType, *ast.ArrayType:
				out[ts.Name.Name] = ts.Type
			}
		}
	}
	return out
}

// isRootStruct reports whether the struct is a top-level Kubernetes object: it
// has APIVersion, Kind and Metadata fields. The Metadata requirement excludes
// object references (claimRef, resourceRefs, ownerReferences, …) which also
// carry apiVersion/kind but are not registrable kinds.
func isRootStruct(st *ast.StructType) bool {
	hasAPIVersion, hasKind, hasMetadata := false, false, false
	for _, field := range st.Fields.List {
		for _, n := range field.Names {
			switch n.Name {
			case "APIVersion":
				hasAPIVersion = true
			case "Kind":
				hasKind = true
			case "Metadata":
				hasMetadata = true
			}
		}
	}
	return hasAPIVersion && hasKind && hasMetadata
}

// hasFieldName reports whether field declares name among its identifiers.
func hasFieldName(field *ast.Field, name string) bool {
	for _, n := range field.Names {
		if n.Name == name {
			return true
		}
	}
	return false
}

// metadataKind reports whether st's Metadata field is an ObjectMeta or a
// ListMeta, matched by the field type's base name (a bare identifier or a
// package-qualified selector). Returns "" if neither matches. isRootStruct
// only checks field names, so a Kind and its KindList both match it; this
// is what lets writeRuntimeObject tell them apart.
func metadataKind(st *ast.StructType) string {
	for _, field := range st.Fields.List {
		if !hasFieldName(field, "Metadata") {
			continue
		}
		star, ok := field.Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		var typeName string
		switch x := star.X.(type) {
		case *ast.Ident:
			typeName = x.Name
		case *ast.SelectorExpr:
			typeName = x.Sel.Name
		default:
			continue
		}
		if typeName == "ObjectMeta" || typeName == "ListMeta" {
			return typeName
		}
	}
	return ""
}

// fieldKind classifies how a field's element type must be deep-copied.
type fieldKind int

const (
	// fkScalar: copy by value (basic types, named string enums, time.Time).
	fkScalar fieldKind = iota
	// fkStruct: a struct with a DeepCopyInto method.
	fkStruct
)

// isJSONRawMessage reports whether typ is exactly json.RawMessage — the
// unexported backing field oapi-codegen generates for a oneOf/anyOf union.
func isJSONRawMessage(typ ast.Expr) bool {
	sel, ok := typ.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "json" && sel.Sel.Name == "RawMessage"
}

// classifyElem classifies the element type expr (the type with any leading
// pointer/slice/map already stripped) as scalar or struct.
func classifyElem(e ast.Expr, structs map[string]bool) fieldKind {
	switch x := e.(type) {
	case *ast.Ident:
		if structs[x.Name] {
			return fkStruct
		}
		// Basic types and named scalar (enum) types copy by value.
		return fkScalar
	case *ast.SelectorExpr:
		// pkg.Type — time.* and known alias types (Time, MicroTime, FieldsV1)
		// are scalars; every other referenced package type is a generated struct
		// with a DeepCopyInto method.
		if pkg, ok := x.X.(*ast.Ident); ok && (pkg.Name == "time" || pkg.Name == roOpenAPITypesAlias) {
			// oapi-codegen emits openapi_types.Email, Date, File and UUID for
			// formatted strings. None of them has a DeepCopyInto method:
			// Email is a string, UUID is an array, Date wraps a time.Time and
			// File holds unexported fields. They copy by value.
			return fkScalar
		}
		if roScalarSelectorTypes[x.Sel.Name] {
			return fkScalar
		}
		return fkStruct
	default:
		return fkScalar
	}
}

// writeDeepCopy appends DeepCopyInto and DeepCopy methods for the struct.
func writeDeepCopy(b *strings.Builder, fset *token.FileSet, name string, st *ast.StructType, structs map[string]bool, aliases map[string]ast.Expr) {
	fmt.Fprintf(b, "\n// DeepCopyInto copies the receiver into out.\n")
	fmt.Fprintf(b, "func (in *%s) DeepCopyInto(out *%s) {\n", name, name)
	b.WriteString("\t*out = *in\n")
	for _, field := range st.Fields.List {
		if len(field.Names) == 0 {
			continue
		}
		for _, n := range field.Names {
			if n.Name == "Items" {
				if arr, ok := field.Type.(*ast.ArrayType); ok {
					writeItemsFieldCopy(b, fset, arr, structs)
					continue
				}
			}
			writeFieldCopy(b, fset, n.Name, field.Type, structs, aliases)
		}
	}
	b.WriteString("}\n")

	fmt.Fprintf(b, "\n// DeepCopy returns a deep copy of the receiver.\n")
	fmt.Fprintf(b, "func (in *%s) DeepCopy() *%s {\n", name, name)
	b.WriteString("\tif in == nil {\n\t\treturn nil\n\t}\n")
	fmt.Fprintf(b, "\tout := new(%s)\n", name)
	b.WriteString("\tin.DeepCopyInto(out)\n\treturn out\n}\n")
}

// writeItemsFieldCopy deep-copies a <Kind>List's Items field. Unlike every
// other field, Items is a plain slice (see fixListItemsFields), so it needs
// its own copy path instead of writeFieldCopy's pointer-unwrap logic.
func writeItemsFieldCopy(b *strings.Builder, fset *token.FileSet, arr *ast.ArrayType, structs map[string]bool) {
	declType := renderType(fset, arr)
	b.WriteString("\tif in.Items != nil {\n")
	fmt.Fprintf(b, "\t\tout.Items = make(%s, len(in.Items))\n", declType)
	if classifyElem(arr.Elt, structs) == fkStruct {
		b.WriteString("\t\tfor i := range in.Items {\n")
		b.WriteString("\t\t\tin.Items[i].DeepCopyInto(&out.Items[i])\n")
		b.WriteString("\t\t}\n")
	} else {
		b.WriteString("\t\tcopy(out.Items, in.Items)\n")
	}
	b.WriteString("\t}\n")
}

// writeFieldCopy appends the deep-copy snippet for a single field. Most
// generated fields are pointers; the leading pointer is handled here, then
// the pointee (scalar, struct, slice or map) is copied appropriately. Named
// aliases to a map or slice are deep-copied like their literal form.
//
// A required object-typed field (see goRemoveRequired) is a non-pointer
// struct value instead. The top-level `*out = *in` in writeDeepCopy shallow-
// copies it, aliasing any pointer fields nested inside it, so it needs its
// own DeepCopyInto call.
func writeFieldCopy(b *strings.Builder, fset *token.FileSet, field string, typ ast.Expr, structs map[string]bool, aliases map[string]ast.Expr) {
	star, ok := typ.(*ast.StarExpr)
	if !ok {
		// A required object-typed field (a bare reference to a known local
		// struct, see goRemoveRequired) needs DeepCopyInto here.
		if id, ok := typ.(*ast.Ident); ok && structs[id.Name] {
			fmt.Fprintf(b, "\tin.%s.DeepCopyInto(&out.%s)\n", field, field)
			return
		}
		// oapi-codegen's unexported `union json.RawMessage` field (its
		// oneOf/anyOf plumbing) needs its bytes copied explicitly: its
		// MarshalJSON exposes the backing slice directly, so the shallow
		// struct assignment would let mutating the copy's raw JSON bytes
		// corrupt the original. Every other non-pointer, non-struct field
		// (scalars, named string/int aliases) is already correctly copied by
		// the shallow assignment.
		if isJSONRawMessage(typ) {
			fmt.Fprintf(b, "\tif in.%s != nil {\n", field)
			fmt.Fprintf(b, "\t\tout.%s = make(json.RawMessage, len(in.%s))\n", field, field)
			fmt.Fprintf(b, "\t\tcopy(out.%s, in.%s)\n", field, field)
			b.WriteString("\t}\n")
		}
		return
	}

	fmt.Fprintf(b, "\tif in.%s != nil {\n", field)
	fmt.Fprintf(b, "\t\tin, out := &in.%s, &out.%s\n", field, field)

	pointee := star.X
	declType := renderType(fset, pointee)
	// Resolve a named alias (e.g. `type Patch = map[string]interface{}`) to its
	// underlying map/slice so it is deep-copied rather than shallow-copied.
	if id, ok := pointee.(*ast.Ident); ok {
		if underlying, isAlias := aliases[id.Name]; isAlias {
			pointee = underlying
		}
	}

	switch p := pointee.(type) {
	case *ast.ArrayType:
		writeSliceCopy(b, declType, p, structs)
	case *ast.MapType:
		writeMapCopy(b, fset, declType, p, structs)
	default:
		fmt.Fprintf(b, "\t\t*out = new(%s)\n", declType)
		if classifyElem(pointee, structs) == fkStruct {
			b.WriteString("\t\t(*in).DeepCopyInto(*out)\n")
		} else {
			b.WriteString("\t\t**out = **in\n")
		}
	}

	b.WriteString("\t}\n")
}

// writeSliceCopy handles a *[]Elem field. declType is the type to allocate (the
// literal slice type or a named alias). On entry in/out are *(*declType).
func writeSliceCopy(b *strings.Builder, declType string, arr *ast.ArrayType, structs map[string]bool) {
	fmt.Fprintf(b, "\t\t*out = new(%s)\n", declType)
	b.WriteString("\t\tif *in != nil {\n")
	b.WriteString("\t\t\tin, out := *in, *out\n")
	fmt.Fprintf(b, "\t\t\t*out = make(%s, len(*in))\n", declType)
	if classifyElem(arr.Elt, structs) == fkStruct {
		b.WriteString("\t\t\tfor i := range *in {\n")
		b.WriteString("\t\t\t\t(*in)[i].DeepCopyInto(&(*out)[i])\n")
		b.WriteString("\t\t\t}\n")
	} else {
		b.WriteString("\t\t\tcopy(*out, *in)\n")
	}
	b.WriteString("\t\t}\n")
}

// writeMapCopy handles a *map[K]V field. declType is the type to allocate (the
// literal map type or a named alias). On entry in/out are *(*declType).
func writeMapCopy(b *strings.Builder, fset *token.FileSet, declType string, m *ast.MapType, structs map[string]bool) {
	valType := renderType(fset, m.Value)
	fmt.Fprintf(b, "\t\t*out = new(%s)\n", declType)
	b.WriteString("\t\tif *in != nil {\n")
	b.WriteString("\t\t\tin, out := *in, *out\n")
	fmt.Fprintf(b, "\t\t\t*out = make(%s, len(*in))\n", declType)
	if classifyElem(m.Value, structs) == fkStruct {
		fmt.Fprintf(b, "\t\t\tfor key, val := range *in {\n")
		fmt.Fprintf(b, "\t\t\t\tvar v %s\n", valType)
		b.WriteString("\t\t\t\tval.DeepCopyInto(&v)\n")
		b.WriteString("\t\t\t\t(*out)[key] = v\n")
		b.WriteString("\t\t\t}\n")
	} else {
		b.WriteString("\t\t\tfor key, val := range *in {\n")
		b.WriteString("\t\t\t\t(*out)[key] = val\n")
		b.WriteString("\t\t\t}\n")
	}
	b.WriteString("\t\t}\n")
}

// writeRuntimeObject appends runtime.Object + schema.ObjectKind methods and a
// scheme-registering init() for a root type.
func writeRuntimeObject(b *strings.Builder, name string, st *ast.StructType, metaKind string) {
	// DeepCopyObject.
	fmt.Fprintf(b, "\n// DeepCopyObject returns a deep copy of the receiver as a runtime.Object.\n")
	fmt.Fprintf(b, "func (in *%s) DeepCopyObject() %s.Object {\n", name, roRuntimeAlias)
	b.WriteString("\tif c := in.DeepCopy(); c != nil {\n\t\treturn c\n\t}\n\treturn nil\n}\n")

	// GetObjectKind returns the object itself, which implements schema.ObjectKind.
	fmt.Fprintf(b, "\n// GetObjectKind implements runtime.Object.\n")
	fmt.Fprintf(b, "func (in *%s) GetObjectKind() schema.ObjectKind { return in }\n", name)

	apiVersionType := fieldElemTypeName(st, "APIVersion")
	kindType := fieldElemTypeName(st, "Kind")

	// GroupVersionKind reads the typed APIVersion/Kind fields.
	fmt.Fprintf(b, "\n// GroupVersionKind implements schema.ObjectKind.\n")
	fmt.Fprintf(b, "func (in *%s) GroupVersionKind() schema.GroupVersionKind {\n", name)
	b.WriteString("\tvar apiVersion, kind string\n")
	b.WriteString("\tif in.APIVersion != nil {\n\t\tapiVersion = string(*in.APIVersion)\n\t}\n")
	b.WriteString("\tif in.Kind != nil {\n\t\tkind = string(*in.Kind)\n\t}\n")
	b.WriteString("\treturn schema.FromAPIVersionAndKind(apiVersion, kind)\n}\n")

	// SetGroupVersionKind writes the typed APIVersion/Kind fields.
	fmt.Fprintf(b, "\n// SetGroupVersionKind implements schema.ObjectKind.\n")
	fmt.Fprintf(b, "func (in *%s) SetGroupVersionKind(gvk schema.GroupVersionKind) {\n", name)
	fmt.Fprintf(b, "\tapiVersion := %s(gvk.GroupVersion().String())\n", apiVersionType)
	b.WriteString("\tin.APIVersion = &apiVersion\n")
	fmt.Fprintf(b, "\tkind := %s(gvk.Kind)\n", kindType)
	b.WriteString("\tin.Kind = &kind\n}\n")

	// Register the type with the package SchemeBuilder (defined in
	// groupversion_info.go) so AddToScheme makes it known to a runtime.Scheme.
	fmt.Fprintf(b, "\nfunc init() {\n")
	fmt.Fprintf(b, "\tSchemeBuilder.Register(func(s *%s.Scheme) error {\n", roRuntimeAlias)
	fmt.Fprintf(b, "\t\ts.AddKnownTypes(GroupVersion, &%s{})\n", name)
	b.WriteString("\t\treturn nil\n\t})\n}\n")

	switch metaKind {
	case "ObjectMeta":
		writeObjectMetaAccessors(b, name)
	case "ListMeta":
		writeListInterfaceAccessors(b, name)
	}
}

// objectMetaField describes one metav1.Object getter/setter pair: the field
// name (used as both the JSON-schema-derived method name and the delegated
// call on the real metav1.ObjectMeta), its exact Go type per the interface,
// and the literal expression to return from the getter when Metadata is nil.
type objectMetaField struct {
	name   string
	goType string
	zero   string
}

// writeObjectMetaAccessors appends the metav1.Object getter/setter pairs for
// name, delegating to its Metadata field. Getters return the zero value when
// Metadata is nil; setters allocate it lazily.
func writeObjectMetaAccessors(b *strings.Builder, name string) {
	// objectMetaFields is every getter/setter pair sigs.k8s.io/apimachinery's
	// metav1.Object interface declares, in its exact declared types.
	objectMetaFields := []objectMetaField{
		{"Namespace", "string", `""`}, //nolint:goconst // Literal type names read better than constants in generated code.
		{"Name", "string", `""`},
		{"GenerateName", "string", `""`},
		{"UID", "k8stypes.UID", `""`},
		{"ResourceVersion", "string", `""`},
		{"Generation", "int64", "0"},
		{"SelfLink", "string", `""`},
		{"CreationTimestamp", "k8smetav1.Time", "k8smetav1.Time{}"},
		{"DeletionTimestamp", "*k8smetav1.Time", "nil"}, //nolint:goconst // Literal zero values read better than constants in generated code.
		{"DeletionGracePeriodSeconds", "*int64", "nil"},
		{"Labels", "map[string]string", "nil"},
		{"Annotations", "map[string]string", "nil"},
		{"Finalizers", "[]string", "nil"},
		{"OwnerReferences", "[]k8smetav1.OwnerReference", "nil"},
		{"ManagedFields", "[]k8smetav1.ManagedFieldsEntry", "nil"},
	}

	for _, f := range objectMetaFields {
		fmt.Fprintf(b, "\nfunc (in *%s) Get%s() %s {\n", name, f.name, f.goType)
		b.WriteString("\tif in.Metadata == nil {\n")
		fmt.Fprintf(b, "\t\treturn %s\n", f.zero)
		b.WriteString("\t}\n")
		fmt.Fprintf(b, "\treturn in.Metadata.Get%s()\n}\n", f.name)

		fmt.Fprintf(b, "\nfunc (in *%s) Set%s(v %s) {\n", name, f.name, f.goType)
		b.WriteString("\tif in.Metadata == nil {\n")
		b.WriteString("\t\tin.Metadata = &k8smetav1.ObjectMeta{}\n")
		b.WriteString("\t}\n")
		fmt.Fprintf(b, "\tin.Metadata.Set%s(v)\n}\n", f.name)
	}
}

// listMetaField is objectMetaField's counterpart for metav1.ListInterface.
type listMetaField struct {
	name   string
	goType string
	zero   string
}

// writeListInterfaceAccessors is writeObjectMetaAccessors' counterpart for
// metav1.ListInterface.
func writeListInterfaceAccessors(b *strings.Builder, name string) {
	// listMetaFields is every getter/setter pair metav1.ListInterface declares.
	listMetaFields := []listMetaField{
		{"ResourceVersion", "string", `""`},
		{"SelfLink", "string", `""`},
		{"Continue", "string", `""`},
		{"RemainingItemCount", "*int64", "nil"},
	}

	for _, f := range listMetaFields {
		fmt.Fprintf(b, "\nfunc (in *%s) Get%s() %s {\n", name, f.name, f.goType)
		b.WriteString("\tif in.Metadata == nil {\n")
		fmt.Fprintf(b, "\t\treturn %s\n", f.zero)
		b.WriteString("\t}\n")
		fmt.Fprintf(b, "\treturn in.Metadata.Get%s()\n}\n", f.name)

		fmt.Fprintf(b, "\nfunc (in *%s) Set%s(v %s) {\n", name, f.name, f.goType)
		b.WriteString("\tif in.Metadata == nil {\n")
		b.WriteString("\t\tin.Metadata = &k8smetav1.ListMeta{}\n")
		b.WriteString("\t}\n")
		fmt.Fprintf(b, "\tin.Metadata.Set%s(v)\n}\n", f.name)
	}
}

// fieldElemTypeName returns the element type name of a pointer field (e.g. for
// `APIVersion *FooAPIVersion` it returns "FooAPIVersion"; for `*string` it
// returns "string"). Used to cast when setting the typed fields.
func fieldElemTypeName(st *ast.StructType, field string) string {
	for _, f := range st.Fields.List {
		for _, n := range f.Names {
			if n.Name != field {
				continue
			}
			if star, ok := f.Type.(*ast.StarExpr); ok {
				if id, ok := star.X.(*ast.Ident); ok {
					return id.Name
				}
			}
		}
	}
	return "string"
}

// renderType renders a type expression to its Go source string.
func renderType(fset *token.FileSet, e ast.Expr) string {
	var sb strings.Builder
	if err := format.Node(&sb, fset, e); err != nil {
		return ""
	}
	return sb.String()
}

// importSpec describes an import to add: an optional alias and the package path.
type importSpec struct {
	alias string
	path  string
}

// ensureImports adds the given imports to the file if not already present,
// inserting them as a new import declaration after the package clause.
// format.Source tidies the result afterward.
func ensureImports(code string, specs []importSpec) (string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", code, parser.ParseComments)
	if err != nil {
		return "", errors.Wrap(err, "failed to parse code for import injection")
	}

	existing := map[string]bool{}
	for _, imp := range f.Imports {
		existing[strings.Trim(imp.Path.Value, `"`)] = true
	}

	var lines []string
	for _, s := range specs {
		if existing[s.path] {
			continue
		}
		if s.alias != "" {
			lines = append(lines, fmt.Sprintf("\t%s %q", s.alias, s.path))
		} else {
			lines = append(lines, fmt.Sprintf("\t%q", s.path))
		}
	}
	if len(lines) == 0 {
		return code, nil
	}
	sort.Strings(lines)

	imports := "import (\n" + strings.Join(lines, "\n") + "\n)\n"

	pkgEnd := fset.Position(f.Name.End()).Offset
	return code[:pkgEnd] + "\n\n" + imports + code[pkgEnd:], nil
}
