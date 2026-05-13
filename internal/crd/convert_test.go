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

package crd

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/afero"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/kube-openapi/pkg/spec3"
	"k8s.io/kube-openapi/pkg/validation/spec"
	"sigs.k8s.io/yaml"

	_ "embed"
)

//go:embed testdata/template.fn.crossplane.io_kclinputs.yaml
var testCRD []byte

func TestFilesToOpenAPI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		crdContent  []byte
		expectedErr bool
	}{
		{
			name:        "ValidCRDFromEmbed",
			crdContent:  testCRD,
			expectedErr: false,
		},
		{
			name:        "InvalidCRD",
			crdContent:  []byte(`invalid: crd content`),
			expectedErr: true,
		},
		{
			name: "CRDMissingVersion",
			crdContent: []byte(`
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: testresources.testgroup.example.com
spec:
  group: testgroup.example.com
  versions: []
  names:
    kind: TestResource
    plural: testresources
`),
			expectedErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fs := afero.NewMemMapFs()
			outputPaths, err := FilesToOpenAPI(fs, tt.crdContent, "test-crd.yaml")

			if tt.expectedErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := len(outputPaths); got != 1 {
				t.Fatalf("len(outputPaths) = %d, want 1", got)
			}

			output, err := afero.ReadFile(fs, outputPaths[0])
			if err != nil {
				t.Fatal(err)
			}

			var openapi *spec3.OpenAPI
			if err := yaml.Unmarshal(output, &openapi); err != nil {
				t.Fatal(err)
			}

			apiVersionDefault := openapi.Components.Schemas["io.crossplane.fn.template.v1beta1.KCLInput"].SchemaProps.Properties["apiVersion"].Default
			if diff := cmp.Diff("template.fn.crossplane.io/v1beta1", apiVersionDefault); diff != "" {
				t.Errorf("apiVersion default (-want +got):\n%s", diff)
			}

			kindDefault := openapi.Components.Schemas["io.crossplane.fn.template.v1beta1.KCLInput"].SchemaProps.Properties["kind"].Default
			if diff := cmp.Diff("KCLInput", kindDefault); diff != "" {
				t.Errorf("kind default (-want +got):\n%s", diff)
			}
		})
	}
}

func TestAddDefaultAPIVersionAndKind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		initialSchema      spec.Schema
		gvk                schema.GroupVersionKind
		expectedAPIVersion string
		expectedKind       string
	}{
		{
			name: "ApiVersionAndKind",
			initialSchema: spec.Schema{
				SchemaProps: spec.SchemaProps{
					Properties: map[string]spec.Schema{
						"apiVersion": {},
						"kind":       {},
					},
				},
			},
			gvk:                schema.GroupVersionKind{Group: "example.com", Version: "v1", Kind: "ExampleKind"},
			expectedAPIVersion: "example.com/v1",
			expectedKind:       "ExampleKind",
		},
		{
			name: "ApiVersion",
			initialSchema: spec.Schema{
				SchemaProps: spec.SchemaProps{
					Properties: map[string]spec.Schema{
						"apiVersion": {},
					},
				},
			},
			gvk:                schema.GroupVersionKind{Group: "example.com", Version: "v2", Kind: "AnotherKind"},
			expectedAPIVersion: "example.com/v2",
			expectedKind:       "",
		},
		{
			name: "Kind",
			initialSchema: spec.Schema{
				SchemaProps: spec.SchemaProps{
					Properties: map[string]spec.Schema{
						"kind": {},
					},
				},
			},
			gvk:                schema.GroupVersionKind{Group: "example.com", Version: "v1alpha1", Kind: "SampleKind"},
			expectedAPIVersion: "",
			expectedKind:       "SampleKind",
		},
		{
			name: "Nothing",
			initialSchema: spec.Schema{
				SchemaProps: spec.SchemaProps{
					Properties: map[string]spec.Schema{},
				},
			},
			gvk:                schema.GroupVersionKind{Group: "example.com", Version: "v1beta1", Kind: "NoDefaults"},
			expectedAPIVersion: "",
			expectedKind:       "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			addDefaultAPIVersionAndKind(&tt.initialSchema, tt.gvk)

			if prop, ok := tt.initialSchema.Properties["apiVersion"]; ok {
				if diff := cmp.Diff(tt.expectedAPIVersion, prop.Default); diff != "" {
					t.Errorf("apiVersion default (-want +got):\n%s", diff)
				}
			}
			if prop, ok := tt.initialSchema.Properties["kind"]; ok {
				if diff := cmp.Diff(tt.expectedKind, prop.Default); diff != "" {
					t.Errorf("kind default (-want +got):\n%s", diff)
				}
			}
		})
	}
}

var testValidCRD = []byte(`
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: objects.kubernetes.crossplane.io
spec:
  group: kubernetes.crossplane.io
  names:
    kind: Object
    plural: objects
    singular: object
  scope: Cluster
  versions:
    - name: v1alpha1
      schema:
        openAPIV3Schema:
          properties:
            spec:
              properties:
                forProvider:
                  properties:
                    manifest:
                      type: object
                      x-kubernetes-embedded-resource: true
                      x-kubernetes-preserve-unknown-fields: true
`)

func TestModifyCRDManifestFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		crdContent  []byte
		expectedErr bool
	}{
		{
			name:        "ValidCRD",
			crdContent:  testValidCRD,
			expectedErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var crd extv1.CustomResourceDefinition
			if err := yaml.Unmarshal(tt.crdContent, &crd); err != nil {
				if tt.expectedErr {
					return
				}
				t.Fatalf("Failed to unmarshal CRD: %v", err)
			}

			modifyCRDManifestFields(&crd)

			modifiedManifest := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"].
				Properties["forProvider"].Properties["manifest"]

			if modifiedManifest.XEmbeddedResource {
				t.Errorf("XEmbeddedResource = true, want false")
			}
			if modifiedManifest.XPreserveUnknownFields != nil {
				t.Errorf("XPreserveUnknownFields = %v, want nil", *modifiedManifest.XPreserveUnknownFields)
			}
			if diff := cmp.Diff("object", modifiedManifest.Type); diff != "" {
				t.Errorf("manifest Type (-want +got):\n%s", diff)
			}
		})
	}
}

func TestUpdateSchemaPropertiesXEmbeddedResource(t *testing.T) {
	tests := []struct {
		name     string
		input    *extv1.JSONSchemaProps
		expected *extv1.JSONSchemaProps
	}{
		{
			name:     "NilSchema",
			input:    nil,
			expected: nil,
		},
		{
			name: "SchemaWithXEmbeddedResourceAndXPreserveUnknownFields",
			input: &extv1.JSONSchemaProps{
				XEmbeddedResource:      true,
				XPreserveUnknownFields: &[]bool{true}[0],
			},
			expected: &extv1.JSONSchemaProps{
				XEmbeddedResource:      false,
				XPreserveUnknownFields: nil,
				Type:                   "object",
				AdditionalProperties: &extv1.JSONSchemaPropsOrBool{
					Allows: true,
					Schema: nil,
				},
			},
		},
		{
			name: "NestedProperties",
			input: &extv1.JSONSchemaProps{
				Properties: map[string]extv1.JSONSchemaProps{
					"nested": {
						XEmbeddedResource:      true,
						XPreserveUnknownFields: &[]bool{true}[0],
					},
				},
			},
			expected: &extv1.JSONSchemaProps{
				Properties: map[string]extv1.JSONSchemaProps{
					"nested": {
						XEmbeddedResource:      false,
						XPreserveUnknownFields: nil,
						Type:                   "object",
						AdditionalProperties: &extv1.JSONSchemaPropsOrBool{
							Allows: true,
							Schema: nil,
						},
					},
				},
			},
		},
		{
			name: "AdditionalProperties",
			input: &extv1.JSONSchemaProps{
				AdditionalProperties: &extv1.JSONSchemaPropsOrBool{
					Schema: &extv1.JSONSchemaProps{
						XEmbeddedResource:      true,
						XPreserveUnknownFields: &[]bool{true}[0],
					},
				},
			},
			expected: &extv1.JSONSchemaProps{
				AdditionalProperties: &extv1.JSONSchemaPropsOrBool{
					Schema: &extv1.JSONSchemaProps{
						XEmbeddedResource:      false,
						XPreserveUnknownFields: nil,
						Type:                   "object",
						AdditionalProperties: &extv1.JSONSchemaPropsOrBool{
							Allows: true,
							Schema: nil,
						},
					},
				},
			},
		},
		{
			name: "Items",
			input: &extv1.JSONSchemaProps{
				Items: &extv1.JSONSchemaPropsOrArray{
					Schema: &extv1.JSONSchemaProps{
						XEmbeddedResource:      true,
						XPreserveUnknownFields: &[]bool{true}[0],
					},
				},
			},
			expected: &extv1.JSONSchemaProps{
				Items: &extv1.JSONSchemaPropsOrArray{
					Schema: &extv1.JSONSchemaProps{
						XEmbeddedResource:      false,
						XPreserveUnknownFields: nil,
						Type:                   "object",
						AdditionalProperties: &extv1.JSONSchemaPropsOrBool{
							Allows: true,
							Schema: nil,
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			updateSchemaPropertiesXEmbeddedResource(tt.input)
			if diff := cmp.Diff(tt.expected, tt.input); diff != "" {
				t.Errorf("updateSchemaPropertiesXEmbeddedResource (-want +got):\n%s", diff)
			}
		})
	}
}
