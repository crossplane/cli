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
	xnetGVK = schema.GroupVersionKind{Group: "example.org", Version: "v1", Kind: "XNetwork"}
	cmGVK   = schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}
	secGVK  = schema.GroupVersionKind{Version: "v1", Kind: "Secret"}

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

func TestFromXRReport(t *testing.T) {
	xrEqual := rdiff(xnetGVK, "prod", dt.DiffTypeEqual, nil, nil)
	validation := dt.OutputError{
		ResourceID: "XNetwork/prod",
		Message:    "schema validation failed",
		ValidationFailures: []dt.ResourceValidationFailure{{
			APIVersion: "v1", Kind: "ConfigMap", Name: "cm", Status: "invalid",
			Errors: []dt.FieldValidationError{{Type: "schema", Field: "data.k", Message: "must be a string", Value: 7.0}},
		}},
	}

	unverified := rdiff(cmGVK, "cm", dt.DiffTypeAdded, nil, map[string]any{})
	unverified.DryRun = &dt.DryRunInfo{SkipReason: dt.DryRunSkipForbidden, Detail: "no create"}
	dryRunReport := &dt.XRReport{
		Groups:   []dt.XRDiffGroup{{XR: xrRef, Diffs: diffs(unverified)}},
		Warnings: []dt.OutputWarning{{Message: "heads up", Context: map[string]string{"k": "v"}}},
	}
	// The derived summary's wording belongs to the result package; the mapper
	// only has to carry it after the raised warnings.
	derived := dt.XRDryRunWarnings(dryRunReport.Groups)
	if len(derived) == 0 {
		t.Fatal("XRDryRunWarnings(...): want a summary of the unverified addition")
	}
	dryRunWarnings := make([]plan.Warning, 0, len(derived))
	for _, w := range derived {
		dryRunWarnings = append(dryRunWarnings, plan.Warning{Message: w.Message, Context: w.Context})
	}

	cases := map[string]struct {
		reason string
		in     *dt.XRReport
		want   plan.Result
	}{
		"EachDiffType": {
			reason: "Each DiffType maps to its action; a delete carries actual-only changes; the XR's own equal diff is a no-op plan.",
			in: &dt.XRReport{Groups: []dt.XRDiffGroup{{
				XR: xrRef,
				Diffs: diffs(
					xrEqual,
					rdiff(cmGVK, "added", dt.DiffTypeAdded, nil, map[string]any{"data": map[string]any{"k": "v"}}),
					rdiff(cmGVK, "modified", dt.DiffTypeModified,
						map[string]any{"data": map[string]any{"k": "old"}},
						map[string]any{"data": map[string]any{"k": "new"}}),
					rdiff(cmGVK, "removed", dt.DiffTypeRemoved, map[string]any{"data": map[string]any{"k": "gone"}}, nil),
				),
			}}},
			want: plan.Result{
				FormatVersion: plan.ResultFormatVersion,
				Summary:       plan.Summary{Create: 1, Update: 1, Destroy: 1, NoOp: 1},
				Composites:    []plan.CompositeResult{{Ref: xrPlan, Status: plan.CompositeChanged}},
				Plans: []*plan.ResourcePlan{
					{
						APIVersion: "v1", Kind: "ConfigMap", Name: "added", Action: plan.ActionCreate, Composite: &xrPlan,
						Changes: []plan.FieldChange{{Path: "data.k", Planned: &plan.Value{Value: "v"}}},
					},
					{
						APIVersion: "v1", Kind: "ConfigMap", Name: "modified", Action: plan.ActionUpdate, Composite: &xrPlan,
						Changes: []plan.FieldChange{{Path: "data.k", Actual: &plan.Value{Value: "old"}, Planned: &plan.Value{Value: "new"}}},
					},
					{
						APIVersion: "v1", Kind: "ConfigMap", Name: "removed", Action: plan.ActionDelete, Composite: &xrPlan,
						Changes: []plan.FieldChange{{Path: "data.k", Actual: &plan.Value{Value: "gone"}}},
					},
					{APIVersion: "example.org/v1", Kind: "XNetwork", Name: "prod", Action: plan.ActionNoOp, Composite: &xrPlan},
				},
				Notes: notes(),
			},
		},
		"Unchanged": {
			reason: "An XR whose only diff is its own equal one is unchanged.",
			in:     &dt.XRReport{Groups: []dt.XRDiffGroup{{XR: xrRef, Diffs: diffs(xrEqual)}}},
			want: plan.Result{
				FormatVersion: plan.ResultFormatVersion,
				Summary:       plan.Summary{NoOp: 1},
				Composites:    []plan.CompositeResult{{Ref: xrPlan, Status: plan.CompositeUnchanged}},
				Plans: []*plan.ResourcePlan{
					{APIVersion: "example.org/v1", Kind: "XNetwork", Name: "prod", Action: plan.ActionNoOp, Composite: &xrPlan},
				},
				Notes: notes(),
			},
		},
		"XRErrorWithValidationFailures": {
			reason: "A failed XR is an errored composite with an error plan; its typed error keeps the validation failures and is not repeated as a run error.",
			in: &dt.XRReport{
				Groups: []dt.XRDiffGroup{{XR: xrRef, Err: &validation}},
				Errors: []dt.OutputError{validation},
			},
			want: plan.Result{
				FormatVersion: plan.ResultFormatVersion,
				Errored:       true,
				Summary:       plan.Summary{Errored: 1},
				Composites:    []plan.CompositeResult{{Ref: xrPlan, Status: plan.CompositeError}},
				Plans: []*plan.ResourcePlan{
					{APIVersion: "example.org/v1", Kind: "XNetwork", Name: "prod", Error: "schema validation failed", Composite: &xrPlan},
				},
				Errors: []plan.Error{{
					Resource: &xrPlan,
					Message:  "schema validation failed",
					ValidationFailures: []plan.ValidationFailure{{
						APIVersion: "v1", Kind: "ConfigMap", Name: "cm", Status: "invalid",
						Errors: []plan.FieldValidationError{{Type: "schema", Field: "data.k", Message: "must be a string", Value: 7.0}},
					}},
				}},
				Notes: notes(),
			},
		},
		"RunErrors": {
			reason: "Errors no group owns (overlaps, interruption) become resource-less errors and mark the result errored.",
			in: &dt.XRReport{
				Groups: []dt.XRDiffGroup{{XR: xrRef, Diffs: diffs(xrEqual)}},
				Errors: []dt.OutputError{{Message: "inputs overlap"}, {Message: "interrupted"}},
			},
			want: plan.Result{
				FormatVersion: plan.ResultFormatVersion,
				Errored:       true,
				Summary:       plan.Summary{NoOp: 1},
				Composites:    []plan.CompositeResult{{Ref: xrPlan, Status: plan.CompositeUnchanged}},
				Plans: []*plan.ResourcePlan{
					{APIVersion: "example.org/v1", Kind: "XNetwork", Name: "prod", Action: plan.ActionNoOp, Composite: &xrPlan},
				},
				Errors: []plan.Error{{Message: "inputs overlap"}, {Message: "interrupted"}},
				Notes:  notes(),
			},
		},
		"Claim": {
			reason: "A claim input keeps the claim's identity in its composite ref.",
			in: &dt.XRReport{Groups: []dt.XRDiffGroup{{
				XR:    corev1.ObjectReference{APIVersion: "example.org/v1", Kind: "Network", Name: "mine", Namespace: "team"},
				Diffs: diffs(rdiff(cmGVK, "cm", dt.DiffTypeAdded, nil, map[string]any{})),
			}}},
			want: plan.Result{
				FormatVersion: plan.ResultFormatVersion,
				Summary:       plan.Summary{Create: 1},
				Composites: []plan.CompositeResult{{
					Ref:    plan.ResourceRef{APIVersion: "example.org/v1", Kind: "Network", Name: "mine", Namespace: "team"},
					Status: plan.CompositeChanged,
				}},
				Plans: []*plan.ResourcePlan{{
					APIVersion: "v1", Kind: "ConfigMap", Name: "cm", Action: plan.ActionCreate,
					Composite: new(plan.ResourceRef{APIVersion: "example.org/v1", Kind: "Network", Name: "mine", Namespace: "team"}),
				}},
				Notes: notes(),
			},
		},
		"SecretRedaction": {
			reason: "A Secret's data and stringData values are sensitive; its other fields are not.",
			in: &dt.XRReport{Groups: []dt.XRDiffGroup{{
				XR: xrRef,
				Diffs: diffs(rdiff(secGVK, "creds", dt.DiffTypeModified,
					map[string]any{"data": map[string]any{"pw": "b2xk"}, "type": "Opaque"},
					map[string]any{"data": map[string]any{"pw": "bmV3"}, "stringData": map[string]any{"u": "me"}, "type": "kv"})),
			}}},
			want: plan.Result{
				FormatVersion: plan.ResultFormatVersion,
				Summary:       plan.Summary{Update: 1},
				Composites:    []plan.CompositeResult{{Ref: xrPlan, Status: plan.CompositeChanged}},
				Plans: []*plan.ResourcePlan{{
					APIVersion: "v1", Kind: "Secret", Name: "creds", Action: plan.ActionUpdate, Composite: &xrPlan,
					Changes: []plan.FieldChange{
						{Path: "data.pw", Actual: &plan.Value{Sensitive: true}, Planned: &plan.Value{Sensitive: true}},
						{Path: "stringData.u", Planned: &plan.Value{Sensitive: true}},
						{Path: "type", Actual: &plan.Value{Value: "Opaque"}, Planned: &plan.Value{Value: "kv"}},
					},
				}},
				Notes: notes(),
			},
		},
		"Dedupe": {
			reason: "A change two inputs reach identically is listed once, under the first; a differing render of it is still listed.",
			in: &dt.XRReport{Groups: []dt.XRDiffGroup{
				{XR: xrRef, Diffs: diffs(rdiff(cmGVK, "shared", dt.DiffTypeAdded, nil, map[string]any{"data": map[string]any{"k": "v"}}))},
				{
					XR:    corev1.ObjectReference{APIVersion: "example.org/v1", Kind: "XNetwork", Name: "dev"},
					Diffs: diffs(rdiff(cmGVK, "shared", dt.DiffTypeAdded, nil, map[string]any{"data": map[string]any{"k": "v"}})),
				},
				{
					XR:    corev1.ObjectReference{APIVersion: "example.org/v1", Kind: "XNetwork", Name: "qa"},
					Diffs: diffs(rdiff(cmGVK, "shared", dt.DiffTypeAdded, nil, map[string]any{"data": map[string]any{"k": "other"}})),
				},
			}},
			want: plan.Result{
				FormatVersion: plan.ResultFormatVersion,
				Summary:       plan.Summary{Create: 2},
				Composites: []plan.CompositeResult{
					{Ref: xrPlan, Status: plan.CompositeChanged},
					{Ref: plan.ResourceRef{APIVersion: "example.org/v1", Kind: "XNetwork", Name: "dev"}, Status: plan.CompositeChanged},
					{Ref: plan.ResourceRef{APIVersion: "example.org/v1", Kind: "XNetwork", Name: "qa"}, Status: plan.CompositeChanged},
				},
				Plans: []*plan.ResourcePlan{
					{
						APIVersion: "v1", Kind: "ConfigMap", Name: "shared", Action: plan.ActionCreate, Composite: &xrPlan,
						Changes: []plan.FieldChange{{Path: "data.k", Planned: &plan.Value{Value: "v"}}},
					},
					{
						APIVersion: "v1", Kind: "ConfigMap", Name: "shared", Action: plan.ActionCreate,
						Composite: new(plan.ResourceRef{APIVersion: "example.org/v1", Kind: "XNetwork", Name: "qa"}),
						Changes:   []plan.FieldChange{{Path: "data.k", Planned: &plan.Value{Value: "other"}}},
					},
				},
				Notes: notes(),
			},
		},
		"DryRunAndWarnings": {
			reason: "An unverified addition carries its DryRun, and the run's warnings precede the derived dry-run summary.",
			in:     dryRunReport,
			want: plan.Result{
				FormatVersion: plan.ResultFormatVersion,
				Summary:       plan.Summary{Create: 1},
				Composites:    []plan.CompositeResult{{Ref: xrPlan, Status: plan.CompositeChanged}},
				Plans: []*plan.ResourcePlan{{
					APIVersion: "v1", Kind: "ConfigMap", Name: "cm", Action: plan.ActionCreate, Composite: &xrPlan,
					DryRun: &plan.Verification{SkipReason: "forbidden", Detail: "no create"},
				}},
				Warnings: append([]plan.Warning{{Message: "heads up", Context: map[string]string{"k": "v"}}}, dryRunWarnings...),
				Notes:    notes(),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := FromXRReport(tc.in, nil)
			if diff := cmp.Diff(tc.want, got, cmpopts.IgnoreFields(plan.ResourcePlan{}, "Before", "After"), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("%s\nFromXRReport(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
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
		want   plan.Result
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
			want: plan.Result{
				FormatVersion: plan.ResultFormatVersion,
				Summary:       plan.Summary{Update: 1, Destroy: 1},
				Composites:    []plan.CompositeResult{{Ref: xrPlan, Status: plan.CompositeChanged, Composition: &compPlan}},
				Compositions: []plan.CompositionResult{{
					Ref:            compPlan,
					RevisionImpact: &plan.RevisionImpact{ChangeScope: "spec", CreatesRevision: true, RepointedComposites: 1, PredictedRevision: "net-abc1234"},
				}},
				Plans: []*plan.ResourcePlan{
					{
						APIVersion: compPlan.APIVersion, Kind: "Composition", Name: "net", Action: plan.ActionUpdate,
						Changes: []plan.FieldChange{{Path: "spec.mode", Actual: &plan.Value{Value: "Pipeline"}, Planned: &plan.Value{Value: "Resources"}}},
					},
					{APIVersion: "v1", Kind: "ConfigMap", Name: "cm", Action: plan.ActionDelete, Composite: &xrPlan},
				},
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
			want: plan.Result{
				FormatVersion: plan.ResultFormatVersion,
				Summary:       plan.Summary{NoOp: 1, Deferred: 1},
				Composites: []plan.CompositeResult{
					{Ref: named("pinned"), Status: plan.CompositeDeferred, Composition: &compPlan, Reason: "manual_policy", Detail: "compositionRevisionRef: net-6"},
					{Ref: named("selector"), Status: plan.CompositeSkipped, Composition: &compPlan, Reason: "revision_selector_mismatch", Detail: "channel=stable"},
					{Ref: named("deleting"), Status: plan.CompositeSkipped, Composition: &compPlan, Reason: "deleting"},
				},
				Compositions: []plan.CompositionResult{{Ref: compPlan}},
				Plans:        []*plan.ResourcePlan{compNoOp},
				Notes:        notes(),
			},
		},
		"Pinned": {
			reason: "A pinned XR simulated with --include-manual keeps its status and is marked pinned.",
			in: composition(dt.CompositionDiff{
				ImpactAnalysis: []dt.XRImpact{{ObjectReference: xrRef, Status: dt.XRStatusUnchanged, Pinned: true}},
			}),
			want: plan.Result{
				FormatVersion: plan.ResultFormatVersion,
				Summary:       plan.Summary{NoOp: 1},
				Composites:    []plan.CompositeResult{{Ref: xrPlan, Status: plan.CompositeUnchanged, Composition: &compPlan, Pinned: true}},
				Compositions:  []plan.CompositionResult{{Ref: compPlan}},
				Plans:         []*plan.ResourcePlan{compNoOp},
				Notes:         notes(),
			},
		},
		"AnalysisSkippedMaskedOnly": {
			reason: "A masked-only Composition is a no-op plan; a skipped analysis is flagged on its Compositions entry.",
			in: composition(dt.CompositionDiff{
				MaskedChangesOnly:     true,
				ImpactAnalysisSkipped: true,
				RevisionImpact:        dt.RevisionImpact{ChangeScope: "metadata", CreatesRevision: true, RepointedComposites: 3},
			}),
			want: plan.Result{
				FormatVersion: plan.ResultFormatVersion,
				Summary:       plan.Summary{NoOp: 1},
				Compositions: []plan.CompositionResult{{
					Ref:                   compPlan,
					RevisionImpact:        &plan.RevisionImpact{ChangeScope: "metadata", CreatesRevision: true, RepointedComposites: 3},
					ImpactAnalysisSkipped: true,
				}},
				Plans: []*plan.ResourcePlan{compNoOp},
				Notes: notes(),
			},
		},
		"CompositionError": {
			reason: "A failed Composition is an error plan and a typed error, with no revision impact.",
			in: composition(dt.CompositionDiff{
				Error: &dt.OutputError{ResourceID: "Composition/net", Message: "cannot load"},
			}),
			want: plan.Result{
				FormatVersion: plan.ResultFormatVersion,
				Errored:       true,
				Summary:       plan.Summary{Errored: 1},
				Compositions:  []plan.CompositionResult{{Ref: compPlan}},
				Plans: []*plan.ResourcePlan{
					{APIVersion: compPlan.APIVersion, Kind: "Composition", Name: "net", Error: "cannot load"},
				},
				Errors: []plan.Error{{Resource: &compPlan, Message: "cannot load"}},
				Notes:  notes(),
			},
		},
		"CompositesNameTheirComposition": {
			reason: "Each affected composite names the Composition whose change reaches it.",
			in: &dt.CompDiffOutput{Compositions: []dt.CompositionDiff{
				{Name: "net", ImpactAnalysis: []dt.XRImpact{{ObjectReference: xrRef, Status: dt.XRStatusUnchanged}}},
				{Name: "db", ImpactAnalysis: []dt.XRImpact{filtered("qa", dt.FilterReasonDeleting, "")}},
			}},
			want: plan.Result{
				FormatVersion: plan.ResultFormatVersion,
				Summary:       plan.Summary{NoOp: 2},
				Composites: []plan.CompositeResult{
					{Ref: xrPlan, Status: plan.CompositeUnchanged, Composition: &compPlan},
					{Ref: named("qa"), Status: plan.CompositeSkipped, Composition: &dbPlan, Reason: "deleting"},
				},
				Compositions: []plan.CompositionResult{{Ref: compPlan}, {Ref: dbPlan}},
				Plans: []*plan.ResourcePlan{
					compNoOp,
					{APIVersion: dbPlan.APIVersion, Kind: dbPlan.Kind, Name: dbPlan.Name, Action: plan.ActionNoOp},
				},
				Notes: notes(),
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
			want: plan.Result{
				FormatVersion: plan.ResultFormatVersion,
				Errored:       true,
				Summary:       plan.Summary{NoOp: 1, Errored: 1},
				Composites:    []plan.CompositeResult{{Ref: xrPlan, Status: plan.CompositeError, Composition: &compPlan}},
				Compositions:  []plan.CompositionResult{{Ref: compPlan}},
				Plans: []*plan.ResourcePlan{
					compNoOp,
					{APIVersion: "example.org/v1", Kind: "XNetwork", Name: "prod", Error: "render failed", Composite: &xrPlan},
				},
				Errors: []plan.Error{{Resource: &xrPlan, Message: "render failed"}, {Message: "interrupted"}},
				Notes:  notes(),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := FromCompReport(tc.in, nil)
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
	key := added.GetDiffKey()

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
		wantPlans  []*plan.ResourcePlan
		wantStatus plan.CompositeStatus
	}{
		"NoProviderEntry": {
			reason:     "With no provider entry the engine plan stands, and an empty plans map drops the not-run note.",
			diff:       modified,
			plans:      map[string]*plan.ResourcePlan{},
			wantPlans:  []*plan.ResourcePlan{cm(plan.ResourcePlan{Action: plan.ActionUpdate, Changes: labelChange, DryRun: forbidden})},
			wantStatus: plan.CompositeChanged,
		},
		"ProviderError": {
			reason:     "A provider error is an error plan keeping the engine's identity, composite and DryRun.",
			diff:       modified,
			plans:      map[string]*plan.ResourcePlan{key: {Kind: "Other", Name: "x", Error: "diff server cannot plan: boom"}},
			wantPlans:  []*plan.ResourcePlan{cm(plan.ResourcePlan{Error: "diff server cannot plan: boom", DryRun: forbidden})},
			wantStatus: plan.CompositeChanged,
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
			wantStatus: plan.CompositeChanged,
		},
		"AddedProviderCreate": {
			reason:     "A provider create replaces the engine's changes.",
			diff:       added,
			plans:      map[string]*plan.ResourcePlan{key: {Action: plan.ActionCreate, Changes: providerChange}},
			wantPlans:  []*plan.ResourcePlan{cm(plan.ResourcePlan{Action: plan.ActionCreate, Changes: providerChange})},
			wantStatus: plan.CompositeChanged,
		},
		"ModifiedProviderReplace": {
			reason: "A provider replace takes the action, changes and replace paths, keeping the engine's DryRun.",
			diff:   modified,
			plans: map[string]*plan.ResourcePlan{key: {
				Action: plan.ActionReplace, Changes: providerChange, ReplacePaths: []string{"spec.forProvider.size"},
			}},
			wantPlans: []*plan.ResourcePlan{cm(plan.ResourcePlan{
				Action: plan.ActionReplace, Changes: providerChange, ReplacePaths: []string{"spec.forProvider.size"}, DryRun: forbidden,
			})},
			wantStatus: plan.CompositeChanged,
		},
		"ModifiedProviderNoOp": {
			reason:     "The cluster object still changes when the provider finds nothing, so the engine's update stands.",
			diff:       modified,
			plans:      map[string]*plan.ResourcePlan{key: {Action: plan.ActionNoOp}},
			wantPlans:  []*plan.ResourcePlan{cm(plan.ResourcePlan{Action: plan.ActionUpdate, Changes: labelChange, DryRun: forbidden})},
			wantStatus: plan.CompositeChanged,
		},
		"EqualProviderUpdateUpgradesComposite": {
			reason:     "Cloud drift on an MR the cluster finds equal is an update, and makes its unchanged composite changed.",
			diff:       equal,
			plans:      map[string]*plan.ResourcePlan{key: {Action: plan.ActionUpdate, Changes: providerChange}},
			wantPlans:  []*plan.ResourcePlan{cm(plan.ResourcePlan{Action: plan.ActionUpdate, Changes: providerChange})},
			wantStatus: plan.CompositeChanged,
		},
		"EqualProviderNoOpDropped": {
			reason:     "A kept-equal MR the provider finds unchanged is dropped, and its composite stays unchanged.",
			diff:       equal,
			plans:      map[string]*plan.ResourcePlan{key: {Action: plan.ActionNoOp}},
			wantStatus: plan.CompositeUnchanged,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			xr := rdiff(xnetGVK, "prod", dt.DiffTypeEqual, nil, nil)
			// Plans sort by kind, so the XR's own no-op plan comes last.
			wantPlans := append(slices.Clone(tc.wantPlans), &plan.ResourcePlan{APIVersion: "example.org/v1", Kind: "XNetwork", Name: "prod", Action: plan.ActionNoOp, Composite: &xrPlan})

			gotXR := FromXRReport(&dt.XRReport{Groups: []dt.XRDiffGroup{{XR: xrRef, Diffs: diffs(xr, tc.diff)}}}, tc.plans)
			if diff := cmp.Diff(wantPlans, gotXR.Plans, cmpopts.IgnoreFields(plan.ResourcePlan{}, "Before", "After"), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("%s\nFromXRReport(...).Plans: -want, +got:\n%s", tc.reason, diff)
			}
			if got := gotXR.Composites[0].Status; got != tc.wantStatus {
				t.Errorf("%s\nFromXRReport(...): want composite %q, got %q", tc.reason, tc.wantStatus, got)
			}
			if len(gotXR.Notes) != 0 {
				t.Errorf("%s\nFromXRReport(...): want no note with provider plans, got %v", tc.reason, gotXR.Notes)
			}

			// The composition path merges the same way. An Unchanged impact
			// carries diffs only when the engine kept equal ones.
			status := dt.XRStatusUnchanged
			if tc.diff.DiffType != dt.DiffTypeEqual {
				status = dt.XRStatusChanged
			}
			gotComp := FromCompReport(&dt.CompDiffOutput{Compositions: []dt.CompositionDiff{{
				Name:           "net",
				ImpactAnalysis: []dt.XRImpact{{ObjectReference: xrRef, Status: status, Diffs: diffs(tc.diff)}},
			}}}, tc.plans)
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
			got := FromXRReport(&dt.XRReport{Groups: []dt.XRDiffGroup{{XR: xrRef, Diffs: diffs(tc.diff)}}}, tc.plans)
			if len(got.Plans) != 1 {
				t.Fatalf("%s\nFromXRReport(...): want 1 plan, got %d", tc.reason, len(got.Plans))
			}
			if diff := cmp.Diff(tc.wantBefore, got.Plans[0].Before); diff != "" {
				t.Errorf("%s\nFromXRReport(...).Before: -want, +got:\n%s", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.wantAfter, got.Plans[0].After); diff != "" {
				t.Errorf("%s\nFromXRReport(...).After: -want, +got:\n%s", tc.reason, diff)
			}
		})
	}

	t.Run("SecretSourceUntouched", func(t *testing.T) {
		d := rdiff(secGVK, "cm", dt.DiffTypeAdded, nil, data("v"))
		FromXRReport(&dt.XRReport{Groups: []dt.XRDiffGroup{{XR: xrRef, Diffs: diffs(d)}}}, nil)
		if diff := cmp.Diff(obj(secGVK, "cm", data("v")), d.Desired.Clean); diff != "" {
			t.Errorf("FromXRReport(...): redaction must not modify the engine's view: -want, +got:\n%s", diff)
		}
	})
}
