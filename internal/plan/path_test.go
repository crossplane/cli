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

func TestPathRoundTrip(t *testing.T) {
	cases := map[string]struct {
		keys []string
		idx  string // appended after the last key, e.g. "[0]"
		path string
		segs []string
	}{
		"Plain":     {keys: []string{"spec", "forProvider", "region"}, path: "spec.forProvider.region", segs: []string{"spec", "forProvider", "region"}},
		"Dots":      {keys: []string{"metadata", "annotations", "crossplane.io/composite"}, path: `metadata.annotations["crossplane.io/composite"]`, segs: []string{"metadata", "annotations", "crossplane.io/composite"}},
		"Quotes":    {keys: []string{"data", `a"b`}, path: `data["a\"b"]`, segs: []string{"data", `a"b`}},
		"Brackets":  {keys: []string{"data", "a[0]"}, path: `data["a[0]"]`, segs: []string{"data", "a[0]"}},
		"Digit":     {keys: []string{"data", "1st"}, path: `data["1st"]`, segs: []string{"data", "1st"}},
		"FirstKey":  {keys: []string{"a.b", "c"}, path: `["a.b"].c`, segs: []string{"a.b", "c"}},
		"ListIndex": {keys: []string{"spec", "pipeline"}, idx: "[0]", path: "spec.pipeline[0]", segs: []string{"spec", "pipeline[0]"}},
		"Nested":    {keys: []string{"spec", "tags", "k8s.io/x", "y_z-1"}, path: `spec.tags["k8s.io/x"].y_z-1`, segs: []string{"spec", "tags", "k8s.io/x", "y_z-1"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p := ""
			for _, k := range tc.keys {
				p = JoinKey(p, k)
			}
			p += tc.idx
			if p != tc.path {
				t.Errorf("JoinKey: got %q, want %q", p, tc.path)
			}
			if diff := cmp.Diff(tc.segs, PathSegments(p)); diff != "" {
				t.Errorf("PathSegments(%q): -want, +got:\n%s", p, diff)
			}
		})
	}
}

func TestPathSegmentsIndexAfterQuotedKey(t *testing.T) {
	want := []string{"spec", "a.b[2]", "c"}
	if diff := cmp.Diff(want, PathSegments(`spec["a.b"][2].c`)); diff != "" {
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
