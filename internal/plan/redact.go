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
	"fmt"
	"reflect"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/fieldpath"
)

// ChangedSensitive marks, on the new side, a sensitive value that differs
// from the old side's, so a change made only to a secret still shows a line.
const ChangedSensitive = "(sensitive value, changed)"

// RedactPaths returns copies of current and desired with the value at each
// path replaced by a placeholder: the sensitive-value marker on the old side,
// and on the new side too when the old side holds an equal value or none;
// otherwise ChangedSensitive. A located map or list is replaced whole. A path
// absent from a side leaves that side alone; a nil side is absent. Where a path
// meets the wrong kind of node (an index on a map, a field under a scalar) the
// node it reached is replaced. The inputs are never changed.
//
// It returns an error, and no copies, when a path can't be redacted: it
// doesn't parse, it resolves to or stops at the root, or a key matches more
// than one map key. The caller must then hide what the objects would print.
func RedactPaths(current, desired *unstructured.Unstructured, paths []string) (*unstructured.Unstructured, *unstructured.Unstructured, error) {
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
		if err := redact(root(cur), root(des), segs); err != nil {
			return nil, nil, errors.Wrapf(err, "cannot redact %q", p)
		}
	}
	return cur, des, nil
}

// A slot is a located value and how to replace it. The root has no set.
type slot struct {
	v   any
	set func(any)
}

// replace replaces each present side with its placeholder.
func replace(o, n *slot) error {
	sensitive := FormatValue(&Value{Sensitive: true})
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

// redact walks segs from o and n, an old and a new node at the same path,
// and replaces what they reach.
func redact(o, n *slot, segs []fieldpath.Segment) error {
	if o != nil && o.v == nil {
		o = nil
	}
	if n != nil && n.v == nil {
		n = nil
	}
	if o == nil && n == nil {
		return nil
	}
	if len(segs) == 0 {
		return replace(o, n)
	}

	used, err := dottedKey(o, n, segs)
	if err != nil {
		return err
	}
	oc, oBad, err := children(o, segs[:used])
	if err != nil {
		return err
	}
	nc, nBad, err := children(n, segs[:used])
	if err != nil {
		return err
	}
	switch {
	case oBad && nBad:
		return replace(o, n)
	case oBad:
		if err := replace(o, nil); err != nil {
			return err
		}
		oc = nil
	case nBad:
		// An old side that resolves differently holds a changed value.
		if n.set == nil {
			return errors.New("the path resolves to the whole object")
		}
		v := ChangedSensitive
		if o == nil {
			v = FormatValue(&Value{Sensitive: true})
		}
		n.set(v)
		nc = nil
	}
	// Keyed matches are paired by order; a side with fewer has none left.
	for i := range max(len(oc), len(nc)) {
		var oi, ni *slot
		if i < len(oc) {
			oi = oc[i]
		}
		if i < len(nc) {
			ni = nc[i]
		}
		if err := redact(oi, ni, segs[used:]); err != nil {
			return err
		}
	}
	return nil
}

// dottedKey returns how many leading segments name one map key. Providers
// don't escape dots in map keys, so tags.kubernetes.io/cluster/x may name the
// key kubernetes.io/cluster/x: the longest run of field segments that joins
// to a key on either side wins; otherwise one segment is one key.
func dottedKey(o, n *slot, segs []fieldpath.Segment) (int, error) {
	run := 0
	for run < len(segs) && segs[run].Type == fieldpath.SegmentField {
		run++
	}
	for j := run; j > 1; j-- {
		key := joinFields(segs[:j])
		for _, s := range []*slot{o, n} {
			if s == nil {
				continue
			}
			m, ok := s.v.(map[string]any)
			if !ok {
				continue
			}
			_, ok, err := mapKey(m, key)
			if err != nil {
				return 0, err
			}
			if ok {
				return j, nil
			}
		}
	}
	return 1, nil
}

func joinFields(segs []fieldpath.Segment) string {
	parts := make([]string, len(segs))
	for i, s := range segs {
		parts[i] = s.Field
	}
	return strings.Join(parts, ".")
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

// children returns what segs (one key, or one index or keyed item) reach
// from s, or bad when s is the wrong kind of node for them.
func children(s *slot, segs []fieldpath.Segment) ([]*slot, bool, error) {
	if s == nil {
		return nil, false, nil
	}
	seg := segs[0]
	switch v := s.v.(type) {
	case map[string]any:
		if seg.Type != fieldpath.SegmentField {
			return nil, true, nil
		}
		k, ok, err := mapKey(v, joinFields(segs))
		if err != nil || !ok {
			return nil, false, err
		}
		return []*slot{{v: v[k], set: func(x any) { v[k] = x }}}, false, nil
	case []any:
		if seg.Type == fieldpath.SegmentIndex {
			i := int(seg.Index)
			if i >= len(v) {
				return nil, false, nil
			}
			return []*slot{{v: v[i], set: func(x any) { v[i] = x }}}, false, nil
		}
		// fieldpath reads [key="b"] as the field key="b.
		key, want, ok := strings.Cut(seg.Field, "=")
		if !ok {
			return nil, true, nil
		}
		want = strings.Trim(want, `"'`)
		var out []*slot
		for i, item := range v {
			m, ok := item.(map[string]any)
			if ok && m[key] != nil && fmt.Sprint(m[key]) == want {
				out = append(out, &slot{v: item, set: func(x any) { v[i] = x }})
			}
		}
		return out, false, nil
	default:
		return nil, true, nil
	}
}
