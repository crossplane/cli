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
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Fallback computes an approximate client-side plan for desired by comparing
// its spec against the live resource's spec. It is used when no diff server
// can plan the resource, and is honest about its limits: no replacement
// detection, no computed values, no provider defaults. live nil means the
// resource does not exist and plans as a create.
func Fallback(desired, live *unstructured.Unstructured, reason string) *ResourcePlan {
	p := NewResourcePlan(desired)
	p.Approximate = true
	p.FallbackReason = reason

	desiredSpec, _, _ := unstructured.NestedMap(desired.Object, "spec")

	if live == nil {
		p.Action = ActionCreate
		p.Changes = createChanges("spec", desiredSpec)
		return p
	}

	liveSpec, _, _ := unstructured.NestedMap(live.Object, "spec")
	p.Changes = diffMaps("spec", liveSpec, desiredSpec)
	if len(p.Changes) == 0 {
		p.Action = ActionNoOp
		return p
	}
	p.Action = ActionUpdate
	return p
}

// createChanges flattens a desired map into planned-only field changes, so a
// create lists what it would set.
func createChanges(path string, m map[string]any) []FieldChange {
	out := make([]FieldChange, 0, len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		out = append(out, flattenValue(path+"."+k, m[k], false)...)
	}
	return out
}

// flattenValue emits one planned-only (or actual-only, for removals) change
// per leaf under path.
func flattenValue(path string, v any, removed bool) []FieldChange {
	switch t := v.(type) {
	case map[string]any:
		out := make([]FieldChange, 0, len(t))
		for _, k := range slices.Sorted(maps.Keys(t)) {
			out = append(out, flattenValue(path+"."+k, t[k], removed)...)
		}
		return out
	case []any:
		out := make([]FieldChange, 0, len(t))
		for i, e := range t {
			out = append(out, flattenValue(fmt.Sprintf("%s[%d]", path, i), e, removed)...)
		}
		return out
	default:
		fc := FieldChange{Path: path}
		if removed {
			fc.Actual = &Value{Value: v}
		} else {
			fc.Planned = &Value{Value: v}
		}
		return []FieldChange{fc}
	}
}

// diffMaps compares two maps recursively and returns the changes that turn
// actual into planned, with paths rooted at path.
func diffMaps(path string, actual, planned map[string]any) []FieldChange {
	var out []FieldChange
	keys := map[string]struct{}{}
	for k := range actual {
		keys[k] = struct{}{}
	}
	for k := range planned {
		keys[k] = struct{}{}
	}
	for _, k := range slices.Sorted(maps.Keys(keys)) {
		kp := path + "." + k
		av, aok := actual[k]
		pv, pok := planned[k]
		switch {
		case !aok:
			out = append(out, flattenValue(kp, pv, false)...)
		case !pok:
			out = append(out, flattenValue(kp, av, true)...)
		default:
			out = append(out, diffValues(kp, av, pv)...)
		}
	}
	return out
}

func diffValues(path string, actual, planned any) []FieldChange {
	am, aok := actual.(map[string]any)
	pm, pok := planned.(map[string]any)
	if aok && pok {
		return diffMaps(path, am, pm)
	}

	as, aok := actual.([]any)
	ps, pok := planned.([]any)
	if aok && pok && len(as) == len(ps) {
		var out []FieldChange
		for i := range as {
			out = append(out, diffValues(fmt.Sprintf("%s[%d]", path, i), as[i], ps[i])...)
		}
		return out
	}

	if equalValues(actual, planned) {
		return nil
	}
	return []FieldChange{{
		Path:    path,
		Actual:  &Value{Value: actual},
		Planned: &Value{Value: planned},
	}}
}

// equalValues compares two leaf values by their JSON encoding. Both sides
// come from JSON or YAML decoding, but with skewed numeric types: YAML yields
// float64 where the API server returns int64. Encoding normalizes that (7 and
// 7.0 both encode as "7") and json.Marshal sorts map keys, so the comparison
// is deterministic for composite values too.
func equalValues(a, b any) bool {
	ab, aerr := json.Marshal(a)
	bb, berr := json.Marshal(b)
	return aerr == nil && berr == nil && bytes.Equal(ab, bb)
}
