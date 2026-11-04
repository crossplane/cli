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
	"fmt"
	"reflect"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/fieldpath"

	"github.com/crossplane/cli/v2/internal/plan"
)

// ChangedSensitive marks, on the new side, a sensitive value that differs
// from the old side's, so a change made only to a secret still shows a line.
const ChangedSensitive = "(sensitive value, changed)"

// redactPaths returns copies of current and desired with the value at each
// path replaced by a placeholder: the sensitive-value marker on the old side,
// and on the new side too when the old side holds an equal value or none;
// otherwise ChangedSensitive. Each side is located on its own. A located map
// or list is replaced whole, and a path under a scalar replaces the scalar. A
// path absent from a side leaves that side alone; a nil side is absent. The
// inputs are never changed.
//
// An index [n] means every item of the list, whatever n is: upjet writes a set
// element's index as a hash, which can be past the end or name the wrong
// item, so the index can't be trusted. The rest of the path is applied to
// each item, and an item without it is absent. Locations are paired across
// the sides by list position for an index and by match order for a keyed
// item, so the changed marker is decided per item.
//
// It returns an error, and no copies, when a path can't be redacted: it
// doesn't parse, it resolves to the root, a segment meets the wrong kind of
// node (an index or keyed item on a map, a field on a list, a keyed item on a
// list of non-maps), a segment matches more than one location on a side, or
// a map key holding brackets may be the one named, as fieldpath splits it.
// The caller must then hide what the objects would print.
func redactPaths(current, desired *unstructured.Unstructured, paths []string) (*unstructured.Unstructured, *unstructured.Unstructured, error) {
	cur, des := current.DeepCopy(), desired.DeepCopy()
	root := func(u *unstructured.Unstructured) *slot {
		if u == nil || u.Object == nil {
			return nil
		}
		return &slot{v: u.Object}
	}
	for _, p := range paths {
		segs, err := fieldpath.Parse(p)
		if err != nil {
			return nil, nil, errors.Wrapf(err, "cannot redact %q", p)
		}
		oc, err := locate(root(cur), segs)
		if err != nil {
			return nil, nil, errors.Wrapf(err, "cannot redact %q", p)
		}
		nc, err := locate(root(des), segs)
		if err != nil {
			return nil, nil, errors.Wrapf(err, "cannot redact %q", p)
		}
		// Locations are paired by where they are; a side lacking one has none.
		pairs := map[string]*[2]*slot{}
		var order []string
		for side, ss := range [][]*slot{oc, nc} {
			for _, s := range ss {
				if pairs[s.at] == nil {
					pairs[s.at] = &[2]*slot{}
					order = append(order, s.at)
				}
				pairs[s.at][side] = s
			}
		}
		for _, at := range order {
			if err := replace(pairs[at][0], pairs[at][1]); err != nil {
				return nil, nil, errors.Wrapf(err, "cannot redact %q", p)
			}
		}
	}
	return cur, des, nil
}

// A slot is a located value, how to replace it, and where it is among the
// items an index or keyed segment matched, for pairing. The root has no set.
type slot struct {
	v   any
	set func(any)
	at  string
}

// replace replaces each present side with its placeholder.
func replace(o, n *slot) error {
	sensitive := plan.FormatValue(&plan.Value{Sensitive: true})
	nv := sensitive
	if o != nil && n != nil && !reflect.DeepEqual(o.v, n.v) {
		nv = ChangedSensitive
	}
	for _, s := range []struct {
		at *slot
		v  string
	}{{o, sensitive}, {n, nv}} {
		if s.at == nil {
			continue
		}
		if s.at.set == nil {
			return errors.New("the path resolves to the whole object")
		}
		s.at.set(s.v)
	}
	return nil
}

// locate returns what segs reach from s. Providers don't escape dots in map
// keys, so tags.kubernetes.io/cluster/x may name the key
// kubernetes.io/cluster/x: every run of field segments that joins to a key is
// tried, and more than one reaching something is ambiguous.
func locate(s *slot, segs []fieldpath.Segment) ([]*slot, error) {
	if s == nil || s.v == nil {
		return nil, nil
	}
	if len(segs) == 0 {
		return []*slot{s}, nil
	}
	switch v := s.v.(type) {
	case map[string]any:
		return locateMap(v, s.at, segs)
	case []any:
		return locateList(v, s.at, segs)
	default:
		// A path under a scalar can't reach past it; the scalar holds the value.
		return []*slot{s}, nil
	}
}

func locateMap(v map[string]any, at string, segs []fieldpath.Segment) ([]*slot, error) {
	seg := segs[0]
	if seg.Type != fieldpath.SegmentField {
		return nil, errors.Errorf("index [%d] meets a map", seg.Index)
	}
	var out []*slot
	keys, found := 0, 0
	for j := 1; j <= len(segs) && segs[j-1].Type == fieldpath.SegmentField; j++ {
		f := joinFields(segs[:j])
		// fieldpath splits a key holding brackets, such as foo[bar], so a key
		// that starts with the joined fields and a bracket may be the one.
		if j < len(segs) && keyWithPrefix(v, f+"[") {
			return nil, errors.Errorf("%q may name a key holding brackets", f)
		}
		k, ok, err := mapKey(v, f)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		keys++
		got, err := locate(&slot{v: v[k], set: func(x any) { v[k] = x }, at: at}, segs[j:])
		if err != nil {
			return nil, err
		}
		if len(got) > 0 {
			found++
			out = got
		}
	}
	if found > 1 {
		return nil, errors.Errorf("%q matches more than one location", seg.Field)
	}
	// fieldpath reads [key="b"] as the field key="b.
	if keys == 0 && strings.Contains(seg.Field, "=") {
		return nil, errors.Errorf("keyed item [%s] meets a map", seg.Field)
	}
	return out, nil
}

func locateList(v []any, at string, segs []fieldpath.Segment) ([]*slot, error) {
	seg := segs[0]
	if seg.Type == fieldpath.SegmentIndex {
		// Every item, whatever the index: see redactPaths.
		var out []*slot
		for i := range v {
			got, err := locate(&slot{v: v[i], set: func(x any) { v[i] = x }, at: fmt.Sprintf("%s[%d]", at, i)}, segs[1:])
			if err != nil {
				return nil, err
			}
			out = append(out, got...)
		}
		return out, nil
	}
	key, want, ok := strings.Cut(seg.Field, "=")
	if !ok {
		return nil, errors.Errorf("field %q meets a list", seg.Field)
	}
	want = strings.Trim(want, `"'`)
	var out []*slot
	n := 0
	for i, item := range v {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, errors.Errorf("keyed item [%s] meets a list of non-maps", seg.Field)
		}
		if m[key] == nil || fmt.Sprint(m[key]) != want {
			continue
		}
		got, err := locate(&slot{v: item, set: func(x any) { v[i] = x }, at: fmt.Sprintf("%s[#%d]", at, n)}, segs[1:])
		n++
		if err != nil {
			return nil, err
		}
		out = append(out, got...)
	}
	return out, nil
}

func joinFields(segs []fieldpath.Segment) string {
	parts := make([]string, len(segs))
	for i, s := range segs {
		parts[i] = s.Field
	}
	return strings.Join(parts, ".")
}

// keyWithPrefix reports whether a key of m, as written or with quotes
// trimmed, starts with p.
func keyWithPrefix(m map[string]any, p string) bool {
	for k := range m {
		if strings.HasPrefix(k, p) || strings.HasPrefix(strings.Trim(k, `"'`), p) {
			return true
		}
	}
	return false
}

// mapKey returns the key of m that f names, as written or with quotes
// trimmed, and whether one does. Two matching keys can't be told apart.
func mapKey(m map[string]any, f string) (string, bool, error) {
	found, n := "", 0
	for k := range m {
		if k == f || strings.Trim(k, `"'`) == f {
			found = k
			n++
		}
	}
	if n > 1 {
		return "", false, errors.Errorf("more than one key matches %q", f)
	}
	return found, n == 1, nil
}

// setPath sets the value at path in u to v, in place, at every location the
// path reaches: a field, the item an index names, or every item a keyed
// segment matches. Map levels are matched as redactPaths matches them. A field
// absent from a map is created, with the map levels below it, when the rest
// of the path is fields alone; a dotted key absent from u is so created as
// nested levels. It sets nothing where the path doesn't parse, resolves to the
// root, or meets a scalar, a missing item or the wrong kind of node, nor at a
// map level more than one key matches. It reports whether it set a value.
//
// fieldpath.Pave.SetValue sets the same path strings, but it can't be used
// throughout: it treats a dotted map key (tags.kubernetes.io/cluster/x) as
// nested levels, so it would miss a present key that holds dots; it sets one
// location, not every item a keyed segment matches; and it doesn't skip a map
// level more than one key matches. setPath handles those and uses SetValue
// only to create levels no key reaches.
func setPath(u *unstructured.Unstructured, path string, v any) bool {
	segs, err := fieldpath.Parse(path)
	if err != nil {
		return false
	}
	return setSegments(u, segs, v)
}

// setSegments sets v at the parsed path segs in u, as setPath does.
func setSegments(u *unstructured.Unstructured, segs fieldpath.Segments, v any) bool {
	if u == nil || u.Object == nil || len(segs) == 0 {
		return false
	}
	return setIn(u.Object, segs, v)
}

// setIn sets v at segs, which are not empty, below node, as setPath does.
func setIn(node any, segs []fieldpath.Segment, v any) bool {
	switch n := node.(type) {
	case map[string]any:
		return setInMap(n, segs, v)
	case []any:
		return setInList(n, segs, v)
	}
	return false
}

func setInMap(n map[string]any, segs fieldpath.Segments, v any) bool {
	if segs[0].Type != fieldpath.SegmentField {
		return false
	}
	key, tail, found := "", segs, 0
	for j := 1; j <= len(segs) && segs[j-1].Type == fieldpath.SegmentField; j++ {
		k, ok, err := mapKey(n, joinFields(segs[:j]))
		if err != nil {
			return false
		}
		if ok {
			key, tail = k, segs[j:]
			found++
		}
	}
	switch {
	case found > 1:
		return false
	case found == 1 && len(tail) == 0:
		n[key] = v
		return true
	case found == 1:
		return setIn(n[key], tail, v)
	}
	// Nothing at these fields exists, so the plain setter's rules apply:
	// create the missing levels. It can't address an index or keyed item.
	for _, seg := range segs {
		if seg.Type != fieldpath.SegmentField || strings.Contains(seg.Field, "=") {
			return false
		}
	}
	return fieldpath.Pave(n).SetValue(segs.String(), v) == nil
}

func setInList(n []any, segs []fieldpath.Segment, v any) bool {
	seg, rest := segs[0], segs[1:]
	if seg.Type == fieldpath.SegmentIndex {
		switch {
		case seg.Index >= uint(len(n)):
			return false
		case len(rest) == 0:
			n[seg.Index] = v
			return true
		}
		return setIn(n[seg.Index], rest, v)
	}
	key, want, ok := strings.Cut(seg.Field, "=")
	if !ok {
		return false
	}
	want = strings.Trim(want, `"'`)
	set := false
	for i, item := range n {
		m, ok := item.(map[string]any)
		if !ok || m[key] == nil || fmt.Sprint(m[key]) != want {
			continue
		}
		if len(rest) == 0 {
			n[i] = v
			set = true
			continue
		}
		set = setIn(m, rest, v) || set
	}
	return set
}
