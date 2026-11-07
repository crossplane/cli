package simtest

import (
	"encoding/json"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	dt "github.com/crossplane/cli/v2/internal/diff/result"
	"github.com/crossplane/cli/v2/internal/plan"
	"github.com/crossplane/cli/v2/internal/simulate"
)

// resultJSON serializes r the way composition simulate's -o json does.
func resultJSON(t *testing.T, r simulate.ProjectResult) string {
	t.Helper()

	out, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("json.Marshal(): %v", err)
	}

	return string(out)
}

func val(v any) *plan.Value { return &plan.Value{Value: v} }

// checkAssertion runs assert against a throwaway T and reports whether it failed as wantFail says.
func checkAssertion(t *testing.T, name string, wantFail bool, assert func(*testing.T)) {
	t.Helper()

	inner := &testing.T{}
	assert(inner)

	if inner.Failed() != wantFail {
		t.Errorf("%s: failed = %t, want %t", name, inner.Failed(), wantFail)
	}
}

var (
	compA = plan.ResourceRef{APIVersion: "apiextensions.crossplane.io/v1", Kind: "Composition", Name: "comp-a"}
	compB = plan.ResourceRef{APIVersion: "apiextensions.crossplane.io/v1", Kind: "Composition", Name: "comp-b"}
	xr1   = plan.ResourceRef{APIVersion: "example.org/v1", Kind: "XNop", Name: "xr-1", Namespace: "default"}
	xr2   = plan.ResourceRef{APIVersion: "example.org/v1", Kind: "XNop", Name: "xr-2", Namespace: "default"}
	xr3   = plan.ResourceRef{APIVersion: "example.org/v1", Kind: "XNop", Name: "xr-3", Namespace: "default"}
)

// compResult is a comp run over two compositions: comp-a reaches a changed, an unchanged and a deleting
// composite; comp-b reaches only a composite whose analysis failed.
func compResult() simulate.ProjectResult {
	return simulate.ProjectResult{
		Result: plan.Result{
			Plans: []*plan.ResourcePlan{
				{
					APIVersion: compA.APIVersion, Kind: compA.Kind, Name: compA.Name, Action: plan.ActionUpdate,
					Changes: []plan.FieldChange{{Path: "spec.mode", Actual: val("Resources"), Planned: val("Pipeline")}},
				},
				{APIVersion: compB.APIVersion, Kind: compB.Kind, Name: compB.Name, Error: "cannot convert composition"},
				{
					APIVersion: "example.org/v1", Kind: "XDownstream", Name: "xr-1-abc12", Namespace: "default", Action: plan.ActionUpdate, Composite: &xr1,
					Changes: []plan.FieldChange{{Path: "spec.forProvider.configData", Actual: val("old"), Planned: val("new-value-42")}},
					Before:  downstreamObject("old"),
					After:   downstreamObject("new-value-42"),
					DryRun:  &plan.Verification{SkipReason: "not-found", Detail: "the server could not find it"},
				},
			},
		},
		Compositions: []simulate.CompositionResult{
			{Ref: compA, RevisionImpact: &simulate.RevisionImpact{ChangeScope: "spec", CreatesRevision: true, RepointedComposites: 2, PredictedRevision: "comp-a-1a2b3c4"}},
			{Ref: compB, ImpactAnalysisSkipped: true},
		},
		Composites: []simulate.CompositeResult{
			{Ref: xr1, Status: simulate.CompositeChanged, Composition: &compA},
			{Ref: xr2, Status: simulate.CompositeUnchanged, Composition: &compA},
			{Ref: xr3, Status: simulate.CompositeSkipped, Reason: simulate.CompositeReasonDeleting, Composition: &compA},
			{Ref: xr1, Status: simulate.CompositeError, Composition: &compB},
		},
		Errors: []simulate.Error{
			{Ref: &compB, Message: "cannot convert composition"},
			{Ref: &xr1, Message: "maximum nesting depth exceeded"},
		},
		Warnings: []simulate.Warning{{Message: "something advisory", Context: map[string]string{"composition": "comp-a"}}},
	}
}

// downstreamObject is xr-1's downstream resource with configData, and an unchanged tier.
func downstreamObject(configData string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.org/v1", "kind": "XDownstream",
		"metadata": map[string]any{"name": "xr-1-abc12", "namespace": "default"},
		"spec":     map[string]any{"forProvider": map[string]any{"configData": configData, "tier": "premium"}},
	}}
}

func TestAssertStructuredCompositionPlan(t *testing.T) {
	out := resultJSON(t, compResult())

	downstream := func() *ResourceExpectation {
		return Expect().WithComposition("comp-a").WithXRImpact("XNop", "xr-1", "default", "changed").
			WithDownstreamResource("modified", "XDownstream", "xr-1-abc12", "default")
	}

	cases := map[string]struct {
		want     Expectation
		wantFail bool
	}{
		"Composition": {want: Expect().
			WithComposition("comp-a").
			WithAffectedResources(3, 1, 1, 0).
			WithFilteredByDeletion(1).
			WithRevisionImpact("spec", true, 2).
			WithPredictedRevisionNamePattern(`^comp-a-[0-9a-f]{7}$`).
			WithCompositionModified().
			WithXRImpact("XNop", "xr-1", "default", "changed").
			WithDownstreamSummary(0, 1, 0).
			WithDownstreamResource("modified", "XDownstream", "xr-1-abc12", "default").
			WithFieldChange("spec.forProvider.configData", "old", "new-value-42").
			WithField("spec.forProvider", map[string]any{"configData": "new-value-42"}).
			WithDryRunSkipped("not-found", "could not find").
			AndXR().
			AndComp().
			WithXRImpact("XNop", "xr-3", "default", "filtered").
			WithFilterReason(string(dt.FilterReasonDeleting))},
		// Counts are per composition: comp-b's errored composite is not comp-a's.
		"CountsArePerComposition": {want: Expect().WithComposition("comp-a").WithAffectedResources(4, 1, 1, 1), wantFail: true},
		"OtherComposition": {want: Expect().
			WithErrorID("Composition/comp-b").
			WithComposition("comp-b").
			WithImpactAnalysisSkipped().
			WithAffectedResources(1, 0, 0, 1).
			WithXRImpact("XNop", "xr-1", "default", "error").
			WithErrorContaining("maximum nesting depth").
			WithErrorResourceID("XNop/xr-1")},
		"MissingComposition":     {want: Expect().WithComposition("comp-c"), wantFail: true},
		"CompositionNotModified": {want: Expect().WithComposition("comp-b").WithCompositionModified(), wantFail: true},
		"WrongRevisionScope":     {want: Expect().WithComposition("comp-a").WithRevisionImpact("metadata", true, 2), wantFail: true},
		"PredictedNameAbsent":    {want: Expect().WithComposition("comp-a").WithoutPredictedRevisionName(), wantFail: true},
		"NoRevisionImpact":       {want: Expect().WithComposition("comp-b").WithRevisionImpact("none", false, 0), wantFail: true},
		"NotSkipped":             {want: Expect().WithComposition("comp-a").WithImpactAnalysisSkipped(), wantFail: true},
		"WrongXRStatus":          {want: Expect().WithComposition("comp-a").WithXRImpact("XNop", "xr-2", "default", "changed"), wantFail: true},
		"WrongFilterReason": {
			want: Expect().WithComposition("comp-a").WithXRImpact("XNop", "xr-3", "default", "filtered").
				WithFilterReason(string(dt.FilterReasonManualPolicy)),
			wantFail: true,
		},
		"WrongDownstreamValue": {want: downstream().WithFieldChange("spec.forProvider.configData", "old", "other"), wantFail: true},
		"WrongOldValue":        {want: downstream().WithFieldChange("spec.forProvider.configData", "older", "new-value-42"), wantFail: true},
		"WrongField":           {want: downstream().WithField("spec.forProvider.configData", "other"), wantFail: true},
		"WrongDownstreamName":  {want: Expect().WithComposition("comp-a").WithXRImpact("XNop", "xr-1", "default", "changed").WithDownstreamResource("modified", "XDownstream", "xr-1", "default"), wantFail: true},
		"DryRunPerformed":      {want: downstream().WithDryRunPerformed(), wantFail: true},
		"WrongDryRunReason":    {want: downstream().WithDryRunSkipped("disabled", ""), wantFail: true},
		// A field rendered identically on both sides has no leaf change: asserting it unchanged passes
		// when the plan's before and after objects both hold the value and nothing at or under it
		// changed, and fails otherwise.
		"UnchangedField":      {want: downstream().WithFieldChange("spec.forProvider.tier", "premium", "premium")},
		"UnchangedWrongValue": {want: downstream().WithFieldChange("spec.forProvider.tier", "basic", "basic"), wantFail: true},
		"UnchangedMissing":    {want: downstream().WithFieldChange("spec.forProvider.size", "large", "large"), wantFail: true},
		"ChangedNotSteady":    {want: downstream().WithFieldChange("spec.forProvider.configData", "old", "old"), wantFail: true},
		"Warning":             {want: Expect().WithWarning("advisory").WithWarningContext(map[string]string{"composition": "comp-a"})},
		"WrongWarningContext": {want: Expect().WithWarning("advisory").WithWarningContext(map[string]string{"composition": "comp-b"}), wantFail: true},
		"MissingWarning":      {want: Expect().WithWarning("advisory").And().WithWarning("other"), wantFail: true},
		"NoWarnings":          {want: Expect().WithNoWarnings(), wantFail: true},
		"AndComposition":      {want: downstream().AndComposition("comp-b").WithImpactAnalysisSkipped()},
		"ErrorOnOtherXR": {
			want:     Expect().WithComposition("comp-a").WithXRImpact("XNop", "xr-2", "default", "unchanged").WithErrorResourceID("XNop/xr-1"),
			wantFail: true,
		},
		"MissingTopLevelError": {want: Expect().WithErrorID("Composition/comp-a"), wantFail: true},
	}

	for name, tc := range cases {
		checkAssertion(t, name, tc.wantFail, func(t *testing.T) { t.Helper(); AssertStructured(t, out, tc.want) })
	}
}

func TestValueAt(t *testing.T) {
	changes := []plan.FieldChange{
		{Path: "spec.a", Actual: val("old-a"), Planned: val("new-a")},
		{Path: "spec.list[0].x", Planned: val("x0")},
		{Path: "spec.list[1].x", Actual: val("gone")},
		{Path: "metadata.labels[app.io/name]", Planned: val("app")},
		{Path: "spec.whole", Planned: val(map[string]any{"inner": map[string]any{"k": "v"}, "nul": nil})},
	}

	cases := map[string]struct {
		path      string
		planned   bool
		want      any
		wantFound bool
	}{
		"Leaf":                  {path: "spec.a", planned: true, want: "new-a", wantFound: true},
		"LeafActual":            {path: "spec.a", want: "old-a", wantFound: true},
		"Subtree":               {path: "spec", planned: true, want: map[string]any{"a": "new-a", "list": []any{map[string]any{"x": "x0"}}, "whole": map[string]any{"inner": map[string]any{"k": "v"}, "nul": nil}}, wantFound: true},
		"SubtreeActual":         {path: "spec.list", want: []any{nil, map[string]any{"x": "gone"}}, wantFound: true},
		"QuotedKey":             {path: "metadata.labels['app.io/name']", planned: true, want: "app", wantFound: true},
		"Absent":                {path: "spec.b", planned: true, want: nil},
		"InsideEnclosingChange": {path: "spec.whole.inner.k", planned: true, want: "v", wantFound: true},
		"NullInsideEnclosing":   {path: "spec.whole.nul", planned: true, want: nil, wantFound: true},
		"AbsentInsideEnclosing": {path: "spec.whole.other", planned: true, want: nil},
		"PrefixIsNotPath":       {path: "spec.list[0].xy", planned: true, want: nil},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, found := valueAt(t, changes, tc.planned, tc.path)
			if !reflect.DeepEqual(got, tc.want) || found != tc.wantFound {
				t.Errorf("valueAt(%q, planned=%t) = %#v, %t, want %#v, %t", tc.path, tc.planned, got, found, tc.want, tc.wantFound)
			}
		})
	}
}

func TestAssertFieldsAndWarnings(t *testing.T) {
	p := &plan.ResourcePlan{
		Changes: []plan.FieldChange{
			{Path: "spec.n", Planned: val(3)},
			{Path: "spec.nul", Planned: val(nil)},
		},
	}
	two := []simulate.Warning{{Message: "advisory"}}

	cases := map[string]struct {
		assert   func(*testing.T)
		wantFail bool
	}{
		"AbsentIsNotNil": {assert: func(t *testing.T) {
			t.Helper()
			assertFields(t, "id", p, "added", map[string]any{"spec.missing": nil}, nil)
		}, wantFail: true},
		"ExplicitNil": {assert: func(t *testing.T) {
			t.Helper()
			assertFields(t, "id", p, "added", map[string]any{"spec.nul": nil}, nil)
		}},
		"IntEqualsFloat": {assert: func(t *testing.T) {
			t.Helper()
			assertFields(t, "id", p, "added", map[string]any{"spec.n": float64(3)}, nil)
		}},
		"IntDiffersFloat": {assert: func(t *testing.T) { t.Helper(); assertFields(t, "id", p, "added", map[string]any{"spec.n": 4.5}, nil) }, wantFail: true},
		"DoubleExpectation": {assert: func(t *testing.T) {
			t.Helper()
			assertWarnings(t, two, []warningMatch{{messageContains: "advisory"}, {messageContains: "advisory"}}, false)
		}, wantFail: true},
		"NoWarningsAndWarning": {assert: func(t *testing.T) {
			t.Helper()
			assertWarnings(t, nil, []warningMatch{{messageContains: "advisory"}}, true)
		}, wantFail: true},
		"DryRunContradiction": {assert: func(t *testing.T) {
			t.Helper()
			assertDryRun(t, "id", nil, dryRunExpectation{mustBePerformed: true, skipReason: "disabled"})
		}, wantFail: true},
	}

	for name, tc := range cases {
		checkAssertion(t, name, tc.wantFail, tc.assert)
	}
}
