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

func TestFormatKclImportPaths(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		paths       []string
		wantImports map[string]string
	}{
		"BasicPath": {
			paths:       []string{"kcl/io.upbound.aws.ec2.v1beta1"},
			wantImports: map[string]string{"ec2v1beta1": "models.io.upbound.aws.ec2.v1beta1"},
		},
		"NestedPath": {
			paths:       []string{"kcl/io/crossplane/contrib/example/v1alpha1"},
			wantImports: map[string]string{"examplev1alpha1": "models.io.crossplane.contrib.example.v1alpha1"},
		},
		"AliasConflict": {
			paths: []string{"kcl/io/example/platformref/aws/v1alpha1", "kcl/io/example/crossplane/aws/v1alpha1"},
			wantImports: map[string]string{
				"awsv1alpha1":           "models.io.example.platformref.aws.v1alpha1",
				"crossplaneawsv1alpha1": "models.io.example.crossplane.aws.v1alpha1",
			},
		},
		"PathWithHyphens": {
			paths:       []string{"kcl/io/k8s/kube-aggregator/apis/apiregistration/v1"},
			wantImports: map[string]string{"apiregistrationv1": "models.io.k8s.kube_aggregator.apis.apiregistration.v1"},
		},
		"NoKCLPrefix": {
			paths:       []string{"python/io/example/aws"},
			wantImports: map[string]string{},
		},
		"JustKCLPrefix": {
			paths:       []string{"kcl/"},
			wantImports: map[string]string{},
		},
		"TopLevelPath": {
			paths:       []string{"kcl/io.example.aws.v1alpha1"},
			wantImports: map[string]string{"awsv1alpha1": "models.io.example.aws.v1alpha1"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			gotImports := FormatKclImportPaths(tc.paths)
			if diff := cmp.Diff(tc.wantImports, gotImports); diff != "" {
				t.Errorf("importPath mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
