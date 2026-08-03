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

package generator

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	devv1alpha1 "github.com/crossplane/cli/v2/apis/dev/v1alpha1"
)

func TestAllLanguagesMatchesAPI(t *testing.T) {
	t.Parallel()

	// The generators returned by AllLanguages must cover exactly the set
	// of language identifiers declared in the API package. If this test
	// fails the two are out of sync; update one to match the other.
	got := make([]string, 0, len(AllLanguages()))
	for _, g := range AllLanguages() {
		got = append(got, g.Language())
	}
	if diff := cmp.Diff(devv1alpha1.SupportedSchemaLanguages(), got); diff != "" {
		t.Errorf("AllLanguages() languages: -want (from API), +got (from generators):\n%s", diff)
	}
}

func TestFilter(t *testing.T) {
	t.Parallel()

	all := AllLanguages()

	tcs := map[string]struct {
		langs []string
		want  []string
	}{
		"Empty": {
			// An empty filter returns the default languages (excluding TypeScript,
			// which requires explicit opt-in due to its Node.js dependency).
			want: []string{
				devv1alpha1.SchemaLanguageGo,
				devv1alpha1.SchemaLanguageJSON,
				devv1alpha1.SchemaLanguageKCL,
				devv1alpha1.SchemaLanguagePython,
			},
		},
		"SingleLanguage": {
			langs: []string{devv1alpha1.SchemaLanguagePython},
			want:  []string{devv1alpha1.SchemaLanguagePython},
		},
		"PreservesAllLanguagesOrder": {
			// Filter preserves the order of AllLanguages, not the order
			// of the input list.
			langs: []string{devv1alpha1.SchemaLanguagePython, devv1alpha1.SchemaLanguageGo},
			want:  []string{devv1alpha1.SchemaLanguageGo, devv1alpha1.SchemaLanguagePython},
		},
		"UnknownLanguageIgnored": {
			// Filter is permissive; validation happens elsewhere.
			langs: []string{devv1alpha1.SchemaLanguagePython, "fortran"},
			want:  []string{devv1alpha1.SchemaLanguagePython},
		},
		"AllLanguages": {
			langs: devv1alpha1.SupportedSchemaLanguages(),
			want:  devv1alpha1.SupportedSchemaLanguages(),
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := Filter(all, tc.langs)
			gotLangs := make([]string, len(got))
			for i, g := range got {
				gotLangs[i] = g.Language()
			}
			if diff := cmp.Diff(tc.want, gotLangs); diff != "" {
				t.Errorf("Filter(...): -want, +got:\n%s", diff)
			}
		})
	}
}
