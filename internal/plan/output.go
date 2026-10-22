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
	"slices"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
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
	// Deferred counts composites pinned to a composition revision, which
	// applying changes nothing for. It does not count as a change.
	Deferred int `json:"deferred"`
}

// Summarize counts the given plans. It cannot know about deferred
// composites; whoever builds Composites sets Summary.Deferred.
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
const ResultFormatVersion = "0.3"

// CompositeStatus is the outcome of simulating one composite resource.
type CompositeStatus string

// The statuses a composite can have.
const (
	CompositeChanged   CompositeStatus = "changed"
	CompositeUnchanged CompositeStatus = "unchanged"
	CompositeError     CompositeStatus = "error"
	// CompositeDeferred means the composite is pinned to a composition
	// revision, so applying changes nothing for it yet.
	CompositeDeferred CompositeStatus = "deferred"
	// CompositeSkipped means the composite was not simulated, for Reason.
	CompositeSkipped CompositeStatus = "skipped"
)

// CompositeResult is the per-composite outcome of a project simulate run.
type CompositeResult struct {
	Ref    ResourceRef     `json:"ref"`
	Status CompositeStatus `json:"status"`
	// Reason is the machine-readable reason a composite was deferred or
	// skipped.
	Reason string `json:"reason,omitempty"`
	// Detail elaborates on Reason, e.g. the pinned revision.
	Detail string `json:"detail,omitempty"`
	// Pinned marks a composite simulated although it is pinned to a
	// composition revision, previewing the moment that revision advances.
	Pinned bool `json:"pinned,omitempty"`
	// Composition is the Composition whose change reaches this composite,
	// set only for a composition simulate run.
	Composition *ResourceRef `json:"composition,omitempty"`
}

// Error is an error from a simulate run, optionally anchored to a resource.
type Error struct {
	Ref                *ResourceRef        `json:"ref,omitempty"`
	Message            string              `json:"message"`
	ValidationFailures []ValidationFailure `json:"validationFailures,omitempty"`
}

// ValidationFailure is one resource that failed schema validation.
type ValidationFailure struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name,omitempty"`
	Namespace  string `json:"namespace,omitempty"`
	// Status is the validator's outcome, e.g. invalid or missingSchema.
	Status string                 `json:"status"`
	Errors []FieldValidationError `json:"errors,omitempty"`
}

// FieldValidationError is one field-level validation error.
type FieldValidationError struct {
	// Type categorizes the error: schema, cel, unknownField or defaulting.
	Type    string `json:"type"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
	Value   any    `json:"value,omitempty"`
}

// Warning is an advisory that did not fail the run.
type Warning struct {
	Message string            `json:"message"`
	Context map[string]string `json:"context,omitempty"`
}

// CompositionResult is the outcome for one changed Composition.
type CompositionResult struct {
	Ref            ResourceRef     `json:"ref"`
	RevisionImpact *RevisionImpact `json:"revisionImpact,omitempty"`
	// ImpactAnalysisSkipped means the composites that would adopt the
	// Composition's revision were deliberately not simulated, so no
	// changed, unchanged or error verdict exists for them. Composites the
	// revision would not reach are still listed in Result.Composites.
	ImpactAnalysisSkipped bool `json:"impactAnalysisSkipped,omitempty"`
}

// RevisionImpact says whether applying a Composition creates a revision, and
// which composites would adopt it.
type RevisionImpact struct {
	// ChangeScope is none, metadata or spec.
	ChangeScope         string `json:"changeScope"`
	CreatesRevision     bool   `json:"createsRevision"`
	RepointedComposites int    `json:"repointedComposites"`
	// PredictedRevision is the revision's predicted name; empty when it
	// cannot be predicted.
	PredictedRevision string `json:"predictedRevision,omitempty"`
}

// Result is the serialized form of a simulate run, for -o json|yaml. Errored
// is the primary gate for wrapping automation: when true, at least one
// resource could not be planned and the run's answer is incomplete.
type Result struct {
	FormatVersion string              `json:"formatVersion"`
	Errored       bool                `json:"errored"`
	Summary       Summary             `json:"summary"`
	Composites    []CompositeResult   `json:"composites,omitempty"`
	Compositions  []CompositionResult `json:"compositions,omitempty"`
	Plans         []*ResourcePlan     `json:"plans"`
	Errors        []Error             `json:"errors,omitempty"`
	Warnings      []Warning           `json:"warnings,omitempty"`
	// Notes are statements about how the result was produced, e.g. that a
	// planning layer did not run.
	Notes []string `json:"notes,omitempty"`
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

// A Renderer writes a simulate result to w in one output format.
type Renderer interface {
	Render(w io.Writer, r Result) error
}

// NewRenderer returns the Renderer for the given output format.
func NewRenderer(format string) (Renderer, error) {
	switch format {
	case FormatPlan:
		return planRenderer{}, nil
	case FormatJSON:
		return jsonRenderer{}, nil
	case FormatYAML:
		return yamlRenderer{}, nil
	}
	return nil, errors.Errorf("unknown output format %q", format)
}

// jsonRenderer renders the serialized Result as indented JSON.
type jsonRenderer struct{}

// Render writes the result to w as JSON.
func (jsonRenderer) Render(w io.Writer, r Result) error {
	out, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return errors.Wrap(err, "cannot marshal plans to JSON")
	}
	_, err = fmt.Fprintln(w, string(out))
	return err
}

// yamlRenderer renders the serialized Result as YAML.
type yamlRenderer struct{}

// Render writes the result to w as YAML.
func (yamlRenderer) Render(w io.Writer, r Result) error {
	out, err := yaml.Marshal(r)
	if err != nil {
		return errors.Wrap(err, "cannot marshal plans to YAML")
	}
	_, err = fmt.Fprint(w, string(out))
	return err
}

// planRenderer renders the human-readable plan format: a header and change
// tree per resource, then a summary with warnings, then any Notes. When any
// plan or composite names a composite, plans print grouped under an
// "XR: Kind.group name" header. A Composition's revision impact prints as
// one line under its plan. Run-level Errors and Warnings are not printed;
// the command reports them on stderr.
type planRenderer struct{}

// Render writes the result to w in the human-readable plan format.
func (planRenderer) Render(w io.Writer, r Result) error {
	impacts := revisionImpactLines(r.Compositions)
	if len(r.Composites) == 0 && !slices.ContainsFunc(r.Plans, func(p *ResourcePlan) bool { return p.Composite != nil }) {
		for _, p := range r.Plans {
			renderPlan(w, p, impacts[p.ResourceRef()])
		}
	} else {
		renderGrouped(w, r, impacts)
	}
	renderSummary(w, r.Plans, r.Summary)
	if len(r.Notes) > 0 {
		_, _ = fmt.Fprintln(w)
		for _, n := range r.Notes {
			_, _ = fmt.Fprintf(w, "Note: %s\n", n)
		}
	}
	return nil
}

// revisionImpactLines returns, per Composition, the line saying whether
// applying it creates a CompositionRevision and how many composites would
// adopt it, and whether their impact went unanalyzed. A Composition with
// neither to say has no line.
func revisionImpactLines(comps []CompositionResult) map[ResourceRef]string {
	lines := map[ResourceRef]string{}
	for _, c := range comps {
		var parts []string
		switch ri := c.RevisionImpact; {
		case ri == nil:
		case ri.CreatesRevision:
			parts = append(parts, fmt.Sprintf("Applying this creates a new CompositionRevision, which %s would adopt.", pluralize(ri.RepointedComposites, "composite", "composites")))
		default:
			parts = append(parts, "Applying this creates no new CompositionRevision.")
		}
		if c.ImpactAnalysisSkipped {
			parts = append(parts, "Impact analysis skipped.")
		}
		if len(parts) > 0 {
			lines[c.Ref] = strings.Join(parts, " ")
		}
	}
	return lines
}

// renderGrouped prints ungrouped plans first, then each composite (in
// Composites order, then any only plans name) with its plans indented
// beneath. A composite's own plan is omitted when it is a no-op.
func renderGrouped(w io.Writer, r Result, impacts map[ResourceRef]string) {
	var refs []ResourceRef
	byRef := map[ResourceRef][]*ResourcePlan{}
	status := map[ResourceRef]CompositeResult{}
	for _, c := range r.Composites {
		if _, ok := status[c.Ref]; !ok {
			refs = append(refs, c.Ref)
		}
		status[c.Ref] = c
	}
	for _, p := range r.Plans {
		if p.Composite == nil {
			renderPlan(w, p, impacts[p.ResourceRef()])
			continue
		}
		ref := *p.Composite
		if _, ok := status[ref]; !ok {
			if _, seen := byRef[ref]; !seen {
				refs = append(refs, ref)
			}
		}
		byRef[ref] = append(byRef[ref], p)
	}

	for _, ref := range refs {
		renderComposite(w, status[ref], ref, byRef[ref])
	}
}

// renderComposite prints one composite's header, its deferral or skip note,
// and its plans indented beneath.
func renderComposite(w io.Writer, c CompositeResult, ref ResourceRef, plans []*ResourcePlan) {
	_, _ = fmt.Fprintln(w, compositeHeader(c, ref))
	switch c.Status {
	case CompositeDeferred:
		pinned := "Pinned to a composition revision"
		if c.Detail != "" {
			pinned += " (" + c.Detail + ")"
		}
		_, _ = fmt.Fprintf(w, "    %s; apply changes nothing until the\n", pinned)
		_, _ = fmt.Fprintln(w, "    revision advances. Rerun with --include-pinned to preview that moment.")
	case CompositeSkipped, CompositeChanged, CompositeUnchanged:
		if c.Detail != "" {
			_, _ = fmt.Fprintf(w, "    %s\n", c.Detail)
		}
	case CompositeError:
	}
	var block strings.Builder
	for _, p := range plans {
		if p.Action == ActionNoOp && p.Error == "" && p.ResourceRef() == ref {
			continue
		}
		renderPlan(&block, p, "")
	}
	for line := range strings.Lines(block.String()) {
		if line != "\n" {
			line = "  " + line
		}
		_, _ = fmt.Fprint(w, line)
	}
	if block.Len() == 0 {
		_, _ = fmt.Fprintln(w)
	}
}

// compositeHeader returns e.g. "XR: XNetwork.example.org prod-network".
func compositeHeader(c CompositeResult, ref ResourceRef) string {
	gvk := schema.FromAPIVersionAndKind(ref.APIVersion, ref.Kind)
	name := ref.Name
	if ref.Namespace != "" {
		name = ref.Namespace + "/" + name
	}
	h := fmt.Sprintf("XR: %s.%s %s", gvk.Kind, gvk.Group, name)
	switch c.Status {
	case CompositeDeferred:
		h += " (deferred)"
	case CompositeSkipped:
		h += " (skipped"
		if c.Reason != "" {
			h += ": " + c.Reason
		}
		h += ")"
	case CompositeChanged, CompositeUnchanged, CompositeError:
	}
	if c.Pinned && c.Detail == "" && c.Status != CompositeDeferred {
		h += " (pinned)"
	}
	return h
}

// renderPlan prints one resource's header, note when not empty, and change
// tree. A delete prints only its header and note, Terraform-style, though it
// carries actual-only changes.
func renderPlan(w io.Writer, p *ResourcePlan, note string) {
	_, _ = fmt.Fprintln(w, header(p))
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
	for _, c := range sortedChanges(changes) {
		if c.Actual == nil && c.Planned == nil && !c.RequiresReplace {
			// No value on either side renders no leaf line, so printing its
			// parent keys would leave them dangling with nothing beneath. A
			// change that forces replacement is the exception: hiding it would
			// leave a replace with nothing that explains it.
			continue
		}
		segs := PathSegments(c.Path)
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

func renderSummary(w io.Writer, plans []*ResourcePlan, s Summary) {
	line := fmt.Sprintf("Plan: %d to add, %d to change, %d to destroy", s.Create, s.Update, s.Destroy)
	if s.Replace > 0 {
		verb := "requires"
		if s.Replace > 1 {
			verb = "require"
		}
		line += fmt.Sprintf(", %d %s replacement", s.Replace, verb)
	}
	_, _ = fmt.Fprintln(w, line+".")
	if s.Deferred > 0 {
		_, _ = fmt.Fprintf(w, "%s deferred (pinned to a composition revision).\n", pluralize(s.Deferred, "composite resource", "composite resources"))
	}

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
		segs := PathSegments(p)
		if len(segs) == 0 {
			continue
		}
		out = append(out, segs[len(segs)-1])
	}
	return out
}

func sortedChanges(changes []FieldChange) []FieldChange {
	out := make([]FieldChange, len(changes))
	copy(out, changes)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
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
