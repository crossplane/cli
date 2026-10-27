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

package plan

import (
	"bytes"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestFieldChangePaths(t *testing.T) {
	cases := map[string]struct {
		planned map[string]any
		path    string
		levels  []string
	}{
		"Plain":               {planned: map[string]any{"spec": map[string]any{"region": "x"}}, path: "spec.region", levels: []string{"spec", "region"}},
		"Dots":                {planned: map[string]any{"metadata": map[string]any{"annotations": map[string]any{"crossplane.io/composite": "x"}}}, path: "metadata.annotations[crossplane.io/composite]", levels: []string{"metadata", "annotations", "crossplane.io/composite"}},
		"Digits":              {planned: map[string]any{"data": map[string]any{"0": "x"}}, path: "data.0", levels: []string{"data", "0"}},
		"Quoted":              {planned: map[string]any{"data": map[string]any{`"q"`: "x"}}, path: `data."q"`, levels: []string{"data", "q"}},
		"Slash":               {planned: map[string]any{"data": map[string]any{"a/b": "x"}}, path: "data.a/b", levels: []string{"data", "a/b"}},
		"FirstKey":            {planned: map[string]any{"a.b": map[string]any{"c": "x"}}, path: "[a.b].c", levels: []string{"a.b", "c"}},
		"ListIndex":           {planned: map[string]any{"spec": map[string]any{"pipeline": []any{map[string]any{"step": "x"}}}}, path: "spec.pipeline[0].step", levels: []string{"spec", "pipeline[0]", "step"}},
		"IndexAfterDottedKey": {planned: map[string]any{"spec": map[string]any{"a.b": []any{"x"}}}, path: "spec[a.b][0]", levels: []string{"spec", "a.b[0]"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			changes := FieldChanges(nil, tc.planned)
			if len(changes) != 1 {
				t.Fatalf("FieldChanges: got %d changes, want 1: %+v", len(changes), changes)
			}
			if changes[0].Path != tc.path {
				t.Errorf("Path: got %q, want %q", changes[0].Path, tc.path)
			}
			if diff := cmp.Diff(tc.levels, DisplayLevels(changes[0].Path)); diff != "" {
				t.Errorf("DisplayLevels(%q): -want, +got:\n%s", changes[0].Path, diff)
			}
		})
	}
}

func TestDisplayLevelsMalformed(t *testing.T) {
	want := []string{"spec.a[b"}
	if diff := cmp.Diff(want, DisplayLevels("spec.a[b")); diff != "" {
		t.Errorf("-want, +got:\n%s", diff)
	}
}

func TestRenderChangeTreeDottedKey(t *testing.T) {
	changes := FieldChanges(nil, map[string]any{
		"metadata": map[string]any{"annotations": map[string]any{"crossplane.io/composite": "my-xr"}},
	})
	var buf bytes.Buffer
	renderChangeTree(&buf, changes)
	want := "    metadata:\n      annotations:\n        + crossplane.io/composite: \"my-xr\"\n"
	if diff := cmp.Diff(want, buf.String()); diff != "" {
		t.Errorf("-want, +got:\n%s", diff)
	}
}
