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

package result

import "testing"

func TestXRDiffGroupHasChanges(t *testing.T) {
	cases := map[string]struct {
		diffs map[string]*ResourceDiff
		want  bool
	}{
		"Empty":     {want: false},
		"EqualOnly": {diffs: map[string]*ResourceDiff{"a": {DiffType: DiffTypeEqual}}, want: false},
		"Added":     {diffs: map[string]*ResourceDiff{"a": {DiffType: DiffTypeEqual}, "b": {DiffType: DiffTypeAdded}}, want: true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			g := XRDiffGroup{Diffs: tc.diffs}
			if got := g.HasChanges(); got != tc.want {
				t.Errorf("HasChanges(): want %t, got %t", tc.want, got)
			}
			if got := (&XRReport{Groups: []XRDiffGroup{g}}).HasChanges(); got != tc.want {
				t.Errorf("XRReport.HasChanges(): want %t, got %t", tc.want, got)
			}
		})
	}
}
