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
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	"github.com/crossplane/cli/v2/internal/plan"
)

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

// CompositeReason is why a composite was deferred or skipped.
type CompositeReason string

const (
	// CompositeReasonManualPolicy means the composite has a Manual
	// compositionUpdatePolicy, so it is pinned to its current revision.
	CompositeReasonManualPolicy CompositeReason = "manualPolicy"
	// CompositeReasonRevisionSelectorMismatch means the composite's
	// compositionRevisionSelector does not match the changed revision.
	CompositeReasonRevisionSelectorMismatch CompositeReason = "revisionSelectorMismatch"
	// CompositeReasonDeleting means the composite is being deleted.
	CompositeReasonDeleting CompositeReason = "deleting"
	// CompositeReasonUnknown means the engine filtered the composite for a
	// reason this version does not recognize.
	CompositeReasonUnknown CompositeReason = "unknown"
)

// CompositeResult is the per-composite outcome of a project simulate run.
type CompositeResult struct {
	Ref    plan.ResourceRef `json:"ref"`
	Status CompositeStatus  `json:"status"`
	// Reason is the machine-readable reason a composite was deferred or
	// skipped: manualPolicy, revisionSelectorMismatch, deleting or unknown.
	Reason CompositeReason `json:"reason,omitempty"`
	// Detail elaborates on Reason, e.g. the pinned revision.
	Detail string `json:"detail,omitempty"`
	// Pinned marks a composite whose Manual compositionUpdatePolicy pins it
	// to a composition revision. With status deferred it was not simulated;
	// with any other status it was simulated anyway, under --include-pinned
	// or rendered against its pinned revision (see Detail).
	Pinned bool `json:"pinned,omitempty"`
	// Composition is the Composition whose change reaches this composite,
	// set only for a composition simulate run.
	Composition *plan.ResourceRef `json:"composition,omitempty"`
}

// Error is an error from a simulate run, optionally anchored to a resource.
type Error struct {
	Ref                *plan.ResourceRef   `json:"ref,omitempty"`
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
	Ref            plan.ResourceRef `json:"ref"`
	RevisionImpact *RevisionImpact  `json:"revisionImpact,omitempty"`
	// ImpactAnalysisSkipped means the composites that would adopt the
	// Composition's revision were deliberately not simulated, so no
	// changed, unchanged or error verdict exists for them. Composites the
	// revision would not reach are still listed in ProjectResult.Composites.
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

// ProjectResult is the serialized form of a project simulate run, for -o
// json|yaml: resource simulate's plan.Result plus the composites,
// compositions, errors, warnings and notes only a project run has. Its
// summary serializes as Result.Summary plus a deferred count taken from
// Composites.
type ProjectResult struct {
	plan.Result

	Composites   []CompositeResult   `json:"composites,omitempty"`
	Compositions []CompositionResult `json:"compositions,omitempty"`
	Errors       []Error             `json:"errors,omitempty"`
	Warnings     []Warning           `json:"warnings,omitempty"`
	// Notes are statements about how the result was produced, e.g. that the
	// provider plans did not run.
	Notes []string `json:"notes,omitempty"`
}

// NewProjectResult assembles the ProjectResult for the given plans and
// composites; errs marks the result errored.
func NewProjectResult(plans []*plan.ResourcePlan, composites []CompositeResult, errs []Error) ProjectResult {
	res := ProjectResult{Result: plan.NewResult(plans), Composites: composites, Errors: errs}
	res.Errored = res.Errored || len(errs) > 0
	return res
}

// Deferred counts the composites pinned to a composition revision, which
// applying changes nothing for. It does not count as a change.
func (r ProjectResult) Deferred() int {
	n := 0
	for _, c := range r.Composites {
		if c.Status == CompositeDeferred {
			n++
		}
	}
	return n
}

// MarshalJSON writes the result with summary, composites and compositions
// ahead of the plans, which can be long, and with the deferred count inside
// summary. YAML goes through it too. Decoding needs no counterpart: summary
// decodes into Result.Summary, and Deferred is counted from Composites.
func (r ProjectResult) MarshalJSON() ([]byte, error) {
	type summary struct {
		plan.Summary

		Deferred int `json:"deferred"`
	}
	return json.Marshal(struct {
		FormatVersion string               `json:"formatVersion"`
		Errored       bool                 `json:"errored"`
		Summary       summary              `json:"summary"`
		Composites    []CompositeResult    `json:"composites,omitempty"`
		Compositions  []CompositionResult  `json:"compositions,omitempty"`
		Plans         []*plan.ResourcePlan `json:"plans"`
		Errors        []Error              `json:"errors,omitempty"`
		Warnings      []Warning            `json:"warnings,omitempty"`
		Notes         []string             `json:"notes,omitempty"`
	}{
		FormatVersion: r.FormatVersion,
		Errored:       r.Errored,
		Summary:       summary{Summary: r.Summary, Deferred: r.Deferred()},
		Composites:    r.Composites,
		Compositions:  r.Compositions,
		Plans:         r.Plans,
		Errors:        r.Errors,
		Warnings:      r.Warnings,
		Notes:         r.Notes,
	})
}

// A Renderer writes a project simulate result to w in one output format.
type Renderer interface {
	Render(w io.Writer, r ProjectResult) error
}

// NewRenderer returns the Renderer for the given output format: one of
// plan.FormatPlan, plan.FormatJSON or plan.FormatYAML.
func NewRenderer(format string) (Renderer, error) {
	switch format {
	case plan.FormatPlan:
		return planRenderer{}, nil
	case plan.FormatJSON:
		return jsonRenderer{}, nil
	case plan.FormatYAML:
		return yamlRenderer{}, nil
	}
	return nil, errors.Errorf("unknown output format %q", format)
}

// jsonRenderer renders the ProjectResult as indented JSON.
type jsonRenderer struct{}

// Render writes the result to w as JSON.
func (jsonRenderer) Render(w io.Writer, r ProjectResult) error {
	out, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return errors.Wrap(err, "cannot marshal plans to JSON")
	}
	_, err = fmt.Fprintln(w, string(out))
	return err
}

// yamlRenderer renders the ProjectResult as YAML.
type yamlRenderer struct{}

// Render writes the result to w as YAML.
func (yamlRenderer) Render(w io.Writer, r ProjectResult) error {
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
func (planRenderer) Render(w io.Writer, r ProjectResult) error {
	impacts := revisionImpactLines(r.Compositions)
	if len(r.Composites) == 0 && !slices.ContainsFunc(r.Plans, func(p *plan.ResourcePlan) bool { return p.Composite != nil }) {
		for _, p := range r.Plans {
			plan.RenderPlan(w, p, impacts[p.ResourceRef()])
		}
	} else {
		renderGrouped(w, r, impacts)
	}
	plan.RenderSummaryLine(w, r.Summary)
	if d := r.Deferred(); d > 0 {
		_, _ = fmt.Fprintf(w, "%s deferred (pinned to a composition revision).\n", pluralize(d, "composite resource", "composite resources"))
	}
	plan.RenderSummaryWarnings(w, r.Plans, r.Summary)
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
func revisionImpactLines(comps []CompositionResult) map[plan.ResourceRef]string {
	lines := map[plan.ResourceRef]string{}
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
func renderGrouped(w io.Writer, r ProjectResult, impacts map[plan.ResourceRef]string) {
	var refs []plan.ResourceRef
	byRef := map[plan.ResourceRef][]*plan.ResourcePlan{}
	status := map[plan.ResourceRef]CompositeResult{}
	for _, c := range r.Composites {
		if _, ok := status[c.Ref]; !ok {
			refs = append(refs, c.Ref)
		}
		status[c.Ref] = c
	}
	for _, p := range r.Plans {
		if p.Composite == nil {
			plan.RenderPlan(w, p, impacts[p.ResourceRef()])
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
func renderComposite(w io.Writer, c CompositeResult, ref plan.ResourceRef, plans []*plan.ResourcePlan) {
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
		if p.Action == plan.ActionNoOp && p.Error == "" && p.ResourceRef() == ref {
			continue
		}
		plan.RenderPlan(&block, p, "")
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
func compositeHeader(c CompositeResult, ref plan.ResourceRef) string {
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
			h += ": " + string(c.Reason)
		}
		h += ")"
	case CompositeChanged, CompositeUnchanged, CompositeError:
	}
	if c.Pinned && c.Detail == "" && c.Status != CompositeDeferred {
		h += " (pinned)"
	}
	return h
}

func pluralize(n int, singular, plural string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %s", n, plural)
}
