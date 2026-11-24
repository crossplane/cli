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

package simulate

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	un "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func testComp(name, kind string, labels map[string]string) *un.Unstructured {
	c := &un.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.crossplane.io/v1",
		"kind":       "Composition",
		"metadata":   map[string]any{"name": name},
		"spec": map[string]any{"compositeTypeRef": map[string]any{
			"apiVersion": "example.org/v1", "kind": kind,
		}},
	}}
	c.SetLabels(labels)
	return c
}

func testInput(kind, name string, spec map[string]any) *un.Unstructured {
	return &un.Unstructured{Object: map[string]any{
		"apiVersion": "example.org/v1", "kind": kind,
		"metadata": map[string]any{"name": name},
		"spec":     spec,
	}}
}

func TestAssignComposites(t *testing.T) {
	xr := schema.GroupVersionKind{Group: "example.org", Version: "v1", Kind: "XBucket"}
	// Claims of kind Bucket compose XBucket; XBucket inputs compose themselves.
	xrdFor := func(def string) func(*un.Unstructured) (xrdInfo, error) {
		return func(in *un.Unstructured) (xrdInfo, error) {
			if in.GetKind() == "Other" {
				return xrdInfo{}, errors.New("boom")
			}
			return xrdInfo{CompositeGVK: xr, DefaultComposition: def}, nil
		}
	}
	a := testComp("a", "XBucket", map[string]string{"tier": "gold"})
	b := testComp("b", "XBucket", map[string]string{"tier": "silver"})
	other := testComp("o", "XOther", map[string]string{"tier": "gold"})

	type args struct {
		comps  []*un.Unstructured
		input  *un.Unstructured
		defalt string
	}
	type want struct {
		comp string
		err  string
	}
	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"V2CompositionRef": {
			reason: "A v2 XR's spec.crossplane.compositionRef picks the Composition.",
			args:   args{comps: []*un.Unstructured{a, b}, input: testInput("XBucket", "x", map[string]any{"crossplane": map[string]any{"compositionRef": map[string]any{"name": "b"}}})},
			want:   want{comp: "b"},
		},
		"V1ClaimCompositionRef": {
			reason: "A claim's spec.compositionRef picks the Composition.",
			args:   args{comps: []*un.Unstructured{a, b}, input: testInput("Bucket", "c", map[string]any{"compositionRef": map[string]any{"name": "a"}})},
			want:   want{comp: "a"},
		},
		"CompositionRefNotInProject": {
			reason: "A compositionRef to a Composition the project doesn't build, or of another type, fails.",
			args:   args{comps: []*un.Unstructured{a, other}, input: testInput("XBucket", "x", map[string]any{"compositionRef": map[string]any{"name": "o"}})},
			want:   want{err: `-f inputs include XBucket "x" (example.org/v1), whose compositionRef "o" is not a project Composition composing XBucket`},
		},
		"CompositionRefMalformed": {
			reason: "A compositionRef name of the wrong type fails rather than being ignored.",
			args:   args{comps: []*un.Unstructured{a}, input: testInput("XBucket", "x", map[string]any{"compositionRef": map[string]any{"name": 3}})},
			want:   want{err: `-f inputs include XBucket "x" (example.org/v1), whose spec.compositionRef.name is malformed: .spec.compositionRef.name accessor error: 3 is of the type int, expected string`},
		},
		"V2Selector": {
			reason: "A v2 compositionSelector matches labels among type-matching Compositions.",
			args:   args{comps: []*un.Unstructured{other, a, b}, input: testInput("XBucket", "x", map[string]any{"crossplane": map[string]any{"compositionSelector": map[string]any{"matchLabels": map[string]any{"tier": "gold"}}}})},
			want:   want{comp: "a"},
		},
		"V1ClaimSelector": {
			reason: "A claim's spec.compositionSelector matches labels.",
			args:   args{comps: []*un.Unstructured{a, b}, input: testInput("Bucket", "c", map[string]any{"compositionSelector": map[string]any{"matchLabels": map[string]any{"tier": "silver"}}})},
			want:   want{comp: "b"},
		},
		"SelectorNoMatch": {
			reason: "A selector matching nothing fails, even if the XRD has a default.",
			args:   args{comps: []*un.Unstructured{a, b}, defalt: "a", input: testInput("XBucket", "x", map[string]any{"compositionSelector": map[string]any{"matchLabels": map[string]any{"tier": "bronze"}}})},
			want:   want{err: `-f inputs include XBucket "x" (example.org/v1), whose compositionSelector matches no project Composition composing XBucket`},
		},
		"SelectorAmbiguous": {
			reason: "A selector matching several Compositions fails naming them.",
			args:   args{comps: []*un.Unstructured{b, a}, input: testInput("XBucket", "x", map[string]any{"compositionSelector": map[string]any{"matchLabels": map[string]any{}}})},
			want:   want{comp: "", err: `-f inputs include XBucket "x" (example.org/v1), which matches several project Compositions (a, b); set its compositionRef to pick one`},
		},
		"DefaultCompositionRef": {
			reason: "With no ref or selector, the XRD's defaultCompositionRef picks.",
			args:   args{comps: []*un.Unstructured{a, b}, defalt: "b", input: testInput("Bucket", "c", map[string]any{})},
			want:   want{comp: "b"},
		},
		"DefaultNotInProject": {
			reason: "A defaultCompositionRef the project doesn't build fails.",
			args:   args{comps: []*un.Unstructured{a}, defalt: "z", input: testInput("XBucket", "x", map[string]any{})},
			want:   want{err: `-f inputs include XBucket "x" (example.org/v1), whose XRD defaultCompositionRef "z" is not a project Composition composing XBucket`},
		},
		"SoleTypeMatch": {
			reason: "Otherwise the only Composition of the composite type picks; a claim matches via its XRD's composite.",
			args:   args{comps: []*un.Unstructured{other, a}, input: testInput("Bucket", "c", nil)},
			want:   want{comp: "a"},
		},
		"NoTypeMatch": {
			reason: "No Composition of the type fails naming the input.",
			args:   args{comps: []*un.Unstructured{other}, input: testInput("XBucket", "x", nil)},
			want:   want{err: `-f inputs include XBucket "x" (example.org/v1), but no project Composition composes its type example.org/v1, Kind=XBucket`},
		},
		"TypeAmbiguous": {
			reason: "Several Compositions of the type fail asking for compositionRef.",
			args:   args{comps: []*un.Unstructured{a, b}, input: testInput("XBucket", "x", nil)},
			want:   want{err: `-f inputs include XBucket "x" (example.org/v1), which matches several project Compositions (a, b); set its compositionRef to pick one`},
		},
		"LookupError": {
			reason: "An XRD lookup error is returned.",
			args:   args{comps: []*un.Unstructured{a}, input: testInput("Other", "x", nil)},
			want:   want{err: "boom"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := assignComposites(tc.args.comps, []*un.Unstructured{tc.args.input}, xrdFor(tc.args.defalt))
			gotErr := ""
			if err != nil {
				gotErr = err.Error()
			}
			if diff := cmp.Diff(tc.want.err, gotErr); diff != "" {
				t.Errorf("\n%s\nassignComposites(...): -want err, +got err:\n%s", tc.reason, diff)
			}
			var want map[string][]*un.Unstructured
			if tc.want.err == "" {
				want = map[string][]*un.Unstructured{tc.want.comp: {tc.args.input}}
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("\n%s\nassignComposites(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}
