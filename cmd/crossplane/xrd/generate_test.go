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
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"

	v2 "github.com/crossplane/crossplane/apis/v2/apiextensions/v2"
)

func TestNewXRDFromExample(t *testing.T) {
	type args struct {
		inputYAML    string
		customPlural string
	}

	type want struct {
		xrd *v2.CompositeResourceDefinition
		err error
	}

	cases := map[string]struct {
		args args
		want want
	}{
		"ClusterScopedXR": {
			args: args{
				inputYAML: `
apiVersion: aws.u5d.io/v1
kind: XEKS
metadata:
  name: test
spec:
  parameters:
    id: test
    region: eu-central-1
`,
				customPlural: "xeks",
			},
			want: want{
				xrd: &v2.CompositeResourceDefinition{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "apiextensions.crossplane.io/v2",
						Kind:       "CompositeResourceDefinition",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name: "xeks.aws.u5d.io",
					},
					Spec: v2.CompositeResourceDefinitionSpec{
						Group: "aws.u5d.io",
						Scope: v2.CompositeResourceScopeCluster,
						Names: extv1.CustomResourceDefinitionNames{
							Categories: []string{"crossplane"},
							Kind:       "XEKS",
							Plural:     "xeks",
						},
						Versions: []v2.CompositeResourceDefinitionVersion{
							{
								Name:          "v1",
								Referenceable: true,
								Served:        true,
								Schema: &v2.CompositeResourceValidation{
									OpenAPIV3Schema: jsonSchemaPropsToRawExtension(&extv1.JSONSchemaProps{
										Description: "XEKS is the Schema for the XEKS API.",
										Type:        "object",
										Properties: map[string]extv1.JSONSchemaProps{
											"spec": {
												Description: "XEKSSpec defines the desired state of XEKS.",
												Type:        "object",
												Properties: map[string]extv1.JSONSchemaProps{
													"parameters": {
														Type: "object",
														Properties: map[string]extv1.JSONSchemaProps{
															"id":     {Type: "string"},
															"region": {Type: "string"},
														},
													},
												},
											},
											"status": {
												Description: "XEKSStatus defines the observed state of XEKS.",
												Type:        "object",
											},
										},
										Required: []string{"spec"},
									}),
								},
							},
						},
					},
				},
			},
		},
		"NamespaceScopedXRC": {
			args: args{
				inputYAML: `
apiVersion: aws.u5d.io/v1
kind: EKS
metadata:
  name: test
  namespace: test-namespace
spec:
  parameters:
    id: test
    region: eu-central-1
`,
				customPlural: "eks",
			},
			want: want{
				xrd: &v2.CompositeResourceDefinition{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "apiextensions.crossplane.io/v2",
						Kind:       "CompositeResourceDefinition",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name: "eks.aws.u5d.io",
					},
					Spec: v2.CompositeResourceDefinitionSpec{
						Group: "aws.u5d.io",
						Scope: v2.CompositeResourceScopeNamespaced,
						Names: extv1.CustomResourceDefinitionNames{
							Categories: []string{"crossplane"},
							Kind:       "EKS",
							Plural:     "eks",
						},
						Versions: []v2.CompositeResourceDefinitionVersion{
							{
								Name:          "v1",
								Referenceable: true,
								Served:        true,
								Schema: &v2.CompositeResourceValidation{
									OpenAPIV3Schema: jsonSchemaPropsToRawExtension(&extv1.JSONSchemaProps{
										Description: "EKS is the Schema for the EKS API.",
										Type:        "object",
										Properties: map[string]extv1.JSONSchemaProps{
											"spec": {
												Description: "EKSSpec defines the desired state of EKS.",
												Type:        "object",
												Properties: map[string]extv1.JSONSchemaProps{
													"parameters": {
														Type: "object",
														Properties: map[string]extv1.JSONSchemaProps{
															"id":     {Type: "string"},
															"region": {Type: "string"},
														},
													},
												},
											},
											"status": {
												Description: "EKSStatus defines the observed state of EKS.",
												Type:        "object",
											},
										},
										Required: []string{"spec"},
									}),
								},
							},
						},
					},
				},
			},
		},
		"CustomPluralPostgres": {
			args: args{
				inputYAML: `
apiVersion: database.u5d.io/v1
kind: Postgres
metadata:
  name: test
spec:
  parameters:
    version: "13"
`,
				customPlural: "postgreses",
			},
			want: want{
				xrd: &v2.CompositeResourceDefinition{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "apiextensions.crossplane.io/v2",
						Kind:       "CompositeResourceDefinition",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name: "postgreses.database.u5d.io",
					},
					Spec: v2.CompositeResourceDefinitionSpec{
						Group: "database.u5d.io",
						Scope: v2.CompositeResourceScopeCluster,
						Names: extv1.CustomResourceDefinitionNames{
							Categories: []string{"crossplane"},
							Kind:       "Postgres",
							Plural:     "postgreses",
						},
						Versions: []v2.CompositeResourceDefinitionVersion{
							{
								Name:          "v1",
								Referenceable: true,
								Served:        true,
								Schema: &v2.CompositeResourceValidation{
									OpenAPIV3Schema: jsonSchemaPropsToRawExtension(&extv1.JSONSchemaProps{
										Description: "Postgres is the Schema for the Postgres API.",
										Type:        "object",
										Properties: map[string]extv1.JSONSchemaProps{
											"spec": {
												Description: "PostgresSpec defines the desired state of Postgres.",
												Type:        "object",
												Properties: map[string]extv1.JSONSchemaProps{
													"parameters": {
														Type: "object",
														Properties: map[string]extv1.JSONSchemaProps{
															"version": {Type: "string"},
														},
													},
												},
											},
											"status": {
												Description: "PostgresStatus defines the observed state of Postgres.",
												Type:        "object",
											},
										},
										Required: []string{"spec"},
									}),
								},
							},
						},
					},
				},
			},
		},
		"BucketWithStatus": {
			args: args{
				inputYAML: `
apiVersion: storage.u5d.io/v1
kind: Bucket
metadata:
  name: test
spec:
  parameters:
    storage: "13"
status:
  bucketName: test
`,
			},
			want: want{
				xrd: &v2.CompositeResourceDefinition{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "apiextensions.crossplane.io/v2",
						Kind:       "CompositeResourceDefinition",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name: "buckets.storage.u5d.io",
					},
					Spec: v2.CompositeResourceDefinitionSpec{
						Group: "storage.u5d.io",
						Scope: v2.CompositeResourceScopeCluster,
						Names: extv1.CustomResourceDefinitionNames{
							Categories: []string{"crossplane"},
							Kind:       "Bucket",
							Plural:     "buckets",
						},
						Versions: []v2.CompositeResourceDefinitionVersion{
							{
								Name:          "v1",
								Referenceable: true,
								Served:        true,
								Schema: &v2.CompositeResourceValidation{
									OpenAPIV3Schema: jsonSchemaPropsToRawExtension(&extv1.JSONSchemaProps{
										Description: "Bucket is the Schema for the Bucket API.",
										Type:        "object",
										Properties: map[string]extv1.JSONSchemaProps{
											"spec": {
												Description: "BucketSpec defines the desired state of Bucket.",
												Type:        "object",
												Properties: map[string]extv1.JSONSchemaProps{
													"parameters": {
														Type: "object",
														Properties: map[string]extv1.JSONSchemaProps{
															"storage": {Type: "string"},
														},
													},
												},
											},
											"status": {
												Description: "BucketStatus defines the observed state of Bucket.",
												Type:        "object",
												Properties: map[string]extv1.JSONSchemaProps{
													"bucketName": {Type: "string"},
												},
											},
										},
										Required: []string{"spec"},
									}),
								},
							},
						},
					},
				},
			},
		},
		"RemoveXPStandardFieldsFromSpec": {
			args: args{
				inputYAML: `
apiVersion: aws.u5d.io/v1
kind: XEKS
metadata:
  name: test
spec:
  parameters:
    id: test
    region: eu-central-1
  resourceRefs:
    - name: resource1
  writeConnectionSecretToRef:
    name: secret
  publishConnectionDetailsTo:
    name: details
  environmentConfigRefs:
    - name: config1
  compositionSelector:
    matchLabels:
      layer: functions
`,
				customPlural: "xeks",
			},
			want: want{
				xrd: &v2.CompositeResourceDefinition{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "apiextensions.crossplane.io/v2",
						Kind:       "CompositeResourceDefinition",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name: "xeks.aws.u5d.io",
					},
					Spec: v2.CompositeResourceDefinitionSpec{
						Group: "aws.u5d.io",
						Scope: v2.CompositeResourceScopeCluster,
						Names: extv1.CustomResourceDefinitionNames{
							Categories: []string{"crossplane"},
							Kind:       "XEKS",
							Plural:     "xeks",
						},
						Versions: []v2.CompositeResourceDefinitionVersion{
							{
								Name:          "v1",
								Referenceable: true,
								Served:        true,
								Schema: &v2.CompositeResourceValidation{
									OpenAPIV3Schema: jsonSchemaPropsToRawExtension(&extv1.JSONSchemaProps{
										Description: "XEKS is the Schema for the XEKS API.",
										Type:        "object",
										Properties: map[string]extv1.JSONSchemaProps{
											"spec": {
												Description: "XEKSSpec defines the desired state of XEKS.",
												Type:        "object",
												Properties: map[string]extv1.JSONSchemaProps{
													"parameters": {
														Type: "object",
														Properties: map[string]extv1.JSONSchemaProps{
															"id":     {Type: "string"},
															"region": {Type: "string"},
														},
													},
												},
											},
											"status": {
												Description: "XEKSStatus defines the observed state of XEKS.",
												Type:        "object",
											},
										},
										Required: []string{"spec"},
									}),
								},
							},
						},
					},
				},
			},
		},
		"MissingAPIVersion": {
			args: args{
				inputYAML: `
kind: Postgres
metadata:
  name: test
spec:
  parameters:
    version: "13"
`,
				customPlural: "postgreses",
			},
			want: want{
				err: errors.New("invalid manifest: apiVersion is required"),
			},
		},
		"MissingKind": {
			args: args{
				inputYAML: `
apiVersion: database.u5d.io/v1
metadata:
  name: test
spec:
  parameters:
    version: "13"
`,
				customPlural: "postgreses",
			},
			want: want{
				err: errors.New("invalid manifest: kind is required"),
			},
		},
		"MissingMetadataName": {
			args: args{
				inputYAML: `
apiVersion: database.u5d.io/v1
kind: Postgres
spec:
  parameters:
    version: "13"
`,
				customPlural: "postgreses",
			},
			want: want{
				err: errors.New("invalid manifest: metadata.name is required"),
			},
		},
		"MissingSpec": {
			args: args{
				inputYAML: `
apiVersion: database.u5d.io/v1
kind: Postgres
metadata:
  name: test
`,
				customPlural: "postgreses",
			},
			want: want{
				err: errors.New("invalid manifest: spec is required"),
			},
		},
		"InvalidTopLevelKey": {
			args: args{
				inputYAML: `
apiVersion: database.u5d.io/v1
kind: Postgres
metadata:
  name: test
spec:
  parameters:
    version: "13"
invalidKey: shouldNotBeHere
`,
				customPlural: "postgreses",
			},
			want: want{
				err: errors.New("invalid manifest: valid top-level keys are: [apiVersion kind metadata spec status additionalPrinterColumns]"),
			},
		},
		"InvalidAPIVersionMultipleSlashes": {
			args: args{
				inputYAML: `
apiVersion: invalid/group/version/v1
kind: InvalidResource
metadata:
  name: test
spec:
  parameters:
    key: value
`,
				customPlural: "invalidresources",
			},
			want: want{
				err: errors.New("invalid manifest: apiVersion must be in the format group/version"),
			},
		},
		"MixedTypesInArray": {
			args: args{
				inputYAML: `
apiVersion: aws.u5d.io/v1
kind: MyClaim
metadata:
  name: my-claim
spec:
  parameters:
    - 1
    - "2"
    - true
`,
				customPlural: "myclaims",
			},
			want: want{
				err: errors.Wrap(errors.New("error inferring property for key 'parameters': mixed types detected in array"), "failed to infer properties for spec"),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := newXRDFromExample([]byte(tc.args.inputYAML), tc.args.customPlural)

			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("newXRDFromExample() error -want, +got:\n%s", diff)
			}

			if diff := cmp.Diff(got, tc.want.xrd, cmpopts.IgnoreFields(extv1.JSONSchemaProps{}, "Required")); diff != "" {
				t.Errorf("newXRDv2() -got, +want:\n%s", diff)
			}
		})
	}
}

func TestNewXRDFromSimpleSchema(t *testing.T) {
	type args struct {
		inputYAML    string
		customPlural string
	}

	type want struct {
		xrd *v2.CompositeResourceDefinition
		err error
	}

	preserveTrue := true

	cases := map[string]struct {
		args args
		want want
	}{
		"BasicSimpleSchema": {
			args: args{
				inputYAML: `
apiVersion: aws.u5d.io/v1
kind: XEKS
metadata:
  name: test
spec:
  region: string
  count: integer
`,
				customPlural: "xeks",
			},
			want: want{
				xrd: &v2.CompositeResourceDefinition{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "apiextensions.crossplane.io/v2",
						Kind:       "CompositeResourceDefinition",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name: "xeks.aws.u5d.io",
					},
					Spec: v2.CompositeResourceDefinitionSpec{
						Group: "aws.u5d.io",
						Scope: v2.CompositeResourceScopeNamespaced,
						Names: extv1.CustomResourceDefinitionNames{
							Categories: []string{"crossplane"},
							Kind:       "XEKS",
							Plural:     "xeks",
						},
						Versions: []v2.CompositeResourceDefinitionVersion{
							{
								Name:          "v1",
								Referenceable: true,
								Served:        true,
								Schema: &v2.CompositeResourceValidation{
									OpenAPIV3Schema: jsonSchemaPropsToRawExtension(&extv1.JSONSchemaProps{
										Description: "XEKS is the Schema for the XEKS API.",
										Type:        "object",
										Properties: map[string]extv1.JSONSchemaProps{
											"spec": {
												Type: "object",
												Properties: map[string]extv1.JSONSchemaProps{
													"region": {Type: "string"},
													"count":  {Type: "integer"},
												},
											},
											"status": {
												Type:       "object",
												Properties: map[string]extv1.JSONSchemaProps{},
											},
										},
										Required: []string{"spec"},
									}),
								},
							},
						},
					},
				},
			},
		},
		"SimpleSchemaWithCELStatus": {
			args: args{
				inputYAML: `
apiVersion: aws.u5d.io/v1
kind: XEKS
metadata:
  name: test
spec:
  region: string
status:
  clusterArn: ${resources.cluster.status.atProvider.arn}
  vpcId: ${resources.vpc.status.atProvider.vpcId}
`,
				customPlural: "xeks",
			},
			want: want{
				xrd: &v2.CompositeResourceDefinition{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "apiextensions.crossplane.io/v2",
						Kind:       "CompositeResourceDefinition",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name: "xeks.aws.u5d.io",
					},
					Spec: v2.CompositeResourceDefinitionSpec{
						Group: "aws.u5d.io",
						Scope: v2.CompositeResourceScopeNamespaced,
						Names: extv1.CustomResourceDefinitionNames{
							Categories: []string{"crossplane"},
							Kind:       "XEKS",
							Plural:     "xeks",
						},
						Versions: []v2.CompositeResourceDefinitionVersion{
							{
								Name:          "v1",
								Referenceable: true,
								Served:        true,
								Schema: &v2.CompositeResourceValidation{
									OpenAPIV3Schema: jsonSchemaPropsToRawExtension(&extv1.JSONSchemaProps{
										Description: "XEKS is the Schema for the XEKS API.",
										Type:        "object",
										Properties: map[string]extv1.JSONSchemaProps{
											"spec": {
												Type: "object",
												Properties: map[string]extv1.JSONSchemaProps{
													"region": {Type: "string"},
												},
											},
											"status": {
												Type: "object",
												Properties: map[string]extv1.JSONSchemaProps{
													"clusterArn": {XPreserveUnknownFields: &preserveTrue},
													"vpcId":      {XPreserveUnknownFields: &preserveTrue},
												},
											},
										},
										Required: []string{"spec"},
									}),
								},
							},
						},
					},
				},
			},
		},
		"SimpleSchemaWithCustomPlural": {
			args: args{
				inputYAML: `
apiVersion: database.u5d.io/v1
kind: Postgres
metadata:
  name: test
spec:
  version: string
`,
				customPlural: "postgreses",
			},
			want: want{
				xrd: &v2.CompositeResourceDefinition{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "apiextensions.crossplane.io/v2",
						Kind:       "CompositeResourceDefinition",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name: "postgreses.database.u5d.io",
					},
					Spec: v2.CompositeResourceDefinitionSpec{
						Group: "database.u5d.io",
						Scope: v2.CompositeResourceScopeNamespaced,
						Names: extv1.CustomResourceDefinitionNames{
							Categories: []string{"crossplane"},
							Kind:       "Postgres",
							Plural:     "postgreses",
						},
						Versions: []v2.CompositeResourceDefinitionVersion{
							{
								Name:          "v1",
								Referenceable: true,
								Served:        true,
								Schema: &v2.CompositeResourceValidation{
									OpenAPIV3Schema: jsonSchemaPropsToRawExtension(&extv1.JSONSchemaProps{
										Description: "Postgres is the Schema for the Postgres API.",
										Type:        "object",
										Properties: map[string]extv1.JSONSchemaProps{
											"spec": {
												Type: "object",
												Properties: map[string]extv1.JSONSchemaProps{
													"version": {Type: "string"},
												},
											},
											"status": {
												Type:       "object",
												Properties: map[string]extv1.JSONSchemaProps{},
											},
										},
										Required: []string{"spec"},
									}),
								},
							},
						},
					},
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := newXRDFromSimpleSchema([]byte(tc.args.inputYAML), tc.args.customPlural)

			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("newXRDFromSimpleSchema() error -want, +got:\n%s", diff)
			}

			if diff := cmp.Diff(got, tc.want.xrd, cmpopts.IgnoreFields(extv1.JSONSchemaProps{}, "Required")); diff != "" {
				t.Errorf("newXRDFromSimpleSchema() -got, +want:\n%s", diff)
			}
		})
	}
}

func jsonSchemaPropsToRawExtension(schema *extv1.JSONSchemaProps) runtime.RawExtension {
	schemaBytes, err := json.Marshal(schema)
	if err != nil {
		panic(err)
	}
	return runtime.RawExtension{Raw: schemaBytes}
}
