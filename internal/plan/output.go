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
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"reflect"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	"github.com/crossplane/cli/v2/internal/health"
)

// The output formats simulate commands share. A unified diff format
// deliberately does not exist yet: crossplane-diff renders real
// full-manifest unified diffs today, and that renderer is what a future
// -o diff should be once its machinery moves into this repo, rather than a
// lower-fidelity imitation built from the plan layer's field changes.
const (
	FormatPlan = "plan"
	FormatJSON = "json"
	FormatYAML = "yaml"
)

// Summary counts plans by what they would do.
type Summary struct {
	Create      int `json:"create"`
	Update      int `json:"update"`
	Destroy     int `json:"destroy"`
	Replace     int `json:"replace"`
	NoOp        int `json:"noop"`
	Approximate int `json:"approximate"`
	Errored     int `json:"errored"`
}

// Changes is how many plans would create, update, replace or delete.
func (s Summary) Changes() int {
	return s.Create + s.Update + s.Destroy + s.Replace
}

// Summarize counts the given plans.
func Summarize(plans []*ResourcePlan) Summary {
	var s Summary
	for _, p := range plans {
		if p.Error != "" {
			s.Errored++
			continue
		}
		if p.Approximate {
			s.Approximate++
		}
		switch p.Action {
		case ActionCreate:
			s.Create++
		case ActionUpdate:
			s.Update++
		case ActionDelete:
			s.Destroy++
		case ActionReplace:
			s.Replace++
		case ActionNoOp:
			s.NoOp++
		}
	}
	return s
}

// ResultFormatVersion versions the serialized result schema so automation
// consuming it can detect incompatible changes. The schema is alpha; the
// version moves to 1.0 when it stabilizes.
const ResultFormatVersion = "0.2"

// Result is the serialized form of a simulate run, for -o json|yaml. Errored
// is the primary gate for wrapping automation: when true, at least one
// resource could not be planned and the run's answer is incomplete.
type Result struct {
	FormatVersion string          `json:"formatVersion"`
	Errored       bool            `json:"errored"`
	Summary       Summary         `json:"summary"`
	Plans         []*ResourcePlan `json:"plans"`
}

// HealthSummary counts what is not healthy before apply. Composites counts
// the composite entries whose own health is not healthy; Failing, NotReady
// and Paused count the other resources, a nested composite without health of
// its own on an entry included. Each is counted once however many composites
// or plans reach it.
type HealthSummary struct {
	Composites int `json:"unhealthyComposites"`
	Failing    int `json:"failing"`
	NotReady   int `json:"notReady"`
	Paused     int `json:"paused"`
}

// HealthCounts returns the health counts of plans: each resource once, under
// the state its first plan reports, matched across API versions.
func HealthCounts(plans []*ResourcePlan) health.Counts {
	t := &health.Tally{}
	for _, p := range plans {
		t.Resource(health.Key(p.APIVersion, p.Kind, p.Namespace, p.Name), p.Health)
	}
	return t.Counts()
}

// Failing reports whether any plan's resource is failing, which sets exit
// bit 4.
func Failing(plans []*ResourcePlan) bool {
	return slices.ContainsFunc(plans, func(p *ResourcePlan) bool { return p.Health != nil && p.Health.State == health.Failing })
}

// MarshalJSON writes the result with its plans' health counts under
// summary.health. YAML goes through it too. Decoding needs no counterpart:
// summary decodes into Summary, and the health counts come from Plans.
func (r Result) MarshalJSON() ([]byte, error) {
	type summary struct {
		Summary

		Health HealthSummary `json:"health"`
	}
	c := HealthCounts(r.Plans)
	return json.Marshal(struct {
		FormatVersion string          `json:"formatVersion"`
		Errored       bool            `json:"errored"`
		Summary       summary         `json:"summary"`
		Plans         []*ResourcePlan `json:"plans"`
	}{
		FormatVersion: r.FormatVersion,
		Errored:       r.Errored,
		Summary:       summary{Summary: r.Summary, Health: HealthSummary{Failing: c.Failing, NotReady: c.NotReady, Paused: c.Paused}},
		Plans:         r.Plans,
	})
}

// NewResult assembles the serializable Result for the given plans.
func NewResult(plans []*ResourcePlan) Result {
	s := Summarize(plans)
	return Result{
		FormatVersion: ResultFormatVersion,
		Errored:       s.Errored > 0,
		Summary:       s,
		Plans:         plans,
	}
}

// A Renderer writes resource plans to w in one output format.
type Renderer interface {
	Render(w io.Writer, plans []*ResourcePlan) error
}

// NewRenderer returns the Renderer for the given output format. With
// fullMessages the plan format prints health condition messages in full,
// wrapped, rather than cut to one line; the structured formats always carry
// them in full.
func NewRenderer(format string, fullMessages bool) (Renderer, error) {
	switch format {
	case FormatPlan:
		return planRenderer{fullMessages: fullMessages}, nil
	case FormatJSON:
		return jsonRenderer{}, nil
	case FormatYAML:
		return yamlRenderer{}, nil
	}
	return nil, errors.Errorf("unknown output format %q", format)
}

// jsonRenderer renders the serialized Result as indented JSON.
type jsonRenderer struct{}

// Render writes the plans to w as JSON.
func (jsonRenderer) Render(w io.Writer, plans []*ResourcePlan) error {
	out, err := json.MarshalIndent(NewResult(plans), "", "  ")
	if err != nil {
		return errors.Wrap(err, "cannot marshal plans to JSON")
	}
	_, err = fmt.Fprintln(w, string(out))
	return err
}

// yamlRenderer renders the serialized Result as YAML.
type yamlRenderer struct{}

// Render writes the plans to w as YAML.
func (yamlRenderer) Render(w io.Writer, plans []*ResourcePlan) error {
	out, err := yaml.Marshal(NewResult(plans))
	if err != nil {
		return errors.Wrap(err, "cannot marshal plans to YAML")
	}
	_, err = fmt.Fprint(w, string(out))
	return err
}

// planRenderer renders the human-readable plan format: a header and change
// tree per resource, marked when its resource is not healthy before apply,
// then a summary, a Health: line when anything is not healthy, and warnings.
type planRenderer struct {
	// fullMessages prints health condition messages in full.
	fullMessages bool
}

// Render writes the plans to w in the human-readable plan format.
func (pr planRenderer) Render(w io.Writer, plans []*ResourcePlan) error {
	for _, p := range plans {
		RenderPlan(w, p, "", pr.fullMessages)
	}
	s := Summarize(plans)
	RenderSummaryLine(w, s)
	if line := health.SummaryLine(HealthCounts(plans)); line != "" {
		_, _ = fmt.Fprintln(w, line)
	}
	RenderSummaryWarnings(w, plans, s)
	return nil
}

// RenderPlan prints one resource's header, its health when it is not
// healthy, note when not empty, and change tree. A delete prints only its
// header, health and note, Terraform-style, though it carries actual-only
// changes. With fullMessages a health condition message prints in full,
// wrapped, rather than cut to one line.
func RenderPlan(w io.Writer, p *ResourcePlan, note string, fullMessages bool) {
	_, _ = fmt.Fprintln(w, header(p))
	for i, l := range health.Lines(p.Health, fullMessages) {
		indent := "    "
		if i > 0 {
			indent += "    "
		}
		_, _ = fmt.Fprintln(w, indent+l)
	}
	if note != "" {
		_, _ = fmt.Fprintf(w, "    %s\n", note)
	}
	if p.Error != "" {
		_, _ = fmt.Fprintf(w, "    cannot plan: %s\n", p.Error)
		_, _ = fmt.Fprintln(w)
		return
	}
	if p.Approximate {
		_, _ = fmt.Fprintf(w, "    (approximate: %s)\n", p.FallbackReason)
	}
	if p.Action != ActionDelete {
		renderChangeTree(w, p.Changes)
	}
	_, _ = fmt.Fprintln(w)
}

// header returns the per-resource line, e.g.
// "[~] Key.kms.aws.upbound.io my-key (update)".
func header(p *ResourcePlan) string {
	gvk := p.GroupVersionKind()
	name := p.Name
	if p.Namespace != "" {
		name = p.Namespace + "/" + name
	}
	what := string(p.Action)
	if p.Error != "" {
		what = "error"
	}
	return fmt.Sprintf("%s %s.%s %s (%s)", symbol(p), gvk.Kind, gvk.Group, name, what)
}

func symbol(p *ResourcePlan) string {
	if p.Error != "" {
		return "[!]"
	}
	if p.Approximate {
		return "[?]"
	}
	switch p.Action {
	case ActionCreate:
		return "[+]"
	case ActionUpdate:
		return "[~]"
	case ActionReplace:
		return "[-/+]"
	case ActionDelete:
		return "[-]"
	case ActionNoOp:
		return "[=]"
	}
	return "[?]"
}

// renderChangeTree prints changes as the user's manifest, annotated: nested
// field names as YAML-style keys, changed leaves marked inline.
func renderChangeTree(w io.Writer, changes []FieldChange) {
	var prev []string
	for _, c := range SortedChanges(changes) {
		if c.Actual == nil && c.Planned == nil && !c.RequiresReplace {
			// No value on either side renders no leaf line, so printing its
			// parent keys would leave them dangling with nothing beneath. A
			// change that forces replacement is the exception: hiding it would
			// leave a replace with nothing that explains it.
			continue
		}
		segs := DisplayLevels(c.Path)
		if len(segs) == 0 {
			continue
		}
		parents, leaf := segs[:len(segs)-1], segs[len(segs)-1]

		// Print the parent keys that differ from the previous change's path.
		common := 0
		for common < len(parents) && common < len(prev) && parents[common] == prev[common] {
			common++
		}
		for i := common; i < len(parents); i++ {
			_, _ = fmt.Fprintf(w, "    %s%s:\n", strings.Repeat("  ", i), parents[i])
		}
		prev = parents

		indent := "    " + strings.Repeat("  ", len(parents))
		note := ""
		if c.RequiresReplace {
			note = "  # forces replacement"
		}
		switch {
		case c.Actual != nil && c.Planned != nil:
			if actual, planned, ok := plainArrays(c.Actual, c.Planned); ok {
				renderArrayChange(w, indent, leaf, actual, planned, note)
				continue
			}
			_, _ = fmt.Fprintf(w, "%s~ %s: %s -> %s%s\n", indent, leaf, FormatValue(c.Actual), FormatValue(c.Planned), note)
		case c.Planned != nil:
			_, _ = fmt.Fprintf(w, "%s+ %s: %s%s\n", indent, leaf, FormatValue(c.Planned), note)
		case c.Actual != nil:
			_, _ = fmt.Fprintf(w, "%s- %s: %s%s\n", indent, leaf, FormatValue(c.Actual), note)
		default:
			_, _ = fmt.Fprintf(w, "%s! %s: (value not reported)%s\n", indent, leaf, note)
		}
	}
}

// RenderSummaryLine prints the "Plan: ..." line counting s.
func RenderSummaryLine(w io.Writer, s Summary) {
	line := fmt.Sprintf("Plan: %d to add, %d to change, %d to destroy", s.Create, s.Update, s.Destroy)
	if s.Replace > 0 {
		verb := "requires"
		if s.Replace > 1 {
			verb = "require"
		}
		line += fmt.Sprintf(", %d %s replacement", s.Replace, verb)
	}
	_, _ = fmt.Fprintln(w, line+".")
}

// RenderSummaryWarnings prints the warnings that follow the summary line:
// replacements, approximate results and resources that could not be planned.
func RenderSummaryWarnings(w io.Writer, plans []*ResourcePlan, s Summary) {
	if s.Replace > 0 {
		named := 0
		for _, p := range plans {
			if p.Error == "" && p.Action == ActionReplace && len(p.ReplacePaths) > 0 {
				named++
			}
		}
		_, _ = fmt.Fprintln(w)
		if named > 0 {
			_, _ = fmt.Fprintf(w, "Warning: %s force replacement: %s.\n", pluralize(named, "resource has fields that", "resources have fields that"), strings.Join(replaceFields(plans), ", "))
		}
		if unnamed := s.Replace - named; unnamed > 0 {
			// The provider decided on a replacement without flagging any
			// reported change as the cause. That happens when the forcing
			// change is one its plan does not report as a field: a collection
			// growing or shrinking, or a parameter behind an unresolved
			// Secret reference.
			_, _ = fmt.Fprintf(w, "Warning: %s replacement, but the provider did not report which field forces it.\n", pluralize(unnamed, "resource requires", "resources require"))
		}
		_, _ = fmt.Fprintln(w, "Crossplane providers never destroy and recreate to apply a change.")
		_, _ = fmt.Fprintln(w, "Applying this leaves the resource unsynced until it is replaced manually.")
	}

	if s.Approximate > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintf(w, "Warning: %s approximate, computed client-side without a provider diff\n", pluralize(s.Approximate, "result is", "results are"))
		_, _ = fmt.Fprintln(w, "server: no replacement detection, no computed values.")
	}

	if s.Errored > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintf(w, "Warning: %s could not be planned.\n", pluralize(s.Errored, "resource", "resources"))
	}
}

func pluralize(n int, singular, plural string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %s", n, plural)
}

// replaceFields returns the deduplicated, sorted field names (last path
// segments) that force replacement across all plans.
func replaceFields(plans []*ResourcePlan) []string {
	set := map[string]struct{}{}
	for _, p := range plans {
		for _, f := range lastSegments(p.ReplacePaths) {
			set[f] = struct{}{}
		}
	}
	return slices.Sorted(maps.Keys(set))
}

func lastSegments(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		segs := DisplayLevels(p)
		if len(segs) == 0 {
			continue
		}
		out = append(out, segs[len(segs)-1])
	}
	return out
}

// SortedChanges returns a copy of changes ordered by path, keeping the
// original order of changes that share one.
func SortedChanges(changes []FieldChange) []FieldChange {
	return slices.SortedStableFunc(slices.Values(changes), func(a, b FieldChange) int {
		return strings.Compare(a.Path, b.Path)
	})
}

// plainArrays returns both sides of a change as arrays when both are
// concrete array values.
func plainArrays(actual, planned *Value) ([]any, []any, bool) {
	if actual.Unknown || actual.Sensitive || actual.Unresolved ||
		planned.Unknown || planned.Sensitive || planned.Unresolved {
		return nil, nil, false
	}
	a, ok := actual.Value.([]any)
	if !ok {
		return nil, nil, false
	}
	p, ok := planned.Value.([]any)
	if !ok {
		return nil, nil, false
	}
	return a, p, true
}

// renderArrayChange renders a whole-array change element by element. The
// differ reports an array whose element count changed as a single change;
// dumping both sides as JSON buries e.g. a one-step Composition pipeline
// insertion in two full copies of the pipeline. Aligning on the longest
// common prefix and suffix shows only the elements that differ.
func renderArrayChange(w io.Writer, indent, leaf string, actual, planned []any, note string) {
	prefix := 0
	for prefix < len(actual) && prefix < len(planned) && reflect.DeepEqual(actual[prefix], planned[prefix]) {
		prefix++
	}
	suffix := 0
	for suffix < len(actual)-prefix && suffix < len(planned)-prefix &&
		reflect.DeepEqual(actual[len(actual)-1-suffix], planned[len(planned)-1-suffix]) {
		suffix++
	}

	_, _ = fmt.Fprintf(w, "%s~ %s (%d -> %d items):%s\n", indent, leaf, len(actual), len(planned), note)
	inner := indent + "  "
	aMid := actual[prefix : len(actual)-suffix]
	pMid := planned[prefix : len(planned)-suffix]
	for i := 0; i < len(aMid) || i < len(pMid); i++ {
		switch {
		case i < len(aMid) && i < len(pMid):
			_, _ = fmt.Fprintf(w, "%s~ [%d]: %s -> %s\n", inner, prefix+i, formatElement(aMid[i]), formatElement(pMid[i]))
		case i < len(pMid):
			_, _ = fmt.Fprintf(w, "%s+ [%d]: %s\n", inner, prefix+i, formatElement(pMid[i]))
		default:
			_, _ = fmt.Fprintf(w, "%s- [%d]: %s\n", inner, prefix+i, formatElement(aMid[i]))
		}
	}
}

// formatElement renders one array element the way FormatValue renders a
// concrete value.
func formatElement(v any) string {
	return FormatValue(&Value{Value: v})
}

// FormatValue renders one side of a change for humans.
func FormatValue(v *Value) string {
	switch {
	case v == nil:
		return "(none)"
	case v.Sensitive:
		return "(sensitive value)"
	case v.Unknown:
		return "(known after apply)"
	case v.Unresolved:
		return "(unresolved reference)"
	}
	switch t := v.Value.(type) {
	case nil:
		return "null"
	case string:
		return fmt.Sprintf("%q", t)
	case map[string]any, []any:
		out, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprintf("%v", t)
		}
		return string(out)
	default:
		return fmt.Sprintf("%v", t)
	}
}
