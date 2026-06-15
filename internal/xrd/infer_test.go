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

package xrd

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

func TestInferProperty(t *testing.T) {
	type want struct {
		output extv1.JSONSchemaProps
		err    error
	}

	cases := map[string]struct {
		input any
		want  want
	}{
		"StringType": {
			input: "hello",
			want: want{
				output: extv1.JSONSchemaProps{Type: schemaTypeString},
			},
		},
		"IntegerType": {
			input: 42,
			want: want{
				output: extv1.JSONSchemaProps{Type: schemaTypeInteger},
			},
		},
		"FloatType": {
			input: 3.14,
			want: want{
				output: extv1.JSONSchemaProps{Type: schemaTypeNumber},
			},
		},
		"IntegerAsFloatType": {
			input: float64(1),
			want: want{
				output: extv1.JSONSchemaProps{Type: schemaTypeInteger},
			},
		},
		"BooleanType": {
			input: true,
			want: want{
				output: extv1.JSONSchemaProps{Type: schemaTypeBoolean},
			},
		},
		"ObjectType": {
			input: map[string]any{
				"key": "value",
			},
			want: want{
				output: extv1.JSONSchemaProps{
					Type: schemaTypeObject,
					Properties: map[string]extv1.JSONSchemaProps{
						"key": {Type: schemaTypeString},
					},
				},
			},
		},
		"ArrayTypeWithElements": {
			input: []any{"one", "two"},
			want: want{
				output: extv1.JSONSchemaProps{
					Type: schemaTypeArray,
					Items: &extv1.JSONSchemaPropsOrArray{
						Schema: &extv1.JSONSchemaProps{Type: schemaTypeString},
					},
				},
			},
		},
		"ArrayWithMixedNumbersIntegerFirst": {
			input: []any{1, float32(3.14)},
			want: want{
				output: extv1.JSONSchemaProps{
					Type: schemaTypeArray,
					Items: &extv1.JSONSchemaPropsOrArray{
						Schema: &extv1.JSONSchemaProps{Type: schemaTypeNumber},
					},
				},
			},
		},
		"ArrayWithMixedNumbersFloatFirst": {
			input: []any{float32(3.14), 1},
			want: want{
				output: extv1.JSONSchemaProps{
					Type: schemaTypeArray,
					Items: &extv1.JSONSchemaPropsOrArray{
						Schema: &extv1.JSONSchemaProps{Type: schemaTypeNumber},
					},
				},
			},
		},
		"ArrayTypeEmpty": {
			input: []any{},
			want: want{
				output: extv1.JSONSchemaProps{
					Type: schemaTypeArray,
					Items: &extv1.JSONSchemaPropsOrArray{
						Schema: &extv1.JSONSchemaProps{Type: schemaTypeObject},
					},
				},
			},
		},
		"NilValue": {
			input: nil,
			want: want{
				output: extv1.JSONSchemaProps{Type: schemaTypeString},
			},
		},
		"ArrayWithMixedTypes": {
			input: []any{1, "2", true},
			want: want{
				output: extv1.JSONSchemaProps{},
				err:    errors.New("mixed types detected in array"),
			},
		},
		"ArrayOfObjectsWithOptionalFields": {
			input: []any{
				map[string]any{
					"name":             "aks-subnet",
					"cidr":             "10.0.1.0/24",
					"serviceEndpoints": []any{"Microsoft.ContainerRegistry"},
				},
				map[string]any{
					"name":             "database-subnet",
					"cidr":             "10.0.2.0/24",
					"delegation":       "Microsoft.DBforMySQL/flexibleServers",
					"serviceEndpoints": []any{"Microsoft.Storage"},
				},
			},
			want: want{
				output: extv1.JSONSchemaProps{
					Type: schemaTypeArray,
					Items: &extv1.JSONSchemaPropsOrArray{
						Schema: &extv1.JSONSchemaProps{
							Type: schemaTypeObject,
							Properties: map[string]extv1.JSONSchemaProps{
								"name": {Type: schemaTypeString},
								"cidr": {Type: schemaTypeString},
								"serviceEndpoints": {
									Type: "array",
									Items: &extv1.JSONSchemaPropsOrArray{
										Schema: &extv1.JSONSchemaProps{Type: schemaTypeString},
									},
								},
								"delegation": {Type: schemaTypeString},
							},
						},
					},
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := inferProperty(tc.input)

			if err != nil || tc.want.err != nil {
				if err == nil || tc.want.err == nil || err.Error() != tc.want.err.Error() {
					t.Errorf("inferProperty() error = %v, wantErr %v", err, tc.want.err)
				}
				return
			}

			if diff := cmp.Diff(got, tc.want.output); diff != "" {
				t.Errorf("inferProperty() -got, +want:\n%s", diff)
			}
		})
	}
}

func TestInferProperties(t *testing.T) {
	type want struct {
		output map[string]extv1.JSONSchemaProps
		err    error
	}

	cases := map[string]struct {
		input map[string]any
		want  want
	}{
		"SimpleObject": {
			input: map[string]any{
				"key1": "value1",
				"key2": 42,
			},
			want: want{
				output: map[string]extv1.JSONSchemaProps{
					"key1": {Type: schemaTypeString},
					"key2": {Type: schemaTypeInteger},
				},
			},
		},
		"NestedObject": {
			input: map[string]any{
				"nested": map[string]any{
					"key": true,
				},
			},
			want: want{
				output: map[string]extv1.JSONSchemaProps{
					"nested": {
						Type: schemaTypeObject,
						Properties: map[string]extv1.JSONSchemaProps{
							"key": {Type: schemaTypeBoolean},
						},
					},
				},
			},
		},
		"ArrayInObject": {
			input: map[string]any{
				"array": []any{"a", "b"},
			},
			want: want{
				output: map[string]extv1.JSONSchemaProps{
					"array": {
						Type: "array",
						Items: &extv1.JSONSchemaPropsOrArray{
							Schema: &extv1.JSONSchemaProps{Type: schemaTypeString},
						},
					},
				},
			},
		},
		"ObjectWithMixedArray": {
			input: map[string]any{
				"array": []any{1, "2"},
			},
			want: want{
				output: nil,
				err:    errors.New("error inferring property for key 'array': mixed types detected in array"),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := InferProperties(tc.input)

			if err != nil || tc.want.err != nil {
				if err == nil || tc.want.err == nil || err.Error() != tc.want.err.Error() {
					t.Errorf("InferProperties() error = %v, wantErr %v", err, tc.want.err)
				}
				return
			}

			if diff := cmp.Diff(got, tc.want.output); diff != "" {
				t.Errorf("InferProperties() -got, +want:\n%s", diff)
			}
		})
	}
}
