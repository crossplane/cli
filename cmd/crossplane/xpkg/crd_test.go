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
	"github.com/spf13/afero"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
)

var testCRD = &extv1.CustomResourceDefinition{
	ObjectMeta: metav1.ObjectMeta{
		Name: "tests.example.org",
	},
	Spec: extv1.CustomResourceDefinitionSpec{
		Group: "example.org",
		Names: extv1.CustomResourceDefinitionNames{
			Kind: "Test",
		},
		Versions: []extv1.CustomResourceDefinitionVersion{
			{
				Name: "v1alpha1",
				Schema: &extv1.CustomResourceValidation{
					OpenAPIV3Schema: &extv1.JSONSchemaProps{
						Type: "object",
						Properties: map[string]extv1.JSONSchemaProps{
							"spec": {
								Type: "object",
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

func TestOpenAPIToJSONSchema(t *testing.T) {
	type args struct {
		props   *extv1.JSONSchemaProps
		group   string
		version string
		kind    string
	}

	type want struct {
		schema map[string]any
		err    error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"BasicSchema": {
			reason: "Should convert a basic OpenAPI schema to JSON Schema with correct metadata",
			args: args{
				props: &extv1.JSONSchemaProps{
					Type: "object",
					Properties: map[string]extv1.JSONSchemaProps{
						"replicas": {
							Type: "integer",
						},
					},
				},
				group:   "example.org",
				version: "v1alpha1",
				kind:    "Test",
			},
			want: want{
				schema: map[string]any{
					"$schema": jsonSchemaDraft07,
					"$id":     "example.org/v1alpha1/test.json",
					"type":    "object",
					"properties": map[string]any{
						"replicas": map[string]any{
							"type": "integer",
						},
					},
					"x-kubernetes-group-version-kind": []map[string]string{
						{
							"group":   "example.org",
							"version": "v1alpha1",
							"kind":    "Test",
						},
					},
				},
			},
		},
		"EmptySchema": {
			reason: "Should handle an empty schema with only type",
			args: args{
				props:   &extv1.JSONSchemaProps{Type: "object"},
				group:   "test.io",
				version: "v1",
				kind:    "Foo",
			},
			want: want{
				schema: map[string]any{
					"$schema": jsonSchemaDraft07,
					"$id":     "test.io/v1/foo.json",
					"type":    "object",
					"x-kubernetes-group-version-kind": []map[string]string{
						{
							"group":   "test.io",
							"version": "v1",
							"kind":    "Foo",
						},
					},
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := openAPIToJSONSchema(tc.args.props, tc.args.group, tc.args.version, tc.args.kind)

			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("%s\nopenAPIToJSONSchema(...): -want error, +got error:\n%s", tc.reason, diff)
			}

			// Compare via JSON to normalize types (float64 vs int, etc.)
			wantJSON, _ := json.Marshal(tc.want.schema)
			gotJSON, _ := json.Marshal(got)

			if diff := cmp.Diff(string(wantJSON), string(gotJSON)); diff != "" {
				t.Errorf("%s\nopenAPIToJSONSchema(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestWriteCRDs(t *testing.T) {
	type args struct {
		crds      []*extv1.CustomResourceDefinition
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
		"SingleCRD": {
			reason: "Should write a single CRD as a YAML file",
			args: args{
				crds:      []*extv1.CustomResourceDefinition{testCRD},
				outputDir: "/out",
			},
			want: want{
				files: []string{"/out/tests.example.org.yaml"},
			},
		},
		"MultipleCRDs": {
			reason: "Should write multiple CRDs as separate YAML files",
			args: args{
				crds: []*extv1.CustomResourceDefinition{
					testCRD,
					{
						ObjectMeta: metav1.ObjectMeta{Name: "foos.example.org"},
						Spec: extv1.CustomResourceDefinitionSpec{
							Group: "example.org",
							Names: extv1.CustomResourceDefinitionNames{Kind: "Foo"},
						},
					},
				},
				outputDir: "/out",
			},
			want: want{
				files: []string{
					"/out/tests.example.org.yaml",
					"/out/foos.example.org.yaml",
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
			_ = fs.MkdirAll(tc.args.outputDir, 0o755)

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

			c := &crdCmd{
				OutputDir: tc.args.outputDir,
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
		"SingleVersion": {
			reason: "Should write a JSON Schema file for a single version CRD",
			args: args{
				crds:      []*extv1.CustomResourceDefinition{testCRD},
				outputDir: "/schemas",
			},
			want: want{
				files: []string{"/schemas/example.org/v1alpha1/test.json"},
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

			c := &crdCmd{
				OutputDir: tc.args.outputDir,
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
				var schema map[string]any
				if err := json.Unmarshal(data, &schema); err != nil {
					t.Errorf("%s\nwriteJSONSchemas(...): file %s is not valid JSON: %v", tc.reason, f, err)
				}

				if schema["$schema"] != jsonSchemaDraft07 {
					t.Errorf("%s\nwriteJSONSchemas(...): file %s missing $schema field", tc.reason, f)
				}
			}
		})
	}
}
