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

func TestAllLanguagesGoOptions(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		reason             string
		opts               []Option
		wantRuntimeObjects bool
		wantAccessors      bool
	}{
		"OffByDefault": {
			reason: "both Go generator features are opt-in",
		},
		"Disabled": {
			reason: "explicitly disabled flags leave the Go generator alone",
			opts:   []Option{WithGoRuntimeObjects(false), WithGoModelAccessors(false)},
		},
		"RuntimeObjectsOnly": {
			reason:             "the option reaches the Go generator, which is what emits the code",
			opts:               []Option{WithGoRuntimeObjects(true)},
			wantRuntimeObjects: true,
		},
		"AccessorsOnly": {
			reason:        "the two options are independent",
			opts:          []Option{WithGoModelAccessors(true)},
			wantAccessors: true,
		},
		"Both": {
			reason:             "neither option clobbers the other",
			opts:               []Option{WithGoModelAccessors(true), WithGoRuntimeObjects(true)},
			wantRuntimeObjects: true,
			wantAccessors:      true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var got *goGenerator
			for _, g := range AllLanguages(tc.opts...) {
				if gg, ok := g.(*goGenerator); ok {
					got = gg
				}
			}
			if got == nil {
				t.Fatal("AllLanguages did not return a Go generator")
			}
			if got.runtimeObjects != tc.wantRuntimeObjects {
				t.Errorf("runtimeObjects = %v, want %v (%s)", got.runtimeObjects, tc.wantRuntimeObjects, tc.reason)
			}
			if got.accessors != tc.wantAccessors {
				t.Errorf("accessors = %v, want %v (%s)", got.accessors, tc.wantAccessors, tc.reason)
			}
		})
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
			// An empty filter returns all languages unchanged.
			want: devv1alpha1.SupportedSchemaLanguages(),
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
