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
	"cmp"
	"maps"
	"reflect"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	apiextensionsv1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1"

	dt "github.com/crossplane/cli/v2/internal/diff/result"
	"github.com/crossplane/cli/v2/internal/plan"
)

// noteProviderPlanNotRun says the cluster-layer result was not refined by
// provider diff servers, so its plans say nothing about the cloud resources.
const noteProviderPlanNotRun = "Cloud-layer plan not run (provider diff servers not yet wired)."

// FromXRReport maps an xr or project diff run onto plan.Result: one plan per
// resource diff (the XR's own included), each grouped under its input XR or
// claim, and one Composites entry per input.
func FromXRReport(r *dt.XRReport) plan.Result {
	if r == nil {
		r = &dt.XRReport{}
	}

	a := newAssembler()
	owned := make([]dt.OutputError, 0, len(r.Groups))

	for _, g := range r.Groups {
		// The group's XR is the input's identity, so a claim input keeps the
		// claim's kind and name.
		xr := resourceRef(g.XR)
		c := plan.CompositeResult{Ref: xr, Status: plan.CompositeUnchanged}

		switch {
		case g.Err != nil:
			c.Status = plan.CompositeError
			a.addError(xr, *g.Err, &xr)
			owned = append(owned, *g.Err)
		case hasChanges(g.Diffs):
			c.Status = plan.CompositeChanged
		}

		a.composites = append(a.composites, c)
		a.addDiffs(g.Diffs, &xr)
	}

	a.addRunErrors(r.Errors, owned)

	return a.result(slices.Concat(r.Warnings, dt.XRDryRunWarnings(r.Groups)))
}

// FromCompReport maps a composition diff run onto plan.Result: a plan and a
// Compositions entry per Composition, and a Composites entry per affected
// composite, with that composite's downstream plans grouped under it.
func FromCompReport(o *dt.CompDiffOutput) plan.Result {
	if o == nil {
		o = &dt.CompDiffOutput{}
	}

	a := newAssembler()
	owned := make([]dt.OutputError, 0, len(o.Errors))
	compositions := make([]plan.CompositionResult, 0, len(o.Compositions))

	for i := range o.Compositions {
		comp := &o.Compositions[i]
		ref := plan.ResourceRef{
			APIVersion: apiextensionsv1.CompositionGroupVersionKind.GroupVersion().String(),
			Kind:       apiextensionsv1.CompositionKind,
			Name:       comp.Name,
		}

		cr := plan.CompositionResult{Ref: ref, ImpactAnalysisSkipped: comp.ImpactAnalysisSkipped}
		// A zero RevisionImpact means the comparison never ran; reporting
		// it would claim the composition creates no revision.
		if ri := comp.RevisionImpact; ri.Determined() {
			cr.RevisionImpact = &plan.RevisionImpact{
				ChangeScope:         ri.ChangeScope,
				CreatesRevision:     ri.CreatesRevision,
				RepointedComposites: ri.RepointedComposites,
				PredictedRevision:   ri.PredictedRevisionName,
			}
		}
		compositions = append(compositions, cr)

		switch {
		case comp.Error != nil:
			// A composition-level error is not in the run's Errors list.
			a.addError(ref, *comp.Error, nil)
		case comp.CompositionDiff != nil:
			a.addDiff(comp.CompositionDiff, nil)
		default:
			// Unchanged, or changed only in fields masked from the diff.
			a.plans = append(a.plans, &plan.ResourcePlan{
				APIVersion: ref.APIVersion, Kind: ref.Kind, Name: ref.Name, Action: plan.ActionNoOp,
			})
		}

		for _, impact := range comp.ImpactAnalysis {
			xr := resourceRef(impact.ObjectReference)
			c := compositeResult(impact)
			c.Composition = &ref
			if impact.Error != nil {
				a.addError(xr, *impact.Error, &xr)
				owned = append(owned, *impact.Error)
			}
			if c.Status == plan.CompositeDeferred {
				a.deferred++
			}

			a.composites = append(a.composites, c)
			a.addDiffs(impact.Diffs, &xr)
		}
	}

	a.addRunErrors(o.Errors, owned)

	res := a.result(slices.Concat(o.Warnings, dt.CompDryRunWarnings(o)))
	res.Compositions = compositions

	return res
}

// compositeResult maps an affected composite's impact onto its status. A
// Manual-policy composite is deferred: applying changes nothing for it until
// its revision advances. Other filtered composites are skipped.
func compositeResult(impact dt.XRImpact) plan.CompositeResult {
	c := plan.CompositeResult{Ref: resourceRef(impact.ObjectReference), Pinned: impact.Pinned}

	switch impact.Status {
	case dt.XRStatusChanged:
		c.Status = plan.CompositeChanged
	case dt.XRStatusUnchanged:
		c.Status = plan.CompositeUnchanged
	case dt.XRStatusError:
		c.Status = plan.CompositeError
	case dt.XRStatusFiltered:
		c.Status = plan.CompositeSkipped
		if impact.FilterReason == dt.FilterReasonManualPolicy {
			c.Status = plan.CompositeDeferred
		}
		c.Reason = string(impact.FilterReason)
		c.Detail = impact.FilterDetail
	default:
		c.Status = plan.CompositeStatus(impact.Status)
	}

	return c
}

// assembler accumulates one run's plans, composites and errors.
type assembler struct {
	plans      []*plan.ResourcePlan
	composites []plan.CompositeResult
	errors     []plan.Error
	deferred   int
	// listed holds, per resource, the unredacted changes already planned,
	// so a change two inputs reach identically is listed once.
	listed map[string][]listedChange
}

type listedChange struct {
	action  plan.Action
	changes []plan.FieldChange
	dryRun  *plan.Verification
}

func newAssembler() *assembler {
	return &assembler{plans: []*plan.ResourcePlan{}, listed: map[string][]listedChange{}}
}

// addDiffs adds a plan per diff, in the structured renderer's canonical order.
func (a *assembler) addDiffs(diffs map[string]*dt.ResourceDiff, composite *plan.ResourceRef) {
	for _, d := range slices.SortedFunc(maps.Values(diffs), diffOrder) {
		a.addDiff(d, composite)
	}
}

// addDiff adds the plan for d under composite, unless an identical change to
// the same resource is already listed (under the first composite reaching
// it). Differing renders of one resource are all listed, as the structured
// renderer's flat view lists them.
func (a *assembler) addDiff(d *dt.ResourceDiff, composite *plan.ResourceRef) {
	p := resourcePlan(d)
	p.Composite = composite

	key := d.GetDiffKey()
	lc := listedChange{action: p.Action, changes: p.Changes, dryRun: p.DryRun}
	if slices.ContainsFunc(a.listed[key], func(l listedChange) bool { return reflect.DeepEqual(l, lc) }) {
		return
	}
	a.listed[key] = append(a.listed[key], lc)

	// Redacted after the duplicate check, so two Secrets differing only in
	// their redacted values are not mistaken for one change.
	p.Changes = redact(d.Gvk, p.Changes)
	a.plans = append(a.plans, p)
}

// addError records e, failing the plan for ref. composite is the composite
// the plan groups under, nil for a Composition.
func (a *assembler) addError(ref plan.ResourceRef, e dt.OutputError, composite *plan.ResourceRef) {
	a.plans = append(a.plans, &plan.ResourcePlan{
		APIVersion: ref.APIVersion, Kind: ref.Kind, Name: ref.Name, Namespace: ref.Namespace,
		Error: e.Message, Composite: composite,
	})
	a.errors = append(a.errors, planError(e, &ref))
}

// addRunErrors records the errors in all that no resource owns: all is the
// run's union error list, and owned the errors already recorded against a
// resource. Each owned entry consumes one equal entry of all.
func (a *assembler) addRunErrors(all, owned []dt.OutputError) {
	rest := slices.Clone(all)
	for _, o := range owned {
		if i := slices.IndexFunc(rest, func(e dt.OutputError) bool { return reflect.DeepEqual(e, o) }); i >= 0 {
			rest = slices.Delete(rest, i, i+1)
		}
	}

	for _, e := range rest {
		a.errors = append(a.errors, planError(e, nil))
	}
}

func (a *assembler) result(warnings []dt.OutputWarning) plan.Result {
	res := plan.NewResult(a.plans)
	res.Summary.Deferred = a.deferred
	res.Composites = a.composites
	res.Errors = a.errors
	res.Errored = res.Errored || len(a.errors) > 0
	res.Notes = []string{noteProviderPlanNotRun}

	for _, w := range warnings {
		res.Warnings = append(res.Warnings, plan.Warning{Message: w.Message, Context: w.Context})
	}

	return res
}

// resourcePlan maps one diff onto a plan. The engine's DiffType is the
// verdict: it already discounts defaulting noise, so the action never depends
// on whether any field changes remain.
func resourcePlan(d *dt.ResourceDiff) *plan.ResourcePlan {
	p := &plan.ResourcePlan{
		APIVersion: d.Gvk.GroupVersion().String(),
		Kind:       d.Gvk.Kind,
		// ResourceName carries the "(generated)" display name.
		Name:      d.ResourceName,
		Namespace: d.Namespace,
		DryRun:    verification(d.DryRun),
	}

	switch d.DiffType {
	case dt.DiffTypeAdded:
		p.Action = plan.ActionCreate
		p.Changes = fieldChanges(nil, d.Desired.Clean)
	case dt.DiffTypeModified:
		p.Action = plan.ActionUpdate
		if d.Current.Clean != nil && d.Desired.Clean != nil {
			p.Changes = fieldChanges(d.Current.Clean, d.Desired.Clean)
		}
	case dt.DiffTypeRemoved:
		// A delete carries what is removed, like Terraform's "before".
		p.Action = plan.ActionDelete
		p.Changes = fieldChanges(d.Current.Clean, nil)
	case dt.DiffTypeEqual:
		// Clean is nil for an equal diff.
		p.Action = plan.ActionNoOp
	}

	return p
}

// fieldChanges diffs the Clean views, which have ignored paths, status and
// server-managed metadata already stripped, minus the fields the plan's
// header already shows.
func fieldChanges(actual, planned *unstructured.Unstructured) []plan.FieldChange {
	changes := plan.FieldChanges(object(actual), object(planned))

	return slices.DeleteFunc(changes, func(c plan.FieldChange) bool {
		switch c.Path {
		case "apiVersion", "kind", "metadata.name", "metadata.namespace":
			return true
		}

		return false
	})
}

func object(u *unstructured.Unstructured) map[string]any {
	if u == nil {
		return nil
	}

	return u.Object
}

// redact marks a Secret's data and stringData values sensitive.
func redact(gvk schema.GroupVersionKind, changes []plan.FieldChange) []plan.FieldChange {
	if gvk.Group != "" || gvk.Kind != "Secret" {
		return changes
	}

	out := slices.Clone(changes)
	for i, c := range out {
		if !isSecretValue(c.Path) {
			continue
		}
		if c.Actual != nil {
			out[i].Actual = &plan.Value{Sensitive: true}
		}
		if c.Planned != nil {
			out[i].Planned = &plan.Value{Sensitive: true}
		}
	}

	return out
}

func isSecretValue(path string) bool {
	for _, field := range []string{"data", "stringData"} {
		if rest, ok := strings.CutPrefix(path, field); ok && (rest == "" || rest[0] == '.' || rest[0] == '[') {
			return true
		}
	}

	return false
}

// diffOrder is the structured renderer's canonical order: kind, then name,
// then the full key.
func diffOrder(a, b *dt.ResourceDiff) int {
	return cmp.Or(
		cmp.Compare(a.Gvk.Kind, b.Gvk.Kind),
		cmp.Compare(a.ResourceName, b.ResourceName),
		cmp.Compare(a.GetDiffKey(), b.GetDiffKey()),
	)
}

// hasChanges reports whether any diff is not equal, the structured
// renderer's rule for a changed XR.
func hasChanges(diffs map[string]*dt.ResourceDiff) bool {
	for _, d := range diffs {
		if d.DiffType != dt.DiffTypeEqual {
			return true
		}
	}

	return false
}

func resourceRef(o corev1.ObjectReference) plan.ResourceRef {
	return plan.ResourceRef{APIVersion: o.APIVersion, Kind: o.Kind, Name: o.Name, Namespace: o.Namespace}
}

func verification(d *dt.DryRunInfo) *plan.Verification {
	if d == nil {
		return nil
	}

	return &plan.Verification{
		Performed:      d.Performed,
		SkipReason:     string(d.SkipReason),
		Detail:         d.Detail,
		StorageVersion: d.StorageVersion,
	}
}

func planError(e dt.OutputError, resource *plan.ResourceRef) plan.Error {
	out := plan.Error{Resource: resource, Message: e.Message}

	for _, f := range e.ValidationFailures {
		vf := plan.ValidationFailure{
			APIVersion: f.APIVersion, Kind: f.Kind, Name: f.Name, Namespace: f.Namespace, Status: f.Status,
		}
		for _, fe := range f.Errors {
			vf.Errors = append(vf.Errors, plan.FieldValidationError{Type: fe.Type, Field: fe.Field, Message: fe.Message, Value: fe.Value})
		}
		out.ValidationFailures = append(out.ValidationFailures, vf)
	}

	return out
}
