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

package v1alpha1

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pkgmetav1 "github.com/crossplane/crossplane/apis/v2/pkg/meta/v1"
)

func TestValidate(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input          *Project
		expectedErrors []string
	}{
		"MinimalValid": {
			input: &Project{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-project",
				},
				Spec: ProjectSpec{
					Repository: "xpkg.upbound.io/acmeco/my-project",
				},
			},
		},
		"MaximalValid": {
			input: &Project{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-project",
				},
				Spec: ProjectSpec{
					Repository: "xpkg.upbound.io/acmeco/my-project",
					ProjectPackageMetadata: ProjectPackageMetadata{
						Maintainer:  "Acme Corporation",
						Source:      "https://github.com/acmeco/my-project.git",
						License:     "Apache-2.0",
						Description: "I'm a unit test",
						Readme:      "Don't use me, I'm a unit test",
					},
					Crossplane: &pkgmetav1.CrossplaneConstraints{
						Version: ">=1.17.0",
					},
					Dependencies: []Dependency{{
						Type: "xpkg",
						Xpkg: &XpkgDependency{
							Package:    "xpkg.upbound.io/upbound/provider-aws-s3",
							Version:    ">=0.2.1",
							APIVersion: "pkg.crossplane.io/v1",
							Kind:       "Provider",
						},
					}},
					Paths: &ProjectPaths{
						APIs:       "apis/",
						Functions:  "functions/",
						Examples:   "examples/",
						Tests:      "tests/",
						Operations: "operations/",
					},
					Architectures: []string{"arch1"},
				},
			},
		},
		"MissingName": {
			input: &Project{
				ObjectMeta: metav1.ObjectMeta{},
				Spec: ProjectSpec{
					Repository: "xpkg.upbound.io/acmeco/my-project",
				},
			},
			expectedErrors: []string{
				"name must not be empty",
			},
		},
		"MissingRepository": {
			input: &Project{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-project",
				},
				Spec: ProjectSpec{},
			},
			expectedErrors: []string{
				"repository must not be empty",
			},
		},
		"AbsolutePaths": {
			input: &Project{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-project",
				},
				Spec: ProjectSpec{
					Repository: "xpkg.upbound.io/acmeco/my-project",
					Paths: &ProjectPaths{
						APIs:       "/tmp/apis",
						Functions:  "/tmp/functions",
						Examples:   "/tmp/examples",
						Tests:      "/tmp/tests",
						Operations: "/tmp/operations",
					},
				},
			},
			expectedErrors: []string{
				"apis path must be relative",
				"functions path must be relative",
				"examples path must be relative",
				"tests path must be relative",
				"operations path must be relative",
			},
		},
		"EmptyArchitectures": {
			input: &Project{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-project",
				},
				Spec: ProjectSpec{
					Repository:    "xpkg.upbound.io/acmeco/my-project",
					Architectures: []string{},
				},
			},
			expectedErrors: []string{
				"architectures must not be empty",
			},
		},
		"ValidAPIDependency": {
			input: &Project{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-project",
				},
				Spec: ProjectSpec{
					Repository: "xpkg.upbound.io/acmeco/my-project",
					Dependencies: []Dependency{
						{
							Type: "crd",
							Git: &GitDependency{
								Repository: "https://github.com/crossplane/crossplane.git",
								Ref:        "v1.14.0",
								Path:       "cluster/crds",
							},
						},
					},
				},
			},
		},
		"InvalidAPIDependencyNoType": {
			input: &Project{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-project",
				},
				Spec: ProjectSpec{
					Repository: "xpkg.upbound.io/acmeco/my-project",
					Dependencies: []Dependency{
						{
							Git: &GitDependency{
								Repository: "https://github.com/crossplane/crossplane.git",
							},
						},
					},
				},
			},
			expectedErrors: []string{
				"dependency 0: type must not be empty",
			},
		},
		"InvalidAPIDependencyNoSource": {
			input: &Project{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-project",
				},
				Spec: ProjectSpec{
					Repository: "xpkg.upbound.io/acmeco/my-project",
					Dependencies: []Dependency{
						{
							Type: "crd",
						},
					},
				},
			},
			expectedErrors: []string{
				"dependency 0: exactly one source (xpkg, git, http, or k8s) must be specified",
			},
		},
		"InvalidAPIDependencyMultipleSources": {
			input: &Project{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-project",
				},
				Spec: ProjectSpec{
					Repository: "xpkg.upbound.io/acmeco/my-project",
					Dependencies: []Dependency{
						{
							Type: "crd",
							Git: &GitDependency{
								Repository: "https://github.com/crossplane/crossplane.git",
							},
							HTTP: &HTTPDependency{
								URL: "https://example.com/api.yaml",
							},
						},
					},
				},
			},
			expectedErrors: []string{
				"dependency 0: exactly one source (xpkg, git, http, or k8s) must be specified",
			},
		},
		"InvalidAPIDependencyGitEmptyRepository": {
			input: &Project{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-project",
				},
				Spec: ProjectSpec{
					Repository: "xpkg.upbound.io/acmeco/my-project",
					Dependencies: []Dependency{
						{
							Type: "crd",
							Git: &GitDependency{
								Repository: "",
							},
						},
					},
				},
			},
			expectedErrors: []string{
				"dependency 0: git: repository must not be empty",
			},
		},
		"InvalidAPIDependencyHTTPEmptyURL": {
			input: &Project{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-project",
				},
				Spec: ProjectSpec{
					Repository: "xpkg.upbound.io/acmeco/my-project",
					Dependencies: []Dependency{
						{
							Type: "crd",
							HTTP: &HTTPDependency{
								URL: "",
							},
						},
					},
				},
			},
			expectedErrors: []string{
				"dependency 0: http: url must not be empty",
			},
		},
		"InvalidAPIDependencyK8sEmptyVersion": {
			input: &Project{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-project",
				},
				Spec: ProjectSpec{
					Repository: "xpkg.upbound.io/acmeco/my-project",
					Dependencies: []Dependency{
						{
							Type: "k8s",
							K8s: &K8sDependency{
								Version: "",
							},
						},
					},
				},
			},
			expectedErrors: []string{
				"dependency 0: k8s: version must not be empty",
			},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := tc.input.Validate()
			if len(tc.expectedErrors) == 0 {
				if err != nil {
					t.Errorf("Validate(): unexpected error: %v", err)
				}
				return
			}
			for _, expected := range tc.expectedErrors {
				if err == nil || !strings.Contains(err.Error(), expected) {
					t.Errorf("Validate(): expected error containing %q, got %v", expected, err)
				}
			}
		})
	}
}

func TestDefault(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input *Project
		want  *Project
	}{
		"FullySpecified": {
			input: &Project{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-project",
				},
				Spec: ProjectSpec{
					Repository: "xpkg.upbound.io/acmeco/my-project",
					ProjectPackageMetadata: ProjectPackageMetadata{
						Maintainer:  "Acme Corporation",
						Source:      "https://github.com/acmeco/my-project.git",
						License:     "Apache-2.0",
						Description: "I'm a unit test",
						Readme:      "Don't use me, I'm a unit test",
					},
					Crossplane: &pkgmetav1.CrossplaneConstraints{
						Version: ">=1.17.0",
					},
					Dependencies: []Dependency{{
						Type: "xpkg",
						Xpkg: &XpkgDependency{
							Package:    "xpkg.upbound.io/upbound/provider-aws-s3",
							Version:    ">=0.2.1",
							APIVersion: "pkg.crossplane.io/v1",
							Kind:       "Provider",
						},
					}},
					Paths: &ProjectPaths{
						APIs:       "not-default-apis/",
						Functions:  "not-default-functions/",
						Examples:   "not-default-examples/",
						Tests:      "not-default-tests/",
						Operations: "not-default-operations/",
					},
					Architectures: []string{"arch1"},
				},
			},
			want: &Project{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-project",
				},
				Spec: ProjectSpec{
					Repository: "xpkg.upbound.io/acmeco/my-project",
					ProjectPackageMetadata: ProjectPackageMetadata{
						Maintainer:  "Acme Corporation",
						Source:      "https://github.com/acmeco/my-project.git",
						License:     "Apache-2.0",
						Description: "I'm a unit test",
						Readme:      "Don't use me, I'm a unit test",
					},
					Crossplane: &pkgmetav1.CrossplaneConstraints{
						Version: ">=1.17.0",
					},
					Dependencies: []Dependency{{
						Type: "xpkg",
						Xpkg: &XpkgDependency{
							Package:    "xpkg.upbound.io/upbound/provider-aws-s3",
							Version:    ">=0.2.1",
							APIVersion: "pkg.crossplane.io/v1",
							Kind:       "Provider",
						},
					}},
					Paths: &ProjectPaths{
						APIs:       "not-default-apis/",
						Functions:  "not-default-functions/",
						Examples:   "not-default-examples/",
						Tests:      "not-default-tests/",
						Operations: "not-default-operations/",
						Schemas:    "schemas",
					},
					Architectures: []string{"arch1"},
				},
			},
		},
		"MinimalValid": {
			input: &Project{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-project",
				},
				Spec: ProjectSpec{
					Repository: "xpkg.upbound.io/acmeco/my-project",
				},
			},
			want: &Project{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-project",
				},
				Spec: ProjectSpec{
					Repository: "xpkg.upbound.io/acmeco/my-project",
					Paths: &ProjectPaths{
						APIs:       "apis",
						Examples:   "examples",
						Functions:  "functions",
						Tests:      "tests",
						Operations: "operations",
						Schemas:    "schemas",
					},
					Architectures: []string{"amd64", "arm64"},
				},
			},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tc.input.Default()
			if diff := cmp.Diff(tc.want, tc.input); diff != "" {
				t.Errorf("Default(): -want, +got:\n%s", diff)
			}
		})
	}
}
