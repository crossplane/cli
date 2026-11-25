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
	"maps"
	"slices"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/structured-merge-diff/v6/fieldpath"

	xpfieldpath "github.com/crossplane/crossplane-runtime/v2/pkg/fieldpath"
)

// Fallback computes an approximate client-side plan for desired by comparing
// its spec against the live resource's spec. It is used when no diff server
// can plan the resource, and is honest about its limits: no replacement
// detection, no computed values, no provider defaults. It diffs desired
// verbatim, so callers should apply the API server's CRD defaults to desired
// first when they can. Live-only fields plan as removals only when applying
// the manifest as a would remove them:
//
//   - ApplyServerSide: a.FieldManager owns them and no other manager does.
//   - ApplyClientSide: live's LastAppliedConfigAnnotation has them and
//     desired drops them, or they are inside a list live and desired
//     differ on, which the merge patch replaces whole. With no annotation
//     only those list replacements remove anything.
//   - ApplyUndetermined: some declarative applier owns them and no other
//     manager does, as applierOwned describes.
//
// Anything else - fields the provider late-initialized, or API server
// defaults owned by nobody - survives an apply of the manifest. live nil
// means the resource does not exist and plans as a create.
func Fallback(desired, live *unstructured.Unstructured, a Applier, reason string) *ResourcePlan {
	p := NewResourcePlan(desired)
	p.Approximate = true
	p.FallbackReason = reason
	p.Before, p.After = CleanView(live), CleanView(desired)

	desiredSpec, _, _ := unstructured.NestedMap(desired.Object, "spec")

	if live == nil {
		p.Action = ActionCreate
		p.Changes = createChanges("spec", desiredSpec)
		return p
	}

	// Removal decisions compare against a set's leaves: a leaf is either a
	// plainly owned field or a whole atomic subtree, while a partially owned
	// parent map keeps its children and never matches.
	var owned *fieldpath.Set
	if a.Mode == ApplyClientSide {
		owned = clientSideRemovable(live, desired)
	} else if owned = applierOwned(live, a); owned != nil {
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

// clientSideRemovable returns the fields of live kubectl's client-side apply
// of desired removes: those under the topmost spec keys live's
// LastAppliedConfigAnnotation has and desired drops, and those inside a list
// live and desired differ on. Its three-way JSON merge patch deletes each such
// key whole, whoever owns it, and sets each such list to desired's whole,
// dropping what only live's has; outside replaced lists it keeps anything the
// annotation lacks. With no annotation there is no original, so only the list
// replacements remove anything; with one that does not parse (which kubectl's
// apply rejects), nothing is removed.
func clientSideRemovable(live, desired *unstructured.Unstructured) *fieldpath.Set {
	removable := fieldpath.NewSet()
	desiredSpec, _, _ := unstructured.NestedMap(desired.Object, "spec")
	if annotation, ok := live.GetAnnotations()[LastAppliedConfigAnnotation]; ok {
		var applied map[string]any
		if err := json.Unmarshal([]byte(annotation), &applied); err != nil {
			return removable
		}
		appliedSpec, _, _ := unstructured.NestedMap(applied, "spec")
		droppedKeys(removable, newCursor("spec"), appliedSpec, desiredSpec)
	}
	liveSpec, _, _ := unstructured.NestedMap(live.Object, "spec")
	replacedLists(removable, newCursor("spec"), liveSpec, desiredSpec)
	return removable
}

// replacedLists inserts into s the path of each list under the cursor that
// live and desired both have and differ on, descending into maps both have.
// The merge patch sets such a list to desired's, so every live-only field in
// it goes. A list's set path stands for everything inside it, as the cursor's
// path freezes at a list.
func replacedLists(s *fieldpath.Set, c cursor, live, desired map[string]any) {
	for k, lv := range live {
		dv, ok := desired[k]
		if !ok {
			continue
		}
		switch l := lv.(type) {
		case map[string]any:
			if dm, ok := dv.(map[string]any); ok {
				replacedLists(s, c.field(k), l, dm)
			}
		case []any:
			if _, ok := dv.([]any); ok && !equalValues(l, dv) {
				s.Insert(c.field(k).fp)
			}
		}
	}
}

// droppedKeys inserts into s the path of each key under the cursor that
// applied has and desired drops, descending into maps both keep. A merge
// patch replaces lists whole, so it never descends into them.
func droppedKeys(s *fieldpath.Set, c cursor, applied, desired map[string]any) {
	for k, av := range applied {
		dv, ok := desired[k]
		if !ok {
			s.Insert(c.field(k).fp)
			continue
		}
		am, aok := av.(map[string]any)
		dm, dok := dv.(map[string]any)
		if aok && dok {
			droppedKeys(s, c.field(k), am, dm)
		}
	}
}

// cursor tracks a position in the diffed object: the crossplane-runtime field
// path for the plan's output, and the structured field path for ownership
// lookups in a managedFields set. The two diverge on purpose: the output path
// indexes list elements, while FieldsV1 sets never index lists positionally,
// so below a list the field path freezes at the list itself and ownership is
// decided there.
type cursor struct {
	segs   xpfieldpath.Segments
	fp     fieldpath.Path
	inList bool
}

func newCursor(root string) cursor {
	return cursor{segs: xpfieldpath.Segments{xpfieldpath.Field(root)}, fp: fieldpath.MakePathOrDie(root)}
}

func (c cursor) field(k string) cursor {
	// The segment is built directly so the JSON path keeps the key verbatim:
	// xpfieldpath.Field would trim its quotes. Parsing the path back
	// (DisplayLevels) still trims them; see below.
	//
	// TODO(jcogilvie): escape or replace fieldpath for keys that do not
	// round-trip. fieldpath has no escaping, so:
	//   - the path does not parse, so the change shows as one display level:
	//     e.g. "a[b", "a]b", "a.b[0]", or the empty key (written "data.");
	//   - the path parses silently as a different one: "a[b]c" comes back as
	//     data.a.b.c, so the change shows as several display levels;
	//   - a leading or trailing quote is trimmed, and a key of only quote
	//     characters trims to empty, so the key is lost.
	// Either way the annotate marker for the change is missed; its values are
	// still printed.
	seg := xpfieldpath.Segment{Type: xpfieldpath.SegmentField, Field: k}
	next := cursor{segs: append(slices.Clip(c.segs), seg), fp: c.fp, inList: c.inList}
	if !c.inList {
		next.fp = append(slices.Clip(c.fp), fieldpath.PathElement{FieldName: &k})
	}
	return next
}

func (c cursor) index(i int) cursor {
	seg := xpfieldpath.Segment{Type: xpfieldpath.SegmentIndex, Index: uint(i)}
	return cursor{segs: append(slices.Clip(c.segs), seg), fp: c.fp, inList: true}
}

// removable reports whether a removal at the cursor would really happen on
// apply: with no ownership information every removal counts, otherwise only
// the fields under one of owned's leaves do.
func (c cursor) removable(owned *fieldpath.Set) bool {
	return owned == nil || ownedLeaf(owned, c.fp)
}

// FieldChanges returns the changes that turn actual into planned, with paths
// rooted at the top level (e.g. metadata.labels.team). A nil planned yields
// actual-only changes, as for a delete; a nil actual yields planned-only
// changes, as for a create.
func FieldChanges(actual, planned map[string]any) []FieldChange {
	return diffMaps(cursor{}, actual, planned, nil)
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
			return []FieldChange{{Path: c.segs.String(), Actual: &Value{Value: v}}}
		}
		return []FieldChange{{Path: c.segs.String(), Planned: &Value{Value: v}}}
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
		Path:    c.segs.String(),
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
