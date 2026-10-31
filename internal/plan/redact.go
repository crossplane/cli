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
// otherwise ChangedSensitive. Each side is located on its own, and the
// locations are paired by order. A located map or list is replaced whole, and
// a path under a scalar replaces the scalar. A path absent from a side leaves
// that side alone; a nil side is absent. The inputs are never changed.
//
// It returns an error, and no copies, when a path can't be redacted: it
// doesn't parse, it resolves to the root, a segment meets the wrong kind of
// node (an index or keyed item on a map, a field on a list, a keyed item on a
// list of non-maps), or a segment matches more than one location on a side.
// The caller must then hide what the objects would print.
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
		oc, err := locate(root(cur), segs)
		if err != nil {
			return nil, nil, errors.Wrapf(err, "cannot redact %q", p)
		}
		nc, err := locate(root(des), segs)
		if err != nil {
			return nil, nil, errors.Wrapf(err, "cannot redact %q", p)
		}
		// Keyed matches are paired by order; a side with fewer has none left.
		for i := range max(len(oc), len(nc)) {
			var o, n *slot
			if i < len(oc) {
				o = oc[i]
			}
			if i < len(nc) {
				n = nc[i]
			}
			if err := replace(o, n); err != nil {
				return nil, nil, errors.Wrapf(err, "cannot redact %q", p)
			}
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
		return locateMap(v, segs)
	case []any:
		return locateList(v, segs)
	default:
		// A path under a scalar can't reach past it; the scalar holds the value.
		return []*slot{s}, nil
	}
}

func locateMap(v map[string]any, segs []fieldpath.Segment) ([]*slot, error) {
	seg := segs[0]
	if seg.Type != fieldpath.SegmentField {
		return nil, errors.Errorf("index [%d] meets a map", seg.Index)
	}
	var out []*slot
	keys, found := 0, 0
	for j := 1; j <= len(segs) && segs[j-1].Type == fieldpath.SegmentField; j++ {
		k, ok, err := mapKey(v, joinFields(segs[:j]))
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		keys++
		got, err := locate(&slot{v: v[k], set: func(x any) { v[k] = x }}, segs[j:])
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

func locateList(v []any, segs []fieldpath.Segment) ([]*slot, error) {
	seg := segs[0]
	if seg.Type == fieldpath.SegmentIndex {
		i := int(seg.Index)
		if i >= len(v) {
			return nil, nil
		}
		return locate(&slot{v: v[i], set: func(x any) { v[i] = x }}, segs[1:])
	}
	key, want, ok := strings.Cut(seg.Field, "=")
	if !ok {
		return nil, errors.Errorf("field %q meets a list", seg.Field)
	}
	want = strings.Trim(want, `"'`)
	var out []*slot
	for i, item := range v {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, errors.Errorf("keyed item [%s] meets a list of non-maps", seg.Field)
		}
		if m[key] == nil || fmt.Sprint(m[key]) != want {
			continue
		}
		got, err := locate(&slot{v: item, set: func(x any) { v[i] = x }}, segs[1:])
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
