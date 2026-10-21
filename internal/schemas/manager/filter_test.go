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

package manager

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/spf13/afero"
)

const (
	storageXRD = `apiVersion: apiextensions.crossplane.io/v2
kind: CompositeResourceDefinition
spec:
  group: storage.example.org
`
	networkXRD = `apiVersion: apiextensions.crossplane.io/v2
kind: CompositeResourceDefinition
spec:
  group: network.example.org
`
)

func TestFilterAPIGroups(t *testing.T) {
	t.Parallel()

	type args struct {
		files      map[string]string
		sourceType SourceType
		globs      []string
	}
	type want struct {
		files map[string]string
		err   error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"NoGlobsKeepsEverything": {
			reason: "Without globs nothing is filtered.",
			args: args{
				files: map[string]string{"a.yaml": storageXRD, "b.yaml": networkXRD},
				globs: nil,
			},
			want: want{files: map[string]string{"a.yaml": storageXRD, "b.yaml": networkXRD}},
		},
		"MatchingFilesAreKept": {
			reason: "A file whose group matches a glob is kept byte for byte; others are dropped.",
			args: args{
				files:      map[string]string{"a.yaml": storageXRD, "b.yaml": networkXRD},
				sourceType: SourceTypeCRD,
				globs:      []string{"storage.*"},
			},
			want: want{files: map[string]string{"a.yaml": storageXRD}},
		},
		"AnyGlobMatches": {
			reason: "Globs are alternatives.",
			args: args{
				files:      map[string]string{"a.yaml": storageXRD, "b.yaml": networkXRD},
				sourceType: SourceTypeCRD,
				globs:      []string{"nomatch", "*.example.org"},
			},
			want: want{files: map[string]string{"a.yaml": storageXRD, "b.yaml": networkXRD}},
		},
		"MultiDocumentFileIsSplit": {
			reason: "A file bundling several groups keeps only the matching documents.",
			args: args{
				files:      map[string]string{"all.yaml": storageXRD + "---\n" + networkXRD},
				sourceType: SourceTypeCRD,
				globs:      []string{"network.example.org"},
			},
			want: want{files: map[string]string{"all.yaml": networkXRD}},
		},
		"NonResourceDocumentsAreKept": {
			reason: "Files without a spec.group, such as a kustomization, are not API resources and are not filtered.",
			args: args{
				files:      map[string]string{"kustomization.yaml": "resources: []\n", "a.yaml": networkXRD},
				sourceType: SourceTypeCRD,
				globs:      []string{"storage.*"},
			},
			want: want{files: map[string]string{"kustomization.yaml": "resources: []\n"}},
		},
		"OpenAPIFilteredByFileNameGroup": {
			reason: "Kubernetes OpenAPI specs are filtered by the group in the file name; the core group spec is always kept.",
			args: args{
				files: map[string]string{
					"api__v1_openapi.json":                     "{}",
					"apis__apps__v1_openapi.json":              "{}",
					"apis__networking.k8s.io__v1_openapi.json": "{}",
				},
				sourceType: SourceTypeOpenAPI,
				globs:      []string{"apps"},
			},
			want: want{files: map[string]string{
				"api__v1_openapi.json":        "{}",
				"apis__apps__v1_openapi.json": "{}",
			}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			from := afero.NewMemMapFs()
			for p, c := range tc.args.files {
				if err := afero.WriteFile(from, p, []byte(c), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			got, err := filterAPIGroups(from, tc.args.sourceType, tc.args.globs)
			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("%s\nfilterAPIGroups(...): -want err, +got err:\n%s", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.files, readFiles(t, got)); diff != "" {
				t.Errorf("%s\nfilterAPIGroups(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestEachYAMLDocument(t *testing.T) {
	t.Parallel()

	type args struct {
		content string
	}
	type want struct {
		docs []string
		err  error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"Empty": {
			reason: "An empty stream has no documents.",
			args:   args{content: ""},
			want:   want{},
		},
		"Single": {
			reason: "A single document is passed as is.",
			args:   args{content: "a: 1\n"},
			want:   want{docs: []string{"a: 1\n"}},
		},
		"Multiple": {
			reason: "Documents are passed in order, without separators.",
			args:   args{content: "a: 1\n---\nb: 2\n"},
			want:   want{docs: []string{"a: 1\n", "b: 2\n"}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var got want
			err := eachYAMLDocument([]byte(tc.args.content), func(doc []byte) error {
				got.docs = append(got.docs, string(doc))
				return nil
			})
			got.err = err
			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(want{}), cmpopts.EquateErrors()); diff != "" {
				t.Errorf("%s\neachYAMLDocument(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}
