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

package xpkg

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/google/go-cmp/cmp"
	"github.com/invopop/jsonschema"
	"github.com/spf13/afero"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
)

const schemaTypeObject = "object"

var testCRD = &extv1.CustomResourceDefinition{
	TypeMeta: metav1.TypeMeta{
		APIVersion: "apiextensions.k8s.io/v1",
		Kind:       "CustomResourceDefinition",
	},
	ObjectMeta: metav1.ObjectMeta{
		Name: "tests.example.org",
	},
	Spec: extv1.CustomResourceDefinitionSpec{
		Group: "example.org",
		Names: extv1.CustomResourceDefinitionNames{
			Kind:     "Test",
			Plural:   "tests",
			Singular: "test",
			ListKind: "TestList",
		},
		Scope: extv1.NamespaceScoped,
		Versions: []extv1.CustomResourceDefinitionVersion{
			{
				Name:    "v1alpha1",
				Served:  true,
				Storage: true,
				Schema: &extv1.CustomResourceValidation{
					OpenAPIV3Schema: &extv1.JSONSchemaProps{
						Type: schemaTypeObject,
						Properties: map[string]extv1.JSONSchemaProps{
							"spec": {
								Type: schemaTypeObject,
								Properties: map[string]extv1.JSONSchemaProps{
									"replicas": {
										Type: "integer",
									},
								},
							},
						},
					},
				},
			},
		},
	},
}

func TestWriteCRDs(t *testing.T) {
	type args struct {
		crds      []*extv1.CustomResourceDefinition
		flat      bool
		outputDir string
	}

	type want struct {
		files []string
		err   error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"Structured": {
			reason: "Should write CRDs organized by group and storage version",
			args: args{
				crds:      []*extv1.CustomResourceDefinition{testCRD},
				outputDir: "/out",
			},
			want: want{
				files: []string{"/out/example.org/v1alpha1/test.yaml"},
			},
		},
		"Flat": {
			reason: "Should write CRDs as flat files when --flat is set",
			args: args{
				crds:      []*extv1.CustomResourceDefinition{testCRD},
				flat:      true,
				outputDir: "/out",
			},
			want: want{
				files: []string{"/out/tests.example.org.yaml"},
			},
		},
		"MultipleCRDs": {
			reason: "Should write multiple CRDs organized by group and version",
			args: args{
				crds: []*extv1.CustomResourceDefinition{
					testCRD,
					{
						ObjectMeta: metav1.ObjectMeta{Name: "foos.example.org"},
						Spec: extv1.CustomResourceDefinitionSpec{
							Group: "example.org",
							Names: extv1.CustomResourceDefinitionNames{Kind: "Foo"},
							Versions: []extv1.CustomResourceDefinitionVersion{
								{Name: "v1beta1", Storage: true},
							},
						},
					},
				},
				outputDir: "/out",
			},
			want: want{
				files: []string{
					"/out/example.org/v1alpha1/test.yaml",
					"/out/example.org/v1beta1/foo.yaml",
				},
			},
		},
		"EmptyList": {
			reason: "Should handle empty CRD list gracefully",
			args: args{
				crds:      []*extv1.CustomResourceDefinition{},
				outputDir: "/out",
			},
			want: want{
				files: []string{},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fs := afero.NewMemMapFs()

			buf := &bytes.Buffer{}
			app, err := kong.New(&struct{}{})
			if err != nil {
				t.Fatalf("cannot create kong app: %v", err)
			}
			k, err := app.Parse([]string{})
			if err != nil {
				t.Fatalf("cannot parse kong: %v", err)
			}
			k.Stdout = buf

			c := &getCRDsCmd{
				OutputDir: tc.args.outputDir,
				Flat:      tc.args.flat,
				fs:        fs,
			}

			err = c.writeCRDs(k, tc.args.crds)

			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("%s\nwriteCRDs(...): -want error, +got error:\n%s", tc.reason, diff)
			}

			for _, f := range tc.want.files {
				exists, _ := afero.Exists(fs, f)
				if !exists {
					t.Errorf("%s\nwriteCRDs(...): expected file %s to exist", tc.reason, f)
				}
			}
		})
	}
}

func TestWriteJSONSchemas(t *testing.T) {
	type args struct {
		crds      []*extv1.CustomResourceDefinition
		flat      bool
		outputDir string
	}

	type want struct {
		files []string
		err   error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"Structured": {
			reason: "Should write JSON Schema files organized by group and version",
			args: args{
				crds:      []*extv1.CustomResourceDefinition{testCRD},
				outputDir: "/schemas",
			},
			want: want{
				files: []string{"/schemas/example.org/v1alpha1/test.json"},
			},
		},
		"Flat": {
			reason: "Should write JSON Schema files as flat files when --flat is set",
			args: args{
				crds:      []*extv1.CustomResourceDefinition{testCRD},
				flat:      true,
				outputDir: "/schemas",
			},
			want: want{
				files: []string{"/schemas/example.org_v1alpha1_test.json"},
			},
		},
		"NoSchema": {
			reason: "Should skip versions without OpenAPI schema",
			args: args{
				crds: []*extv1.CustomResourceDefinition{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "nils.example.org"},
						Spec: extv1.CustomResourceDefinitionSpec{
							Group: "example.org",
							Names: extv1.CustomResourceDefinitionNames{Kind: "Nil"},
							Versions: []extv1.CustomResourceDefinitionVersion{
								{Name: "v1", Schema: nil},
							},
						},
					},
				},
				outputDir: "/schemas",
			},
			want: want{
				files: []string{},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fs := afero.NewMemMapFs()

			buf := &bytes.Buffer{}
			app, err := kong.New(&struct{}{})
			if err != nil {
				t.Fatalf("cannot create kong app: %v", err)
			}
			k, err := app.Parse([]string{})
			if err != nil {
				t.Fatalf("cannot parse kong: %v", err)
			}
			k.Stdout = buf

			c := &getCRDsCmd{
				OutputDir: tc.args.outputDir,
				Flat:      tc.args.flat,
				fs:        fs,
			}

			err = c.writeJSONSchemas(k, tc.args.crds)

			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("%s\nwriteJSONSchemas(...): -want error, +got error:\n%s", tc.reason, diff)
			}

			for _, f := range tc.want.files {
				exists, _ := afero.Exists(fs, f)
				if !exists {
					t.Errorf("%s\nwriteJSONSchemas(...): expected file %s to exist", tc.reason, f)
				}

				data, _ := afero.ReadFile(fs, f)
				var schema jsonschema.Schema
				if err := json.Unmarshal(data, &schema); err != nil {
					t.Errorf("%s\nwriteJSONSchemas(...): file %s is not valid JSON Schema: %v", tc.reason, f, err)
				}
			}
		})
	}
}
