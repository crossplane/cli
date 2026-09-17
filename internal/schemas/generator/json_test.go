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
	"embed"
	"encoding/json"
	"strings"
	"testing"

	"github.com/invopop/jsonschema"
	"github.com/spf13/afero"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kube-openapi/pkg/validation/spec"
)

//go:embed testdata/*.json
var testdataJSONFS embed.FS

func TestGenerateFromCRD(t *testing.T) {
	inputFS := afero.NewBasePathFs(afero.FromIOFS{FS: testdataFS}, "testdata")
	schemaFS, err := jsonGenerator{}.GenerateFromCRD(t.Context(), inputFS, nil)
	if err != nil {
		t.Fatal(err)
	}

	expectedFiles := []string{
		"models/io-k8s-apimachinery-pkg-apis-meta-v1-DeleteOptions.schema.json",
		"models/io-k8s-apimachinery-pkg-apis-meta-v1-FieldsV1.schema.json",
		"models/io-k8s-apimachinery-pkg-apis-meta-v1-ListMeta.schema.json",
		"models/io-k8s-apimachinery-pkg-apis-meta-v1-ManagedFieldsEntry.schema.json",
		"models/io-k8s-apimachinery-pkg-apis-meta-v1-ObjectMeta.schema.json",
		"models/io-k8s-apimachinery-pkg-apis-meta-v1-OwnerReference.schema.json",
		"models/io-k8s-apimachinery-pkg-apis-meta-v1-Patch.schema.json",
		"models/io-k8s-apimachinery-pkg-apis-meta-v1-Preconditions.schema.json",
		"models/io-k8s-apimachinery-pkg-apis-meta-v1-StatusCause.schema.json",
		"models/io-k8s-apimachinery-pkg-apis-meta-v1-StatusDetails.schema.json",
		"models/io-k8s-apimachinery-pkg-apis-meta-v1-Status.schema.json",
		"models/io-k8s-apimachinery-pkg-apis-meta-v1-Time.schema.json",
		"models/co-acme-platform-v1alpha1-AccountScaffold.schema.json",
		"models/co-acme-platform-v1alpha1-AccountScaffoldList.schema.json",
		"models/co-acme-platform-v1alpha1-XAccountScaffold.schema.json",
		"models/co-acme-platform-v1alpha1-XAccountScaffoldList.schema.json",
	}

	for _, path := range expectedFiles {
		exists, err := afero.Exists(schemaFS, path)
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("expected model file %s does not exist", path)
		}

		contents, err := afero.ReadFile(schemaFS, path)
		if err != nil {
			t.Fatal(err)
		}

		var schema jsonschema.Schema
		if err := json.Unmarshal(contents, &schema); err != nil {
			t.Fatalf("failed to unmarshal %s: %v", path, err)
		}
	}
}

func TestMutateJSONSchema(t *testing.T) {
	t.Run("ObjectWithProperties", func(t *testing.T) {
		s := &jsonschema.Schema{
			Type: "object",
		}
		s.Properties = jsonschema.NewProperties()
		s.Properties.Set("name", &jsonschema.Schema{Type: "string"})

		mutateJSONSchema(s)

		if s.AdditionalProperties != jsonschema.FalseSchema {
			t.Error("expected additionalProperties to be false for object with properties")
		}
	})

	t.Run("EmptyObject", func(t *testing.T) {
		s := &jsonschema.Schema{
			Type: "object",
		}

		mutateJSONSchema(s)

		if s.AdditionalProperties != nil {
			t.Error("expected additionalProperties to remain nil for empty object")
		}
	})
}

func TestRewriteComponentRefs(t *testing.T) {
	cRef := func(n string) spec.Ref { return spec.MustCreateRef("#/components/schemas/" + n) }

	s := &spec.Schema{
		SchemaProps: spec.SchemaProps{
			Properties: map[string]spec.Schema{
				"a": {SchemaProps: spec.SchemaProps{Ref: cRef("A")}},
			},
			Items: &spec.SchemaOrArray{
				Schema: &spec.Schema{SchemaProps: spec.SchemaProps{Ref: cRef("B")}},
			},
			AllOf: []spec.Schema{{SchemaProps: spec.SchemaProps{Ref: cRef("C")}}},
			Not:   &spec.Schema{SchemaProps: spec.SchemaProps{Ref: cRef("D")}},
		},
	}
	rewriteComponentRefs(s)

	bs, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	raw := string(bs)
	if strings.Contains(raw, "#/components/schemas/") {
		t.Fatalf("refs remain: %s", raw)
	}
	for _, name := range []string{"A", "B", "C", "D"} {
		if !strings.Contains(raw, "#/$defs/"+name) {
			t.Errorf("missing #/$defs/%s in output", name)
		}
	}
}

func TestCRDsToJSONSchemasRewritesRefs(t *testing.T) {
	crd := &extv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "widgets.example.org"},
		Spec: extv1.CustomResourceDefinitionSpec{
			Group: "example.org",
			Names: extv1.CustomResourceDefinitionNames{
				Kind: "Widget", Plural: "widgets", Singular: "widget", ListKind: "WidgetList",
			},
			Scope: extv1.NamespaceScoped,
			Versions: []extv1.CustomResourceDefinitionVersion{{
				Name: "v1", Served: true, Storage: true,
				Schema: &extv1.CustomResourceValidation{
					OpenAPIV3Schema: &extv1.JSONSchemaProps{
						Type:       "object",
						Properties: map[string]extv1.JSONSchemaProps{"spec": {Type: "object"}},
					},
				},
			}},
		},
	}

	schemas, err := CRDsToJSONSchemas([]*extv1.CustomResourceDefinition{crd})
	if err != nil {
		t.Fatalf("CRDsToJSONSchemas: %v", err)
	}
	if len(schemas) == 0 {
		t.Fatal("expected at least one schema")
	}

	raw := string(schemas[0].Data)
	if strings.Contains(raw, "#/components/schemas/") {
		t.Fatal("output still contains #/components/schemas/ refs")
	}

	var parsed map[string]any
	if err := json.Unmarshal(schemas[0].Data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	defs, ok := parsed["$defs"].(map[string]any)
	if !ok || len(defs) == 0 {
		t.Fatal("expected $defs with at least one entry")
	}

	// Verify every #/$defs/ ref in the output resolves to an actual $defs entry.
	for _, match := range strings.Split(raw, "#/$defs/")[1:] {
		name, _, _ := strings.Cut(match, "\"")
		if _, ok := defs[name]; !ok {
			t.Errorf("#/$defs/%s referenced but not defined in $defs", name)
		}
	}
}

func TestGenerateFromOpenAPI(t *testing.T) {
	inputFS := afero.NewBasePathFs(afero.FromIOFS{FS: testdataJSONFS}, "testdata")
	schemaFS, err := jsonGenerator{}.GenerateFromOpenAPI(t.Context(), inputFS, nil)
	if err != nil {
		t.Fatal(err)
	}

	expectedFiles := []string{
		"models/io-k8s-api-authentication-v1-BoundObjectReference.schema.json",
		"models/io-k8s-api-authentication-v1-TokenRequest.schema.json",
		"models/io-k8s-api-authentication-v1-TokenRequestSpec.schema.json",
		"models/io-k8s-api-authentication-v1-TokenRequestStatus.schema.json",
		"models/io-k8s-api-autoscaling-v1-Scale.schema.json",
		"models/io-k8s-api-autoscaling-v1-ScaleSpec.schema.json",
		"models/io-k8s-api-autoscaling-v1-ScaleStatus.schema.json",
		"models/io-k8s-api-core-v1-ConfigMap.schema.json",
		"models/io-k8s-api-core-v1-Pod.schema.json",
		"models/io-k8s-api-core-v1-Service.schema.json",
		"models/io-k8s-api-policy-v1-Eviction.schema.json",
		"models/io-k8s-apimachinery-pkg-api-resource-Quantity.schema.json",
		"models/io-k8s-apimachinery-pkg-apis-meta-v1-Condition.schema.json",
		"models/io-k8s-apimachinery-pkg-apis-meta-v1-ObjectMeta.schema.json",
		"models/io-k8s-apimachinery-pkg-apis-meta-v1-Status.schema.json",
		"models/io-k8s-apimachinery-pkg-runtime-RawExtension.schema.json",
		"models/io-k8s-apimachinery-pkg-util-intstr-IntOrString.schema.json",
	}

	for _, path := range expectedFiles {
		exists, err := afero.Exists(schemaFS, path)
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("expected model file %s does not exist", path)
		}

		contents, err := afero.ReadFile(schemaFS, path)
		if err != nil {
			t.Fatal(err)
		}

		var schema jsonschema.Schema
		if err := json.Unmarshal(contents, &schema); err != nil {
			t.Fatalf("failed to unmarshal %s: %v", path, err)
		}
	}
}
