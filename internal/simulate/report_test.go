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
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	dt "github.com/crossplane/cli/v2/internal/diff/result"
	"github.com/crossplane/cli/v2/internal/plan"
)

var (
	cmGVK  = schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}
	secGVK = schema.GroupVersionKind{Version: "v1", Kind: "Secret"}

	xrRef    = corev1.ObjectReference{APIVersion: "example.org/v1", Kind: "XNetwork", Name: "prod"}
	xrPlan   = plan.ResourceRef{APIVersion: "example.org/v1", Kind: "XNetwork", Name: "prod"}
	compPlan = plan.ResourceRef{APIVersion: "apiextensions.crossplane.io/v1", Kind: "Composition", Name: "net"}
)

func obj(gvk schema.GroupVersionKind, name string, fields map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{}}
	maps.Copy(u.Object, fields)
	u.SetGroupVersionKind(gvk)
	u.SetName(name)
	return u
}

func rdiff(gvk schema.GroupVersionKind, name string, t dt.DiffType, current, desired map[string]any) *dt.ResourceDiff {
	d := &dt.ResourceDiff{Gvk: gvk, ResourceName: name, DiffType: t}
	if current != nil {
		d.Current.Clean = obj(gvk, name, current)
	}
	if desired != nil {
		d.Desired.Clean = obj(gvk, name, desired)
	}
	return d
}

func diffs(ds ...*dt.ResourceDiff) map[string]*dt.ResourceDiff {
	m := map[string]*dt.ResourceDiff{}
	for _, d := range ds {
		m[d.GetDiffKey()] = d
	}
	return m
}

func notes() []string { return []string{noteProviderPlanNotRun} }

// held returns plans as a run's ProviderPlans, each planned from the one of
// ds with its key. nil plans stay nil: no provider plan ran.
func held(plans map[string]*plan.ResourcePlan, ds ...*dt.ResourceDiff) *ProviderPlans {
	if plans == nil {
		return nil
	}
	h := map[string]*dt.ResourceDiff{}
	for _, d := range ds {
		if _, ok := plans[d.GetDiffKey()]; ok {
			h[d.GetDiffKey()] = d
		}
	}
	return &ProviderPlans{Plans: plans, Held: h}
}

// fromImpacts runs FromCompReport over one composition affecting the XRs
// whose diffs are given, and returns the plans after the composition's own.
func fromImpacts(plans *ProviderPlans, impacts ...dt.XRImpact) []*plan.ResourcePlan {
	return FromCompReport(&dt.CompDiffOutput{Compositions: []dt.CompositionDiff{{Name: "net", ImpactAnalysis: impacts}}}, plans, nil).Plans[1:]
}

func impact(ref corev1.ObjectReference, ds ...*dt.ResourceDiff) dt.XRImpact {
	return dt.XRImpact{ObjectReference: ref, Status: dt.XRStatusChanged, Diffs: diffs(ds...)}
}

func TestFromCompReport(t *testing.T) {
	composition := func(c dt.CompositionDiff) *dt.CompDiffOutput {
		if c.Name == "" {
			c.Name = "net"
		}
		return &dt.CompDiffOutput{Compositions: []dt.CompositionDiff{c}}
	}
	compNoOp := &plan.ResourcePlan{APIVersion: compPlan.APIVersion, Kind: compPlan.Kind, Name: compPlan.Name, Action: plan.ActionNoOp}
	filtered := func(name string, reason dt.FilterReason, detail string) dt.XRImpact {
		return dt.XRImpact{
			ObjectReference: corev1.ObjectReference{APIVersion: "example.org/v1", Kind: "XNetwork", Name: name},
			Status:          dt.XRStatusFiltered, FilterReason: reason, FilterDetail: detail,
		}
	}
	named := func(name string) plan.ResourceRef {
		return plan.ResourceRef{APIVersion: "example.org/v1", Kind: "XNetwork", Name: name}
	}
	xrErr := dt.OutputError{ResourceID: "XNetwork/prod", Message: "render failed"}
	dbPlan := plan.ResourceRef{APIVersion: compPlan.APIVersion, Kind: compPlan.Kind, Name: "db"}

	cases := map[string]struct {
		reason string
		in     *dt.CompDiffOutput
		want   ProjectResult
	}{
		"CompositionAndRevisionImpact": {
			reason: "A changed Composition is an ungrouped plan with a Compositions entry carrying its revision impact; affected XRs group their plans.",
			in: composition(dt.CompositionDiff{
				CompositionDiff: rdiff(schema.GroupVersionKind{Group: "apiextensions.crossplane.io", Version: "v1", Kind: "Composition"}, "net", dt.DiffTypeModified,
					map[string]any{"spec": map[string]any{"mode": "Pipeline"}},
					map[string]any{"spec": map[string]any{"mode": "Resources"}}),
				RevisionImpact: dt.RevisionImpact{ChangeScope: "spec", CreatesRevision: true, RepointedComposites: 1, PredictedRevisionName: "net-abc1234"},
				ImpactAnalysis: []dt.XRImpact{{
					ObjectReference: xrRef, Status: dt.XRStatusChanged,
					Diffs: diffs(rdiff(cmGVK, "cm", dt.DiffTypeRemoved, map[string]any{}, nil)),
				}},
			}),
			want: ProjectResult{
				Result: plan.Result{
					FormatVersion: plan.ResultFormatVersion,
					Summary:       plan.Summary{Update: 1, Destroy: 1},
					Plans: []*plan.ResourcePlan{
						{
							APIVersion: compPlan.APIVersion, Kind: "Composition", Name: "net", Action: plan.ActionUpdate,
							Changes: []plan.FieldChange{{Path: "spec.mode", Actual: &plan.Value{Value: "Pipeline"}, Planned: &plan.Value{Value: "Resources"}}},
						},
						{APIVersion: "v1", Kind: "ConfigMap", Name: "cm", Action: plan.ActionDelete, Composite: &xrPlan},
					},
				},
				Composites: []CompositeResult{{Ref: xrPlan, Status: CompositeChanged, Composition: &compPlan}},
				Compositions: []CompositionResult{{
					Ref:            compPlan,
					RevisionImpact: &RevisionImpact{ChangeScope: "spec", CreatesRevision: true, RepointedComposites: 1, PredictedRevision: "net-abc1234"},
				}},
				Notes: notes(),
			},
		},
		"FilterReasons": {
			reason: "A Manual-policy XR is deferred and counted; selector and deletion filters are skipped. Each keeps its reason and detail.",
			in: composition(dt.CompositionDiff{
				Scoped: false,
				ImpactAnalysis: []dt.XRImpact{
					filtered("pinned", dt.FilterReasonManualPolicy, "compositionRevisionRef: net-6"),
					filtered("selector", dt.FilterReasonRevisionSelectorMismatch, "channel=stable"),
					filtered("deleting", dt.FilterReasonDeleting, ""),
				},
			}),
			want: ProjectResult{
				Result: plan.Result{
					FormatVersion: plan.ResultFormatVersion,
					Summary:       plan.Summary{NoOp: 1},
					Plans:         []*plan.ResourcePlan{compNoOp},
				},
				Composites: []CompositeResult{
					{Ref: named("pinned"), Status: CompositeDeferred, Composition: &compPlan, Reason: "manualPolicy", Detail: "compositionRevisionRef: net-6", Pinned: true},
					{Ref: named("selector"), Status: CompositeSkipped, Composition: &compPlan, Reason: "revisionSelectorMismatch", Detail: "channel=stable"},
					{Ref: named("deleting"), Status: CompositeSkipped, Composition: &compPlan, Reason: "deleting"},
				},
				Compositions: []CompositionResult{{Ref: compPlan}},
				Notes:        notes(),
			},
		},
		"Pinned": {
			reason: "A pinned XR simulated with --include-pinned keeps its status and is marked pinned.",
			in: composition(dt.CompositionDiff{
				ImpactAnalysis: []dt.XRImpact{{ObjectReference: xrRef, Status: dt.XRStatusUnchanged, Pinned: true}},
			}),
			want: ProjectResult{
				Result: plan.Result{
					FormatVersion: plan.ResultFormatVersion,
					Summary:       plan.Summary{NoOp: 1},
					Plans:         []*plan.ResourcePlan{compNoOp},
				},
				Composites:   []CompositeResult{{Ref: xrPlan, Status: CompositeUnchanged, Composition: &compPlan, Pinned: true}},
				Compositions: []CompositionResult{{Ref: compPlan}},
				Notes:        notes(),
			},
		},
		"PinnedRenderedRevision": {
			reason: "A pinned XR rendered against its revision maps to its changed status, with a detail naming the revision, not deferred.",
			in: composition(dt.CompositionDiff{
				ImpactAnalysis: []dt.XRImpact{{ObjectReference: xrRef, Status: dt.XRStatusChanged, Pinned: true, RenderedRevision: "net-6"}},
			}),
			want: ProjectResult{
				Result: plan.Result{
					FormatVersion: plan.ResultFormatVersion,
					Summary:       plan.Summary{NoOp: 1},
					Plans:         []*plan.ResourcePlan{compNoOp},
				},
				Composites:   []CompositeResult{{Ref: xrPlan, Status: CompositeChanged, Composition: &compPlan, Pinned: true, Detail: "rendered against pinned revision net-6 with the project's functions"}},
				Compositions: []CompositionResult{{Ref: compPlan}},
				Notes:        notes(),
			},
		},
		"AnalysisSkippedMaskedOnly": {
			reason: "A masked-only Composition is a no-op plan; a skipped analysis is flagged on its Compositions entry.",
			in: composition(dt.CompositionDiff{
				MaskedChangesOnly:     true,
				ImpactAnalysisSkipped: true,
				RevisionImpact:        dt.RevisionImpact{ChangeScope: "metadata", CreatesRevision: true, RepointedComposites: 3},
			}),
			want: ProjectResult{
				Result: plan.Result{
					FormatVersion: plan.ResultFormatVersion,
					Summary:       plan.Summary{NoOp: 1},
					Plans:         []*plan.ResourcePlan{compNoOp},
				},
				Compositions: []CompositionResult{{
					Ref:                   compPlan,
					RevisionImpact:        &RevisionImpact{ChangeScope: "metadata", CreatesRevision: true, RepointedComposites: 3},
					ImpactAnalysisSkipped: true,
				}},
				Notes: notes(),
			},
		},
		"CompositionError": {
			reason: "A failed Composition is an error plan and a typed error, with no revision impact.",
			in: composition(dt.CompositionDiff{
				Error: &dt.OutputError{ResourceID: "Composition/net", Message: "cannot load"},
			}),
			want: ProjectResult{
				Result: plan.Result{
					FormatVersion: plan.ResultFormatVersion,
					Errored:       true,
					Summary:       plan.Summary{Errored: 1},
					Plans: []*plan.ResourcePlan{
						{APIVersion: compPlan.APIVersion, Kind: "Composition", Name: "net", Error: "cannot load"},
					},
				},
				Compositions: []CompositionResult{{Ref: compPlan}},
				Errors:       []Error{{Ref: &compPlan, Message: "cannot load"}},
				Notes:        notes(),
			},
		},
		"CompositesNameTheirComposition": {
			reason: "Each affected composite names the Composition whose change reaches it.",
			in: &dt.CompDiffOutput{Compositions: []dt.CompositionDiff{
				{Name: "net", ImpactAnalysis: []dt.XRImpact{{ObjectReference: xrRef, Status: dt.XRStatusUnchanged}}},
				{Name: "db", ImpactAnalysis: []dt.XRImpact{filtered("qa", dt.FilterReasonDeleting, "")}},
			}},
			want: ProjectResult{
				Result: plan.Result{
					FormatVersion: plan.ResultFormatVersion,
					Summary:       plan.Summary{NoOp: 2},
					Plans: []*plan.ResourcePlan{
						compNoOp,
						{APIVersion: dbPlan.APIVersion, Kind: dbPlan.Kind, Name: dbPlan.Name, Action: plan.ActionNoOp},
					},
				},
				Composites: []CompositeResult{
					{Ref: xrPlan, Status: CompositeUnchanged, Composition: &compPlan},
					{Ref: named("qa"), Status: CompositeSkipped, Composition: &dbPlan, Reason: "deleting"},
				},
				Compositions: []CompositionResult{{Ref: compPlan}, {Ref: dbPlan}},
				Notes:        notes(),
			},
		},
		"UnknownEngineStatus": {
			reason: "A status the engine adds later is reported as unknown, not passed into the public enum.",
			in: composition(dt.CompositionDiff{
				ImpactAnalysis: []dt.XRImpact{{ObjectReference: xrRef, Status: dt.XRStatus("bogus")}},
			}),
			want: ProjectResult{
				Result: plan.Result{
					FormatVersion: plan.ResultFormatVersion,
					Summary:       plan.Summary{NoOp: 1},
					Plans:         []*plan.ResourcePlan{compNoOp},
				},
				Composites:   []CompositeResult{{Ref: xrPlan, Status: CompositeUnknown, Composition: &compPlan}},
				Compositions: []CompositionResult{{Ref: compPlan}},
				Notes:        notes(),
			},
		},
		"XRErrorAndRunErrors": {
			reason: "An errored XR's error is anchored to it and not repeated; the interruption is a run error.",
			in: &dt.CompDiffOutput{
				Compositions: []dt.CompositionDiff{{
					Name:           "net",
					ImpactAnalysis: []dt.XRImpact{{ObjectReference: xrRef, Status: dt.XRStatusError, Error: &xrErr}},
				}},
				Errors: []dt.OutputError{xrErr, {Message: "interrupted"}},
			},
			want: ProjectResult{
				Result: plan.Result{
					FormatVersion: plan.ResultFormatVersion,
					Errored:       true,
					Summary:       plan.Summary{NoOp: 1, Errored: 1},
					Plans: []*plan.ResourcePlan{
						compNoOp,
						{APIVersion: "example.org/v1", Kind: "XNetwork", Name: "prod", Error: "render failed", Composite: &xrPlan},
					},
				},
				Composites:   []CompositeResult{{Ref: xrPlan, Status: CompositeError, Composition: &compPlan}},
				Compositions: []CompositionResult{{Ref: compPlan}},
				Errors:       []Error{{Ref: &xrPlan, Message: "render failed"}, {Message: "interrupted"}},
				Notes:        notes(),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := FromCompReport(tc.in, nil, nil)
			if diff := cmp.Diff(tc.want, got, cmpopts.IgnoreFields(plan.ResourcePlan{}, "Before", "After"), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("%s\nFromCompReport(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

// TestProviderPlanMerge covers each row of how a provider plan merges into the
// engine's plan for the same diff key, and the composite status upgrade.
func TestProviderPlanMerge(t *testing.T) {
	added := rdiff(cmGVK, "cm", dt.DiffTypeAdded, nil, map[string]any{"data": map[string]any{"k": "v"}})
	modified := rdiff(cmGVK, "cm", dt.DiffTypeModified,
		map[string]any{"metadata": map[string]any{"labels": map[string]any{"a": "1"}}},
		map[string]any{"metadata": map[string]any{"labels": map[string]any{"a": "2"}}})
	modified.DryRun = &dt.DryRunInfo{SkipReason: dt.DryRunSkipForbidden, Detail: "no create"}
	equal := rdiff(cmGVK, "cm", dt.DiffTypeEqual, nil, nil)
	keptEqual := rdiff(cmGVK, "cm", dt.DiffTypeEqual, nil, nil)
	keptEqual.Desired.Raw = obj(cmGVK, "cm", nil)
	key := added.GetDiffKey()

	policy := rdiff(cmGVK, "cm", dt.DiffTypeModified,
		map[string]any{"spec": map[string]any{"deletionPolicy": "Orphan", "forProvider": map[string]any{"size": "1"}}},
		map[string]any{"spec": map[string]any{"deletionPolicy": "Delete", "forProvider": map[string]any{"size": "2"}}})
	policyChange := plan.FieldChange{Path: "spec.deletionPolicy", Actual: &plan.Value{Value: "Orphan"}, Planned: &plan.Value{Value: "Delete"}}
	labelChange := []plan.FieldChange{{Path: "metadata.labels.a", Actual: &plan.Value{Value: "1"}, Planned: &plan.Value{Value: "2"}}}
	providerChange := []plan.FieldChange{{Path: "spec.forProvider.size", Actual: &plan.Value{Value: "1"}, Planned: &plan.Value{Value: "2"}}}
	forbidden := &plan.Verification{SkipReason: "forbidden", Detail: "no create"}
	cm := func(p plan.ResourcePlan) *plan.ResourcePlan {
		p.APIVersion, p.Kind, p.Name, p.Composite = "v1", "ConfigMap", "cm", &xrPlan
		return &p
	}

	cases := map[string]struct {
		reason     string
		diff       *dt.ResourceDiff
		plans      map[string]*plan.ResourcePlan
		keep       func(*unstructured.Unstructured) bool
		wantPlans  []*plan.ResourcePlan
		wantStatus CompositeStatus
	}{
		"NoProviderEntry": {
			reason:     "With no provider entry the engine plan stands, and an empty plans map drops the not-run note.",
			diff:       modified,
			plans:      map[string]*plan.ResourcePlan{},
			wantPlans:  []*plan.ResourcePlan{cm(plan.ResourcePlan{Action: plan.ActionUpdate, Changes: labelChange, DryRun: forbidden})},
			wantStatus: CompositeChanged,
		},
		"ProviderError": {
			reason:     "A provider error is an error plan keeping the engine's identity, composite and DryRun.",
			diff:       modified,
			plans:      map[string]*plan.ResourcePlan{key: {Kind: "Other", Name: "x", Error: "diff server cannot plan: boom"}},
			wantPlans:  []*plan.ResourcePlan{cm(plan.ResourcePlan{Error: "diff server cannot plan: boom", DryRun: forbidden})},
			wantStatus: CompositeChanged,
		},
		"ProviderApproximate": {
			reason: "A NotSupported fallback keeps the engine's diff, marked approximate with the provider's reason.",
			diff:   modified,
			plans: map[string]*plan.ResourcePlan{key: {
				Action: plan.ActionUpdate, Changes: providerChange, Approximate: true, FallbackReason: "the provider cannot compute a diff",
			}},
			wantPlans: []*plan.ResourcePlan{cm(plan.ResourcePlan{
				Action: plan.ActionUpdate, Changes: labelChange, DryRun: forbidden, Approximate: true, FallbackReason: "the provider cannot compute a diff",
			})},
			wantStatus: CompositeChanged,
		},
		"AddedProviderCreate": {
			reason:     "A provider create keeps the engine's changes it does not report.",
			diff:       added,
			plans:      map[string]*plan.ResourcePlan{key: {Action: plan.ActionCreate, Changes: providerChange}},
			wantPlans:  []*plan.ResourcePlan{cm(plan.ResourcePlan{Action: plan.ActionCreate, Changes: append(slices.Clone(providerChange), plan.FieldChange{Path: "data.k", Planned: &plan.Value{Value: "v"}})})},
			wantStatus: CompositeChanged,
		},
		"ModifiedProviderReplace": {
			reason: "A provider replace takes the action and replace paths, adds the engine's other changes, keeping the engine's DryRun.",
			diff:   modified,
			plans: map[string]*plan.ResourcePlan{key: {
				Action: plan.ActionReplace, Changes: providerChange, ReplacePaths: []string{"spec.forProvider.size"},
			}},
			wantPlans: []*plan.ResourcePlan{cm(plan.ResourcePlan{
				Action: plan.ActionReplace, Changes: append(slices.Clone(providerChange), labelChange...), ReplacePaths: []string{"spec.forProvider.size"}, DryRun: forbidden,
			})},
			wantStatus: CompositeChanged,
		},
		"ModifiedProviderUpdateKeepsClusterChange": {
			reason:     "A deletionPolicy change the provider does not report stays beside its update; the forProvider change it does report is listed once.",
			diff:       policy,
			plans:      map[string]*plan.ResourcePlan{key: {Action: plan.ActionUpdate, Changes: providerChange}},
			wantPlans:  []*plan.ResourcePlan{cm(plan.ResourcePlan{Action: plan.ActionUpdate, Changes: append(slices.Clone(providerChange), policyChange)})},
			wantStatus: CompositeChanged,
		},
		"ModifiedProviderNoOp": {
			reason:     "The cluster object still changes when the provider finds nothing, so the engine's update stands.",
			diff:       modified,
			plans:      map[string]*plan.ResourcePlan{key: {Action: plan.ActionNoOp}},
			wantPlans:  []*plan.ResourcePlan{cm(plan.ResourcePlan{Action: plan.ActionUpdate, Changes: labelChange, DryRun: forbidden})},
			wantStatus: CompositeChanged,
		},
		"EqualProviderUpdateUpgradesComposite": {
			reason:     "Cloud drift on an MR the cluster finds equal is an update, and makes its unchanged composite changed.",
			diff:       equal,
			plans:      map[string]*plan.ResourcePlan{key: {Action: plan.ActionUpdate, Changes: providerChange}},
			wantPlans:  []*plan.ResourcePlan{cm(plan.ResourcePlan{Action: plan.ActionUpdate, Changes: providerChange})},
			wantStatus: CompositeChanged,
		},
		"EqualProviderNoOpDropped": {
			reason:     "A kept-equal MR the provider finds unchanged is dropped, and its composite stays unchanged.",
			diff:       equal,
			plans:      map[string]*plan.ResourcePlan{key: {Action: plan.ActionNoOp}},
			wantStatus: CompositeUnchanged,
		},
		"KeptEqualWithoutPlanDropped": {
			reason:     "A kept-equal MR no provider planned is dropped; the XR's own equal diff stays a no-op plan.",
			diff:       keptEqual,
			plans:      map[string]*plan.ResourcePlan{},
			keep:       func(u *unstructured.Unstructured) bool { return u.GetKind() == "ConfigMap" },
			wantStatus: CompositeUnchanged,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// An Unchanged impact
			// carries diffs only when the engine kept equal ones.
			status := dt.XRStatusUnchanged
			if tc.diff.DiffType != dt.DiffTypeEqual {
				status = dt.XRStatusChanged
			}
			gotComp := FromCompReport(&dt.CompDiffOutput{Compositions: []dt.CompositionDiff{{
				Name:           "net",
				ImpactAnalysis: []dt.XRImpact{{ObjectReference: xrRef, Status: status, Diffs: diffs(tc.diff)}},
			}}}, held(tc.plans, tc.diff), tc.keep)
			if diff := cmp.Diff(tc.wantPlans, gotComp.Plans[1:], cmpopts.IgnoreFields(plan.ResourcePlan{}, "Before", "After"), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("%s\nFromCompReport(...).Plans: -want, +got:\n%s", tc.reason, diff)
			}
			if got := gotComp.Composites[0].Status; got != tc.wantStatus {
				t.Errorf("%s\nFromCompReport(...): want composite %q, got %q", tc.reason, tc.wantStatus, got)
			}
		})
	}
}

// TestPlanObjects covers the before and after objects each plan carries: the
// engine's Clean views, nil on the side that does not exist, Secret values
// redacted, and kept through a provider plan's merge.
func TestPlanObjects(t *testing.T) {
	data := func(v string) map[string]any { return map[string]any{"data": map[string]any{"k": v}} }
	cm := func(v string) *unstructured.Unstructured { return obj(cmGVK, "cm", data(v)) }
	redacted := obj(secGVK, "cm", data("(sensitive value)"))

	cases := map[string]struct {
		reason     string
		diff       *dt.ResourceDiff
		plans      map[string]*plan.ResourcePlan
		wantBefore *unstructured.Unstructured
		wantAfter  *unstructured.Unstructured
	}{
		"Create": {
			reason:    "A create has no before.",
			diff:      rdiff(cmGVK, "cm", dt.DiffTypeAdded, nil, data("v")),
			wantAfter: cm("v"),
		},
		"Update": {
			reason:     "An update carries both Clean views.",
			diff:       rdiff(cmGVK, "cm", dt.DiffTypeModified, data("old"), data("new")),
			wantBefore: cm("old"),
			wantAfter:  cm("new"),
		},
		"Delete": {
			reason:     "A delete has no after.",
			diff:       rdiff(cmGVK, "cm", dt.DiffTypeRemoved, data("gone"), nil),
			wantBefore: cm("gone"),
		},
		"Secret": {
			reason:     "A Secret's data values are redacted on both sides, as its changes are.",
			diff:       rdiff(secGVK, "cm", dt.DiffTypeModified, data("old"), data("new")),
			wantBefore: redacted,
			wantAfter:  redacted,
		},
		"ProviderSensitive": {
			reason: "A path the provider marks sensitive is redacted in before and after, as in the changes.",
			diff:   rdiff(cmGVK, "cm", dt.DiffTypeModified, data("old"), data("new")),
			plans: map[string]*plan.ResourcePlan{rdiff(cmGVK, "cm", dt.DiffTypeModified, nil, nil).GetDiffKey(): {
				Action: plan.ActionUpdate,
				Changes: []plan.FieldChange{{
					Path: "data.k", Actual: &plan.Value{Sensitive: true}, Planned: &plan.Value{Sensitive: true},
				}},
			}},
			wantBefore: obj(cmGVK, "cm", data("(sensitive value)")),
			wantAfter:  obj(cmGVK, "cm", data("(sensitive value, changed)")),
		},
		"ProviderPlanned": {
			reason: "A provider plan replaces the changes but keeps the engine's objects.",
			diff:   rdiff(cmGVK, "cm", dt.DiffTypeModified, data("old"), data("new")),
			plans: map[string]*plan.ResourcePlan{rdiff(cmGVK, "cm", dt.DiffTypeModified, nil, nil).GetDiffKey(): {
				Action: plan.ActionReplace, Before: cm("provider"), After: cm("provider"),
			}},
			wantBefore: cm("old"),
			wantAfter:  cm("new"),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := fromImpacts(held(tc.plans, tc.diff), impact(xrRef, tc.diff))
			if len(got) != 1 {
				t.Fatalf("%s\nFromCompReport(...): want 1 plan, got %d", tc.reason, len(got))
			}
			if diff := cmp.Diff(tc.wantBefore, got[0].Before); diff != "" {
				t.Errorf("%s\nFromCompReport(...).Before: -want, +got:\n%s", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.wantAfter, got[0].After); diff != "" {
				t.Errorf("%s\nFromCompReport(...).After: -want, +got:\n%s", tc.reason, diff)
			}
		})
	}

	t.Run("SecretSourceUntouched", func(t *testing.T) {
		d := rdiff(secGVK, "cm", dt.DiffTypeAdded, nil, data("v"))
		fromImpacts(nil, impact(xrRef, d))
		if diff := cmp.Diff(obj(secGVK, "cm", data("v")), d.Desired.Clean); diff != "" {
			t.Errorf("FromCompReport(...): redaction must not modify the engine's view: -want, +got:\n%s", diff)
		}
	})
}

// TestSecretDottedKeysRedactedInObjects pins that before and after redact
// Secret keys containing dots.
func TestSecretDottedKeysRedactedInObjects(t *testing.T) {
	d := rdiff(secGVK, "creds", dt.DiffTypeModified,
		map[string]any{"data": map[string]any{"tls.crt": "b2xk", ".dockerconfigjson": "b2xk"}},
		map[string]any{"data": map[string]any{"tls.crt": "bmV3", ".dockerconfigjson": "bmV3"}})
	for _, p := range fromImpacts(nil, impact(xrRef, d)) {
		if p.Kind != "Secret" {
			continue
		}
		for _, o := range []*unstructured.Unstructured{p.Before, p.After} {
			for _, k := range []string{"tls.crt", ".dockerconfigjson"} {
				if v, _, _ := unstructured.NestedString(o.Object, "data", k); v != "(sensitive value)" {
					t.Errorf("data[%q] = %q, want redacted", k, v)
				}
			}
		}
	}
}

func TestCompositeReason(t *testing.T) {
	cases := map[dt.FilterReason]CompositeReason{
		dt.FilterReasonManualPolicy:             CompositeReasonManualPolicy,
		dt.FilterReasonRevisionSelectorMismatch: CompositeReasonRevisionSelectorMismatch,
		dt.FilterReasonDeleting:                 CompositeReasonDeleting,
		"":                                      "",
		"something_new":                         CompositeReasonUnknown,
	}
	for in, want := range cases {
		if got := compositeReason(in); got != want {
			t.Errorf("compositeReason(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestProviderPlanOnlyForItsRendering pins that a provider plan merges only
// into a diff that renders its resource as the planned diff did: another XR's
// differing rendering of the same resource keeps its engine plan.
func TestProviderPlanOnlyForItsRendering(t *testing.T) {
	data := func(v string) map[string]any { return map[string]any{"data": map[string]any{"k": v}} }
	prod := rdiff(cmGVK, "shared", dt.DiffTypeAdded, nil, data("v"))
	qa := rdiff(cmGVK, "shared", dt.DiffTypeAdded, nil, data("other"))
	qaRef := corev1.ObjectReference{APIVersion: "example.org/v1", Kind: "XNetwork", Name: "qa"}
	provider := []plan.FieldChange{{Path: "spec.forProvider.size", Planned: &plan.Value{Value: "2"}}}
	plans := held(map[string]*plan.ResourcePlan{prod.GetDiffKey(): {Action: plan.ActionCreate, Changes: provider}}, prod)

	got := fromImpacts(plans, impact(xrRef, prod), impact(qaRef, qa))

	want := []*plan.ResourcePlan{
		{
			APIVersion: "v1", Kind: "ConfigMap", Name: "shared", Action: plan.ActionCreate, Composite: &xrPlan,
			Changes: append(slices.Clone(provider), plan.FieldChange{Path: "data.k", Planned: &plan.Value{Value: "v"}}),
		},
		{
			APIVersion: "v1", Kind: "ConfigMap", Name: "shared", Action: plan.ActionCreate,
			Composite: new(plan.ResourceRef{APIVersion: "example.org/v1", Kind: "XNetwork", Name: "qa"}),
			Changes:   []plan.FieldChange{{Path: "data.k", Planned: &plan.Value{Value: "other"}}},
		},
	}
	if diff := cmp.Diff(want, got, cmpopts.IgnoreFields(plan.ResourcePlan{}, "Before", "After")); diff != "" {
		t.Errorf("FromCompReport(...).Plans: want prod's plan with the provider's change and qa's without: -want, +got:\n%s", diff)
	}

	// Another XR's removal of the resource is never planned, so never takes prod's create.
	if p := plans.For(rdiff(cmGVK, "shared", dt.DiffTypeRemoved, data("v"), nil)); p != nil {
		t.Errorf("For(removal) = %v, want no plan", p)
	}
}
