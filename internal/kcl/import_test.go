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

package kcl

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestFormatKclImportPath(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		path            string
		existingAliases map[string]bool
		wantImport      string
		wantAlias       string
	}{
		"BasicPath": {
			path:            "kcl/io.upbound.aws.ec2.v1beta1",
			existingAliases: map[string]bool{},
			wantImport:      "models.io.upbound.aws.ec2.v1beta1",
			wantAlias:       "ec2v1beta1",
		},
		"NestedPath": {
			path:            "kcl/io/crossplane/contrib/example/v1alpha1",
			existingAliases: map[string]bool{},
			wantImport:      "models.io.crossplane.contrib.example.v1alpha1",
			wantAlias:       "examplev1alpha1",
		},
		"AliasConflict": {
			path:            "kcl/io/example/platformref/aws/v1alpha1",
			existingAliases: map[string]bool{"awsv1alpha1": true},
			wantImport:      "models.io.example.platformref.aws.v1alpha1",
			wantAlias:       "platformrefawsv1alpha1",
		},
		"PathWithHyphens": {
			path:            "kcl/io/k8s/kube-aggregator/apis/apiregistration/v1",
			existingAliases: map[string]bool{},
			wantImport:      "models.io.k8s.kube_aggregator.apis.apiregistration.v1",
			wantAlias:       "apiregistrationv1",
		},
		"NoKCLPrefix": {
			path:            "python/io/example/aws",
			existingAliases: map[string]bool{},
			wantImport:      "",
			wantAlias:       "",
		},
		"JustKCLPrefix": {
			path:            "kcl/",
			existingAliases: map[string]bool{},
			wantImport:      "",
			wantAlias:       "",
		},
		"TopLevelPath": {
			path:            "kcl/io.example.aws.v1alpha1",
			existingAliases: map[string]bool{},
			wantImport:      "models.io.example.aws.v1alpha1",
			wantAlias:       "awsv1alpha1",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			gotImport, gotAlias := FormatKclImportPath(tc.path, tc.existingAliases)
			if diff := cmp.Diff(tc.wantImport, gotImport); diff != "" {
				t.Errorf("importPath mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantAlias, gotAlias); diff != "" {
				t.Errorf("alias mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
