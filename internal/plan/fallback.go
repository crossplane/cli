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
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/structured-merge-diff/v6/fieldpath"
)

// Fallback computes an approximate client-side plan for desired by comparing
// its spec against the live resource's spec. It is used when no diff server
// can plan the resource, and is honest about its limits: no replacement
// detection, no computed values, no provider defaults. It diffs desired
// verbatim, so callers should apply the API server's CRD defaults to desired
// first when they can. Live-only fields plan as removals only when live's
// managedFields say a declarative applier owns them: anything else - fields
// the provider late-initialized, or API server defaults owned by nobody -
// survives an apply of the manifest. live nil means the resource does not
// exist and plans as a create.
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

	// Ownership decisions compare against the set's leaves: a leaf is either a
	// plainly owned field or a whole atomic subtree, while a partially owned
	// parent map keeps its children and never matches.
	owned := applierOwned(live)
	if owned != nil {
		owned = owned.Leaves()
	}

	liveSpec, _, _ := unstructured.NestedMap(live.Object, "spec")
	p.Changes = diffMaps(newCursor("spec"), liveSpec, desiredSpec, owned)
	if len(p.Changes) == 0 {
		p.Action = ActionNoOp
		return p
	}
	p.Action = ActionUpdate
	return p
}

// cursor tracks a position in the diffed object: the rendered path for the
// plan's output, and the structured field path for ownership lookups in a
// managedFields set. The two diverge on purpose: the rendered path is lossy
// (a map key may itself contain dots or brackets) and indexes list elements,
// while FieldsV1 sets never index lists positionally, so below a list the
// field path freezes at the list itself and ownership is decided there.
type cursor struct {
	str    string
	fp     fieldpath.Path
	inList bool
}

func newCursor(root string) cursor {
	return cursor{str: root, fp: fieldpath.MakePathOrDie(root)}
}

func (c cursor) field(k string) cursor {
	next := cursor{str: c.str + "." + k, fp: c.fp, inList: c.inList}
	if !c.inList {
		next.fp = append(slices.Clip(c.fp), fieldpath.PathElement{FieldName: &k})
	}
	return next
}

func (c cursor) index(i int) cursor {
	return cursor{str: fmt.Sprintf("%s[%d]", c.str, i), fp: c.fp, inList: true}
}

// removable reports whether a removal at the cursor would really happen on
// apply: with no ownership information every removal counts, otherwise only
// applier-owned fields do.
func (c cursor) removable(owned *fieldpath.Set) bool {
	return owned == nil || ownedLeaf(owned, c.fp)
}

// FieldChanges returns the changes that turn actual into planned, with paths
// rooted at the top level (e.g. metadata.labels.team). A nil planned yields
// actual-only changes, as for a delete; a nil actual yields planned-only
// changes, as for a create.
func FieldChanges(actual, planned map[string]any) []FieldChange {
	out := diffMaps(cursor{}, actual, planned, nil)
	for i := range out {
		out[i].Path = strings.TrimPrefix(out[i].Path, ".")
	}
	return out
}

// createChanges flattens a desired map into planned-only field changes, so a
// create lists what it would set.
func createChanges(path string, m map[string]any) []FieldChange {
	out := make([]FieldChange, 0, len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		out = append(out, flattenValue(newCursor(path).field(k), m[k], false, nil)...)
	}
	return out
}

// flattenValue emits one planned-only (or actual-only, for removals) change
// per leaf under the cursor. Removals outside the applier-owned set stay
// silent: applying the manifest would not remove them.
func flattenValue(c cursor, v any, removed bool, owned *fieldpath.Set) []FieldChange {
	switch t := v.(type) {
	case map[string]any:
		out := make([]FieldChange, 0, len(t))
		for _, k := range slices.Sorted(maps.Keys(t)) {
			out = append(out, flattenValue(c.field(k), t[k], removed, owned)...)
		}
		return out
	case []any:
		out := make([]FieldChange, 0, len(t))
		for i, e := range t {
			out = append(out, flattenValue(c.index(i), e, removed, owned)...)
		}
		return out
	default:
		if removed {
			if !c.removable(owned) {
				return nil
			}
			return []FieldChange{{Path: c.str, Actual: &Value{Value: v}}}
		}
		return []FieldChange{{Path: c.str, Planned: &Value{Value: v}}}
	}
}

// diffMaps compares two maps recursively and returns the changes that turn
// actual into planned, with paths rooted at the cursor.
func diffMaps(c cursor, actual, planned map[string]any, owned *fieldpath.Set) []FieldChange {
	var out []FieldChange
	keys := map[string]struct{}{}
	for k := range actual {
		keys[k] = struct{}{}
	}
	for k := range planned {
		keys[k] = struct{}{}
	}
	for _, k := range slices.Sorted(maps.Keys(keys)) {
		kc := c.field(k)
		av, aok := actual[k]
		pv, pok := planned[k]
		switch {
		case !aok:
			out = append(out, flattenValue(kc, pv, false, owned)...)
		case !pok:
			out = append(out, flattenValue(kc, av, true, owned)...)
		default:
			out = append(out, diffValues(kc, av, pv, owned)...)
		}
	}
	return out
}

func diffValues(c cursor, actual, planned any, owned *fieldpath.Set) []FieldChange {
	am, aok := actual.(map[string]any)
	pm, pok := planned.(map[string]any)
	if aok && pok {
		return diffMaps(c, am, pm, owned)
	}

	as, aok := actual.([]any)
	ps, pok := planned.([]any)
	if aok && pok && len(as) == len(ps) {
		var out []FieldChange
		for i := range as {
			out = append(out, diffValues(c.index(i), as[i], ps[i], owned)...)
		}
		return out
	}

	if equalValues(actual, planned) {
		return nil
	}
	return []FieldChange{{
		Path:    c.str,
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
