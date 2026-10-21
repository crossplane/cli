package simtest

import (
	"encoding/json"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	dt "github.com/crossplane/cli/v2/internal/diff/result"
	"github.com/crossplane/cli/v2/internal/plan"
)

// resultJSON serializes r the way project simulate's -o json does.
func resultJSON(t *testing.T, r plan.Result) string {
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
	xrA = plan.ResourceRef{APIVersion: "example.org/v1", Kind: "XBucket", Name: "changed-xr", Namespace: "default"}
	xrB = plan.ResourceRef{APIVersion: "example.org/v1", Kind: "XBucket", Name: "unchanged-xr", Namespace: "default"}
	xrC = plan.ResourceRef{APIVersion: "example.org/v1", Kind: "XBucket", Name: "broken-xr", Namespace: "default"}
)

// bucket is bucket-a's object in region, with an unchanged tier.
func bucket(region string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.org/v1", "kind": "Bucket",
		"metadata": map[string]any{"name": "bucket-a", "namespace": "default"},
		"spec":     map[string]any{"region": region, "tier": "premium"},
	}}
}

// xrResult is an xr run: one changed XR (a modified Bucket, a created ConfigMap and a deleted Secret),
// one unchanged XR, and one errored XR.
func xrResult() plan.Result {
	plans := []*plan.ResourcePlan{
		{
			APIVersion: "example.org/v1", Kind: "Bucket", Name: "bucket-a", Namespace: "default", Action: plan.ActionUpdate, Composite: &xrA,
			Before: bucket("us-east-1"), After: bucket("us-west-2"),
			Changes: []plan.FieldChange{
				{Path: "spec.region", Actual: val("us-east-1"), Planned: val("us-west-2")},
				{Path: `metadata.annotations["example.org/source"]`, Actual: val("old"), Planned: val("new")},
			},
		},
		{
			APIVersion: "v1", Kind: "ConfigMap", Name: "cm-x7k2p", Namespace: "default", Action: plan.ActionCreate, Composite: &xrA,
			Changes: []plan.FieldChange{
				{Path: "data.key", Planned: val("value")},
				{Path: "spec.items[0].name", Planned: val("first")},
				{Path: "spec.items[1].name", Planned: val("second")},
			},
		},
		{
			APIVersion: "v1", Kind: "Secret", Name: "gone", Namespace: "default", Action: plan.ActionDelete, Composite: &xrA,
			Changes: []plan.FieldChange{{Path: "type", Actual: val("Opaque")}},
		},
		{APIVersion: xrB.APIVersion, Kind: xrB.Kind, Name: xrB.Name, Namespace: xrB.Namespace, Action: plan.ActionNoOp, Composite: &xrB},
		{APIVersion: xrC.APIVersion, Kind: xrC.Kind, Name: xrC.Name, Namespace: xrC.Namespace, Error: "cannot get composition", Composite: &xrC},
	}

	return plan.Result{
		Summary: plan.Summarize(plans),
		Composites: []plan.CompositeResult{
			{Ref: xrA, Status: plan.CompositeChanged},
			{Ref: xrB, Status: plan.CompositeUnchanged},
			{Ref: xrC, Status: plan.CompositeError},
		},
		Plans:    plans,
		Errors:   []plan.Error{{Resource: &xrC, Message: "cannot get composition"}},
		Warnings: []plan.Warning{{Message: "something advisory", Context: map[string]string{"composition": "c"}}},
	}
}

func TestParseResult(t *testing.T) {
	if _, err := ParseResult(`{"plans": [`); err == nil {
		t.Error("ParseResult(): want an error for malformed JSON")
	}

	got, err := ParseResult(resultJSON(t, xrResult()))
	if err != nil {
		t.Fatalf("ParseResult(): %v", err)
	}

	if len(got.Plans) != 5 || len(got.Composites) != 3 {
		t.Errorf("ParseResult(): got %d plans and %d composites, want 5 and 3", len(got.Plans), len(got.Composites))
	}
}

func TestAssertStructuredDiff(t *testing.T) {
	out := resultJSON(t, xrResult())

	// Any resource expectation pins the change count, so a case about one change lists the others.
	withoutBucket := func() *ExpectedDiff {
		return ExpectDiff().WithAddedResource("ConfigMap", "cm-x7k2p", "default").And().WithRemovedResource("Secret", "gone", "default").And()
	}
	withoutConfigMap := func() *ExpectedDiff {
		return ExpectDiff().WithModifiedResource("Bucket", "bucket-a", "default").And().WithRemovedResource("Secret", "gone", "default").And()
	}

	cases := map[string]struct {
		want     DiffExpectation
		wantFail bool
	}{
		"Summary":      {want: ExpectDiff().WithSummary(1, 1, 1)},
		"WrongSummary": {want: ExpectDiff().WithSummary(0, 1, 1), wantFail: true},
		"AllChanges": {want: ExpectDiff().
			WithModifiedResource("Bucket", "bucket-a", "default").
			WithFieldChange("spec.region", "us-east-1", "us-west-2").
			WithFieldChange("metadata.annotations['example.org/source']", "old", "new").
			And().
			WithAddedResource("ConfigMap", "cm-x7k2p", "default").
			WithField("data.key", "value").
			WithField("spec.items", []any{map[string]any{"name": "first"}, map[string]any{"name": "second"}}).
			WithField("spec.items[1].name", "second").
			And().
			WithRemovedResource("Secret", "gone", "default").
			WithField("type", "Opaque")},
		// Any resource expectation pins the change count, so an unlisted change fails.
		"UnlistedChange": {want: ExpectDiff().
			WithModifiedResource("Bucket", "bucket-a", "default").And().
			WithAddedResource("ConfigMap", "cm-x7k2p", "default"), wantFail: true},
		"WrongAction": {want: ExpectDiff().WithAddedResource("Bucket", "bucket-a", "default"), wantFail: true},
		"WrongNewValue": {
			want:     withoutBucket().WithModifiedResource("Bucket", "bucket-a", "default").WithFieldChange("spec.region", "us-east-1", "eu-west-1"),
			wantFail: true,
		},
		"WrongOldValue": {
			want:     withoutBucket().WithModifiedResource("Bucket", "bucket-a", "default").WithFieldChange("spec.region", "eu-west-1", "us-west-2"),
			wantFail: true,
		},
		"NamePattern":    {want: withoutConfigMap().WithAddedResource("ConfigMap", "", "default").WithNamePattern(`^cm-[a-z0-9]{5}$`)},
		"BadNamePattern": {want: withoutConfigMap().WithAddedResource("ConfigMap", "", "default").WithNamePattern(`^other-`), wantFail: true},
		// A field rendered identically on both sides has no leaf change: asserting it unchanged passes
		// when the plan's before and after objects both hold the value and nothing at or under it
		// changed, and fails otherwise.
		"UnchangedField": {want: withoutBucket().WithModifiedResource("Bucket", "bucket-a", "default").WithFieldChange("spec.tier", "premium", "premium")},
		"UnchangedWrongValue": {
			want:     withoutBucket().WithModifiedResource("Bucket", "bucket-a", "default").WithFieldChange("spec.tier", "basic", "basic"),
			wantFail: true,
		},
		"UnchangedMissing": {
			want:     withoutBucket().WithModifiedResource("Bucket", "bucket-a", "default").WithFieldChange("spec.size", "large", "large"),
			wantFail: true,
		},
		"ChangedNotSteady": {
			want:     withoutBucket().WithModifiedResource("Bucket", "bucket-a", "default").WithFieldChange("spec.region", "us-east-1", "us-east-1"),
			wantFail: true,
		},
		"FieldAbsent":       {want: withoutConfigMap().WithAddedResource("ConfigMap", "cm-x7k2p", "default").WithFieldAbsent("spec.other")},
		"FieldNotAbsent":    {want: withoutConfigMap().WithAddedResource("ConfigMap", "cm-x7k2p", "default").WithFieldAbsent("data.key"), wantFail: true},
		"Error":             {want: ExpectDiff().WithError("XBucket/broken-xr").WithMessageContaining("cannot get")},
		"WrongErrorMessage": {want: ExpectDiff().WithError("XBucket/broken-xr").WithMessageContaining("timeout"), wantFail: true},
		"MissingError":      {want: ExpectDiff().WithError("XBucket/changed-xr"), wantFail: true},
		"Warning":           {want: ExpectDiff().WithWarning("advisory").WithWarningContext(map[string]string{"composition": "c"})},
		"WrongContext":      {want: ExpectDiff().WithWarning("advisory").WithWarningContext(map[string]string{"composition": "d"}), wantFail: true},
		"NoWarnings":        {want: ExpectDiff().WithNoWarnings(), wantFail: true},
		"GroupedByXR": {want: ExpectDiff().
			WithXRs(
				XR("XBucket", "changed-xr", "default").
					Status("changed").
					Summary(1, 1, 1).
					Change("modified", "Bucket", "bucket-a", "default").
					WithFieldChange("spec.region", "us-east-1", "us-west-2").
					Change("added", "ConfigMap", "cm-x7k2p", "default").
					Change("removed", "Secret", "gone", "default"),
				XR("XBucket", "unchanged-xr", "default").Status("unchanged"),
				XR("XBucket", "broken-xr", "default").Status("error").Err("XBucket/broken-xr"),
			).
			Build()},
		"GroupedWrongStatus": {want: ExpectDiff().
			WithXRs(
				XR("XBucket", "changed-xr", "default").Status("unchanged"),
				XR("XBucket", "unchanged-xr", "default"),
				XR("XBucket", "broken-xr", "default"),
			).
			Build(), wantFail: true},
		// Two expectations cannot both consume the same entry.
		"GroupedDuplicate": {want: ExpectDiff().
			WithXRs(
				XR("XBucket", "changed-xr", "default"),
				XR("XBucket", "changed-xr", "default"),
				XR("XBucket", "broken-xr", "default"),
			).
			Build(), wantFail: true},
		"GroupedErrorOnWrongXR": {want: ExpectDiff().
			WithXRs(
				XR("XBucket", "changed-xr", "default").Err("XBucket/broken-xr"),
				XR("XBucket", "unchanged-xr", "default"),
				XR("XBucket", "broken-xr", "default"),
			).
			Build(), wantFail: true},
	}

	for name, tc := range cases {
		checkAssertion(t, name, tc.wantFail, func(t *testing.T) { t.Helper(); AssertStructuredDiff(t, out, tc.want) })
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
func compResult() plan.Result {
	return plan.Result{
		Compositions: []plan.CompositionResult{
			{Ref: compA, RevisionImpact: &plan.RevisionImpact{ChangeScope: "spec", CreatesRevision: true, RepointedComposites: 2, PredictedRevision: "comp-a-1a2b3c4"}},
			{Ref: compB, ImpactAnalysisSkipped: true},
		},
		Composites: []plan.CompositeResult{
			{Ref: xr1, Status: plan.CompositeChanged, Composition: &compA},
			{Ref: xr2, Status: plan.CompositeUnchanged, Composition: &compA},
			{Ref: xr3, Status: plan.CompositeSkipped, Reason: string(dt.FilterReasonDeleting), Composition: &compA},
			{Ref: xr1, Status: plan.CompositeError, Composition: &compB},
		},
		Plans: []*plan.ResourcePlan{
			{
				APIVersion: compA.APIVersion, Kind: compA.Kind, Name: compA.Name, Action: plan.ActionUpdate,
				Changes: []plan.FieldChange{{Path: "spec.mode", Actual: val("Resources"), Planned: val("Pipeline")}},
			},
			{APIVersion: compB.APIVersion, Kind: compB.Kind, Name: compB.Name, Error: "cannot convert composition"},
			{
				APIVersion: "example.org/v1", Kind: "XDownstream", Name: "xr-1-abc12", Namespace: "default", Action: plan.ActionUpdate, Composite: &xr1,
				Changes: []plan.FieldChange{{Path: "spec.forProvider.configData", Actual: val("old"), Planned: val("new-value-42")}},
				DryRun:  &plan.Verification{SkipReason: "not-found", Detail: "the server could not find it"},
			},
		},
		Errors: []plan.Error{
			{Resource: &compB, Message: "cannot convert composition"},
			{Resource: &xr1, Message: "maximum nesting depth exceeded"},
		},
	}
}

func TestAssertStructuredCompDiff(t *testing.T) {
	out := resultJSON(t, compResult())

	downstream := func() *DownstreamResourceExpectation {
		return ExpectCompDiff().WithComposition("comp-a").WithXRImpact("XNop", "xr-1", "default", "changed").
			WithDownstreamResource("modified", "XDownstream", "xr-1-abc12", "default")
	}

	cases := map[string]struct {
		want     CompDiffExpectation
		wantFail bool
	}{
		"Composition": {want: ExpectCompDiff().
			WithComposition("comp-a").
			WithAffectedResources(3, 1, 1, 0).
			WithFilteredByDeletion(1).
			WithRevisionImpact("spec", true, 2).
			WithPredictedRevisionNamePattern(`^comp-a-[0-9a-f]{7}$`).
			WithCompositionModified().
			WithCompositionFieldChange("spec.mode", "Resources", "Pipeline").
			WithXRImpact("XNop", "xr-1", "default", "changed").
			WithDownstreamSummary(0, 1, 0).
			WithDownstreamResource("modified", "XDownstream", "", "default").
			WithNamePattern(`^xr-1-`).
			WithFieldChange("spec.forProvider.configData", "old", "new-value-42").
			WithFieldValuePattern("spec.forProvider.configData", `^new-value-\d+$`).
			WithDryRunSkipped("not-found", "could not find").
			AndXR().
			AndComp().
			WithXRImpact("XNop", "xr-3", "default", "filtered").
			WithFilterReason(string(dt.FilterReasonDeleting))},
		// Counts are per composition: comp-b's errored composite is not comp-a's.
		"CountsArePerComposition": {want: ExpectCompDiff().WithComposition("comp-a").WithAffectedResources(4, 1, 1, 1), wantFail: true},
		"OtherComposition": {want: ExpectCompDiff().
			WithError("Composition/comp-b").
			WithComposition("comp-b").
			WithCompositionErrorContaining("cannot convert").
			WithImpactAnalysisSkipped().
			WithAffectedResources(1, 0, 0, 1).
			WithXRImpact("XNop", "xr-1", "default", "error").
			WithErrorContaining("maximum nesting depth").
			WithErrorResourceID("XNop/xr-1")},
		"MissingComposition":  {want: ExpectCompDiff().WithComposition("comp-c"), wantFail: true},
		"WrongRevisionScope":  {want: ExpectCompDiff().WithComposition("comp-a").WithRevisionImpact("metadata", true, 2), wantFail: true},
		"PredictedNameAbsent": {want: ExpectCompDiff().WithComposition("comp-a").WithoutPredictedRevisionName(), wantFail: true},
		"NoRevisionImpact":    {want: ExpectCompDiff().WithComposition("comp-b").WithRevisionImpact("none", false, 0), wantFail: true},
		"NotSkipped":          {want: ExpectCompDiff().WithComposition("comp-a").WithImpactAnalysisSkipped(), wantFail: true},
		"WrongXRStatus":       {want: ExpectCompDiff().WithComposition("comp-a").WithXRImpact("XNop", "xr-2", "default", "changed"), wantFail: true},
		"WrongFilterReason": {
			want: ExpectCompDiff().WithComposition("comp-a").WithXRImpact("XNop", "xr-3", "default", "filtered").
				WithFilterReason(string(dt.FilterReasonManualPolicy)),
			wantFail: true,
		},
		"WrongDownstreamValue": {want: downstream().WithFieldChange("spec.forProvider.configData", "old", "other"), wantFail: true},
		"BadValuePattern":      {want: downstream().WithFieldValuePattern("spec.forProvider.configData", `^old$`), wantFail: true},
		"DryRunPerformed":      {want: downstream().WithDryRunPerformed(), wantFail: true},
		"ErrorOnOtherXR": {
			want:     ExpectCompDiff().WithComposition("comp-a").WithXRImpact("XNop", "xr-2", "default", "unchanged").WithErrorResourceID("XNop/xr-1"),
			wantFail: true,
		},
		"MissingTopLevelError": {want: ExpectCompDiff().WithError("Composition/comp-a"), wantFail: true},
	}

	for name, tc := range cases {
		checkAssertion(t, name, tc.wantFail, func(t *testing.T) { t.Helper(); AssertStructuredCompDiff(t, out, tc.want) })
	}
}

func TestParsePath(t *testing.T) {
	cases := map[string]struct {
		path    string
		want    []pathToken
		wantErr bool
	}{
		"Dotted":        {path: "spec.forProvider.region", want: []pathToken{{key: "spec"}, {key: "forProvider"}, {key: "region"}}},
		"Index":         {path: "spec.items[2].name", want: []pathToken{{key: "spec"}, {key: "items"}, {index: 2, isIndex: true}, {key: "name"}}},
		"DoubleQuoted":  {path: `metadata.annotations["a.b/c"]`, want: []pathToken{{key: "metadata"}, {key: "annotations"}, {key: "a.b/c"}}},
		"SingleQuoted":  {path: "metadata.annotations['a.b/c']", want: []pathToken{{key: "metadata"}, {key: "annotations"}, {key: "a.b/c"}}},
		"EscapedQuote":  {path: `data["say \"hi\""]`, want: []pathToken{{key: "data"}, {key: `say "hi"`}}},
		"BadIndex":      {path: "spec.items[x]", wantErr: true},
		"Unterminated":  {path: "spec.items[1", wantErr: true},
		"UnterminatedQ": {path: "metadata['a.b", wantErr: true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := parsePath(tc.path)
			if (err != nil) != tc.wantErr {
				t.Fatalf("parsePath(%q): err = %v, wantErr %t", tc.path, err, tc.wantErr)
			}

			if !tc.wantErr && !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parsePath(%q) = %+v, want %+v", tc.path, got, tc.want)
			}
		})
	}
}

func TestValueAt(t *testing.T) {
	changes := []plan.FieldChange{
		{Path: "spec.a", Actual: val("old-a"), Planned: val("new-a")},
		{Path: "spec.list[0].x", Planned: val("x0")},
		{Path: "spec.list[1].x", Actual: val("gone")},
		{Path: `metadata.labels["app.io/name"]`, Planned: val("app")},
	}

	cases := map[string]struct {
		path    string
		planned bool
		want    any
	}{
		"Leaf":            {path: "spec.a", planned: true, want: "new-a"},
		"LeafActual":      {path: "spec.a", want: "old-a"},
		"Subtree":         {path: "spec", planned: true, want: map[string]any{"a": "new-a", "list": []any{map[string]any{"x": "x0"}}}},
		"SubtreeActual":   {path: "spec.list", want: []any{nil, map[string]any{"x": "gone"}}},
		"QuotedKey":       {path: "metadata.labels['app.io/name']", planned: true, want: "app"},
		"Absent":          {path: "spec.b", planned: true, want: nil},
		"PrefixIsNotPath": {path: "spec.list[0].xy", planned: true, want: nil},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := valueAt(t, changes, tc.planned, tc.path); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("valueAt(%q, planned=%t) = %#v, want %#v", tc.path, tc.planned, got, tc.want)
			}
		})
	}
}
