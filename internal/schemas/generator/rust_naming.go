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
	"strings"
	"unicode"

	"github.com/gobuffalo/flect"
)

// Rust types the emitter refers to. Everything outside the prelude is written
// fully qualified, so generated files need no use declarations.
const (
	rustValueType  = "serde_json::Value"
	rustMapType    = "std::collections::BTreeMap"
	rustStringType = "String"

	// rustUnnamed stands in for a field or module name with no letter or digit
	// in it. An underscore alone is not an identifier Rust accepts there.
	rustUnnamed = "unnamed"
)

// rustKeywords maps every Rust keyword and reserved word to whether it is
// valid as a raw identifier (r#name). crate, self, Self and super are not, so
// they get a trailing underscore instead.
//
//nolint:gochecknoglobals // Effectively a constant; Go has no constant maps.
var rustKeywords = map[string]bool{
	// Strict keywords.
	"as": true, "async": true, "await": true, "break": true, "const": true,
	"continue": true, "dyn": true, "else": true, "enum": true, "extern": true,
	"false": true, "fn": true, "for": true, "if": true, "impl": true,
	"in": true, "let": true, "loop": true, "match": true, "mod": true,
	"move": true, "mut": true, "pub": true, "ref": true, "return": true,
	"static": true, "struct": true, "trait": true, "true": true, "type": true,
	"unsafe": true, "use": true, "where": true, "while": true,
	// Strict keywords that cannot be raw identifiers.
	"crate": false, "self": false, "Self": false, "super": false,
	// Reserved for future use.
	"abstract": true, "become": true, "box": true, "do": true, "final": true,
	"gen": true, "macro": true, "override": true, "priv": true, "try": true,
	"typeof": true, "unsized": true, "virtual": true, "yield": true,
}

// rustPreludeTypes are type names the generated code uses unqualified. A
// generated type of the same name would shadow them, so it gets a trailing
// underscore.
//
//nolint:gochecknoglobals // Effectively a constant; Go has no constant maps.
var rustPreludeTypes = map[string]bool{
	"Box": true, "Option": true, "String": true, "Vec": true,
}

// rustSplitWords splits an identifier into its words. Boundaries are:
//
//   - any character that is not a letter or a digit (dropped);
//   - a lowercase letter or digit followed by an uppercase letter, so
//     "apiVersion" is "api" "Version" and "sha256Sum" is "sha256" "Sum";
//   - inside a run of uppercase letters, the position before the last one when
//     it is followed by at least two lowercase letters, so "HTTPServer" is
//     "HTTP" "Server" and "VMSize" is "VM" "Size".
//
// The two-lowercase-letter requirement in the last rule is what keeps plural
// acronyms and version-like names in one piece: "podCIDRs" stays "pod" "CIDRs"
// and "IPv6" stays one word, where heck would split them. Digits never start or
// end a word on their own, so "v1alpha1" is a single word.
func rustSplitWords(s string) []string {
	rs := []rune(s)
	words := make([]string, 0, 4)
	cur := make([]rune, 0, len(rs))

	flush := func() {
		if len(cur) > 0 {
			words = append(words, string(cur))
			cur = cur[:0]
		}
	}

	for i, r := range rs {
		if !isRustWordRune(r) {
			flush()
			continue
		}
		if len(cur) > 0 {
			prev := cur[len(cur)-1]
			switch {
			case unicode.IsUpper(r) && (unicode.IsLower(prev) || unicode.IsDigit(prev)):
				flush()
			case unicode.IsUpper(r) && unicode.IsUpper(prev) && rustStartsWord(rs[i+1:]):
				flush()
			}
		}
		cur = append(cur, r)
	}
	flush()

	return words
}

func isRustWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// rustStartsWord reports whether rs begins with at least two lowercase letters,
// which is what makes the preceding uppercase letter the start of a new word.
func rustStartsWord(rs []rune) bool {
	n := 0
	for _, r := range rs {
		if !unicode.IsLower(r) {
			break
		}
		if n++; n == 2 {
			return true
		}
	}
	return false
}

// rustSnakeCase renders s in snake_case, without identifier sanitization.
func rustSnakeCase(s string) string {
	words := rustSplitWords(s)
	for i, w := range words {
		words[i] = strings.ToLower(w)
	}
	return strings.Join(words, "_")
}

// rustUpperCamelCase renders s in UpperCamelCase, without identifier
// sanitization.
func rustUpperCamelCase(s string) string {
	var b strings.Builder
	for _, w := range rustSplitWords(s) {
		rs := []rune(strings.ToLower(w))
		rs[0] = unicode.ToUpper(rs[0])
		b.WriteString(string(rs))
	}
	return b.String()
}

// rustFieldIdent turns a JSON property name into a Rust field identifier. The
// JSON name is always preserved in a serde rename attribute, so the identifier
// only has to be valid, not reversible.
func rustFieldIdent(jsonName string) string {
	name := rustSnakeCase(jsonName)
	if name == "" {
		return rustUnnamed
	}
	if unicode.IsDigit(rune(name[0])) {
		name = "_" + name
	}
	if raw, ok := rustKeywords[name]; ok {
		if raw {
			return "r#" + name
		}
		return name + "_"
	}
	return name
}

// rustTypeName sanitizes s into a Rust type identifier. Unlike field and module
// names it keeps the input's spelling, so Kubernetes kinds stay recognizable
// (JSONSchemaProps does not become JsonSchemaProps). It returns "" when s has
// nothing to make a name of, and such a schema is not generated at all.
func rustTypeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '_' || isRustWordRune(r) {
			b.WriteRune(r)
		}
	}

	name := b.String()
	if strings.Trim(name, "_") == "" {
		return ""
	}

	rs := []rune(name)
	if unicode.IsDigit(rs[0]) {
		name = "_" + name
	} else {
		rs[0] = unicode.ToUpper(rs[0])
		name = string(rs)
	}

	// Type names are UpperCamelCase, so the only keyword they can collide with
	// is Self. Raw identifiers would work here but read badly in generated
	// code, hence the underscore.
	if _, ok := rustKeywords[name]; ok || rustPreludeTypes[name] {
		name += "_"
	}

	return name
}

// rustNestedTypeName returns the name for the struct generated from an inline
// object at property field of the type named owner.
func rustNestedTypeName(owner, field string) string {
	return owner + rustUpperCamelCase(field)
}

// rustItemTypeName returns the name for the struct generated from the items of
// an array whose own type name would be nested. It singularizes the last word,
// so an inline object under "resourceRefs" becomes <Owner>ResourceRef. Names
// whose last word has no distinct singular form get an Item suffix instead.
func rustItemTypeName(nested string) string {
	words := rustSplitWords(nested)
	if len(words) == 0 {
		return nested + "Item"
	}

	last := words[len(words)-1]
	singular := flect.Singularize(last)
	if strings.EqualFold(singular, last) || !strings.HasSuffix(nested, last) {
		return nested + "Item"
	}

	// Replace only the last word, so the rest of the name keeps the spelling it
	// inherited from its parent type.
	return strings.TrimSuffix(nested, last) + rustUpperFirst(singular)
}

// rustUpperFirst uppercases the first rune of s, leaving the rest alone.
func rustUpperFirst(s string) string {
	if s == "" {
		return s
	}
	rs := []rune(s)
	rs[0] = unicode.ToUpper(rs[0])
	return string(rs)
}

// rustModuleSegment sanitizes one segment of a component schema name into a
// Rust module identifier. The result is also the directory name on disk, so
// every segment we emit is a valid identifier by construction and the module
// tree can be rebuilt from a directory listing.
func rustModuleSegment(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}

	name := b.String()
	if strings.Trim(name, "_") == "" {
		return rustUnnamed
	}
	if name[0] >= '0' && name[0] <= '9' {
		name = "_" + name
	}
	// Raw identifiers are not allowed for r#crate, r#self and r#super, so
	// module names take an underscore for every keyword.
	if _, ok := rustKeywords[name]; ok {
		name += "_"
	}

	return name
}

// rustSplitSchemaName splits a component schema name into the module path and
// type name it maps to. Component schema names are reversed dotted group names
// for CRDs (co.acme.platform.v1alpha1.XAccountScaffold) and canonical package
// paths for Kubernetes types (io.k8s.api.core.v1.Pod), so all a segment needs
// is sanitization. typ is "" for a name that ends in nothing usable, which is
// what a CRD without spec.names.listKind gives its list schema.
func rustSplitSchemaName(name string) (module []string, typ string) {
	segments := strings.Split(name, ".")
	typ = rustTypeName(segments[len(segments)-1])

	module = make([]string, 0, len(segments)-1)
	for _, s := range segments[:len(segments)-1] {
		module = append(module, rustModuleSegment(s))
	}

	return module, typ
}

// rustPathForSchemaName returns the absolute path of the type generated for a
// component schema, e.g.
// crate::io::k8s::apimachinery::pkg::apis::meta::v1::ObjectMeta.
func rustPathForSchemaName(name string) string {
	module, typ := rustSplitSchemaName(name)
	return "crate::" + strings.Join(append(module, typ), "::")
}

// rustFileStem returns the file name, without the .rs extension, holding the
// type named typ. Files are an implementation detail (every type is re-exported
// by its module), so the stem is just the lowercased type name, kept a valid
// identifier because mod.rs declares it.
func rustFileStem(typ string) string {
	stem := strings.ToLower(typ)
	if _, ok := rustKeywords[stem]; ok || stem == "lib" {
		stem += "_"
	}
	return stem
}

// rustRefTarget returns the component schema name a $ref points at, or "" if it
// points anywhere else.
func rustRefTarget(ref string) string {
	target, ok := strings.CutPrefix(ref, "#/components/schemas/")
	if !ok {
		return ""
	}
	return target
}
