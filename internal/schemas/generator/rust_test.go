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
	"context"
	"io/fs"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/spf13/afero"
	"k8s.io/kube-openapi/pkg/validation/spec"

	"github.com/crossplane/cli/v2/internal/schemas/runner"
)

// Helpers for building the small hand-written schemas the emitter tests use.
// Real CRD and Kubernetes documents are covered by the testdata tests below;
// these give the emitter cases with no incidental detail.

func rustTestSchema(t spec.Schema, description string) spec.Schema {
	t.Description = description
	return t
}

func rustTestScalar(typ, format string) spec.Schema {
	return spec.Schema{SchemaProps: spec.SchemaProps{Type: spec.StringOrArray{typ}, Format: format}}
}

func rustTestObject(props map[string]spec.Schema) spec.Schema {
	return spec.Schema{SchemaProps: spec.SchemaProps{Type: spec.StringOrArray{"object"}, Properties: props}}
}

func rustTestArray(items spec.Schema) spec.Schema {
	return spec.Schema{SchemaProps: spec.SchemaProps{
		Type:  spec.StringOrArray{"array"},
		Items: &spec.SchemaOrArray{Schema: &items},
	}}
}

func rustTestMap(values spec.Schema) spec.Schema {
	return spec.Schema{SchemaProps: spec.SchemaProps{
		Type:                 spec.StringOrArray{"object"},
		AdditionalProperties: &spec.SchemaOrBool{Allows: true, Schema: &values},
	}}
}

// rustTestRef builds the allOf-wrapped reference CRD and Kubernetes documents
// use, which is also the shape a plain $ref has to survive.
func rustTestRef(schema string) spec.Schema {
	return spec.Schema{SchemaProps: spec.SchemaProps{
		AllOf: []spec.Schema{{SchemaProps: spec.SchemaProps{Ref: spec.MustCreateRef("#/components/schemas/" + schema)}}},
	}}
}

func rustTestEnum(values ...string) spec.Schema {
	s := rustTestScalar("string", "")
	for _, v := range values {
		s.Enum = append(s.Enum, v)
	}
	return s
}

func rustTestDefault(s spec.Schema, def string) spec.Schema {
	s.Default = def
	return s
}

// rustTestEmit renders the file the named schema leads, going through the real
// grouping so a test sees what the generator would write.
func rustTestEmit(t *testing.T, name string, schemas map[string]*spec.Schema) string {
	t.Helper()

	boxed := rustDetectCycles(schemas)
	for _, group := range rustGroupSchemas(schemas) {
		if group.primary == name {
			return string(rustEmitFile(group, schemas, boxed))
		}
	}
	t.Fatalf("no file is named after schema %q", name)
	return ""
}

// TestRustEmitSchemaFile locks the shape of the generated code: field
// attributes, optionality, type mapping, the naming of inline structs, and the
// consts and Default impl that let a function build a resource without naming
// its group and version.
func TestRustEmitSchemaFile(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		reason string
		args   struct {
			schemas map[string]*spec.Schema
		}
		want struct {
			code string
		}
	}{
		"Resource": {
			reason: "A resource's fields are optional and renamed to their wire names, each kind of schema maps to its Rust type, inline objects become structs named after their path, and a root gets its consts and a Default impl.",
			args: struct{ schemas map[string]*spec.Schema }{schemas: map[string]*spec.Schema{
				"io.k8s.apimachinery.pkg.apis.meta.v1.ObjectMeta": {SchemaProps: rustTestObject(map[string]spec.Schema{
					"name": rustTestScalar("string", ""),
				}).SchemaProps},
				"com.example.v1.Widget": {SchemaProps: rustTestSchema(rustTestObject(map[string]spec.Schema{
					"apiVersion": rustTestDefault(rustTestScalar("string", ""), "example.com/v1"),
					"kind":       rustTestDefault(rustTestScalar("string", ""), "Widget"),
					"metadata":   rustTestRef("io.k8s.apimachinery.pkg.apis.meta.v1.ObjectMeta"),
					"spec": rustTestSchema(rustTestObject(map[string]spec.Schema{
						"replicas": rustTestScalar("integer", "int32"),
						"weight":   rustTestScalar("integer", ""),
						"ratio":    rustTestScalar("number", ""),
						"enabled":  rustTestScalar("boolean", ""),
						"type":     rustTestSchema(rustTestEnum("Static", "Dynamic"), "How the widget is wired up."),
						"labels":   rustTestMap(rustTestScalar("string", "")),
						"hosts":    rustTestArray(rustTestScalar("string", "")),
						"rules": rustTestArray(rustTestObject(map[string]spec.Schema{
							"port": rustTestScalar("integer", "int32"),
						})),
						"free":    rustTestScalar("object", ""),
						"unknown": {},
					}), "The widget's desired state."),
				}), "Widget is a widget.").SchemaProps},
			}},
			want: struct{ code string }{code: `// Code generated by github.com/crossplane/cli/v2 DO NOT EDIT.

/// Widget is a widget.
#[derive(Clone, Debug, PartialEq, serde::Serialize, serde::Deserialize)]
pub struct Widget {
    #[serde(rename = "apiVersion", default, skip_serializing_if = "Option::is_none")]
    pub api_version: Option<String>,

    #[serde(rename = "kind", default, skip_serializing_if = "Option::is_none")]
    pub kind: Option<String>,

    #[serde(rename = "metadata", default, skip_serializing_if = "Option::is_none")]
    pub metadata: Option<crate::io::k8s::apimachinery::pkg::apis::meta::v1::ObjectMeta>,

    /// The widget's desired state.
    #[serde(rename = "spec", default, skip_serializing_if = "Option::is_none")]
    pub spec: Option<WidgetSpec>,
}

impl Widget {
    /// The apiVersion of this resource.
    pub const API_VERSION: &'static str = "example.com/v1";

    /// The kind of this resource.
    pub const KIND: &'static str = "Widget";
}

impl Default for Widget {
    fn default() -> Self {
        Self {
            api_version: Some(Self::API_VERSION.to_string()),
            kind: Some(Self::KIND.to_string()),
            metadata: None,
            spec: None,
        }
    }
}

/// The widget's desired state.
#[derive(Clone, Debug, Default, PartialEq, serde::Serialize, serde::Deserialize)]
pub struct WidgetSpec {
    #[serde(rename = "enabled", default, skip_serializing_if = "Option::is_none")]
    pub enabled: Option<bool>,

    #[serde(rename = "free", default, skip_serializing_if = "Option::is_none")]
    pub free: Option<serde_json::Value>,

    #[serde(rename = "hosts", default, skip_serializing_if = "Option::is_none")]
    pub hosts: Option<Vec<String>>,

    #[serde(rename = "labels", default, skip_serializing_if = "Option::is_none")]
    pub labels: Option<std::collections::BTreeMap<String, String>>,

    #[serde(rename = "ratio", default, skip_serializing_if = "Option::is_none")]
    pub ratio: Option<f64>,

    #[serde(rename = "replicas", default, skip_serializing_if = "Option::is_none")]
    pub replicas: Option<i32>,

    #[serde(rename = "rules", default, skip_serializing_if = "Option::is_none")]
    pub rules: Option<Vec<WidgetSpecRule>>,

    /// How the widget is wired up.
    ///
    /// Allowed values: ` + "`Static`, `Dynamic`" + `.
    #[serde(rename = "type", default, skip_serializing_if = "Option::is_none")]
    pub r#type: Option<String>,

    #[serde(rename = "unknown", default, skip_serializing_if = "Option::is_none")]
    pub unknown: Option<serde_json::Value>,

    #[serde(rename = "weight", default, skip_serializing_if = "Option::is_none")]
    pub weight: Option<i64>,
}

#[derive(Clone, Debug, Default, PartialEq, serde::Serialize, serde::Deserialize)]
pub struct WidgetSpecRule {
    #[serde(rename = "port", default, skip_serializing_if = "Option::is_none")]
    pub port: Option<i32>,
}
`},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := rustTestEmit(t, "com.example.v1.Widget", tc.args.schemas)
			if diff := cmp.Diff(tc.want.code, got); diff != "" {
				t.Errorf("\n%s\nemitted Widget: -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

// TestRustEmitNonObjectSchemas covers the named schemas that are not objects:
// scalar wrappers such as Time, unstructured types such as RawExtension, and
// the scalar unions Quantity and IntOrString.
func TestRustEmitNonObjectSchemas(t *testing.T) {
	t.Parallel()

	schemas := map[string]*spec.Schema{
		"io.k8s.apimachinery.pkg.apis.meta.v1.Time":     {SchemaProps: rustTestScalar("string", "date-time").SchemaProps},
		"io.k8s.apimachinery.pkg.runtime.RawExtension":  {SchemaProps: rustTestScalar("object", "").SchemaProps},
		"io.k8s.apimachinery.pkg.api.resource.Quantity": {SchemaProps: spec.SchemaProps{OneOf: []spec.Schema{rustTestScalar("string", ""), rustTestScalar("number", "")}}},
		"io.k8s.apimachinery.pkg.util.intstr.IntOrString": {SchemaProps: spec.SchemaProps{
			Format: "int-or-string",
			OneOf:  []spec.Schema{rustTestScalar("integer", ""), rustTestScalar("string", "")},
		}},
	}

	cases := map[string]struct {
		reason string
		args   struct{ schema string }
		want   struct{ code string }
	}{
		"ScalarWrapperBecomesAlias": {
			reason: "A named scalar is an alias for the Rust type the scalar maps to.",
			args:   struct{ schema string }{schema: "io.k8s.apimachinery.pkg.apis.meta.v1.Time"},
			want:   struct{ code string }{code: "pub type Time = String;\n"},
		},
		"UnstructuredObjectBecomesValue": {
			reason: "An object that declares no properties can hold anything, so it is an alias for a JSON value.",
			args:   struct{ schema string }{schema: "io.k8s.apimachinery.pkg.runtime.RawExtension"},
			want:   struct{ code string }{code: "pub type RawExtension = serde_json::Value;\n"},
		},
		"ScalarUnionBecomesUntaggedEnum": {
			reason: "A choice of scalars is an untagged enum with a variant for each.",
			args:   struct{ schema string }{schema: "io.k8s.apimachinery.pkg.api.resource.Quantity"},
			want: struct{ code string }{code: `#[derive(Clone, Debug, PartialEq, serde::Serialize, serde::Deserialize)]
#[serde(untagged)]
pub enum Quantity {
    String(String),
    Number(f64),
}
`},
		},
		"IntOrStringKeepsVariantOrder": {
			reason: "serde tries untagged variants in order, so they keep the order the schema lists them in.",
			args:   struct{ schema string }{schema: "io.k8s.apimachinery.pkg.util.intstr.IntOrString"},
			want: struct{ code string }{code: `#[derive(Clone, Debug, PartialEq, serde::Serialize, serde::Deserialize)]
#[serde(untagged)]
pub enum IntOrString {
    Int(i64),
    String(String),
}
`},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			code := rustTestEmit(t, tc.args.schema, schemas)
			got := struct{ code string }{code: strings.TrimPrefix(code, rustGeneratedHeader+"\n\n")}
			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(got)); diff != "" {
				t.Errorf("\n%s\nemitted code for %q: -want, +got:\n%s", tc.reason, tc.args.schema, diff)
			}
		})
	}
}

// TestRustBoxesReferenceCycles covers the types that reference each other
// inline. Without a Box they would have infinite size and the crate would not
// compile; a reference through a Vec or a map is already indirect and must not
// be boxed, or the models get needlessly awkward to use.
func TestRustBoxesReferenceCycles(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		reason string
		args   struct {
			schemas map[string]*spec.Schema
		}
		want struct {
			edges []rustEdge
			// contains is the code the file a schema leads must contain.
			contains map[string][]string
		}
	}{
		"SelfReference": {
			reason: "A type that refers to itself inline, the JSONSchemaProps shape, is boxed. Its reference through a Vec is already indirect, and its reference to another type closes no cycle.",
			args: struct{ schemas map[string]*spec.Schema }{schemas: map[string]*spec.Schema{
				"com.example.v1.Node": {SchemaProps: rustTestObject(map[string]spec.Schema{
					"child":    rustTestRef("com.example.v1.Node"),
					"children": rustTestArray(rustTestRef("com.example.v1.Node")),
					"leaf":     rustTestRef("com.example.v1.Leaf"),
				}).SchemaProps},
				"com.example.v1.Leaf": {SchemaProps: rustTestObject(map[string]spec.Schema{
					"name": rustTestScalar("string", ""),
				}).SchemaProps},
			}},
			want: struct {
				edges    []rustEdge
				contains map[string][]string
			}{
				edges: []rustEdge{{from: "com.example.v1.Node", to: "com.example.v1.Node"}},
				contains: map[string][]string{"com.example.v1.Node": {
					"pub child: Option<Box<crate::com::example::v1::Node>>,",
					"pub children: Option<Vec<crate::com::example::v1::Node>>,",
					"pub leaf: Option<crate::com::example::v1::Leaf>,",
				}},
			},
		},
		"MutualReference": {
			reason: "Of two types that refer to each other, only the reference that closes the cycle is boxed. Left is walked first, so that is the one back from Right.",
			args: struct{ schemas map[string]*spec.Schema }{schemas: map[string]*spec.Schema{
				"com.example.v1.Left": {SchemaProps: rustTestObject(map[string]spec.Schema{
					"right": rustTestRef("com.example.v1.Right"),
				}).SchemaProps},
				"com.example.v1.Right": {SchemaProps: rustTestObject(map[string]spec.Schema{
					"left": rustTestRef("com.example.v1.Left"),
				}).SchemaProps},
			}},
			want: struct {
				edges    []rustEdge
				contains map[string][]string
			}{
				edges: []rustEdge{{from: "com.example.v1.Right", to: "com.example.v1.Left"}},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := slices.SortedFunc(maps.Keys(rustDetectCycles(tc.args.schemas)), func(a, b rustEdge) int {
				if c := strings.Compare(a.from, b.from); c != 0 {
					return c
				}
				return strings.Compare(a.to, b.to)
			})
			if diff := cmp.Diff(tc.want.edges, got, cmp.AllowUnexported(rustEdge{})); diff != "" {
				t.Errorf("\n%s\nrustDetectCycles(...): -want boxed edges, +got boxed edges:\n%s", tc.reason, diff)
			}

			for schema, contains := range tc.want.contains {
				code := rustTestEmit(t, schema, tc.args.schemas)
				for _, want := range contains {
					if !strings.Contains(code, want) {
						t.Errorf("\n%s\ngenerated %s is missing %q:\n%s", tc.reason, schema, want, code)
					}
				}
			}
		})
	}
}

// TestRustReferenceToUnknownSchemaFallsBack covers a reference we cannot
// resolve. Emitting the path anyway would produce a crate that doesn't compile,
// which is worse than an untyped field.
func TestRustReferenceToUnknownSchemaFallsBack(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		reason string
		args   struct {
			schemas map[string]*spec.Schema
		}
		want struct {
			contains []string
		}
	}{
		"MissingSchema": {
			reason: "A reference to a schema the source does not define falls back to a JSON value.",
			args: struct{ schemas map[string]*spec.Schema }{schemas: map[string]*spec.Schema{
				"com.example.v1.Widget": {SchemaProps: rustTestObject(map[string]spec.Schema{
					"other": rustTestRef("com.example.v1.Missing"),
				}).SchemaProps},
			}},
			want: struct{ contains []string }{contains: []string{"pub other: Option<serde_json::Value>,"}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := rustTestEmit(t, "com.example.v1.Widget", tc.args.schemas)
			for _, want := range tc.want.contains {
				if !strings.Contains(got, want) {
					t.Errorf("\n%s\ngenerated Widget is missing %q:\n%s", tc.reason, want, got)
				}
			}
		})
	}
}

// TestRustFileOwner covers which file a schema is emitted into. The Kubernetes
// API names a kind's helper types after it, which is what makes grouping by
// name prefix work; the cases that don't are the interesting ones.
func TestRustFileOwner(t *testing.T) {
	t.Parallel()

	kinds := map[string]bool{
		"Pod":         true,
		"PodList":     true,
		"PodTemplate": true,
		"Service":     true,
		"ServiceList": true,
	}

	cases := map[string]struct {
		reason string
		args   struct{ typ string }
		want   struct{ owner string }
	}{
		"KindOwnsItself": {
			reason: "A kind is emitted into a file of its own.",
			args:   struct{ typ string }{typ: "Pod"},
			want:   struct{ owner string }{owner: "Pod"},
		},
		"ListJoinsItsKind": {
			reason: "A <Kind>List wraps its kind, so it is emitted with it.",
			args:   struct{ typ string }{typ: "PodList"},
			want:   struct{ owner string }{owner: "Pod"},
		},
		"HelperJoinsItsKind": {
			reason: "A type named after a kind is one of its helpers.",
			args:   struct{ typ string }{typ: "PodSpec"},
			want:   struct{ owner string }{owner: "Pod"},
		},
		"LongestKindPrefixWins": {
			reason: "PodTemplate is a kind of its own, so its helpers are its own, not Pod's.",
			args:   struct{ typ string }{typ: "PodTemplateSpec"},
			want:   struct{ owner string }{owner: "PodTemplate"},
		},
		"KindIsNotSwallowedByAShorterKind": {
			reason: "A kind keeps its own file even when a shorter kind's name starts it.",
			args:   struct{ typ string }{typ: "PodTemplate"},
			want:   struct{ owner string }{owner: "PodTemplate"},
		},
		"PrefixMustEndAWord": {
			reason: "A prefix only counts at a word boundary, or Pod would own every type whose name merely starts with those three letters.",
			args:   struct{ typ string }{typ: "Podium"},
			want:   struct{ owner string }{owner: "Podium"},
		},
		"SharedTypeKeepsItsOwnFile": {
			reason: "A type named after no kind keeps a file of its own.",
			args:   struct{ typ string }{typ: "ObjectMeta"},
			want:   struct{ owner string }{owner: "ObjectMeta"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := struct{ owner string }{owner: rustFileOwner(tc.args.typ, kinds)}
			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(got)); diff != "" {
				t.Errorf("\n%s\nrustFileOwner(%q): -want, +got:\n%s", tc.reason, tc.args.typ, diff)
			}
		})
	}
}

// TestRustGroupSchemas covers the grouping end to end, on the shape a
// Kubernetes OpenAPI document has: a kind, its list, its spec and status as
// separate component schemas, plus shared types named after no kind.
func TestRustGroupSchemas(t *testing.T) {
	t.Parallel()

	kind := func(k string) *spec.Schema {
		s := rustTestObject(map[string]spec.Schema{
			"apiVersion": rustTestDefault(rustTestScalar("string", ""), "apps/v1"),
			"kind":       rustTestDefault(rustTestScalar("string", ""), k),
		})
		return &spec.Schema{SchemaProps: s.SchemaProps}
	}
	helper := func() *spec.Schema {
		s := rustTestObject(map[string]spec.Schema{"name": rustTestScalar("string", "")})
		return &spec.Schema{SchemaProps: s.SchemaProps}
	}

	cases := map[string]struct {
		reason string
		args   struct {
			schemas map[string]*spec.Schema
		}
		want struct {
			// files is the schemas each file holds, the one it is named after
			// first.
			files map[string][]string
		}
	}{
		"KindWithListAndHelpers": {
			reason: "A kind's list, spec and status are emitted with it, and a schema named after no kind keeps a file of its own.",
			args: struct{ schemas map[string]*spec.Schema }{schemas: map[string]*spec.Schema{
				"io.k8s.api.apps.v1.Deployment":                   kind("Deployment"),
				"io.k8s.api.apps.v1.DeploymentList":               kind("DeploymentList"),
				"io.k8s.api.apps.v1.DeploymentSpec":               helper(),
				"io.k8s.api.apps.v1.DeploymentStatus":             helper(),
				"io.k8s.api.apps.v1.DeploymentStrategy":           helper(),
				"io.k8s.api.apps.v1.RollingUpdateDeployment":      helper(),
				"io.k8s.apimachinery.pkg.apis.meta.v1.ObjectMeta": helper(),
			}},
			want: struct{ files map[string][]string }{files: map[string][]string{
				"src/io/k8s/api/apps/v1/deployment.rs": {
					"io.k8s.api.apps.v1.Deployment",
					"io.k8s.api.apps.v1.DeploymentList",
					"io.k8s.api.apps.v1.DeploymentSpec",
					"io.k8s.api.apps.v1.DeploymentStatus",
					"io.k8s.api.apps.v1.DeploymentStrategy",
				},
				// Named after no kind, so it keeps its own file even though a
				// reader might expect it under Deployment.
				"src/io/k8s/api/apps/v1/rollingupdatedeployment.rs": {
					"io.k8s.api.apps.v1.RollingUpdateDeployment",
				},
				"src/io/k8s/apimachinery/pkg/apis/meta/v1/objectmeta.rs": {
					"io.k8s.apimachinery.pkg.apis.meta.v1.ObjectMeta",
				},
			}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := make(map[string][]string)
			for _, g := range rustGroupSchemas(tc.args.schemas) {
				got[path.Join(g.dir, g.stem+".rs")] = g.schemas
			}

			if diff := cmp.Diff(tc.want.files, got); diff != "" {
				t.Errorf("\n%s\nrustGroupSchemas(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestGenerateFromCRDRust(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		reason string
		args   struct {
			input afero.Fs
		}
		want struct {
			present []string
			absent  []string
			// contains is the code a generated file must contain.
			contains map[string][]string
		}
	}{
		"CRDsAndXRDs": {
			reason: "Every CRD and XRD is generated into one valid crate, in which a resource carries its own group, version and kind, and a list is emitted with the kind it wraps.",
			args:   struct{ input afero.Fs }{input: afero.NewBasePathFs(afero.FromIOFS{FS: testdataFS}, "testdata")},
			want: struct {
				present  []string
				absent   []string
				contains map[string][]string
			}{
				present: []string{
					"models/Cargo.toml",
					"models/src/lib.rs",
					"models/src/co/acme/platform/v1alpha1/mod.rs",
					"models/src/co/acme/platform/v1alpha1/xaccountscaffold.rs",
					"models/src/co/acme/platform/v1alpha1/accountscaffold.rs",
					"models/src/io/upbound/azure/web/v1beta2/linuxfunctionapp.rs",
					"models/src/io/cilium/v2/ciliumclusterwidenetworkpolicy.rs",
					"models/src/com/example/v1/widget.rs",
					"models/src/io/k8s/apimachinery/pkg/apis/meta/v1/objectmeta.rs",
					"models/src/io/k8s/apimachinery/pkg/apis/meta/v1/time.rs",
				},
				// The list type is emitted with the kind it wraps rather than in
				// a file of its own.
				absent: []string{"models/src/co/acme/platform/v1alpha1/xaccountscaffoldlist.rs"},
				contains: map[string][]string{
					"models/src/co/acme/platform/v1alpha1/xaccountscaffold.rs": {
						// The XR is a resource, so it carries its own group,
						// version and kind.
						"pub struct XAccountScaffold {",
						`pub const API_VERSION: &'static str = "platform.acme.co/v1alpha1";`,
						`pub const KIND: &'static str = "XAccountScaffold";`,
						"impl Default for XAccountScaffold {",
						"api_version: Some(Self::API_VERSION.to_string()),",
						"kind: Some(Self::KIND.to_string()),",
						// Crossplane machinery fields come from the XRD expansion.
						"pub struct XAccountScaffoldSpec {",
						"pub struct XAccountScaffoldSpecParameters {",
						// Shared Kubernetes types are referenced, not inlined.
						"pub metadata: Option<crate::io::k8s::apimachinery::pkg::apis::meta::v1::ObjectMeta>,",
						// The list must not become a second XAccountScaffold.
						"pub struct XAccountScaffoldList {",
					},
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schemaFS, err := rustGenerator{}.GenerateFromCRD(t.Context(), tc.args.input, nil)
			if err != nil {
				t.Fatalf("\n%s\nGenerateFromCRD(...): %v", tc.reason, err)
			}

			for _, p := range tc.want.present {
				exists, err := afero.Exists(schemaFS, p)
				if err != nil {
					t.Fatal(err)
				}
				if !exists {
					t.Errorf("\n%s\nexpected model file %s does not exist", tc.reason, p)
				}
			}
			for _, p := range tc.want.absent {
				exists, err := afero.Exists(schemaFS, p)
				if err != nil {
					t.Fatal(err)
				}
				if exists {
					t.Errorf("\n%s\n%s was emitted instead of being grouped with its kind", tc.reason, p)
				}
			}

			assertValidRustCrate(t, afero.NewBasePathFs(schemaFS, "models"))

			for p, contains := range tc.want.contains {
				contents, err := afero.ReadFile(schemaFS, p)
				if err != nil {
					t.Fatal(err)
				}
				for _, want := range contains {
					if !strings.Contains(string(contents), want) {
						t.Errorf("\n%s\n%s is missing %q", tc.reason, p, want)
					}
				}
			}
		})
	}
}

// TestGenerateFromCRDRustValidationOnlyCombinators covers CRDs that use
// anyOf/oneOf purely for validation, like Cilium's "exactly one of
// endpointSelector and nodeSelector". The fields they constrain must still be
// generated from the schema's regular properties.
func TestGenerateFromCRDRustValidationOnlyCombinators(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		reason string
		args   struct {
			input afero.Fs
		}
		want struct {
			// contains is the code a generated file must contain.
			contains map[string][]string
		}
	}{
		"CiliumExactlyOneOf": {
			reason: "The fields a validation-only anyOf or oneOf constrains are still generated from the schema's regular properties.",
			args:   struct{ input afero.Fs }{input: afero.NewBasePathFs(afero.FromIOFS{FS: testdataFS}, "testdata")},
			want: struct{ contains map[string][]string }{contains: map[string][]string{
				"models/src/io/cilium/v2/ciliumclusterwidenetworkpolicy.rs": {
					"pub endpoint_selector:",
					"pub node_selector:",
					"pub ingress:",
					"pub egress:",
				},
			}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schemaFS, err := rustGenerator{}.GenerateFromCRD(t.Context(), tc.args.input, nil)
			if err != nil {
				t.Fatalf("\n%s\nGenerateFromCRD(...): %v", tc.reason, err)
			}

			for p, contains := range tc.want.contains {
				contents, err := afero.ReadFile(schemaFS, p)
				if err != nil {
					t.Fatal(err)
				}
				for _, want := range contains {
					if !strings.Contains(string(contents), want) {
						t.Errorf("\n%s\n%s is missing %q", tc.reason, p, want)
					}
				}
			}
		})
	}
}

func TestGenerateFromOpenAPIRust(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		reason string
		args   struct {
			input afero.Fs
		}
		want struct {
			present []string
			absent  []string
			// contains is the code a generated file must contain.
			contains map[string][]string
		}
	}{
		"KubernetesDocuments": {
			reason: "Documents served by an API server are generated into one valid crate, in which a kind takes its group, version and kind from its extension and is emitted with its helpers.",
			args:   struct{ input afero.Fs }{input: afero.NewBasePathFs(afero.FromIOFS{FS: testdataJSONFS}, "testdata")},
			want: struct {
				present  []string
				absent   []string
				contains map[string][]string
			}{
				present: []string{
					"models/Cargo.toml",
					"models/src/lib.rs",
					"models/src/io/k8s/api/core/v1/pod.rs",
					"models/src/io/k8s/api/resource/v1/deviceclass.rs",
					"models/src/io/k8s/apimachinery/pkg/apis/meta/v1/objectmeta.rs",
					"models/src/io/k8s/apimachinery/pkg/api/resource/quantity.rs",
					"models/src/io/k8s/apimachinery/pkg/util/intstr/intorstring.rs",
				},
				absent: []string{
					"models/src/io/k8s/api/core/v1/podspec.rs",
					"models/src/io/k8s/api/core/v1/podlist.rs",
				},
				contains: map[string][]string{
					"models/src/io/k8s/api/core/v1/pod.rs": {
						"pub struct Pod {",
						// Schemas served by an API server declare their GVK in
						// an extension, which goAddDefaults turns into the
						// defaults we key on.
						`pub const API_VERSION: &'static str = "v1";`,
						`pub const KIND: &'static str = "Pod";`,
						"pub metadata: Option<crate::io::k8s::apimachinery::pkg::apis::meta::v1::ObjectMeta>,",
						"pub spec: Option<crate::io::k8s::api::core::v1::PodSpec>,",
						// Kubernetes documents declare a kind's helpers as
						// component schemas of their own; they are emitted with
						// the kind rather than one file each.
						"pub struct PodSpec {",
						"pub struct PodStatus {",
						"pub struct PodList {",
					},
					// A kind of its own keeps its own file, and takes its own
					// helpers with it.
					"models/src/io/k8s/api/core/v1/podtemplate.rs": {
						"pub struct PodTemplate {",
						"pub struct PodTemplateSpec {",
					},
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schemaFS, err := rustGenerator{}.GenerateFromOpenAPI(t.Context(), tc.args.input, nil)
			if err != nil {
				t.Fatalf("\n%s\nGenerateFromOpenAPI(...): %v", tc.reason, err)
			}

			for _, p := range tc.want.present {
				exists, err := afero.Exists(schemaFS, p)
				if err != nil {
					t.Fatal(err)
				}
				if !exists {
					t.Errorf("\n%s\nexpected model file %s does not exist", tc.reason, p)
				}
			}
			for _, p := range tc.want.absent {
				exists, err := afero.Exists(schemaFS, p)
				if err != nil {
					t.Fatal(err)
				}
				if exists {
					t.Errorf("\n%s\n%s was emitted instead of being grouped with its kind", tc.reason, p)
				}
			}

			assertValidRustCrate(t, afero.NewBasePathFs(schemaFS, "models"))

			for p, contains := range tc.want.contains {
				contents, err := afero.ReadFile(schemaFS, p)
				if err != nil {
					t.Fatal(err)
				}
				for _, want := range contains {
					if !strings.Contains(string(contents), want) {
						t.Errorf("\n%s\n%s is missing %q", tc.reason, p, want)
					}
				}
			}
		})
	}
}

// TestGenerateRustFlowsAgree covers the two generation flows sharing one crate.
// The schema manager copies each source's output over the last and deletes
// nothing, a CRD source and a Kubernetes source both emit the shared Kubernetes
// types, and which of them is copied last is not under our control. So a file
// both flows write has to come out the same from either, and the merged crate
// has to be valid in both orders.
func TestGenerateRustFlowsAgree(t *testing.T) {
	t.Parallel()

	gen := &rustGenerator{}

	fromCRD, err := gen.GenerateFromCRD(t.Context(), afero.NewBasePathFs(afero.FromIOFS{FS: testdataFS}, "testdata"), nil)
	if err != nil {
		t.Fatal(err)
	}
	fromOpenAPI, err := gen.GenerateFromOpenAPI(t.Context(), afero.NewBasePathFs(afero.FromIOFS{FS: testdataJSONFS}, "testdata"), nil)
	if err != nil {
		t.Fatal(err)
	}

	crd := rustReadTree(t, afero.NewBasePathFs(fromCRD, rustModelsDir))
	openAPI := rustReadTree(t, afero.NewBasePathFs(fromOpenAPI, rustModelsDir))

	shared := 0
	for p, want := range crd {
		got, ok := openAPI[p]
		// The module declarations and the manifest describe a whole crate, and
		// are rebuilt from the merged one.
		if !ok || path.Base(p) == "mod.rs" || path.Base(p) == "lib.rs" || p == "Cargo.toml" {
			continue
		}
		shared++
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("%s differs between the CRD flow and the OpenAPI flow (-crd +openapi):\n%s", p, diff)
		}
	}
	if shared < 2 {
		t.Fatalf("the flows share %d files; the testdata no longer covers the shared Kubernetes types", shared)
	}

	cases := map[string]struct {
		reason string
		args   struct {
			sources []map[string]string
		}
	}{
		"CRDThenOpenAPI": {
			reason: "A Kubernetes dependency added to a project that already has CRD models leaves a valid crate.",
			args: struct{ sources []map[string]string }{
				sources: []map[string]string{crd, openAPI},
			},
		},
		"OpenAPIThenCRD": {
			reason: "CRD models generated into a project that already has Kubernetes models leave a valid crate.",
			args: struct{ sources []map[string]string }{
				sources: []map[string]string{openAPI, crd},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			crateFS := afero.NewMemMapFs()
			for _, source := range tc.args.sources {
				for p, contents := range source {
					if err := afero.WriteFile(crateFS, p, []byte(contents), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				if err := BuildRustModuleTree(crateFS); err != nil {
					t.Fatalf("\n%s\nBuildRustModuleTree(...): %v", tc.reason, err)
				}
			}

			assertValidRustCrate(t, crateFS)
		})
	}
}

// TestRustSharedTypesAreNeverKinds pins the rule the flows agree by: a shared
// Kubernetes type is a plain struct in a file of its own even when its schema
// says it is a resource, which the OpenAPI flow's does for Status and the CRD
// flow's does not.
func TestRustSharedTypesAreNeverKinds(t *testing.T) {
	t.Parallel()

	object := func(props map[string]spec.Schema) *spec.Schema {
		return &spec.Schema{SchemaProps: rustTestObject(props).SchemaProps}
	}

	cases := map[string]struct {
		reason string
		args   struct {
			schemas map[string]*spec.Schema
		}
		want struct {
			present []string
			// notContains is the code a generated file must not contain.
			notContains map[string][]string
		}
	}{
		"StatusDeclaredAsAResource": {
			reason: "A shared type must not be treated as a resource, nor have the types named after it grouped into its file.",
			args: struct{ schemas map[string]*spec.Schema }{schemas: map[string]*spec.Schema{
				"io.k8s.apimachinery.pkg.apis.meta.v1.Status": object(map[string]spec.Schema{
					"apiVersion": rustTestDefault(rustTestEnum("v1"), "v1"),
					"kind":       rustTestDefault(rustTestEnum("Status"), "Status"),
					"details":    rustTestRef("io.k8s.apimachinery.pkg.apis.meta.v1.StatusDetails"),
				}),
				"io.k8s.apimachinery.pkg.apis.meta.v1.StatusDetails": object(map[string]spec.Schema{
					"name": rustTestScalar("string", ""),
				}),
			}},
			want: struct {
				present     []string
				notContains map[string][]string
			}{
				present: []string{"src/io/k8s/apimachinery/pkg/apis/meta/v1/statusdetails.rs"},
				notContains: map[string][]string{
					"src/io/k8s/apimachinery/pkg/apis/meta/v1/status.rs": {
						"pub struct StatusDetails",
						"impl Default for Status",
						"API_VERSION",
						"Allowed values",
					},
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schemaFS, err := rustGenerateModels(tc.args.schemas)
			if err != nil {
				t.Fatalf("\n%s\nrustGenerateModels(...): %v", tc.reason, err)
			}
			crateFS := afero.NewBasePathFs(schemaFS, rustModelsDir)
			assertValidRustCrate(t, crateFS)

			for p, notContains := range tc.want.notContains {
				contents, err := afero.ReadFile(crateFS, p)
				if err != nil {
					t.Fatal(err)
				}
				for _, unwanted := range notContains {
					if strings.Contains(string(contents), unwanted) {
						t.Errorf("\n%s\n%s contains %q:\n%s", tc.reason, p, unwanted, contents)
					}
				}
			}
			for _, p := range tc.want.present {
				if ok, _ := afero.Exists(crateFS, p); !ok {
					t.Errorf("\n%s\n%s does not exist", tc.reason, p)
				}
			}
		})
	}
}

// TestRustSkipsSchemasWithoutATypeName covers a CRD that leaves
// spec.names.listKind to the API server's defaulting: its list schema is named
// after the group and version alone. The Go generator drops such a schema, and
// so do we, rather than emit a type, a file and a module called "_".
func TestRustSkipsSchemasWithoutATypeName(t *testing.T) {
	t.Parallel()

	task := &spec.Schema{SchemaProps: rustTestObject(map[string]spec.Schema{
		"image": rustTestScalar("string", ""),
	}).SchemaProps}
	list := &spec.Schema{SchemaProps: rustTestObject(map[string]spec.Schema{
		"items": rustTestArray(rustTestRef("com.example.v1.Task")),
	}).SchemaProps}

	cases := map[string]struct {
		reason string
		args   struct {
			schemas map[string]*spec.Schema
		}
		want struct {
			files []string
		}
	}{
		"ListWithoutAName": {
			reason: "The nameless list schema is dropped and the kind is generated as usual.",
			args: struct{ schemas map[string]*spec.Schema }{
				schemas: map[string]*spec.Schema{"com.example.v1.Task": task, "com.example.v1.": list},
			},
			want: struct{ files []string }{
				files: []string{"Cargo.toml", "src/com/example/mod.rs", "src/com/example/v1/mod.rs", "src/com/example/v1/task.rs", "src/com/mod.rs", "src/lib.rs"},
			},
		},
		"NothingLeft": {
			reason: "A source with nothing but nameless schemas generates no crate at all.",
			args: struct{ schemas map[string]*spec.Schema }{
				schemas: map[string]*spec.Schema{"com.example.v1.": list},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schemaFS, err := rustGenerateModels(maps.Clone(tc.args.schemas))
			if err != nil {
				t.Fatalf("\n%s\nrustGenerateModels(...): %v", tc.reason, err)
			}

			var got []string
			if schemaFS != nil {
				crateFS := afero.NewBasePathFs(schemaFS, rustModelsDir)
				assertValidRustCrate(t, crateFS)
				got = slices.Sorted(maps.Keys(rustReadTree(t, crateFS)))
			}
			if diff := cmp.Diff(tc.want.files, got); diff != "" {
				t.Errorf("\n%s\nrustGenerateModels(...): -want files, +got files:\n%s", tc.reason, diff)
			}
		})
	}
}

// TestRustFieldIdentifiers covers properties that map to the same Rust
// identifier, which real CRDs have (Istio's mirrorPercent and mirror_percent,
// prometheus-operator's proxyURL and proxyUrl). Each keeps its own name on the
// wire; the second to ask for an identifier gets a numeric suffix.
func TestRustFieldIdentifiers(t *testing.T) {
	t.Parallel()

	fieldRE := regexp.MustCompile(`#\[serde\(rename = "([^"]+)"[^\n]*\n    pub ((?:r#)?\w+):`)

	cases := map[string]struct {
		reason string
		args   struct {
			properties []string
		}
		want struct {
			fields map[string]string
		}
	}{
		"SnakeAndCamel": {
			reason: "A camelCase and a snake_case spelling of one name.",
			args:   struct{ properties []string }{properties: []string{"mirrorPercent", "mirror_percent"}},
			want: struct{ fields map[string]string }{fields: map[string]string{
				"mirrorPercent":  "mirror_percent",
				"mirror_percent": "mirror_percent_2",
			}},
		},
		"AcronymCasing": {
			reason: "Two casings of an acronym.",
			args:   struct{ properties []string }{properties: []string{"proxyURL", "proxyUrl"}},
			want: struct{ fields map[string]string }{fields: map[string]string{
				"proxyURL": "proxy_url",
				"proxyUrl": "proxy_url_2",
			}},
		},
		"KeywordsWithoutARawForm": {
			reason: "self and Self both take a trailing underscore.",
			args:   struct{ properties []string }{properties: []string{"Self", "self"}},
			want: struct{ fields map[string]string }{fields: map[string]string{
				"Self": "self_",
				"self": "self__2",
			}},
		},
		"NoLetterOrDigit": {
			reason: "A property name with nothing to make an identifier of still gets a valid one.",
			args:   struct{ properties []string }{properties: []string{"-"}},
			want:   struct{ fields map[string]string }{fields: map[string]string{"-": "unnamed"}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			props := make(map[string]spec.Schema, len(tc.args.properties))
			for _, p := range tc.args.properties {
				props[p] = rustTestScalar("string", "")
			}
			schemas := map[string]*spec.Schema{
				"com.example.v1.Widget": {SchemaProps: rustTestObject(props).SchemaProps},
			}

			got := make(map[string]string)
			for _, m := range fieldRE.FindAllStringSubmatch(rustTestEmit(t, "com.example.v1.Widget", schemas), -1) {
				got[m[1]] = m[2]
			}
			if diff := cmp.Diff(tc.want.fields, got); diff != "" {
				t.Errorf("\n%s\n-want identifiers, +got identifiers, by JSON name:\n%s", tc.reason, diff)
			}
		})
	}
}

// TestRustAdditionalProperties covers objects that have named properties and
// allow others. The others are kept in a map flattened into the struct, so a
// resource read into a model and written back loses nothing.
func TestRustAdditionalProperties(t *testing.T) {
	t.Parallel()

	withExtension := func(s spec.Schema) spec.Schema {
		s.AddExtension(rustPreserveUnknownFields, true)
		return s
	}
	withAdditional := func(s spec.Schema, ap *spec.SchemaOrBool) spec.Schema {
		s.AdditionalProperties = ap
		return s
	}
	known := map[string]spec.Schema{"known": rustTestScalar("string", "")}
	values := rustTestScalar("string", "")

	cases := map[string]struct {
		reason string
		args   struct {
			schema spec.Schema
		}
		want struct {
			contains    []string
			notContains []string
		}
	}{
		"TypedValues": {
			reason: "additionalProperties with a schema gives the map that schema's type, as the Go models do.",
			args:   struct{ schema spec.Schema }{schema: withAdditional(rustTestObject(known), &spec.SchemaOrBool{Allows: true, Schema: &values})},
			want: struct{ contains, notContains []string }{contains: []string{
				"    /// Properties the schema allows without naming them.\n" +
					"    #[serde(flatten, default, skip_serializing_if = \"std::collections::BTreeMap::is_empty\")]\n" +
					"    pub additional_properties: std::collections::BTreeMap<String, String>,\n",
			}},
		},
		"AnyValues": {
			reason: "additionalProperties: true allows values of any type.",
			args:   struct{ schema spec.Schema }{schema: withAdditional(rustTestObject(known), &spec.SchemaOrBool{Allows: true})},
			want: struct{ contains, notContains []string }{contains: []string{
				"    pub additional_properties: std::collections::BTreeMap<String, serde_json::Value>,\n",
			}},
		},
		"PreserveUnknownFields": {
			reason: "The API server keeps the unknown fields of such an object, so the model has to as well.",
			args:   struct{ schema spec.Schema }{schema: withExtension(rustTestObject(known))},
			want: struct{ contains, notContains []string }{contains: []string{
				"    pub additional_properties: std::collections::BTreeMap<String, serde_json::Value>,\n",
			}},
		},
		"NameTaken": {
			reason: "A property with the map's name keeps it, and the map gives way.",
			args: struct{ schema spec.Schema }{schema: withExtension(rustTestObject(map[string]spec.Schema{
				"additionalProperties": rustTestScalar("string", ""),
			}))},
			want: struct{ contains, notContains []string }{contains: []string{
				"    pub additional_properties: Option<String>,\n",
				"    pub additional_properties_2: std::collections::BTreeMap<String, serde_json::Value>,\n",
			}},
		},
		"Resource": {
			reason: "A resource's hand-written Default starts the map out empty.",
			args: struct{ schema spec.Schema }{schema: withExtension(rustTestObject(map[string]spec.Schema{
				"apiVersion": rustTestDefault(rustTestScalar("string", ""), "example.com/v1"),
				"kind":       rustTestDefault(rustTestScalar("string", ""), "Widget"),
			}))},
			want: struct{ contains, notContains []string }{contains: []string{
				"            additional_properties: std::collections::BTreeMap::new(),\n",
			}},
		},
		"ClosedObject": {
			reason: "An object that allows nothing beyond its properties gets no map.",
			args:   struct{ schema spec.Schema }{schema: rustTestObject(known)},
			want:   struct{ contains, notContains []string }{notContains: []string{"additional_properties", "flatten"}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schemas := map[string]*spec.Schema{"com.example.v1.Widget": {
				SchemaProps:      tc.args.schema.SchemaProps,
				VendorExtensible: tc.args.schema.VendorExtensible,
			}}
			got := rustTestEmit(t, "com.example.v1.Widget", schemas)

			for _, want := range tc.want.contains {
				if !strings.Contains(got, want) {
					t.Errorf("\n%s\nmissing:\n%s\ngot:\n%s", tc.reason, want, got)
				}
			}
			for _, unwanted := range tc.want.notContains {
				if strings.Contains(got, unwanted) {
					t.Errorf("\n%s\nshould not contain %q, got:\n%s", tc.reason, unwanted, got)
				}
			}
		})
	}
}

// TestRustWriteDoc covers the line endings a description can carry. A bare
// carriage return is not allowed in a Rust doc comment, so it ends a line, the
// same way it does in the Go models.
func TestRustWriteDoc(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		reason string
		args   struct {
			doc string
		}
		want struct {
			out string
		}
	}{
		"LineFeed": {
			reason: "One comment line per line of the description.",
			args:   struct{ doc string }{doc: "first\nsecond\n"},
			want:   struct{ out string }{out: "/// first\n/// second\n"},
		},
		"CarriageReturnLineFeed": {
			reason: "A Windows line ending is one line ending.",
			args:   struct{ doc string }{doc: "first\r\nsecond"},
			want:   struct{ out string }{out: "/// first\n/// second\n"},
		},
		"BareCarriageReturn": {
			reason: "A carriage return on its own ends a line too.",
			args:   struct{ doc string }{doc: "first\rsecond"},
			want:   struct{ out string }{out: "/// first\n/// second\n"},
		},
		"Empty": {
			reason: "No description, no comment.",
			args:   struct{ doc string }{doc: " \n"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var sb strings.Builder
			rustWriteDoc(&sb, "", tc.args.doc)
			if diff := cmp.Diff(tc.want.out, sb.String()); diff != "" {
				t.Errorf("\n%s\nrustWriteDoc(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

// TestRustFeatures covers the features that let a function compile only the
// modules of models it imports: which modules get one, and what each has to
// enable for the crate to compile with it alone.
func TestRustFeatures(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		reason string
		args   struct {
			files map[string]string
		}
		want struct {
			features map[string][]string
		}
	}{
		"ReferencedModule": {
			reason: "A module enables the modules its models refer to, and not itself.",
			args: struct{ files map[string]string }{files: map[string]string{
				"src/io/upbound/s3/v1beta1/bucket.rs": "pub struct Bucket {\n    pub metadata: Option<crate::io::k8s::meta::v1::ObjectMeta>,\n" +
					"    pub items: Option<Vec<crate::io::upbound::s3::v1beta1::Bucket>>,\n}\n",
				"src/io/k8s/meta/v1/objectmeta.rs": "pub struct ObjectMeta {}\n",
			}},
			want: struct{ features map[string][]string }{features: map[string][]string{
				"io-upbound-s3-v1beta1": {"io-k8s-meta-v1"},
				"io-k8s-meta-v1":        {},
			}},
		},
		"ReferenceInADescription": {
			reason: "A path in a doc comment is prose from a schema, not a reference.",
			args: struct{ files map[string]string }{files: map[string]string{
				"src/com/example/v1/widget.rs":     "/// See crate::io::k8s::meta::v1::ObjectMeta.\npub struct Widget {}\n",
				"src/io/k8s/meta/v1/objectmeta.rs": "pub struct ObjectMeta {}\n",
			}},
			want: struct{ features map[string][]string }{features: map[string][]string{
				"com-example-v1": {},
				"io-k8s-meta-v1": {},
			}},
		},
		"ModelsInsideAModuleOfModels": {
			reason: "A module is declared by the one above it, so it needs that one compiled.",
			args: struct{ files map[string]string }{files: map[string]string{
				"src/com/example/widget.rs":    "pub struct Widget {}\n",
				"src/com/example/v1/gadget.rs": "pub struct Gadget {}\n",
			}},
			want: struct{ features map[string][]string }{features: map[string][]string{
				"com-example":    {},
				"com-example-v1": {"com-example"},
			}},
		},
		"ModelsInTheCrateRoot": {
			reason: "Models with no module around them cannot be gated, and referring to them needs no feature.",
			args: struct{ files map[string]string }{files: map[string]string{
				"src/widget.rs":                "pub struct Widget {}\n",
				"src/com/example/v1/gadget.rs": "pub struct Gadget {\n    pub widget: Option<crate::Widget>,\n}\n",
			}},
			want: struct{ features map[string][]string }{features: map[string][]string{
				"com-example-v1": {},
			}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			crateFS := afero.NewMemMapFs()
			for p, contents := range tc.args.files {
				if err := afero.WriteFile(crateFS, p, []byte(rustGeneratedHeader+"\n\n"+contents), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := BuildRustModuleTree(crateFS); err != nil {
				t.Fatalf("\n%s\nBuildRustModuleTree(...): %v", tc.reason, err)
			}

			features, err := rustCollectFeatures(crateFS)
			if err != nil {
				t.Fatal(err)
			}
			got := make(map[string][]string, len(features))
			for _, f := range features {
				got[f.name] = f.deps
			}
			if diff := cmp.Diff(tc.want.features, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("\n%s\n-want features, +got features:\n%s", tc.reason, diff)
			}

			assertRustFeatures(t, crateFS)
		})
	}
}

// TestGenerateRustIsDeterministic guards the property that makes committing
// generated schemas practical: the same input produces byte-identical output.
func TestGenerateRustIsDeterministic(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		reason string
		args   struct {
			input afero.Fs
		}
	}{
		"CRDsAndXRDs": {
			reason: "Two runs over the same input produce byte-identical crates.",
			args:   struct{ input afero.Fs }{input: afero.NewBasePathFs(afero.FromIOFS{FS: testdataFS}, "testdata")},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			first, err := rustGenerator{}.GenerateFromCRD(t.Context(), tc.args.input, nil)
			if err != nil {
				t.Fatalf("\n%s\nGenerateFromCRD(...): %v", tc.reason, err)
			}
			second, err := rustGenerator{}.GenerateFromCRD(t.Context(), tc.args.input, nil)
			if err != nil {
				t.Fatalf("\n%s\nGenerateFromCRD(...): %v", tc.reason, err)
			}

			if diff := cmp.Diff(rustReadTree(t, first), rustReadTree(t, second)); diff != "" {
				t.Errorf("\n%s\n-first run, +second run:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestGenerateRustNoInput(t *testing.T) {
	t.Parallel()

	type generate func(context.Context, afero.Fs, runner.SchemaRunner) (afero.Fs, error)

	cases := map[string]struct {
		reason string
		args   struct {
			generate generate
		}
	}{
		"NoCRDs": {
			reason: "An input with no CRDs generates nothing, rather than an empty crate.",
			args:   struct{ generate generate }{generate: rustGenerator{}.GenerateFromCRD},
		},
		"NoOpenAPIDocuments": {
			reason: "An input with no OpenAPI documents generates nothing, rather than an empty crate.",
			args:   struct{ generate generate }{generate: rustGenerator{}.GenerateFromOpenAPI},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := tc.args.generate(t.Context(), afero.NewMemMapFs(), nil)
			if err != nil {
				t.Fatalf("\n%s\ngenerate(...): %v", tc.reason, err)
			}
			if got != nil {
				t.Errorf("\n%s\ngenerate(...) returned a filesystem", tc.reason)
			}
		})
	}
}

// TestBuildRustModuleTree covers what the schema manager relies on: the module
// declarations describe whatever is in the crate, so a second source adding
// files to a module the first source created leaves a valid crate.
func TestBuildRustModuleTree(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		reason string
		args   struct {
			// sources is the files each source adds to the crate, in the
			// order the sources are generated.
			sources [][]string
		}
		want struct {
			files map[string]string
		}
	}{
		"SecondSourceExtendsTheCrate": {
			reason: "The declarations are rebuilt from whatever the crate holds, so a source that only saw its own schemas still leaves a valid crate.",
			args: struct{ sources [][]string }{sources: [][]string{
				// The first source generates one kind, plus a shared
				// Kubernetes type.
				{
					"src/com/example/v1/widget.rs",
					"src/io/k8s/apimachinery/pkg/apis/meta/v1/objectmeta.rs",
				},
				// A second source adds a kind to a module that already exists,
				// and a module of its own.
				{
					"src/com/example/v1/gadget.rs",
					"src/co/acme/platform/v1alpha1/xaccountscaffold.rs",
				},
			}},
			want: struct{ files map[string]string }{files: map[string]string{
				"src/lib.rs": rustGeneratedHeader + "\n" + rustCrateAttributes + `
pub mod co;
pub mod com;
pub mod io;
`,
				"src/com/example/v1/mod.rs": rustGeneratedHeader + `

mod gadget;
pub use gadget::*;
mod widget;
pub use widget::*;
`,
				"src/com/example/mod.rs": rustGeneratedHeader + `

#[cfg(feature = "com-example-v1")]
pub mod v1;
`,
				// The manifest is rebuilt with the tree: a feature per module
				// of models, all of them on by default.
				"Cargo.toml": rustCargoToml + rustFeaturesHeader + `default = ["all"]
all = [
    "co-acme-platform-v1alpha1",
    "com-example-v1",
    "io-k8s-apimachinery-pkg-apis-meta-v1",
]
co-acme-platform-v1alpha1 = []
com-example-v1 = []
io-k8s-apimachinery-pkg-apis-meta-v1 = []
`,
				"src/co/acme/platform/v1alpha1/mod.rs": rustGeneratedHeader + `

mod xaccountscaffold;
pub use xaccountscaffold::*;
`,
			}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			crateFS := afero.NewMemMapFs()
			for _, source := range tc.args.sources {
				for _, p := range source {
					if err := afero.WriteFile(crateFS, p, []byte(rustGeneratedHeader+"\n"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				if err := BuildRustModuleTree(crateFS); err != nil {
					t.Fatalf("\n%s\nBuildRustModuleTree(...): %v", tc.reason, err)
				}
			}

			for p, want := range tc.want.files {
				got, err := afero.ReadFile(crateFS, p)
				if err != nil {
					t.Fatal(err)
				}
				if diff := cmp.Diff(want, string(got)); diff != "" {
					t.Errorf("\n%s\n%s: -want, +got:\n%s", tc.reason, p, diff)
				}
			}

			assertValidRustCrate(t, crateFS)
		})
	}
}

var (
	rustTypeDeclRE = regexp.MustCompile(`(?m)^pub (?:struct|enum|type) (\w+)`)
	rustFieldRE    = regexp.MustCompile(`(?m)^    pub ((?:r#)?\w+): `)
	rustModDeclRE  = regexp.MustCompile(`(?m)^(?:pub )?mod (\w+);`)
)

// assertValidRustCrate checks the properties a Rust compiler would catch but a
// unit test in Go cannot: that no module declares the same type twice, whatever
// file it is in, that no struct declares the same field twice, that nothing is
// named with a bare underscore, that every struct field carries the serde
// attributes the round trip depends on, and that every module declares exactly
// the children of its directory.
func assertValidRustCrate(t *testing.T, crateFS afero.Fs) {
	t.Helper()

	// A module re-exports every file of its directory, so a type name has to be
	// unique in the directory, not just in its file.
	declaredIn := make(map[string]string)

	err := afero.Walk(crateFS, "src", func(p string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if strings.TrimSuffix(info.Name(), ".rs") == "_" {
			t.Errorf("%s is named with a bare underscore, which is not a Rust identifier", p)
		}
		if info.IsDir() {
			assertRustModuleDeclarations(t, crateFS, p)
			return nil
		}
		if !strings.HasSuffix(p, ".rs") || info.Name() == "mod.rs" || info.Name() == "lib.rs" {
			return nil
		}

		contents, err := afero.ReadFile(crateFS, p)
		if err != nil {
			return err
		}
		code := string(contents)

		if !strings.HasPrefix(code, rustGeneratedHeader) {
			t.Errorf("%s does not start with the generated code header", p)
		}

		for _, m := range rustTypeDeclRE.FindAllStringSubmatch(code, -1) {
			if m[1] == "_" {
				t.Errorf("%s declares a type named with a bare underscore", p)
			}
			key := path.Join(path.Dir(p), m[1])
			if other, ok := declaredIn[key]; ok {
				t.Errorf("%s declares type %s, which %s declares too", p, m[1], other)
			}
			declaredIn[key] = p
		}

		// Every field is optional and renamed, and skips serialization when
		// unset, so a function's desired state carries only what it set. The
		// one exception is the map of additional properties, which is flattened
		// into its struct and skipped when empty.
		fields := make(map[string]bool)
		lines := strings.Split(code, "\n")
		for i, line := range lines {
			if strings.HasPrefix(line, "pub struct ") {
				fields = make(map[string]bool)
				continue
			}

			m := rustFieldRE.FindStringSubmatch(line + "\n")
			if m == nil {
				continue
			}
			if m[1] == "_" {
				t.Errorf("%s:%d declares a field named with a bare underscore", p, i+1)
			}
			if fields[m[1]] {
				t.Errorf("%s:%d declares field %s twice in one struct", p, i+1, m[1])
			}
			fields[m[1]] = true

			if i > 0 && strings.HasPrefix(lines[i-1], `    #[serde(flatten, default, skip_serializing_if = `) {
				continue
			}
			if !strings.HasSuffix(line, ",") || !strings.Contains(line, "Option<") {
				t.Errorf("%s:%d field %s is not optional: %s", p, i+1, m[1], line)
			}
			if i == 0 || !strings.HasPrefix(lines[i-1], `    #[serde(rename = `) {
				t.Errorf("%s:%d field %s is not preceded by a serde rename attribute", p, i+1, m[1])
			}
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	assertRustFeatures(t, crateFS)
}

var (
	rustFeatureTableRE = regexp.MustCompile(`(?m)^([\w-]+) = \[([^\]]*)\]`)
	rustFeatureNameRE  = regexp.MustCompile(`"([^"]+)"`)
	rustGatedModRE     = regexp.MustCompile(`(?m)^(?:#\[cfg\(feature = "([^"]+)"\)\]\n)?pub mod (\w+);`)
)

// assertRustFeatures checks the features the way cargo and rustc would when a
// function picks some of them: every module that holds models is gated by a
// feature of its own, every feature named anywhere is declared, all of them are
// on by default, and a feature enables the feature of every module its models
// refer to, or the crate would not compile with that feature alone.
func assertRustFeatures(t *testing.T, crateFS afero.Fs) {
	t.Helper()

	manifest, err := afero.ReadFile(crateFS, "Cargo.toml")
	if err != nil {
		t.Fatal(err)
	}
	_, table, _ := strings.Cut(string(manifest), "[features]\n")

	declared := make(map[string][]string)
	for _, m := range rustFeatureTableRE.FindAllStringSubmatch(table, -1) {
		names := rustFeatureNameRE.FindAllStringSubmatch(m[2], -1)
		deps := make([]string, len(names))
		for i, d := range names {
			deps[i] = d[1]
		}
		declared[m[1]] = deps
	}
	for _, deps := range declared {
		for _, d := range deps {
			if _, ok := declared[d]; !ok {
				t.Errorf("Cargo.toml: feature %q is enabled by another but not declared", d)
			}
		}
	}

	// The feature gating each directory, from the module declarations.
	gates := make(map[string]string)
	holdsModels := make(map[string]bool)
	err = afero.Walk(crateFS, "src", func(p string, info fs.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if rustIsTypeFile(info.Name()) {
			holdsModels[path.Dir(p)] = true
			return nil
		}
		contents, err := afero.ReadFile(crateFS, p)
		if err != nil {
			return err
		}
		for _, m := range rustGatedModRE.FindAllStringSubmatch(string(contents), -1) {
			if m[1] != "" {
				gates[path.Join(path.Dir(p), m[2])] = m[1]
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var all []string
	for dir := range holdsModels {
		if dir == "src" {
			continue
		}
		feature, ok := gates[dir]
		if !ok {
			t.Errorf("%s holds models but no feature gates it", dir)
			continue
		}
		if _, ok := declared[feature]; !ok {
			t.Errorf("%s is gated by feature %q, which Cargo.toml does not declare", dir, feature)
		}
		all = append(all, feature)
	}
	for dir := range gates {
		if !holdsModels[dir] {
			t.Errorf("%s is gated but holds no models", dir)
		}
	}
	slices.Sort(all)

	if len(all) == 0 {
		return
	}
	if diff := cmp.Diff([]string{"all"}, declared["default"]); diff != "" {
		t.Errorf("Cargo.toml: default features (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(all, declared["all"]); diff != "" {
		t.Errorf("Cargo.toml: the all feature does not enable exactly the gated modules (-want +got):\n%s", diff)
	}

	// What a feature enables, itself included, following the table the way
	// cargo does.
	var enabled func(feature string, seen map[string]bool)
	enabled = func(feature string, seen map[string]bool) {
		if seen[feature] {
			return
		}
		seen[feature] = true
		for _, d := range declared[feature] {
			enabled(d, seen)
		}
	}

	for dir := range holdsModels {
		feature, ok := gates[dir]
		if !ok {
			continue
		}
		on := make(map[string]bool)
		enabled(feature, on)

		// Every gated module on the path to this one, and every module its
		// models refer to, has to be compiled along with it.
		needed := make(map[string]string)
		for parent := path.Dir(dir); parent != "src" && parent != "."; parent = path.Dir(parent) {
			if f, ok := gates[parent]; ok {
				needed[f] = "it is declared inside " + parent
			}
		}
		entries, err := afero.ReadDir(crateFS, dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.IsDir() || !rustIsTypeFile(e.Name()) {
				continue
			}
			contents, err := afero.ReadFile(crateFS, path.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			for _, target := range rustReferencedModules(string(contents)) {
				if f, ok := gates[target]; ok {
					needed[f] = e.Name() + " refers to " + target
				}
			}
		}

		for f, why := range needed {
			if !on[f] {
				t.Errorf("Cargo.toml: feature %q does not enable %q, but %s", feature, f, why)
			}
		}
	}
}

// assertRustModuleDeclarations checks that the mod.rs (or lib.rs) of a
// directory declares exactly its subdirectories and files.
func assertRustModuleDeclarations(t *testing.T, crateFS afero.Fs, dir string) {
	t.Helper()

	name := "mod.rs"
	if dir == "src" {
		name = "lib.rs"
	}

	contents, err := afero.ReadFile(crateFS, path.Join(dir, name))
	if err != nil {
		t.Errorf("directory %s has no %s: %v", dir, name, err)
		return
	}

	var declared []string
	for _, m := range rustModDeclRE.FindAllStringSubmatch(string(contents), -1) {
		if m[1] == "_" {
			t.Errorf("%s declares a module named with a bare underscore", path.Join(dir, name))
		}
		declared = append(declared, m[1])
	}
	slices.Sort(declared)

	entries, err := afero.ReadDir(crateFS, dir)
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, e := range entries {
		if e.IsDir() {
			want = append(want, e.Name())
			continue
		}
		if stem, ok := strings.CutSuffix(e.Name(), ".rs"); ok && e.Name() != name {
			want = append(want, stem)
		}
	}
	slices.Sort(want)

	if diff := cmp.Diff(want, declared); diff != "" {
		t.Errorf("%s does not declare its directory's children (-want +got):\n%s", path.Join(dir, name), diff)
	}
}

// rustReadTree reads every file of a generated filesystem into a map, for
// comparing two runs.
func rustReadTree(t *testing.T, from afero.Fs) map[string]string {
	t.Helper()

	tree := make(map[string]string)
	err := afero.Walk(from, "", func(p string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		contents, err := afero.ReadFile(from, p)
		if err != nil {
			return err
		}
		tree[p] = string(contents)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	return tree
}
