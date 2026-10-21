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
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/sergi/go-diff/diffmatchpatch"

	dt "github.com/crossplane/cli/v2/internal/diff/result"
	"github.com/crossplane/cli/v2/internal/plan"
)

// The -o diff annotations for provider plans, as terraform writes them.
const (
	knownAfterApply    = "(known after apply)"
	forcesReplacement  = "  # forces replacement"
	opInsert, opDelete = diffmatchpatch.DiffInsert, diffmatchpatch.DiffDelete
	opEqual            = diffmatchpatch.DiffEqual
)

// AnnotateXRReport returns r with each resource diff annotated with its
// provider plan from plans (see annotateDiff), for the -o diff renderer. It
// never mutates r: the result is a shallow copy with cloned groups and diff
// maps, and each annotated diff is a copy with new LineDiffs. Empty plans
// return an equivalent copy, which renders exactly as r does.
func AnnotateXRReport(r *dt.XRReport, plans map[string]*plan.ResourcePlan) *dt.XRReport {
	if r == nil {
		return nil
	}

	out := *r
	out.Groups = slices.Clone(r.Groups)
	for i := range out.Groups {
		out.Groups[i].Diffs, _ = annotateDiffs(out.Groups[i].Diffs, plans)
	}

	return &out
}

// AnnotateCompReport returns o with each affected composite's diffs annotated
// with their provider plans, as AnnotateXRReport does. An unchanged composite
// whose provider plans find drift becomes changed, so the composition
// renderer lists and counts it.
func AnnotateCompReport(o *dt.CompDiffOutput, plans map[string]*plan.ResourcePlan) *dt.CompDiffOutput {
	if o == nil {
		return nil
	}

	out := *o
	out.Compositions = slices.Clone(o.Compositions)
	for i := range out.Compositions {
		comp := &out.Compositions[i]
		comp.ImpactAnalysis = slices.Clone(comp.ImpactAnalysis)
		for j := range comp.ImpactAnalysis {
			impact := &comp.ImpactAnalysis[j]
			diffs, drifted := annotateDiffs(impact.Diffs, plans)
			impact.Diffs = diffs
			if drifted && impact.Status == dt.XRStatusUnchanged {
				impact.Status = dt.XRStatusChanged
				comp.AffectedResources.Unchanged--
				comp.AffectedResources.WithChanges++
			}
		}
	}

	return &out
}

// ReplacedBy returns the renderer.DiffOptions.Replaced hook for plans: a diff
// is replaced when its provider plan replaces it. It returns nil for empty
// plans, so -o diff without provider plans renders as it always has.
func ReplacedBy(plans map[string]*plan.ResourcePlan) func(*dt.ResourceDiff) bool {
	if len(plans) == 0 {
		return nil
	}

	return func(d *dt.ResourceDiff) bool {
		pp := plans[d.GetDiffKey()]
		return pp != nil && pp.Error == "" && !pp.Approximate && pp.Action == plan.ActionReplace
	}
}

// annotateDiffs returns a copy of diffs with each diff annotated with its
// provider plan, and whether an equal diff became a change. Unannotated
// diffs are shared, not copied. A nil map stays nil.
func annotateDiffs(diffs map[string]*dt.ResourceDiff, plans map[string]*plan.ResourcePlan) (map[string]*dt.ResourceDiff, bool) {
	if diffs == nil {
		return nil, false
	}

	out := maps.Clone(diffs)
	drifted := false
	for k, d := range diffs {
		a := annotateDiff(d, plans[d.GetDiffKey()])
		out[k] = a
		drifted = drifted || (d.DiffType == dt.DiffTypeEqual && a.DiffType != dt.DiffTypeEqual)
	}

	return out, drifted
}

// annotateDiff returns d annotated with its provider plan pp, or d itself
// when there is nothing to annotate. Only a provider's own plan annotates: an
// approximate or failed one carries no replacement or computed values.
//
// In an added or modified diff, a field the provider computes prints as
// "(known after apply)", a value the provider marks sensitive as "(sensitive
// value)", and the "+" line of a field that forces replacement ends
// "  # forces replacement". Other provider changes, such as defaults, show
// only in -o plan.
func annotateDiff(d *dt.ResourceDiff, pp *plan.ResourcePlan) *dt.ResourceDiff {
	if pp == nil || pp.Error != "" || pp.Approximate || len(pp.Changes) == 0 {
		return d
	}

	switch d.DiffType {
	case dt.DiffTypeAdded, dt.DiffTypeModified:
	case dt.DiffTypeEqual:
		return driftDiff(d, pp)
	case dt.DiffTypeRemoved:
		return d
	}

	lines := flatten(d.LineDiffs)
	walk(lines, opDelete, func(op diffmatchpatch.Operation) bool { return op != opInsert })
	walk(lines, opInsert, desired)

	changes := sortedChanges(pp.Changes)
	changed := false
	for _, c := range changes {
		var ok bool
		switch {
		case c.Planned != nil && c.Planned.Unknown:
			lines, ok = markUnknown(lines, plan.PathSegments(c.Path))
		case (c.Planned != nil && c.Planned.Sensitive) || (c.Actual != nil && c.Actual.Sensitive):
			lines, ok = markSensitive(lines, plan.PathSegments(c.Path))
		}
		changed = changed || ok
	}
	for _, c := range changes {
		if c.RequiresReplace {
			changed = markReplace(lines, plan.PathSegments(c.Path), d.DiffType == dt.DiffTypeAdded) || changed
		}
	}

	if !changed {
		return d
	}

	out := *d
	out.LineDiffs = chunk(lines)

	return &out
}

// driftDiff renders an equal diff the provider would change, which is drift
// between the cluster and the external resource, as a modified block: a
// "- path: actual" and "+ path: planned" pair per change, with computed
// values and replacement marked as elsewhere. It returns d when no change has
// a value to print.
//
// ponytail: D-17 does not cover drift, and this block is pending the user's
// confirmation; drop or change it here alone.
func driftDiff(d *dt.ResourceDiff, pp *plan.ResourcePlan) *dt.ResourceDiff {
	if !isChange(pp.Action) {
		return d
	}

	var lines []line
	for _, c := range sortedChanges(pp.Changes) {
		note := ""
		if c.RequiresReplace {
			note = forcesReplacement
		}
		if c.Actual != nil {
			text := c.Path + ": " + plan.FormatValue(c.Actual)
			if c.Planned == nil {
				text += note
			}
			lines = append(lines, line{op: opDelete, text: text})
		}
		if c.Planned != nil {
			lines = append(lines, line{op: opInsert, text: c.Path + ": " + plan.FormatValue(c.Planned) + note})
		}
	}
	if len(lines) == 0 {
		return d
	}

	out := *d
	out.DiffType = dt.DiffTypeModified
	out.LineDiffs = chunk(lines)

	return &out
}

// A line is one line of a diff body, with the field it is on.
type line struct {
	op   diffmatchpatch.Operation
	text string
	// segs is the field path of the line, as plan.PathSegments splits it,
	// on the side the line belongs to: the desired side for an equal line.
	segs []string
	// value is the offset in text of the field's scalar value, or -1 when
	// the line has none: a parent, or a continuation. A block scalar's value
	// starts at its "|" or ">".
	value int
	// parent means the line opens a map or list; children starts its
	// children's column.
	parent   bool
	children int
	// item is the path of the list item a "- key: value" line opens, and
	// itemCol its keys' column.
	item    []string
	itemCol int
	// cont means the line continues the scalar of the line before it.
	cont bool
}

func desired(op diffmatchpatch.Operation) bool { return op != opDelete }

// flatten splits diffs into lines, as the renderer's formatter does.
func flatten(diffs []diffmatchpatch.Diff) []line {
	var lines []line
	for _, d := range diffs {
		texts := strings.Split(d.Text, "\n")
		if strings.HasSuffix(d.Text, "\n") {
			texts = texts[:len(texts)-1]
		}
		if len(texts) == 0 {
			texts = []string{""}
		}
		for _, t := range texts {
			lines = append(lines, line{op: d.Type, text: t, value: -1})
		}
	}

	return lines
}

// chunk joins consecutive lines of one op back into diffs.
func chunk(lines []line) []diffmatchpatch.Diff {
	var out []diffmatchpatch.Diff
	for _, l := range lines {
		if n := len(out); n > 0 && out[n-1].Type == l.op {
			out[n-1].Text += l.text + "\n"
			continue
		}
		out = append(out, diffmatchpatch.Diff{Type: l.op, Text: l.text + "\n"})
	}

	return out
}

// A frame is an open map key or list item while walking.
type frame struct {
	indent int
	segs   []string
	items  int
	item   bool
}

// walker sets the field path of the lines on one side of a diff.
type walker struct {
	stack []frame
	// scalar is the indent the open scalar's continuation lines are deeper
	// than, and scalarSegs its path; nil when no scalar is open.
	scalar     int
	scalarSegs []string
}

// walk sets the field path of the lines on one side of a diff: the lines
// side reports true for, of which only those with op own (and the equal
// lines, for the desired side) are written. The body is YAML as
// sigs.k8s.io/yaml writes it: two-space indents, with a list's items at its
// key's indent.
//
// ponytail: an indent-to-key stack places map keys and positional list items
// only. A list of maps keyed by a field, or a value inside a block scalar,
// is not placed, and gets no inline annotation; the header, the summary and
// -o plan still carry it. Upgrade path: map by the YAML AST (yaml.v3 node
// positions) instead.
func walk(lines []line, own diffmatchpatch.Operation, side func(diffmatchpatch.Operation) bool) {
	w := &walker{stack: []frame{{indent: -1}}}
	for i := range lines {
		l := &lines[i]
		if !side(l.op) {
			continue
		}
		var ignore line
		if l.op != own && (own != opInsert || l.op != opEqual) {
			// Walked for its structure only.
			ignore = *l
			l = &ignore
		}
		w.line(l)
	}
}

// line places one line.
func (w *walker) line(l *line) {
	indent := len(l.text) - len(strings.TrimLeft(l.text, " "))
	blank := strings.TrimSpace(l.text) == ""
	if w.scalarSegs != nil && (blank || indent > w.scalar) {
		l.segs, l.cont = w.scalarSegs, true
		return
	}
	w.scalarSegs = nil
	if blank {
		return
	}

	col, rest := indent, l.text[indent:]
	for rest == "-" || strings.HasPrefix(rest, "- ") {
		segs := w.item(col)
		if rest == "-" {
			l.segs, l.parent, l.children = segs, true, col+2
			return
		}
		rest, col = rest[2:], col+2
		if rest == "-" || strings.HasPrefix(rest, "- ") {
			// The item opens a list of its own.
			continue
		}
		if _, _, ok := splitKey(rest); !ok {
			// A scalar item.
			l.segs, l.value = segs, col
			w.scalar, w.scalarSegs = col-2, segs
			return
		}
		l.item, l.itemCol = segs, col
	}

	key, off, ok := splitKey(rest)
	if !ok {
		// Not a field: leave it unplaced.
		return
	}
	segs := w.key(col, key)
	l.segs = segs
	if off < 0 {
		l.parent, l.children = true, col+2
		return
	}
	// A scalar, which may continue on deeper lines: folded, or a block.
	l.value = col + off
	w.scalar, w.scalarSegs = col, segs
}

// item opens the next list item at col, returning its path.
func (w *walker) item(col int) []string {
	for top := w.stack[len(w.stack)-1]; top.indent > col || (top.item && top.indent == col); top = w.stack[len(w.stack)-1] {
		w.stack = w.stack[:len(w.stack)-1]
	}
	p := &w.stack[len(w.stack)-1]
	segs := withIndex(p.segs, p.items)
	p.items++
	w.stack = append(w.stack, frame{indent: col, segs: segs, item: true})

	return segs
}

// key opens the map key at col, returning its path.
func (w *walker) key(col int, key string) []string {
	for w.stack[len(w.stack)-1].indent >= col {
		w.stack = w.stack[:len(w.stack)-1]
	}
	segs := append(slices.Clone(w.stack[len(w.stack)-1].segs), key)
	w.stack = append(w.stack, frame{indent: col, segs: segs})

	return segs
}

// splitKey splits a "key: value" or "key:" line into its unquoted key and
// the offset of its value, -1 for none.
func splitKey(rest string) (string, int, bool) {
	var key, after string
	switch {
	case rest == "":
		return "", 0, false
	case rest[0] == '"':
		q, err := strconv.QuotedPrefix(rest)
		if err != nil {
			return "", 0, false
		}
		key, _ = strconv.Unquote(q)
		after = rest[len(q):]
	case rest[0] == '\'':
		end := -1
		for i := 1; i < len(rest) && end < 0; i++ {
			switch {
			case rest[i] != '\'':
			case strings.HasPrefix(rest[i:], "''"):
				i++
			default:
				end = i
			}
		}
		if end < 0 {
			return "", 0, false
		}
		key, after = strings.ReplaceAll(rest[1:end], "''", "'"), rest[end+1:]
	default:
		i := strings.Index(rest, ": ")
		if i < 0 {
			if !strings.HasSuffix(rest, ":") {
				return "", 0, false
			}
			i = len(rest) - 1
		}
		key, after = rest[:i], rest[i:]
	}

	switch {
	case after == ":":
		return key, -1, true
	case !strings.HasPrefix(after, ": "):
		return "", 0, false
	}
	return key, len(rest) - len(after) + 2, true
}

func withIndex(segs []string, n int) []string {
	idx := "[" + strconv.Itoa(n) + "]"
	if len(segs) == 0 {
		return []string{idx}
	}
	out := slices.Clone(segs)
	out[len(out)-1] += idx

	return out
}

// find returns the index of the first line of an op in ops on the field
// segs, skipping continuations, or -1.
func find(lines []line, segs []string, ops ...diffmatchpatch.Operation) int {
	return slices.IndexFunc(lines, func(l line) bool {
		return !l.cont && slices.Contains(ops, l.op) && slices.Equal(l.segs, segs)
	})
}

// markUnknown marks the field segs as known only after apply: its "+" line's
// value is replaced; its equal line is split into the old value removed and
// the unknown one added; a "-" line only is followed by the unknown one
// added; and when the field is absent, a line is added after the deepest
// ancestor present, naming the missing levels dotted. It returns the new
// lines, and whether it placed the field.
func markUnknown(lines []line, segs []string) ([]line, bool) {
	if len(segs) == 0 {
		return lines, false
	}

	i := find(lines, segs, opInsert, opEqual)
	if i < 0 {
		i = find(lines, segs, opDelete)
	}
	if i >= 0 {
		if lines[i].value < 0 {
			return lines, false
		}
		return setValue(lines, i, knownAfterApply, true), true
	}

	k, anchor, indent, ok := ancestor(lines, segs)
	if !ok {
		return lines, false
	}
	text := strings.Repeat(" ", indent) + strings.Join(segs[k:], ".") + ": " + knownAfterApply
	added := line{op: opInsert, text: text, segs: segs, value: len(text) - len(knownAfterApply)}

	return slices.Insert(lines, anchor+1, added), true
}

// markSensitive replaces every value of the field segs with "(sensitive
// value)", on each side. It returns the new lines, and whether it replaced
// any.
func markSensitive(lines []line, segs []string) ([]line, bool) {
	sensitive := plan.FormatValue(&plan.Value{Sensitive: true})
	changed := false
	for i := 0; i < len(lines); i++ {
		if l := lines[i]; !l.cont && l.value >= 0 && slices.Equal(l.segs, segs) {
			lines = setValue(lines, i, sensitive, false)
			changed = true
		}
	}

	return lines, changed
}

// setValue sets the scalar value of line i to v, dropping its continuation
// lines. With split, the old value stays as removed lines and v is added
// after them, unless line i is already added. Without, the value is set on
// each side the value spans: a changed value stays a change.
func setValue(lines []line, i int, v string, split bool) []line {
	l := lines[i]
	end := i + 1
	for end < len(lines) && lines[end].cont && slices.Equal(lines[end].segs, l.segs) {
		end++
	}
	span := lines[i:end]
	set := l
	set.text = l.text[:l.value] + v
	with := func(op diffmatchpatch.Operation) line { s := set; s.op = op; return s }

	var out []line
	switch {
	case split && l.op != opInsert:
		// The old value is the header and its continuations on the current
		// side, all removed.
		for _, c := range span {
			if c.op != opInsert {
				c.op = opDelete
				out = append(out, c)
			}
		}
		out = append(out, with(opInsert))
	case slices.ContainsFunc(span, func(c line) bool { return c.op != l.op }):
		out = []line{with(opDelete), with(opInsert)}
	default:
		out = []line{set}
	}

	return slices.Replace(lines, i, end, out...)
}

// ancestor finds the deepest ancestor of segs on the desired side: the
// number of its path segments, the index of the last line of its subtree,
// and its children's column. The document itself is the ancestor of last
// resort. ok is false when the deepest ancestor holds a scalar, which can
// have no children.
func ancestor(lines []line, segs []string) (int, int, int, bool) {
	for k := len(segs) - 1; k >= 1; k-- {
		at := segs[:k]
		i := slices.IndexFunc(lines, func(l line) bool {
			return desired(l.op) && !l.cont && (slices.Equal(l.segs, at) || slices.Equal(l.item, at))
		})
		if i < 0 {
			continue
		}
		children := lines[i].itemCol
		if !slices.Equal(lines[i].item, at) {
			if !lines[i].parent {
				return 0, 0, 0, false
			}
			children = lines[i].children
		}
		last := i
		for j := i + 1; j < len(lines); j++ {
			if desired(lines[j].op) && within(lines[j].segs, at) {
				last = j
			}
		}

		return k, last, children, true
	}

	last := -1
	for j, l := range lines {
		if desired(l.op) {
			last = j
		}
	}

	return 0, last, 0, last >= 0
}

// markReplace ends the "+" line of the field segs with the replacement note.
// When the field has no "+" line of its own, the first "+" line in its
// subtree is marked; failing that, in an added diff, the "+" line of its
// deepest ancestor. It reports whether it marked a line.
func markReplace(lines []line, segs []string, added bool) bool {
	if len(segs) == 0 {
		return false
	}
	i := find(lines, segs, opInsert)
	if i < 0 {
		i = slices.IndexFunc(lines, func(l line) bool { return l.op == opInsert && !l.cont && within(l.segs, segs) })
	}
	for k := len(segs) - 1; i < 0 && added && k >= 1; k-- {
		at := segs[:k]
		i = slices.IndexFunc(lines, func(l line) bool {
			return l.op == opInsert && !l.cont && (slices.Equal(l.segs, at) || slices.Equal(l.item, at))
		})
	}
	if i < 0 || strings.HasSuffix(lines[i].text, forcesReplacement) {
		return false
	}
	lines[i].text += forcesReplacement

	return true
}

// within reports whether the path segs is in the subtree at the path at,
// which includes a list's items: spec.ports[0] is within spec.ports.
func within(segs, at []string) bool {
	n := len(at)
	if len(segs) < n || !slices.Equal(segs[:n-1], at[:n-1]) {
		return false
	}

	return segs[n-1] == at[n-1] || strings.HasPrefix(segs[n-1], at[n-1]+"[")
}

func sortedChanges(changes []plan.FieldChange) []plan.FieldChange {
	return slices.SortedStableFunc(slices.Values(changes), func(a, b plan.FieldChange) int {
		return strings.Compare(a.Path, b.Path)
	})
}
