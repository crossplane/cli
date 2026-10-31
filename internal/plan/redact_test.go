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
	"testing"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestRedactPaths(t *testing.T) {
	const s, c = "(sensitive value)", ChangedSensitive
	spec := func(m map[string]any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{"spec": m}}
	}
	tags := func(kv ...string) []any {
		var out []any
		for i := 0; i < len(kv); i += 2 {
			out = append(out, map[string]any{"key": kv[i], "value": kv[i+1]})
		}
		return out
	}

	type args struct {
		current, desired *unstructured.Unstructured
		paths            []string
	}
	type want struct {
		current, desired *unstructured.Unstructured
		err              bool
	}
	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"Changed": {
			reason: "A changed value is the plain marker on the old side and the changed marker on the new.",
			args:   args{spec(map[string]any{"pw": "a", "x": int64(1)}), spec(map[string]any{"pw": "b", "x": int64(1)}), []string{"spec.pw"}},
			want:   want{current: spec(map[string]any{"pw": s, "x": int64(1)}), desired: spec(map[string]any{"pw": c, "x": int64(1)})},
		},
		"Equal": {
			reason: "An unchanged value is the plain marker on both sides.",
			args:   args{spec(map[string]any{"pw": "a"}), spec(map[string]any{"pw": "a"}), []string{"spec.pw"}},
			want:   want{current: spec(map[string]any{"pw": s}), desired: spec(map[string]any{"pw": s})},
		},
		"Added": {
			reason: "With no old value the new side is the plain marker.",
			args:   args{nil, spec(map[string]any{"pw": "b"}), []string{"spec.pw"}},
			want:   want{desired: spec(map[string]any{"pw": s})},
		},
		"AddedField": {
			reason: "A field absent from the old side leaves it alone.",
			args:   args{spec(map[string]any{}), spec(map[string]any{"pw": "b"}), []string{"spec.pw"}},
			want:   want{current: spec(map[string]any{}), desired: spec(map[string]any{"pw": s})},
		},
		"Absent": {
			reason: "A path absent from both sides changes nothing.",
			args:   args{spec(map[string]any{"a": int64(1)}), spec(map[string]any{"a": int64(1)}), []string{"spec.pw", "spec.l[3]"}},
			want:   want{current: spec(map[string]any{"a": int64(1)}), desired: spec(map[string]any{"a": int64(1)})},
		},
		"Subtree": {
			reason: "A located map is replaced whole.",
			args:   args{spec(map[string]any{"pw": map[string]any{"a": "1"}}), spec(map[string]any{"pw": map[string]any{"a": "2"}}), []string{"spec.pw"}},
			want:   want{current: spec(map[string]any{"pw": s}), desired: spec(map[string]any{"pw": c})},
		},
		"Keyed": {
			reason: "A keyed item is located by its key, with quotes trimmed.",
			args:   args{spec(map[string]any{"tags": tags("a", "1", "b", "2")}), spec(map[string]any{"tags": tags("a", "1", "b", "3")}), []string{`spec.tags[key="b"].value`}},
			want: want{
				current: spec(map[string]any{"tags": []any{map[string]any{"key": "a", "value": "1"}, map[string]any{"key": "b", "value": s}}}),
				desired: spec(map[string]any{"tags": []any{map[string]any{"key": "a", "value": "1"}, map[string]any{"key": "b", "value": c}}}),
			},
		},
		"MultiMatch": {
			reason: "Every keyed match is redacted, paired by order.",
			args:   args{nil, spec(map[string]any{"tags": tags("b", "1", "b", "2")}), []string{`spec.tags[key=b].value`}},
			want:   want{desired: spec(map[string]any{"tags": []any{map[string]any{"key": "b", "value": s}, map[string]any{"key": "b", "value": s}}})},
		},
		"Index": {
			reason: "A whole-item index path redacts every item of the list.",
			args:   args{nil, spec(map[string]any{"l": []any{"x", "y"}}), []string{"spec.l[1]"}},
			want:   want{desired: spec(map[string]any{"l": []any{s, s}})},
		},
		"IndexHash": {
			reason: "An index past the end, as upjet writes a set hash, still redacts every item.",
			args: args{
				spec(map[string]any{"rule": []any{map[string]any{"name": "a", "pw": "OLD"}}}),
				spec(map[string]any{"rule": []any{map[string]any{"name": "a", "pw": "NEW"}}}),
				[]string{"spec.rule[2876543210].pw"},
			},
			want: want{
				current: spec(map[string]any{"rule": []any{map[string]any{"name": "a", "pw": s}}}),
				desired: spec(map[string]any{"rule": []any{map[string]any{"name": "a", "pw": c}}}),
			},
		},
		"IndexSmallHash": {
			reason: "An index naming the wrong item still redacts the rest of the path in every item.",
			args: args{
				spec(map[string]any{"rule": []any{map[string]any{"pw": "a"}, map[string]any{"pw": "OLD"}}}),
				spec(map[string]any{"rule": []any{map[string]any{"pw": "a"}, map[string]any{"pw": "NEW"}}}),
				[]string{"spec.rule[0].pw"},
			},
			want: want{
				current: spec(map[string]any{"rule": []any{map[string]any{"pw": s}, map[string]any{"pw": s}}}),
				desired: spec(map[string]any{"rule": []any{map[string]any{"pw": s}, map[string]any{"pw": c}}}),
			},
		},
		"IndexItemsPairedByPosition": {
			reason: "Items are paired by position; an item lacking the rest of the path is absent, and an added item is plain.",
			args: args{
				spec(map[string]any{"l": []any{map[string]any{"pw": "a"}, map[string]any{"x": int64(1)}}}),
				spec(map[string]any{"l": []any{map[string]any{"pw": "b"}, map[string]any{"pw": "c"}, map[string]any{"pw": "d"}}}),
				[]string{"spec.l[1].pw"},
			},
			want: want{
				current: spec(map[string]any{"l": []any{map[string]any{"pw": s}, map[string]any{"x": int64(1)}}}),
				desired: spec(map[string]any{"l": []any{map[string]any{"pw": c}, map[string]any{"pw": s}, map[string]any{"pw": s}}}),
			},
		},
		"QuoteTrimmedKey": {
			reason: "A key written with quotes is matched by the path without them.",
			args:   args{spec(map[string]any{"'pw'": "a"}), spec(map[string]any{"'pw'": "b"}), []string{"spec['pw']"}},
			want:   want{current: spec(map[string]any{"'pw'": s}), desired: spec(map[string]any{"'pw'": c})},
		},
		"AmbiguousKey": {
			reason: "Two keys matching one field fail closed.",
			args:   args{nil, spec(map[string]any{"pw": "a", "'pw'": "b"}), []string{"spec.pw"}},
			want:   want{err: true},
		},
		"ScalarAncestor": {
			reason: "A path under a scalar replaces the scalar.",
			args:   args{spec(map[string]any{"doc": `{"pw":"a"}`}), spec(map[string]any{"doc": `{"pw":"b"}`}), []string{"spec.doc.pw"}},
			want:   want{current: spec(map[string]any{"doc": s}), desired: spec(map[string]any{"doc": c})},
		},
		"IndexOnMap": {
			reason: "An index on a map fails closed.",
			args:   args{spec(map[string]any{"m": map[string]any{"a": int64(1)}}), nil, []string{"spec.m[0]"}},
			want:   want{err: true},
		},
		"FieldOnList": {
			reason: "A plain field on a list fails closed.",
			args:   args{nil, spec(map[string]any{"l": []any{int64(1)}}), []string{"spec.l.a"}},
			want:   want{err: true},
		},
		"KeyedOnMap": {
			reason: "A keyed item on a map fails closed.",
			args:   args{spec(map[string]any{"tags": map[string]any{"b": "a"}}), spec(map[string]any{"tags": map[string]any{"b": "b"}}), []string{`spec.tags[key="b"]`}},
			want:   want{err: true},
		},
		"KeyedOnScalarList": {
			reason: "A keyed item on a list of non-maps fails closed.",
			args:   args{spec(map[string]any{"tags": []any{"a"}}), spec(map[string]any{"tags": []any{"b"}}), []string{`spec.tags[key="b"]`}},
			want:   want{err: true},
		},
		"OneSideMismatch": {
			reason: "A side that resolves differently is replaced where it stopped; the other is changed.",
			args:   args{spec(map[string]any{"pw": map[string]any{"a": int64(1)}}), spec(map[string]any{"pw": "x"}), []string{"spec.pw.a"}},
			want:   want{current: spec(map[string]any{"pw": map[string]any{"a": s}}), desired: spec(map[string]any{"pw": c})},
		},
		"Root": {
			reason: "A path stopping at the root fails closed.",
			args:   args{nil, spec(map[string]any{"pw": "a"}), []string{"[0]"}},
			want:   want{err: true},
		},
		"Unparseable": {
			reason: "A path that doesn't parse fails closed.",
			args:   args{nil, spec(map[string]any{"pw": "a"}), []string{"spec[pw"}},
			want:   want{err: true},
		},
		"DottedKeyPresent": {
			reason: "A map key holding dots is matched by the joined segments.",
			args: args{
				spec(map[string]any{"tags": map[string]any{"kubernetes.io/cluster/x": "a"}}),
				spec(map[string]any{"tags": map[string]any{"kubernetes.io/cluster/x": "b"}}),
				[]string{"spec.tags.kubernetes.io/cluster/x"},
			},
			want: want{
				current: spec(map[string]any{"tags": map[string]any{"kubernetes.io/cluster/x": s}}),
				desired: spec(map[string]any{"tags": map[string]any{"kubernetes.io/cluster/x": c}}),
			},
		},
		"DottedKeyOneSide": {
			reason: "A dotted key on one side is located there; the other side lacks it.",
			args: args{
				spec(map[string]any{"tags": map[string]any{}}),
				spec(map[string]any{"tags": map[string]any{"a.b": "v"}}),
				[]string{"spec.tags.a.b"},
			},
			want: want{current: spec(map[string]any{"tags": map[string]any{}}), desired: spec(map[string]any{"tags": map[string]any{"a.b": s}})},
		},
		"DottedOldNestedNew": {
			reason: "Each side is located on its own: a dotted key on one, nesting on the other.",
			args: args{
				spec(map[string]any{"tags": map[string]any{"a.b": "a"}}),
				spec(map[string]any{"tags": map[string]any{"a": map[string]any{"b": "b"}}}),
				[]string{"spec.tags.a.b"},
			},
			want: want{
				current: spec(map[string]any{"tags": map[string]any{"a.b": s}}),
				desired: spec(map[string]any{"tags": map[string]any{"a": map[string]any{"b": c}}}),
			},
		},
		"DottedAndNestedSameSide": {
			reason: "A dotted key and nesting both matching on one side fail closed.",
			args:   args{nil, spec(map[string]any{"tags": map[string]any{"a.b": "x", "a": map[string]any{"b": "y"}}}), []string{"spec.tags.a.b"}},
			want:   want{err: true},
		},
		"DottedKeyAbsent": {
			reason: "With no dotted key, segments are nesting.",
			args:   args{nil, spec(map[string]any{"tags": map[string]any{"a": map[string]any{"b": "v"}}}), []string{"spec.tags.a.b"}},
			want:   want{desired: spec(map[string]any{"tags": map[string]any{"a": map[string]any{"b": s}}})},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			before := []*unstructured.Unstructured{tc.args.current.DeepCopy(), tc.args.desired.DeepCopy()}
			cur, des, err := RedactPaths(tc.args.current, tc.args.desired, tc.args.paths)
			if diff := cmp.Diff(tc.want.err, err != nil); diff != "" {
				t.Errorf("\n%s\nRedactPaths(...): -want error, +got error (%v):\n%s", tc.reason, err, diff)
			}
			if diff := cmp.Diff(tc.want.current, cur); diff != "" {
				t.Errorf("\n%s\nRedactPaths(...): -want current, +got:\n%s", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.desired, des); diff != "" {
				t.Errorf("\n%s\nRedactPaths(...): -want desired, +got:\n%s", tc.reason, diff)
			}
			after := []*unstructured.Unstructured{tc.args.current, tc.args.desired}
			if diff := cmp.Diff(before, after); diff != "" {
				t.Errorf("\n%s\nRedactPaths(...): changed its inputs:\n%s", tc.reason, diff)
			}
		})
	}
}
