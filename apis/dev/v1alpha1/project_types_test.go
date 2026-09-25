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
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestProjectSchemasGetLanguages(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		reason  string
		schemas *ProjectSchemas
		want    []string
	}{
		"NilReceiver": {
			reason:  "an absent Schemas config defaults to every language except TypeScript",
			schemas: nil,
			want:    DefaultSchemaLanguages(),
		},
		"UnspecifiedLanguages": {
			reason:  "a Schemas config with no Languages defaults the same as a nil receiver",
			schemas: &ProjectSchemas{},
			want:    DefaultSchemaLanguages(),
		},
		"ExplicitLanguages": {
			reason:  "an explicit list is returned unchanged",
			schemas: &ProjectSchemas{Languages: []string{SchemaLanguagePython}},
			want:    []string{SchemaLanguagePython},
		},
		"ExplicitTypeScript": {
			reason:  "TypeScript is only included when named explicitly",
			schemas: &ProjectSchemas{Languages: []string{SchemaLanguageTypeScript}},
			want:    []string{SchemaLanguageTypeScript},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := tc.schemas.GetLanguages()
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("GetLanguages(): -want, +got:\n%s\n%s", diff, tc.reason)
			}
		})
	}
}

func TestDefaultSchemaLanguagesExcludesTypeScript(t *testing.T) {
	t.Parallel()

	for _, lang := range DefaultSchemaLanguages() {
		if lang == SchemaLanguageTypeScript {
			t.Errorf("DefaultSchemaLanguages() includes %q; TypeScript generation starts a Docker container and must be opt-in", SchemaLanguageTypeScript)
		}
	}
}
