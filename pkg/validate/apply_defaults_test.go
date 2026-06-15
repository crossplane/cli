/*
Copyright 2024 The Crossplane Authors.

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

package validate

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	runtimeschema "k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
)

func TestApplyDefaults(t *testing.T) {
	type args struct {
		resource *unstructured.Unstructured
		gvk      runtimeschema.GroupVersionKind
		crds     []*extv1.CustomResourceDefinition
	}

	type want struct {
		resource *unstructured.Unstructured
		err      error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"NoCRDFound": {
			reason: "Should return nil when no matching CRD is found (skip defaulting)",
			args: args{
				resource: &unstructured.Unstructured{
					Object: map[string]any{
						"apiVersion": "test.org/v1alpha1",
						"kind":       "Test",
						"spec":       map[string]any{"replicas": 3},
					},
				},
				gvk:  runtimeschema.GroupVersionKind{Group: "test.org", Version: "v1alpha1", Kind: "Test"},
				crds: []*extv1.CustomResourceDefinition{},
			},
			want: want{
				resource: &unstructured.Unstructured{
					Object: map[string]any{
						"apiVersion": "test.org/v1alpha1",
						"kind":       "Test",
						"spec":       map[string]any{"replicas": 3},
					},
				},
			},
		},
		"ApplySimpleDefault": {
			reason: "Should apply default value to missing property",
			args: args{
				resource: &unstructured.Unstructured{
					Object: map[string]any{
						"apiVersion": "test.org/v1alpha1",
						"kind":       "Test",
						"spec":       map[string]any{"replicas": 3},
					},
				},
				gvk: runtimeschema.GroupVersionKind{Group: "test.org", Version: "v1alpha1", Kind: "Test"},
				crds: []*extv1.CustomResourceDefinition{{
					Spec: extv1.CustomResourceDefinitionSpec{
						Group: "test.org",
						Names: extv1.CustomResourceDefinitionNames{Kind: "Test"},
						Versions: []extv1.CustomResourceDefinitionVersion{{
							Name: "v1alpha1",
							Schema: &extv1.CustomResourceValidation{
								OpenAPIV3Schema: &extv1.JSONSchemaProps{
									Type: "object",
									Properties: map[string]extv1.JSONSchemaProps{
										"spec": {
											Type: "object",
											Properties: map[string]extv1.JSONSchemaProps{
												"replicas": {Type: "integer"},
												"deletionPolicy": {
													Type:    "string",
													Default: &extv1.JSON{Raw: []byte(`"Delete"`)},
												},
											},
										},
									},
								},
							},
						}},
					},
				}},
			},
			want: want{
				resource: &unstructured.Unstructured{
					Object: map[string]any{
						"apiVersion": "test.org/v1alpha1",
						"kind":       "Test",
						"spec": map[string]any{
							"replicas":       3,
							"deletionPolicy": "Delete",
						},
					},
				},
			},
		},
		"DoNotOverrideExisting": {
			reason: "Should not override existing values with defaults",
			args: args{
				resource: &unstructured.Unstructured{
					Object: map[string]any{
						"apiVersion": "test.org/v1alpha1",
						"kind":       "Test",
						"spec": map[string]any{
							"replicas":       3,
							"deletionPolicy": "Retain",
						},
					},
				},
				gvk: runtimeschema.GroupVersionKind{Group: "test.org", Version: "v1alpha1", Kind: "Test"},
				crds: []*extv1.CustomResourceDefinition{{
					Spec: extv1.CustomResourceDefinitionSpec{
						Group: "test.org",
						Names: extv1.CustomResourceDefinitionNames{Kind: "Test"},
						Versions: []extv1.CustomResourceDefinitionVersion{{
							Name: "v1alpha1",
							Schema: &extv1.CustomResourceValidation{
								OpenAPIV3Schema: &extv1.JSONSchemaProps{
									Type: "object",
									Properties: map[string]extv1.JSONSchemaProps{
										"spec": {
											Type: "object",
											Properties: map[string]extv1.JSONSchemaProps{
												"replicas": {Type: "integer"},
												"deletionPolicy": {
													Type:    "string",
													Default: &extv1.JSON{Raw: []byte(`"Delete"`)},
												},
											},
										},
									},
								},
							},
						}},
					},
				}},
			},
			want: want{
				resource: &unstructured.Unstructured{
					Object: map[string]any{
						"apiVersion": "test.org/v1alpha1",
						"kind":       "Test",
						"spec": map[string]any{
							"replicas":       3,
							"deletionPolicy": "Retain",
						},
					},
				},
			},
		},
		"NestedDefaults": {
			reason: "Should apply defaults to nested objects",
			args: args{
				resource: &unstructured.Unstructured{
					Object: map[string]any{
						"apiVersion": "test.org/v1alpha1",
						"kind":       "Test",
						"spec": map[string]any{
							"forProvider": map[string]any{"region": "us-east-1"},
						},
					},
				},
				gvk: runtimeschema.GroupVersionKind{Group: "test.org", Version: "v1alpha1", Kind: "Test"},
				crds: []*extv1.CustomResourceDefinition{{
					Spec: extv1.CustomResourceDefinitionSpec{
						Group: "test.org",
						Names: extv1.CustomResourceDefinitionNames{Kind: "Test"},
						Versions: []extv1.CustomResourceDefinitionVersion{{
							Name: "v1alpha1",
							Schema: &extv1.CustomResourceValidation{
								OpenAPIV3Schema: &extv1.JSONSchemaProps{
									Type: "object",
									Properties: map[string]extv1.JSONSchemaProps{
										"spec": {
											Type: "object",
											Properties: map[string]extv1.JSONSchemaProps{
												"forProvider": {
													Type: "object",
													Properties: map[string]extv1.JSONSchemaProps{
														"region": {Type: "string"},
														"instanceType": {
															Type:    "string",
															Default: &extv1.JSON{Raw: []byte(`"t3.micro"`)},
														},
													},
												},
												"deletionPolicy": {
													Type:    "string",
													Default: &extv1.JSON{Raw: []byte(`"Delete"`)},
												},
											},
										},
									},
								},
							},
						}},
					},
				}},
			},
			want: want{
				resource: &unstructured.Unstructured{
					Object: map[string]any{
						"apiVersion": "test.org/v1alpha1",
						"kind":       "Test",
						"spec": map[string]any{
							"forProvider": map[string]any{
								"region":       "us-east-1",
								"instanceType": "t3.micro",
							},
							"deletionPolicy": "Delete",
						},
					},
				},
			},
		},
		"ComplexDefaults": {
			reason: "Should apply complex default values (objects, arrays)",
			args: args{
				resource: &unstructured.Unstructured{
					Object: map[string]any{
						"apiVersion": "test.org/v1alpha1",
						"kind":       "Test",
						"spec":       map[string]any{"name": "test"},
					},
				},
				gvk: runtimeschema.GroupVersionKind{Group: "test.org", Version: "v1alpha1", Kind: "Test"},
				crds: []*extv1.CustomResourceDefinition{{
					Spec: extv1.CustomResourceDefinitionSpec{
						Group: "test.org",
						Names: extv1.CustomResourceDefinitionNames{Kind: "Test"},
						Versions: []extv1.CustomResourceDefinitionVersion{{
							Name: "v1alpha1",
							Schema: &extv1.CustomResourceValidation{
								OpenAPIV3Schema: &extv1.JSONSchemaProps{
									Type: "object",
									Properties: map[string]extv1.JSONSchemaProps{
										"spec": {
											Type: "object",
											Properties: map[string]extv1.JSONSchemaProps{
												"name": {Type: "string"},
												"metadata": {
													Type:    "object",
													Default: &extv1.JSON{Raw: []byte(`{"labels":{"app":"default-app"}}`)},
												},
												"tags": {
													Type:    "array",
													Default: &extv1.JSON{Raw: []byte(`["default","tag"]`)},
												},
											},
										},
									},
								},
							},
						}},
					},
				}},
			},
			want: want{
				resource: &unstructured.Unstructured{
					Object: map[string]any{
						"apiVersion": "test.org/v1alpha1",
						"kind":       "Test",
						"spec": map[string]any{
							"name": "test",
							"metadata": map[string]any{
								"labels": map[string]any{"app": "default-app"},
							},
							"tags": []any{"default", "tag"},
						},
					},
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := applyDefaults(tc.args.resource, tc.args.gvk, tc.args.crds)
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("%s\napplyDefaults(...): -want err, +got err:\n%s", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.resource, tc.args.resource); diff != "" {
				t.Errorf("%s\napplyDefaults(...): -want resource, +got resource:\n%s", tc.reason, diff)
			}
		})
	}
}
