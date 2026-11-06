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
	"strings"

	"github.com/sergi/go-diff/diffmatchpatch"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	sigsyaml "sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/fieldpath"

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

// AnnotateCompReport returns o with each affected composite's diffs annotated
// with their provider plans (see ProviderPlans.For and annotateDiff), for the
// -o diff renderer. It never mutates o. An unchanged composite
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
// changes, such as defaults, show only in -o plan. Whatever pp is, each data
// and stringData value of a core Secret is sensitive, in an added, modified
// or removed diff.
func annotateDiff(d *dt.ResourceDiff, pp *plan.ResourcePlan) *dt.ResourceDiff {
	// A Secret's values are redacted with or without a plan.
	var secret []plan.FieldChange
	if d.DiffType != dt.DiffTypeEqual {
		for _, p := range plan.SecretPaths(d.Current.Clean, d.Desired.Clean) {
			secret = append(secret, plan.FieldChange{Path: p, Actual: &plan.Value{Sensitive: true}})
		}
		// A Secret missing a view its diff needs can't be read for keys:
		// a sensitive change makes objectDiff hide the body.
		if d.Gvk.Group == "" && d.Gvk.Kind == "Secret" &&
			((d.DiffType != dt.DiffTypeRemoved && d.Desired.Clean == nil) ||
				(d.DiffType != dt.DiffTypeAdded && d.Current.Clean == nil)) {
			secret = append(secret, plan.FieldChange{Path: "data", Actual: &plan.Value{Sensitive: true}})
		}
	}
	if pp == nil || pp.Error != "" || pp.Approximate {
		pp = &plan.ResourcePlan{}
	}
	if len(pp.Changes) == 0 && len(secret) == 0 && (d.DiffType != dt.DiffTypeEqual || !isChange(pp.Action)) {
		return d
	}

	switch d.DiffType {
	case dt.DiffTypeAdded, dt.DiffTypeModified:
	case dt.DiffTypeEqual:
		return driftDiff(d, pp)
	case dt.DiffTypeRemoved:
		// Only sensitive values change a removed diff: nothing is planned
		// or replaced on a delete.
		var sens []plan.FieldChange
		for _, c := range pp.Changes {
			if (c.Actual != nil && c.Actual.Sensitive) || (c.Planned != nil && c.Planned.Sensitive) {
				sens = append(sens, plan.FieldChange{Path: c.Path, Actual: &plan.Value{Sensitive: true}})
			}
		}
		pp = &plan.ResourcePlan{Changes: sens}
	}

	changes := append(plan.SortedChanges(pp.Changes), secret...)
	diffs, des, changed, err := objectDiff(d, changes)
	if err != nil {
		out := *d
		// An insert, so --compact, which prints only changes, keeps it.
		out.LineDiffs = []diffmatchpatch.Diff{{Type: opInsert, Text: diffHidden + "\n"}}
		return &out
	}

	lines := flatten(diffs)
	for _, c := range changes {
		if c.RequiresReplace {
			changed = markReplace(lines, des, c.Path, d.DiffType == dt.DiffTypeAdded) || changed
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
// marks sensitive, on either side, replaced as redactPaths does, and
// each computed value set to "(known after apply)" on the desired side, as
// setPath sets it. The diff is made again from the copies, as
// compare.GenerateDiff makes it, so no secret reaches the text. It returns an
// error when a sensitive value can't be redacted: the caller must hide the
// body. It also returns the desired copy the diff's new side marshals, nil
// for a diff without its desired view.
func objectDiff(d *dt.ResourceDiff, changes []plan.FieldChange) ([]diffmatchpatch.Diff, *unstructured.Unstructured, bool, error) {
	var paths []string
	for _, c := range changes {
		if (c.Actual != nil && c.Actual.Sensitive) || (c.Planned != nil && c.Planned.Sensitive) {
			paths = append(paths, c.Path)
		}
	}
	// An added diff has no current side and a removed one no desired side;
	// any other side must have its view.
	if len(paths) > 0 && ((d.DiffType != dt.DiffTypeRemoved && d.Desired.Clean == nil) ||
		(d.DiffType != dt.DiffTypeAdded && d.Current.Clean == nil)) {
		return nil, nil, false, errors.New("a diff without its objects can't be redacted")
	}

	cur, des := d.Current.Clean, d.Desired.Clean.DeepCopy()
	changed := len(paths) > 0
	if changed {
		var err error
		if cur, des, err = redactPaths(cur, des, paths); err != nil {
			return nil, nil, false, err
		}
	}
	for _, c := range changes {
		if c.Planned != nil && c.Planned.Unknown {
			changed = setPath(des, c.Path, knownAfterApply) || changed
		}
	}
	if !changed {
		return d.LineDiffs, des, false, nil
	}

	old, err := text(cur)
	if err != nil {
		return nil, nil, false, err
	}
	nu, err := text(des)
	if err != nil {
		return nil, nil, false, err
	}

	return compare.GetLineDiff(old, nu), des, true, nil
}

// text returns u as compare.GenerateDiff writes it, "" for nil.
func text(u *unstructured.Unstructured) (string, error) {
	if u == nil {
		return "", nil
	}
	b, err := sigsyaml.Marshal(u.Object)
	return string(b), err
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

// A line is one line of a diff body.
type line struct {
	op   diffmatchpatch.Operation
	text string
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
			lines = append(lines, line{op: d.Type, text: t})
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

// probe is set at a field to find its lines: it has no spaces, so it is never
// wrapped, and no YAML special characters, so it is never quoted.
const probe = "CROSSPLANE-PROBE-7f3c"

// markReplace ends with the replacement note the first "+" line of the field
// at path in des, the desired object the diff's new side marshals. With the
// field absent, in an added diff, it marks its nearest present ancestor's
// first "+" line; a path running into a string, such as a JSON policy, marks
// the string's. It reports whether it marked a line.
//
// The field's lines are found by a probe: des is marshalled again with the
// field's value replaced, and the lines that differ from the original text
// are the field's, because sigsyaml writes keys sorted, so the lines before
// and after a value don't depend on it, and the line diff keeps lines whole.
//
// ponytail: a field is found only where setPath reaches it, so a keyed
// item or index the desired side lacks gets no mark in a modified diff, and a
// keyed segment matching several items marks the first "+" line from the
// first match to the last. The header, the summary and -o plan still say
// replace.
func markReplace(lines []line, des *unstructured.Unstructured, path string, added bool) bool {
	segs, err := fieldpath.Parse(path)
	if err != nil || des == nil {
		return false
	}
	orig, err := text(des)
	if err != nil {
		return false
	}
	for n := len(segs); n >= 1; n-- {
		if n < len(segs) && !added && !scalarAt(des, segs[:n]) {
			// Only a create marks an ancestor, unless the path runs into
			// a string, such as a JSON policy, which holds the field.
			continue
		}
		from, to, ok := fieldLines(orig, des, segs[:n])
		if !ok {
			continue
		}
		// Count the new side's lines, the equal and added ones, to find the
		// field's first added line.
		at := 0
		for i, l := range lines {
			if l.op == opDelete {
				continue
			}
			if l.op == opInsert && at >= from && at < to {
				// A line already marked keeps its one marker; the next
				// line may be a field that forces nothing.
				if strings.HasSuffix(l.text, forcesReplacement) {
					return false
				}
				lines[i].text += forcesReplacement
				return true
			}
			at++
		}
		return false
	}
	return false
}

// scalarAt reports whether des holds a string, number or bool at segs, a
// path of fields and indexes.
func scalarAt(des *unstructured.Unstructured, segs fieldpath.Segments) bool {
	v, err := fieldpath.Pave(des.Object).GetValue(segs.String())
	if err != nil {
		return false
	}
	switch v.(type) {
	case map[string]any, []any, nil:
		return false
	}
	return true
}

// fieldLines returns the range of lines of orig, the text of des, that the
// field at path is on, or false when des lacks it.
func fieldLines(orig string, des *unstructured.Unstructured, segs fieldpath.Segments) (int, int, bool) {
	c := des.DeepCopy()
	if !setSegments(c, segs, probe) {
		return 0, 0, false
	}
	got, err := text(c)
	if err != nil {
		return 0, 0, false
	}
	a, b := strings.Split(orig, "\n"), strings.Split(got, "\n")
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	// A field setPath had to create holds no line of orig: it is absent.
	return pre, len(a) - suf, len(a)-suf > pre
}
