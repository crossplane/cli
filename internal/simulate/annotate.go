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
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	sigsyaml "sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	"github.com/crossplane/cli/v2/internal/diff/compare"
	dt "github.com/crossplane/cli/v2/internal/diff/result"
	"github.com/crossplane/cli/v2/internal/plan"
)

// The -o diff annotations for provider plans, as terraform writes them.
const (
	knownAfterApply    = "(known after apply)"
	forcesReplacement  = "  # forces replacement"
	opInsert, opDelete = diffmatchpatch.DiffInsert, diffmatchpatch.DiffDelete
	opEqual            = diffmatchpatch.DiffEqual
	diffHidden         = "# diff hidden: it holds sensitive values that can't be redacted"
)

// AnnotateXRReport returns r with each resource diff annotated with its
// provider plan from plans (see ProviderPlans.For and annotateDiff), for the
// -o diff renderer. It never mutates r: the result is a shallow copy with
// cloned groups and diff maps, and each annotated diff is a copy with new
// LineDiffs. Empty plans return an equivalent copy, which renders exactly as
// r does.
func AnnotateXRReport(r *dt.XRReport, plans *ProviderPlans) *dt.XRReport {
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
func AnnotateCompReport(o *dt.CompDiffOutput, plans *ProviderPlans) *dt.CompDiffOutput {
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
// is replaced when its provider plan (see ProviderPlans.For) replaces it. It
// returns nil for empty plans, so -o diff without provider plans renders as it
// always has.
func ReplacedBy(plans *ProviderPlans) func(*dt.ResourceDiff) bool {
	if plans == nil || len(plans.Plans) == 0 {
		return nil
	}

	return func(d *dt.ResourceDiff) bool {
		pp := plans.For(d)
		if pp == nil && d.DiffType == dt.DiffTypeModified {
			// The renderer sees annotated diffs, and annotateDiff turns an
			// equal diff a provider plan changes into a modified one: its
			// plan is the one held for the equal diff it was made from.
			eq := *d
			eq.DiffType = dt.DiffTypeEqual
			pp = plans.For(&eq)
		}
		return pp != nil && pp.Error == "" && !pp.Approximate && pp.Action == plan.ActionReplace
	}
}

// annotateDiffs returns a copy of diffs with each diff annotated with its
// provider plan, and whether an equal diff became a change. Unannotated
// diffs are shared, not copied. A nil map stays nil.
func annotateDiffs(diffs map[string]*dt.ResourceDiff, plans *ProviderPlans) (map[string]*dt.ResourceDiff, bool) {
	if diffs == nil {
		return nil, false
	}

	out := maps.Clone(diffs)
	drifted := false
	for k, d := range diffs {
		a := annotateDiff(d, plans.For(d))
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
// "(known after apply)", a value the provider marks sensitive on either side
// as "(sensitive value)" (on the new side "(sensitive value, changed)" when it
// changed), and the "+" line of a field that forces replacement ends
// "  # forces replacement". A diff whose sensitive values can't be redacted
// keeps its header but its body is one "# diff hidden" line. Other provider
// changes, such as defaults, show only in -o plan.
func annotateDiff(d *dt.ResourceDiff, pp *plan.ResourcePlan) *dt.ResourceDiff {
	if pp == nil || pp.Error != "" || pp.Approximate {
		return d
	}
	if len(pp.Changes) == 0 && (d.DiffType != dt.DiffTypeEqual || !isChange(pp.Action)) {
		return d
	}

	switch d.DiffType {
	case dt.DiffTypeAdded, dt.DiffTypeModified:
	case dt.DiffTypeEqual:
		return driftDiff(d, pp)
	case dt.DiffTypeRemoved:
		return d
	}

	changes := plan.SortedChanges(pp.Changes)
	diffs, changed, err := objectDiff(d, changes)
	if err != nil {
		out := *d
		// An insert, so --compact, which prints only changes, keeps it.
		out.LineDiffs = []diffmatchpatch.Diff{{Type: opInsert, Text: diffHidden + "\n"}}
		return &out
	}

	lines := flatten(diffs)
	s := place(lines)
	for _, c := range changes {
		if c.RequiresReplace {
			changed = markReplace(lines, resolve(s, plan.DisplayLevels(c.Path)), d.DiffType == dt.DiffTypeAdded) || changed
		}
	}

	if !changed {
		return d
	}

	out := *d
	out.LineDiffs = chunk(lines)

	return &out
}

// objectDiff returns d's line diff with the provider's values written into
// copies of the Clean views, and whether it wrote any: every value a change
// marks sensitive, on either side, replaced as plan.RedactPaths does, and
// each computed value set to "(known after apply)" on the desired side, as
// plan.SetPath sets it. The diff is made again from the copies, as
// compare.GenerateDiff makes it, so no secret reaches the text. It returns an
// error when a sensitive value can't be redacted: the caller must hide the
// body. A diff without its desired view keeps its line diff, unmarked.
func objectDiff(d *dt.ResourceDiff, changes []plan.FieldChange) ([]diffmatchpatch.Diff, bool, error) {
	var paths []string
	for _, c := range changes {
		if (c.Actual != nil && c.Actual.Sensitive) || (c.Planned != nil && c.Planned.Sensitive) {
			paths = append(paths, c.Path)
		}
	}
	// An added diff has no current side; any other side must have its view.
	if len(paths) > 0 && (d.Desired.Clean == nil || (d.DiffType != dt.DiffTypeAdded && d.Current.Clean == nil)) {
		return nil, false, errors.New("a diff without its objects can't be redacted")
	}
	if d.Desired.Clean == nil {
		return d.LineDiffs, false, nil
	}

	cur, des := d.Current.Clean, d.Desired.Clean.DeepCopy()
	changed := len(paths) > 0
	if changed {
		var err error
		if cur, des, err = plan.RedactPaths(cur, des, paths); err != nil {
			return nil, false, err
		}
	}
	for _, c := range changes {
		if c.Planned != nil && c.Planned.Unknown {
			changed = plan.SetPath(des, c.Path, knownAfterApply) || changed
		}
	}
	if !changed {
		return d.LineDiffs, false, nil
	}

	text := func(u *unstructured.Unstructured) (string, error) {
		if u == nil {
			return "", nil
		}
		b, err := sigsyaml.Marshal(u.Object)
		return string(b), err
	}
	old, err := text(cur)
	if err != nil {
		return nil, false, err
	}
	nu, err := text(des)
	if err != nil {
		return nil, false, err
	}

	return compare.GetLineDiff(old, nu), true, nil
}

// driftDiff renders an equal diff the provider would change, which is drift
// between the cluster and the external resource, as a modified block: a
// "- path: actual" and "+ path: planned" pair per change, with computed
// values and replacement marked as elsewhere. It returns d when no change has
// a value to print.
func driftDiff(d *dt.ResourceDiff, pp *plan.ResourcePlan) *dt.ResourceDiff {
	if !isChange(pp.Action) {
		return d
	}

	var lines []line
	for _, c := range plan.SortedChanges(pp.Changes) {
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
	if len(lines) == 0 && pp.Action == plan.ActionReplace {
		lines = append(lines, line{op: opInsert, text: "# replaced by the provider; no field reported"})
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
	// segs is the field path of the line, as plan.DisplayLevels splits it
	// with list items by index, on the side the line belongs to: the
	// desired side for an equal line.
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

// place sets the field of each line of the new side, the equal and added
// lines, from its YAML, and returns the side's document, nil when it does not
// parse.
//
// ponytail: lines are placed by gopkg.in/yaml.v3 node positions, including
// the fields of a literal (|) block scalar holding a YAML or JSON map or
// list, and a list item keyed by a field (see resolve). Sensitive values are
// redacted, and unknowns set, before this, in the objects (see objectDiff); a list index in a
// sensitive path redacts that field in every item, because upjet writes a set
// element's index as a hash, so the index can't be trusted. The coarse cases
// are:
//   - a side that doesn't parse: no marker is placed on it;
//   - a folded (>), quoted or plain multi-line scalar, a literal one that is
//     not a map or list, or a flow map or list on one line (pw: {a: x}, or a
//     JSON field that shares its line): a replacement marker inside is not
//     placed;
//   - a keyed level matching more than one item: the marker goes on the first
//     only;
//   - a key that starts or ends with a quote, which plan.DisplayLevels trims
//     as fieldpath.Parse does, gets no marker.
//
// The header, the summary and -o plan still carry what isn't marked inline.
// Upgrade path for the keys: carry fieldpath segments, not display levels,
// in plan.FieldChange.
func place(lines []line) *yaml.Node {
	return placeSide(lines, func(op diffmatchpatch.Operation) bool { return op != opDelete })
}

// placeSide parses the lines side reports true for as one YAML document, and
// places them. It returns the document's root node.
func placeSide(lines []line, side func(diffmatchpatch.Operation) bool) *yaml.Node {
	p := &placer{lines: lines}
	var b strings.Builder
	for i, l := range lines {
		if side(l.op) {
			p.index = append(p.index, i)
			b.WriteString(l.text + "\n")
		}
	}
	root := parse(b.String())
	if root != nil && last(root) > len(p.index) {
		// The tree names lines the side lacks, so its positions can't be
		// trusted: treat the side as unparsed rather than misplace a mark.
		root = nil
	}
	if root != nil {
		p.place(root, nil, 0, 0, len(p.index))
	}

	return root
}

// unbreak returns s with the runes yaml.v3 reads as line breaks, other than
// \n, replaced by a rune it does not, so its node lines and rune columns match
// the lines split on \n. It blanks a line holding only them and spaces, as
// yaml.v3 would read it, keeping the line count.
func unbreak(s string) string {
	breaks := strings.NewReplacer("\r", "\uFFFD", "\u0085", "\uFFFD", "\u2028", "\uFFFD", "\u2029", "\uFFFD")
	ls := strings.Split(s, "\n")
	for i, l := range ls {
		if strings.Trim(l, " \r\u0085\u2028\u2029") == "" {
			ls[i] = ""
			continue
		}
		ls[i] = breaks.Replace(l)
	}

	return strings.Join(ls, "\n")
}

// parse returns the root node of the YAML document s, or nil when s is empty
// or does not parse.
func parse(s string) *yaml.Node {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(unbreak(s)), &doc); err != nil || len(doc.Content) == 0 {
		return nil
	}

	return doc.Content[0]
}

// content returns the map or list, YAML or JSON, the literal block scalar n
// holds, or nil.
func content(n *yaml.Node) *yaml.Node {
	if n == nil || n.Kind != yaml.ScalarNode || n.Style&yaml.LiteralStyle == 0 {
		return nil
	}
	root := parse(n.Value)
	if root == nil || (root.Kind != yaml.MappingNode && root.Kind != yaml.SequenceNode) {
		return nil
	}

	return root
}

// A placer places the lines of one side of a diff.
type placer struct {
	lines []line
	// index[n-1] is the index in lines of the side's line n.
	index []int
	// scratch takes the writes to a line past the side's end, which a
	// well-formed node never names.
	scratch line
}

// get returns the side's line n, 1-based, to place.
func (p *placer) get(n int) *line {
	if n < 1 || n > len(p.index) {
		p.scratch = line{}
		return &p.scratch
	}

	return &p.lines[p.index[n-1]]
}

// reset returns the side's line n, cleared of any earlier placement.
func (p *placer) reset(n int) *line {
	l := p.get(n)
	*l = line{op: l.op, text: l.text, value: -1}

	return l
}

// A block is one YAML document on a side, the whole side or the content of a
// literal block scalar, whose node line and column are offset by row and col
// and whose last line is the side's line end.
type block struct {
	*placer

	row, col, end int
	// starts are the side's lines a field starts on, and scalars the values
	// that may continue on the lines after their first.
	starts  map[int]bool
	scalars []scalar
}

// A scalar is a scalar value at the path segs, starting on the side's line
// row.
type scalar struct {
	row  int
	segs []string
	node *yaml.Node
}

// place places the document root at the path segs, offset as a block is.
// The lines a scalar spans after its first are its continuations, and a
// literal one's map or list is placed in turn.
func (p *placer) place(root *yaml.Node, segs []string, row, col, end int) {
	b := &block{placer: p, row: row, col: col, end: end, starts: map[int]bool{}}
	b.node(root, segs)
	for _, s := range b.scalars {
		// Its lines run to the next field, blank ones included as a kept (|+)
		// literal holds them, or to the block's end, less trailing blank
		// lines. A blank is only spaces or tabs: U+2028 is content.
		stop := s.row
		for n := s.row + 1; n <= end && !b.starts[n]; n++ {
			if strings.Trim(p.lines[p.index[n-1]].text, " \t") != "" || b.starts[n+1] {
				stop = n
			}
		}
		for n := s.row + 1; n <= stop; n++ {
			l := p.reset(n)
			l.segs, l.cont = s.segs, true
		}
		c := content(s.node)
		if c == nil || stop == s.row || s.row+last(c) > stop {
			continue
		}
		// The content's columns are offset by the block's indentation: where
		// its root node starts on its line, less its column in the value.
		t := p.lines[p.index[s.row+c.Line-1]].text
		p.place(c, s.segs, s.row, len(t)-len(strings.TrimLeft(t, " "))-(c.Column-1), stop)
	}
}

// node places the fields of the map or list n at the path segs.
func (b *block) node(n *yaml.Node, segs []string) {
	flow := n.Style&yaml.FlowStyle != 0
	switch n.Kind { //nolint:exhaustive // Only maps and lists hold fields.
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			if flow && !alone(n, i, 2) {
				continue
			}
			k, s := n.Content[i], append(slices.Clone(segs), n.Content[i].Value)
			b.field(k, n.Content[i+1], s, flow)
		}
	case yaml.SequenceNode:
		for i, it := range n.Content {
			if flow && !alone(n, i, 1) {
				continue
			}
			s := withIndex(segs, i)
			if it.Kind != yaml.MappingNode || len(it.Content) == 0 || it.Content[0].Line != it.Line || flow {
				b.field(it, it, s, flow)
				continue
			}
			// A "- key: value" item, whose first key is on its line.
			b.node(it, s)
			l := b.get(b.row + it.Line)
			l.item, l.itemCol = s, b.col+it.Column-1
		}
	}
}

// field places the field at the path segs whose key, or list item, is at and
// whose value is v.
func (b *block) field(at, v *yaml.Node, segs []string, flow bool) {
	n := b.row + at.Line
	b.starts[n] = true
	l := b.reset(n)
	l.segs = segs

	if (v.Kind == yaml.MappingNode || v.Kind == yaml.SequenceNode) && len(v.Content) > 0 &&
		(v.Style&yaml.FlowStyle == 0 || last(v) > v.Line) {
		// A map or list on lines of its own.
		l.parent, l.children = true, b.col+v.Content[0].Column-1
		if v.Kind == yaml.SequenceNode && v.Style&yaml.FlowStyle == 0 {
			l.children = b.col + v.Column + 1
		}
		b.node(v, segs)
		return
	}

	// A scalar, or a map or list on one line.
	switch off := offset(l.text, b.col+v.Column-1); {
	case v.Line != at.Line:
	case off >= len(l.text):
		// No value written, as in "key:".
		l.parent, l.children = true, b.col+at.Column+1
	default:
		l.value = off
	}
	if v.Kind == yaml.ScalarNode && !flow {
		b.scalars = append(b.scalars, scalar{row: n, segs: segs, node: v})
	}
}

// alone reports whether the field n.Content[i] of the flow map or list n,
// with its value n.Content[i+step-1], has its lines to itself, as in
// indented JSON. A field that shares a line is left unplaced.
func alone(n *yaml.Node, i, step int) bool {
	at := n.Content[i]
	return at.Line > n.Line &&
		(i == 0 || last(n.Content[i-1]) < at.Line) &&
		(i+step >= len(n.Content) || n.Content[i+step].Line > last(n.Content[i+step-1]))
}

// last returns the last line a node in n starts on.
func last(n *yaml.Node) int {
	l := n.Line
	for _, c := range n.Content {
		l = max(l, last(c))
	}

	return l
}

// offset returns the byte offset in text of its rune column col, counted
// from 0, or len(text) past its end.
func offset(text string, col int) int {
	for i := range text {
		if col == 0 {
			return i
		}
		col--
	}

	return len(text)
}

// resolve returns levels as the document doc places them. A keyed level,
// key=value after a list, as plan.DisplayLevels splits tags[key="k"], is the
// index of the first list item whose key field holds value: tags[0]. Levels
// below a field doc lacks are kept as they are. It returns nil when doc is
// nil, or when a keyed level names an item doc lacks.
func resolve(doc *yaml.Node, levels []string) []string {
	if all := resolveAll(doc, levels); len(all) > 0 {
		return all[0]
	}

	return nil
}

// resolveAll returns levels as resolve does, once per list item a keyed
// level matches, in document order.
func resolveAll(doc *yaml.Node, levels []string) [][]string {
	if doc == nil {
		return nil
	}

	return walk(doc, make([]string, 0, len(levels)), levels)
}

// walk resolves levels below the node n at the path out.
func walk(n *yaml.Node, out, levels []string) [][]string {
	for k, lv := range levels {
		name, idx := splitIndexes(lv)
		key, want, keyed := strings.Cut(name, "=")
		switch {
		case keyed && n == nil && len(out) > 0:
			// Under a field doc lacks: a keyed item, or a key holding "=".
			return nil
		case keyed && n != nil && n.Kind == yaml.SequenceNode && len(out) > 0:
			want = strings.Trim(want, `"'`)
			var all [][]string
			for i, it := range n.Content {
				if f := lookup(it, key); f == nil || f.Kind != yaml.ScalarNode || f.Value != want {
					continue
				}
				o := slices.Clone(out)
				o[len(o)-1] += "[" + strconv.Itoa(i) + "]" + lv[len(name):]
				all = append(all, walk(descend(it, idx), o, levels[k+1:])...)
			}
			return all
		default:
			out = append(out, lv)
			n = descend(lookup(n, name), idx)
		}
	}

	return [][]string{out}
}

// descend returns the item of n at the list indexes idx, or nil, as the map
// or list a literal block scalar holds when it holds one.
func descend(n *yaml.Node, idx []int) *yaml.Node {
	for _, i := range idx {
		if n == nil || n.Kind != yaml.SequenceNode || i >= len(n.Content) {
			return nil
		}
		n = n.Content[i]
	}
	if c := content(n); c != nil {
		return c
	}

	return n
}

// lookup returns the value of key in the map n, or nil.
func lookup(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}

	return nil
}

// splitIndexes splits a display level into its key and list indexes:
// "ports[0][1]" is "ports" and [0 1].
func splitIndexes(lv string) (string, []int) {
	var idx []int
	for strings.HasSuffix(lv, "]") {
		open := strings.LastIndexByte(lv, '[')
		if open < 0 {
			break
		}
		i, err := strconv.Atoi(lv[open+1 : len(lv)-1])
		if err != nil {
			break
		}
		idx = append([]int{i}, idx...)
		lv = lv[:open]
	}

	return lv, idx
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

// markReplace ends the "+" line of the field segs with the replacement note.
// When the field has no "+" line of its own, the first "+" line in its
// subtree is marked; failing that, in an added diff, the "+" line of its
// deepest ancestor. It reports whether it marked a line.
func markReplace(lines []line, segs []string, added bool) bool {
	if len(segs) == 0 {
		return false
	}
	i := slices.IndexFunc(lines, func(l line) bool { return l.op == opInsert && !l.cont && slices.Equal(l.segs, segs) })
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
