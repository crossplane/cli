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
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestRustCasing covers the casing algorithm and the identifier sanitization
// built on it. The Kubernetes API is full of names that trip naive casing
// (acronyms, plural acronyms, digits, Rust keywords), so every case here is a
// name a real CRD or built-in type uses.
func TestRustCasing(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		args struct{ in string }
		want struct{ snake, camel, field, typ string }
	}{
		"CamelCase": {
			args: struct{ in string }{in: "apiVersion"},
			want: struct{ snake, camel, field, typ string }{
				snake: "api_version", camel: "ApiVersion", field: "api_version", typ: "ApiVersion",
			},
		},
		"CrossplaneField": {
			args: struct{ in string }{in: "writeConnectionSecretToRef"},
			want: struct{ snake, camel, field, typ string }{
				snake: "write_connection_secret_to_ref", camel: "WriteConnectionSecretToRef",
				field: "write_connection_secret_to_ref", typ: "WriteConnectionSecretToRef",
			},
		},
		"PluralAcronymStaysOneWord": {
			args: struct{ in string }{in: "podCIDRs"},
			want: struct{ snake, camel, field, typ string }{
				snake: "pod_cidrs", camel: "PodCidrs", field: "pod_cidrs", typ: "PodCIDRs",
			},
		},
		"AcronymBeforeWord": {
			args: struct{ in string }{in: "HTTPServer"},
			want: struct{ snake, camel, field, typ string }{
				snake: "http_server", camel: "HttpServer", field: "http_server", typ: "HTTPServer",
			},
		},
		"ShortAcronymBeforeWord": {
			args: struct{ in string }{in: "VMSize"},
			want: struct{ snake, camel, field, typ string }{
				snake: "vm_size", camel: "VmSize", field: "vm_size", typ: "VMSize",
			},
		},
		"AcronymOnly": {
			args: struct{ in string }{in: "TTL"},
			want: struct{ snake, camel, field, typ string }{
				snake: "ttl", camel: "Ttl", field: "ttl", typ: "TTL",
			},
		},
		"DigitInsideWord": {
			args: struct{ in string }{in: "IPv6"},
			want: struct{ snake, camel, field, typ string }{
				snake: "ipv6", camel: "Ipv6", field: "ipv6", typ: "IPv6",
			},
		},
		"DigitBeforeUpper": {
			args: struct{ in string }{in: "sha256Sum"},
			want: struct{ snake, camel, field, typ string }{
				snake: "sha256_sum", camel: "Sha256Sum", field: "sha256_sum", typ: "Sha256Sum",
			},
		},
		"APIVersionSegment": {
			args: struct{ in string }{in: "v1alpha1"},
			want: struct{ snake, camel, field, typ string }{
				snake: "v1alpha1", camel: "V1alpha1", field: "v1alpha1", typ: "V1alpha1",
			},
		},
		"DashedExtension": {
			args: struct{ in string }{in: "x-kubernetes-foo"},
			want: struct{ snake, camel, field, typ string }{
				snake: "x_kubernetes_foo", camel: "XKubernetesFoo", field: "x_kubernetes_foo", typ: "Xkubernetesfoo",
			},
		},
		"AlreadyPascal": {
			args: struct{ in string }{in: "DeviceAttribute"},
			want: struct{ snake, camel, field, typ string }{
				snake: "device_attribute", camel: "DeviceAttribute", field: "device_attribute", typ: "DeviceAttribute",
			},
		},
		"KubernetesKindWithAcronym": {
			args: struct{ in string }{in: "JSONSchemaProps"},
			want: struct{ snake, camel, field, typ string }{
				snake: "json_schema_props", camel: "JsonSchemaProps", field: "json_schema_props", typ: "JSONSchemaProps",
			},
		},
		// int, bool and string are legal Rust field names; they are only
		// reserved as type names. The DRA API has fields named exactly this.
		"PrimitiveNameInt": {
			args: struct{ in string }{in: "int"},
			want: struct{ snake, camel, field, typ string }{
				snake: "int", camel: "Int", field: "int", typ: "Int",
			},
		},
		"PrimitiveNameBool": {
			args: struct{ in string }{in: "bool"},
			want: struct{ snake, camel, field, typ string }{
				snake: "bool", camel: "Bool", field: "bool", typ: "Bool",
			},
		},
		"PreludeTypeName": {
			args: struct{ in string }{in: "string"},
			want: struct{ snake, camel, field, typ string }{
				snake: "string", camel: "String", field: "string", typ: "String_",
			},
		},
		"KeywordAsRawIdent": {
			args: struct{ in string }{in: "type"},
			want: struct{ snake, camel, field, typ string }{
				snake: "type", camel: "Type", field: "r#type", typ: "Type",
			},
		},
		"KeywordRef": {
			args: struct{ in string }{in: "ref"},
			want: struct{ snake, camel, field, typ string }{
				snake: "ref", camel: "Ref", field: "r#ref", typ: "Ref",
			},
		},
		"KeywordMatch": {
			args: struct{ in string }{in: "match"},
			want: struct{ snake, camel, field, typ string }{
				snake: "match", camel: "Match", field: "r#match", typ: "Match",
			},
		},
		// gen is only reserved from the 2024 edition on, which is the edition
		// the generated crate declares.
		"KeywordOfEdition2024": {
			args: struct{ in string }{in: "gen"},
			want: struct{ snake, camel, field, typ string }{
				snake: "gen", camel: "Gen", field: "r#gen", typ: "Gen",
			},
		},
		// self, crate, Self and super are the keywords raw identifiers cannot
		// express, so they take a trailing underscore.
		"KeywordWithoutRawForm": {
			args: struct{ in string }{in: "self"},
			want: struct{ snake, camel, field, typ string }{
				snake: "self", camel: "Self", field: "self_", typ: "Self_",
			},
		},
		"KeywordCrate": {
			args: struct{ in string }{in: "crate"},
			want: struct{ snake, camel, field, typ string }{
				snake: "crate", camel: "Crate", field: "crate_", typ: "Crate",
			},
		},
		"LeadingDigit": {
			args: struct{ in string }{in: "1abc"},
			want: struct{ snake, camel, field, typ string }{
				snake: "1abc", camel: "1abc", field: "_1abc", typ: "_1abc",
			},
		},
		"NoLetterOrDigit": {
			args: struct{ in string }{in: "-"},
			want: struct{ snake, camel, field, typ string }{
				snake: "", camel: "", field: "unnamed", typ: "",
			},
		},
		"Empty": {
			args: struct{ in string }{in: ""},
			want: struct{ snake, camel, field, typ string }{
				snake: "", camel: "", field: "unnamed", typ: "",
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := struct{ snake, camel, field, typ string }{
				snake: rustSnakeCase(tc.args.in),
				camel: rustUpperCamelCase(tc.args.in),
				field: rustFieldIdent(tc.args.in),
				typ:   rustTypeName(tc.args.in),
			}
			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(got)); diff != "" {
				t.Errorf("casing of %q: -want, +got:\n%s", tc.args.in, diff)
			}
		})
	}
}

func TestRustModuleSegment(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		args struct{ in string }
		want struct{ segment string }
	}{
		"Simple":         {args: struct{ in string }{in: "platform"}, want: struct{ segment string }{segment: "platform"}},
		"Version":        {args: struct{ in string }{in: "v1alpha1"}, want: struct{ segment string }{segment: "v1alpha1"}},
		"MixedCase":      {args: struct{ in string }{in: "apiMachinery"}, want: struct{ segment string }{segment: "apimachinery"}},
		"Dashed":         {args: struct{ in string }{in: "apiextensions-apiserver"}, want: struct{ segment string }{segment: "apiextensions_apiserver"}},
		"NonIdentifier":  {args: struct{ in string }{in: "a+b"}, want: struct{ segment string }{segment: "a_b"}},
		"LeadingDigit":   {args: struct{ in string }{in: "2fa"}, want: struct{ segment string }{segment: "_2fa"}},
		"Keyword":        {args: struct{ in string }{in: "type"}, want: struct{ segment string }{segment: "type_"}},
		"KeywordNoRaw":   {args: struct{ in string }{in: "crate"}, want: struct{ segment string }{segment: "crate_"}},
		"Empty":          {args: struct{ in string }{in: ""}, want: struct{ segment string }{segment: "unnamed"}},
		"AllNonAlphanum": {args: struct{ in string }{in: "--"}, want: struct{ segment string }{segment: "unnamed"}},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := struct{ segment string }{segment: rustModuleSegment(tc.args.in)}
			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(got)); diff != "" {
				t.Errorf("rustModuleSegment(%q): -want, +got:\n%s", tc.args.in, diff)
			}
		})
	}
}

func TestRustSchemaNames(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		args struct{ schema string }
		want struct{ path, stem string }
	}{
		"XRD": {
			args: struct{ schema string }{schema: "co.acme.platform.v1alpha1.XAccountScaffold"},
			want: struct{ path, stem string }{
				path: "crate::co::acme::platform::v1alpha1::XAccountScaffold",
				stem: "xaccountscaffold",
			},
		},
		"ProviderCRD": {
			args: struct{ schema string }{schema: "io.upbound.aws.s3.v1beta2.Bucket"},
			want: struct{ path, stem string }{
				path: "crate::io::upbound::aws::s3::v1beta2::Bucket",
				stem: "bucket",
			},
		},
		"KubernetesMeta": {
			args: struct{ schema string }{schema: "io.k8s.apimachinery.pkg.apis.meta.v1.ObjectMeta"},
			want: struct{ path, stem string }{
				path: "crate::io::k8s::apimachinery::pkg::apis::meta::v1::ObjectMeta",
				stem: "objectmeta",
			},
		},
		"DashedPackage": {
			args: struct{ schema string }{schema: "io.k8s.apiextensions-apiserver.pkg.apis.apiextensions.v1.JSONSchemaProps"},
			want: struct{ path, stem string }{
				path: "crate::io::k8s::apiextensions_apiserver::pkg::apis::apiextensions::v1::JSONSchemaProps",
				stem: "jsonschemaprops",
			},
		},
		"NoPackage": {
			args: struct{ schema string }{schema: "Widget"},
			want: struct{ path, stem string }{path: "crate::Widget", stem: "widget"},
		},
		// A kind named Type would give a file whose mod declaration is a
		// keyword, so the stem takes an underscore.
		"KeywordStem": {
			args: struct{ schema string }{schema: "com.example.v1.Type"},
			want: struct{ path, stem string }{path: "crate::com::example::v1::Type", stem: "type_"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, typ := rustSplitSchemaName(tc.args.schema)
			got := struct{ path, stem string }{
				path: rustPathForSchemaName(tc.args.schema),
				stem: rustFileStem(typ),
			}
			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(got)); diff != "" {
				t.Errorf("names for %q: -want, +got:\n%s", tc.args.schema, diff)
			}
		})
	}
}

func TestRustItemTypeName(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		args struct{ nested string }
		want struct{ item string }
	}{
		"Plural": {
			args: struct{ nested string }{nested: "XAccountScaffoldStatusConditions"},
			want: struct{ item string }{item: "XAccountScaffoldStatusCondition"},
		},
		"PluralAbbreviation": {
			args: struct{ nested string }{nested: "XAccountScaffoldSpecResourceRefs"},
			want: struct{ item string }{item: "XAccountScaffoldSpecResourceRef"},
		},
		"KeepsParentSpelling": {
			args: struct{ nested string }{nested: "JSONSchemaPropsEnums"},
			want: struct{ item string }{item: "JSONSchemaPropsEnum"},
		},
		"AlreadySingular": {
			args: struct{ nested string }{nested: "BucketSpecForProvider"},
			want: struct{ item string }{item: "BucketSpecForProviderItem"},
		},
		"Unpluralizable": {
			args: struct{ nested string }{nested: "PodSpecStatus"},
			want: struct{ item string }{item: "PodSpecStatusItem"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := struct{ item string }{item: rustItemTypeName(tc.args.nested)}
			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(got)); diff != "" {
				t.Errorf("rustItemTypeName(%q): -want, +got:\n%s", tc.args.nested, diff)
			}
		})
	}
}
