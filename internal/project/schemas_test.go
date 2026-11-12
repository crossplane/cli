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

package project

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	devv1alpha1 "github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/internal/config"
)

func TestSchemaGenerators(t *testing.T) {
	type args struct {
		proj *devv1alpha1.Project
	}
	type want struct {
		languages []string
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"ConfiguredLanguages": {
			reason: "Should only return generators for the languages configured in the project",
			args: args{
				proj: &devv1alpha1.Project{
					Spec: devv1alpha1.ProjectSpec{
						Schemas: &devv1alpha1.ProjectSchemas{
							Languages: []string{devv1alpha1.SchemaLanguageGo},
						},
					},
				},
			},
			want: want{
				languages: []string{devv1alpha1.SchemaLanguageGo},
			},
		},
		"NoLanguagesConfigured": {
			reason: "Should return generators for all supported languages when the project does not configure any",
			args: args{
				proj: &devv1alpha1.Project{},
			},
			want: want{
				languages: devv1alpha1.SupportedSchemaLanguages(),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := SchemaGenerators(tc.args.proj, &config.Config{})

			languages := make([]string, 0, len(got))
			for _, g := range got {
				languages = append(languages, g.Language())
			}
			if diff := cmp.Diff(tc.want.languages, languages); diff != "" {
				t.Errorf("\n%s\nSchemaGenerators(...): -want languages, +got languages:\n%s", tc.reason, diff)
			}
		})
	}
}
