package simtest

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	dt "github.com/crossplane/cli/v2/internal/diff/result"
	"github.com/crossplane/cli/v2/internal/plan"
)

// The assertions below read project simulate's structured output, plan.Result, behind the builder API
// in structured_assertions.go. The builders speak crossplane-diff's vocabulary (added/modified/removed
// changes, xrs[] entries, impactAnalysis[] entries); this file maps each onto its plan.Result home:
//
//   - a change is a ResourcePlan, its type the plan's Action (create/update/delete);
//   - a field value or old/new pair is read from the plan's leaf FieldChanges, a non-leaf path being
//     re-assembled from the leaves under it, or, for a pair whose sides are equal, from the plan's
//     before and after objects;
//   - an xrs[] or impactAnalysis[] entry is a Composites entry, its changes the plans grouped under it;
//   - a composition is a Compositions entry plus its own (ungrouped) plan;
//   - an error's resourceID ("Kind/name") is the Kind and Name of its Errors[].resource.

// changeAction maps a builder change word onto its plan action, empty for an unknown word.
func changeAction(changeType string) plan.Action {
	switch changeType {
	case dt.DiffTypeWordAdded:
		return plan.ActionCreate
	case dt.DiffTypeWordModified:
		return plan.ActionUpdate
	case dt.DiffTypeWordRemoved:
		return plan.ActionDelete
	default:
		return ""
	}
}

// statusFiltered is crossplane-diff's status for a composite left out of impact analysis, which
// plan.Result splits into deferred (Manual policy) and skipped (any other reason).
const statusFiltered = "filtered"

// ParseResult parses project simulate's -o json output.
func ParseResult(jsonOutput string) (plan.Result, error) {
	var res plan.Result
	if err := json.Unmarshal([]byte(jsonOutput), &res); err != nil {
		return res, fmt.Errorf("failed to parse plan result JSON output: %w", err)
	}

	return res, nil
}

// AssertStructured checks a plan.Result against expected, running each section only if it was
// expected. Accepts any builder level, so callers don't need And()/AndXR()/AndComp().
func AssertStructured(t *testing.T, jsonOutput string, e Expectation) {
	t.Helper()

	expected := e.expectation()

	res, err := ParseResult(jsonOutput)
	if err != nil {
		t.Fatalf("%v\nOutput was:\n%s", err, jsonOutput)
	}

	if s := expected.summary; s != nil {
		assertSummary(t, "Summary", s, res.Summary.Create, res.Summary.Update, res.Summary.Destroy, res.Summary.Replace)
	}

	changes := changePlans(res.Plans, func(*plan.ResourcePlan) bool { return true })
	assertResourceExpectations(t, "", changes, expected.resources)

	if len(expected.resources) > 0 && len(changes) != len(expected.resources) {
		t.Errorf("Expected %d changes, got %d: %v", len(expected.resources), len(changes), planIDs(changes))
	}

	for _, want := range expected.errors {
		got := findMatchingError(res.Errors, want.resourceID)
		if got == nil {
			t.Errorf("Expected error for resourceID %q not found in output. Actual error IDs: %v",
				want.resourceID, errorIDs(res.Errors))

			continue
		}

		assertErrorMatch(t, *got, want)
	}

	// Pinning any error expectations pins the count too. Zero expectations = no opinion on errors.
	if len(expected.errors) > 0 && len(res.Errors) != len(expected.errors) {
		t.Errorf("Expected %d errors in structured output, got %d", len(expected.errors), len(res.Errors))
	}

	assertWarnings(t, res.Warnings, warningMatches(expected.warnings, func(w *WarningExpectation) warningMatch { return w.warningMatch }), expected.noWarnings)

	assertXRExpectations(t, res, expected.xrs)

	for _, id := range expected.errorIDs {
		if findMatchingError(res.Errors, id) == nil {
			t.Errorf("Expected top-level error with resourceID %q not found. Actual error IDs: %v", id, errorIDs(res.Errors))
		}
	}

	for _, want := range expected.compositions {
		assertComposition(t, res, want)
	}
}

func assertComposition(t *testing.T, res plan.Result, want *CompositionExpectation) {
	t.Helper()

	i := slices.IndexFunc(res.Compositions, func(c plan.CompositionResult) bool { return c.Ref.Name == want.name })
	if i < 0 {
		names := make([]string, 0, len(res.Compositions))
		for _, c := range res.Compositions {
			names = append(names, c.Ref.Name)
		}

		t.Errorf("Expected composition %s not found in output. Actual compositions: %v", want.name, names)

		return
	}

	comp := res.Compositions[i]
	label := "Composition " + want.name
	compPlan := findPlan(res.Plans, comp.Ref, nil)

	if want.errorContains != "" {
		got := ""
		if compPlan != nil {
			got = compPlan.Error
		}

		if !strings.Contains(got, want.errorContains) {
			t.Errorf("%s: expected error containing %q, got %q", label, want.errorContains, got)
		}
	}

	composites := slices.DeleteFunc(slices.Clone(res.Composites), func(c plan.CompositeResult) bool {
		return c.Composition == nil || *c.Composition != comp.Ref
	})

	assertAffected(t, label, composites, want)

	if want.impactAnalysisSkipped != nil && comp.ImpactAnalysisSkipped != *want.impactAnalysisSkipped {
		t.Errorf("%s: ImpactAnalysisSkipped: expected %t, got %t", label, *want.impactAnalysisSkipped, comp.ImpactAnalysisSkipped)
	}

	assertRevisionImpact(t, label, comp.RevisionImpact, want)

	if want.compositionChangeType != "" {
		switch {
		case compPlan == nil:
			t.Errorf("%s: expected composition changes but found no plan for it", label)
		case compPlan.Action != changeAction(want.compositionChangeType):
			t.Errorf("%s: expected action %s, got %q (error %q)", label, changeAction(want.compositionChangeType), compPlan.Action, compPlan.Error)
		default:
			assertFields(t, label, compPlan, want.compositionChangeType, nil, want.compositionFieldChanges, nil)
		}
	}

	for _, wantXR := range want.xrImpacts {
		assertXRImpact(t, res, label, composites, wantXR)
	}
}

// assertAffected checks the composition's affected-composite counts. Total counts every composite the
// composition reaches, filtered ones included.
func assertAffected(t *testing.T, label string, composites []plan.CompositeResult, want *CompositionExpectation) {
	t.Helper()

	count := func(f func(plan.CompositeResult) bool) int {
		n := 0
		for _, c := range composites {
			if f(c) {
				n++
			}
		}

		return n
	}
	status := func(s plan.CompositeStatus) func(plan.CompositeResult) bool {
		return func(c plan.CompositeResult) bool { return c.Status == s }
	}

	check := func(name string, want *int, got int) {
		if want != nil && got != *want {
			t.Errorf("%s: AffectedResources.%s: expected %d, got %d", label, name, *want, got)
		}
	}

	check("Total", want.affectedTotal, len(composites))
	check("WithChanges", want.affectedWithChanges, count(status(plan.CompositeChanged)))
	check("Unchanged", want.affectedUnchanged, count(status(plan.CompositeUnchanged)))
	check("WithErrors", want.affectedWithErrors, count(status(plan.CompositeError)))
	check("FilteredByDeletion", want.affectedFilteredByDeletion, count(func(c plan.CompositeResult) bool {
		return c.Status == plan.CompositeSkipped && c.Reason == plan.CompositeReasonDeleting
	}))
}

func assertRevisionImpact(t *testing.T, label string, got *plan.RevisionImpact, want *CompositionExpectation) {
	t.Helper()

	if want.revisionImpact == nil && want.predictedRevisionName == nil {
		return
	}

	if got == nil {
		if want.revisionImpact != nil || want.predictedRevisionName.pattern != "" {
			t.Errorf("%s: expected a revisionImpact, but none was reported", label)
		}

		return
	}

	if w := want.revisionImpact; w != nil {
		if got.ChangeScope != w.changeScope {
			t.Errorf("%s: RevisionImpact.ChangeScope: expected %q, got %q", label, w.changeScope, got.ChangeScope)
		}

		if got.CreatesRevision != w.createsRevision {
			t.Errorf("%s: RevisionImpact.CreatesRevision: expected %t, got %t", label, w.createsRevision, got.CreatesRevision)
		}

		if got.RepointedComposites != w.repointedComposites {
			t.Errorf("%s: RevisionImpact.RepointedComposites: expected %d, got %d", label, w.repointedComposites, got.RepointedComposites)
		}
	}

	w := want.predictedRevisionName
	switch {
	case w == nil:
	case w.pattern == "":
		if got.PredictedRevision != "" {
			t.Errorf("%s: RevisionImpact.PredictedRevision: expected absent, got %q", label, got.PredictedRevision)
		}
	default:
		matched, err := regexp.MatchString(w.pattern, got.PredictedRevision)

		switch {
		case err != nil:
			t.Errorf("%s: RevisionImpact.PredictedRevision: invalid pattern %q: %v", label, w.pattern, err)
		case !matched:
			t.Errorf("%s: RevisionImpact.PredictedRevision: %q does not match pattern %q", label, got.PredictedRevision, w.pattern)
		}
	}
}

func assertXRImpact(t *testing.T, res plan.Result, label string, composites []plan.CompositeResult, want *XRImpactExpectation) {
	t.Helper()

	i := slices.IndexFunc(composites, func(c plan.CompositeResult) bool {
		return refMatches(c.Ref, want.kind, want.name, want.namespace, nil, want.anyNameAllowed, false)
	})
	if i < 0 {
		t.Errorf("%s: Expected XR impact %s/%s (ns=%s) not found. Actual XRs: %v",
			label, want.kind, want.name, want.namespace, compositeIDs(composites))

		return
	}

	got := composites[i]
	label = fmt.Sprintf("%s: XR %s/%s", label, want.kind, got.Ref.Name)

	if !statusMatches(got.Status, want.status) {
		t.Errorf("%s: expected status %s, got %s", label, want.status, got.Status)
	}

	if want.filterReason != "" && string(got.Reason) != want.filterReason {
		t.Errorf("%s: expected filterReason %s, got %s", label, want.filterReason, got.Reason)
	}

	errs := slices.DeleteFunc(slices.Clone(res.Errors), func(e plan.Error) bool { return e.Ref == nil || *e.Ref != got.Ref })

	if want.errorContains != "" {
		msgs := make([]string, 0, len(errs))
		for _, e := range errs {
			msgs = append(msgs, e.Message)
		}

		if text := strings.Join(msgs, "; "); !strings.Contains(text, want.errorContains) {
			t.Errorf("%s: expected error containing %q, got %q", label, want.errorContains, text)
		}
	}

	for _, id := range want.errorIDs {
		if findMatchingError(errs, id) == nil {
			t.Errorf("%s: expected error with resourceID %q not found. Actual error IDs: %v", label, id, errorIDs(errs))
		}
	}

	downstream := changePlans(res.Plans, groupedUnder(got.Ref))

	if s := want.downstreamSummary; s != nil {
		create, update, destroy, replace := countActions(downstream)
		assertSummary(t, label+": downstream summary", s, create, update, destroy, replace)
	}

	for _, wantRes := range want.downstreamResources {
		p := findMatchingPlan(downstream, wantRes.changeType, wantRes.kind, wantRes.name, wantRes.namespace, wantRes.namePattern, wantRes.anyNameAllowed)
		if p == nil {
			t.Errorf("%s: expected downstream %s resource %s/%s not found. Actual: %v",
				label, wantRes.changeType, wantRes.kind, wantRes.name, planIDs(downstream))

			continue
		}

		id := fmt.Sprintf("%s: downstream %s %s/%s", label, wantRes.changeType, wantRes.kind, p.Name)
		assertDryRun(t, id, p.DryRun, wantRes.dryRun)
		assertFields(t, id, p, wantRes.changeType, wantRes.fieldValues, wantRes.fieldChanges, wantRes.fieldValuePatterns)
		assertSpec(t, id, p, wantRes)
	}
}

// statusMatches compares a composite's status with crossplane-diff's, whose "filtered" plan.Result
// splits into deferred and skipped.
func statusMatches(got plan.CompositeStatus, want string) bool {
	if want == statusFiltered {
		// Assert filterReason when it matters which of deferred or skipped applies.
		return got == plan.CompositeDeferred || got == plan.CompositeSkipped
	}

	return string(got) == want
}

// assertXRExpectations checks the xrs[] expectations against Composites.
//
// Each expectation consumes the entry it matches, so two expectations can never both resolve to the
// same entry, and the entry count is pinned once the test has an opinion on xrs[] at all. Without those
// guards a duplicated identity satisfied every expectation that matched it (issues #477, #488).
func assertXRExpectations(t *testing.T, res plan.Result, expected []*XRExpectation) {
	t.Helper()

	if len(expected) == 0 {
		return
	}

	if len(res.Composites) != len(expected) {
		t.Errorf("Expected %d xrs[] entries, got %d: %v", len(expected), len(res.Composites), compositeIDs(res.Composites))
	}

	used := make([]bool, len(res.Composites))

	for _, want := range expected {
		i := -1
		for j, c := range res.Composites {
			if !used[j] && refMatches(c.Ref, want.kind, want.name, want.namespace, want.namePattern, want.anyName, true) {
				i = j
				break
			}
		}

		if i < 0 {
			t.Errorf("Expected xrs[] entry %s/%s (ns=%s) not found. Actual: %v",
				want.kind, want.name, want.namespace, compositeIDs(res.Composites))

			continue
		}

		used[i] = true
		got := res.Composites[i]
		label := fmt.Sprintf("%s/%s", got.Ref.Kind, got.Ref.Name)

		if want.status != "" && !statusMatches(got.Status, want.status) {
			t.Errorf("xr %s: status: expected %q, got %q", label, want.status, got.Status)
		}

		changes := changePlans(res.Plans, groupedUnder(got.Ref))

		if s := want.summary; s != nil {
			create, update, destroy, replace := countActions(changes)
			assertSummary(t, "xr "+label+": summary", s, create, update, destroy, replace)
		}

		assertResourceExpectations(t, label, changes, want.changes)

		// Pin the entry's change count too, so a change the test did not ask for cannot hide inside a
		// matched entry.
		if len(want.changes) > 0 && len(changes) != len(want.changes) {
			t.Errorf("xr %s: expected %d changes, got %d: %v", label, len(want.changes), len(changes), planIDs(changes))
		}

		for _, id := range want.errorIDs {
			e := findMatchingError(res.Errors, id)
			if e == nil || e.Ref == nil || *e.Ref != got.Ref {
				t.Errorf("xr %s: expected error with resourceID %q not found against it. Actual error IDs: %v", label, id, errorIDs(res.Errors))
			}
		}
	}
}

// assertResourceExpectations checks each expectation against a set of change plans. scope is "" for
// the flat view, or an XR's Kind/name, and is used only in failure messages.
func assertResourceExpectations(t *testing.T, scope string, changes []*plan.ResourcePlan, resources []*ResourceExpectation) {
	t.Helper()

	prefix := ""
	if scope != "" {
		prefix = "xr " + scope + ": "
	}

	for _, want := range resources {
		p := findMatchingPlan(changes, want.changeType, want.kind, want.name, want.namespace, want.namePattern, want.anyNameAllowed)
		if p == nil {
			t.Errorf("%sExpected %s resource %s/%s (ns=%s) not found. Actual resources: %v",
				prefix, want.changeType, want.kind, want.name, want.namespace, planIDs(changes))

			continue
		}

		id := fmt.Sprintf("%s%s %s/%s", prefix, want.changeType, want.kind, want.name)
		assertDryRun(t, id, p.DryRun, want.dryRun)
		assertFields(t, id, p, want.changeType, want.fieldValues, want.fieldChanges, want.fieldValuePatterns)

		assertSpec(t, id, p, want)
	}
}

// assertSpec checks a plan's whole spec against the expectation's WithSpec, when set.
func assertSpec(t *testing.T, id string, p *plan.ResourcePlan, want *ResourceExpectation) {
	t.Helper()

	if want.specMatch == nil {
		return
	}

	if spec := valueAt(t, p.Changes, want.changeType != dt.DiffTypeWordRemoved, "spec"); !reflect.DeepEqual(spec, want.specMatch) {
		t.Errorf("%s: spec mismatch: expected %v, got %v", id, want.specMatch, spec)
	}
}

// assertFields checks a plan's field expectations. values and patterns read the planned side, or the
// actual side of a delete, which carries what is removed. changes compare both sides.
func assertFields(t *testing.T, id string, p *plan.ResourcePlan, changeType string, values map[string]any, changes map[string][2]any, patterns map[string]*regexp.Regexp) {
	t.Helper()

	planned := changeType != dt.DiffTypeWordRemoved

	for path, want := range values {
		if got := valueAt(t, p.Changes, planned, path); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: field %s: expected %v, got %v", id, path, want, got)
		}

		if want == nil && planned {
			assertAbsentInAfter(t, id, p, path)
		}
	}

	for path, want := range changes {
		if want[0] == nil && want[1] == nil {
			t.Fatalf("%s: field %s: WithFieldChange(path, nil, nil) asserts nothing; use WithFieldAbsent", id, path)
		}

		// FieldChanges are leaf changes only, so a field rendered identically on both sides is read from
		// the plan's objects instead, and must have no change at or under it.
		if want[0] != nil && reflect.DeepEqual(want[0], want[1]) {
			if c := changesUnder(t, p.Changes, path); len(c) > 0 {
				t.Errorf("%s: field %s: expected unchanged (%v), got changes %+v", id, path, want[0], c)
			}

			for i, u := range []*unstructured.Unstructured{p.Before, p.After} {
				if got, ok := objectValueAt(t, u, path); !ok || !reflect.DeepEqual(got, want[0]) {
					t.Errorf("%s: field %s in %s: expected %v, got %v (present: %t)", id, path, []string{"before", "after"}[i], want[0], got, ok)
				}
			}

			continue
		}

		if got := valueAt(t, p.Changes, false, path); !reflect.DeepEqual(got, want[0]) {
			t.Errorf("%s: field %s old value: expected %v, got %v", id, path, want[0], got)
		}

		if got := valueAt(t, p.Changes, true, path); !reflect.DeepEqual(got, want[1]) {
			t.Errorf("%s: field %s new value: expected %v, got %v", id, path, want[1], got)
		}
	}

	assertPatterns(t, id, p, planned, patterns)
}

// assertPatterns checks each field value against its pattern, failing on a missing field.
func assertPatterns(t *testing.T, id string, p *plan.ResourcePlan, planned bool, patterns map[string]*regexp.Regexp) {
	t.Helper()

	for path, pattern := range patterns {
		v := valueAt(t, p.Changes, planned, path)
		if v == nil {
			t.Errorf("%s: field %s: expected a value matching pattern %q, but the field is missing", id, path, pattern.String())
			continue
		}

		got := fmt.Sprintf("%v", v)
		if !pattern.MatchString(got) {
			t.Errorf("%s: field %s value %q does not match pattern %q", id, path, got, pattern.String())
		}
	}
}

// assertAbsentInAfter requires path to be absent from the plan's After object, not only from its leaf
// changes, when After is set.
func assertAbsentInAfter(t *testing.T, id string, p *plan.ResourcePlan, path string) {
	t.Helper()

	if p.After == nil {
		return
	}

	if got, ok := objectValueAt(t, p.After, path); ok {
		t.Errorf("%s: field %s: expected absent, but after carries %v", id, path, got)
	}
}

// assertDryRun checks a plan's dry-run verification: either that its desired state was reported as NOT
// verified against the apiserver (for a reason, optionally with the cluster's own explanation), or that
// no such report is present at all.
func assertDryRun(t *testing.T, id string, got *plan.Verification, want dryRunExpectation) {
	t.Helper()

	if want.mustBePerformed {
		if got != nil {
			t.Errorf("%s: expected the desired state to have been dry-run against the apiserver, but it reports skipReason %q (detail: %q)",
				id, got.SkipReason, got.Detail)
		}

		return
	}

	if want.skipReason == "" {
		return
	}

	if got == nil {
		t.Errorf("%s: expected dryRun.skipReason %q, but no dryRun was reported (absence means the desired state went through the apiserver)",
			id, want.skipReason)

		return
	}

	if got.SkipReason != want.skipReason {
		t.Errorf("%s: dryRun.skipReason: expected %q, got %q", id, want.skipReason, got.SkipReason)
	}

	// Performed must be false whenever the object is present; see dt.DryRunInfo.
	if got.Performed {
		t.Errorf("%s: dryRun.performed was true on a reported skip, which is contradictory", id)
	}

	if want.detailSubstring != "" && !strings.Contains(got.Detail, want.detailSubstring) {
		t.Errorf("%s: dryRun.detail %q does not contain %q — the cluster's own explanation must be propagated, not swallowed",
			id, got.Detail, want.detailSubstring)
	}

	if want.storageVersion != "" && got.StorageVersion != want.storageVersion {
		t.Errorf("%s: dryRun.storageVersion: expected %q, got %q", id, want.storageVersion, got.StorageVersion)
	}
}

// assertWarnings checks each expectation against got, then — if the test pinned any expectations, or
// asserted there are none — verifies no extras were emitted, mirroring the errors[] guard. Without the
// count check, warning duplication is untestable: a regression that emits the same advisory once per
// XR still satisfies every find-first-match expectation. No expectations and no noWarnings = no opinion.
func assertWarnings(t *testing.T, got []plan.Warning, want []warningMatch, noWarnings bool) {
	t.Helper()

	for _, w := range want {
		assertWarningExpectation(t, got, w)
	}

	if (len(want) > 0 || noWarnings) && len(got) != len(want) {
		t.Errorf("Expected %d warnings in structured output, got %d: %v", len(want), len(got), warningMessages(got))
	}
}

// assertWarningExpectation finds a warning whose message contains want's substring and, when the
// expectation pins one, whose context map is exactly want's. Both conditions select the warning, since
// several warnings legitimately share a message and differ only in context.
func assertWarningExpectation(t *testing.T, got []plan.Warning, want warningMatch) {
	t.Helper()

	var contextsSeen []map[string]string

	for _, w := range got {
		if !strings.Contains(w.Message, want.messageContains) {
			continue
		}

		if want.context == nil || reflect.DeepEqual(want.context, w.Context) {
			return
		}

		contextsSeen = append(contextsSeen, w.Context)
	}

	if len(contextsSeen) > 0 {
		t.Errorf("warning %q context mismatch: expected %v, got only %v", want.messageContains, want.context, contextsSeen)
		return
	}

	t.Errorf("expected a warning containing %q in structured output; got %v", want.messageContains, warningMessages(got))
}

// assertErrorMatch checks a single error against its expectation.
func assertErrorMatch(t *testing.T, got plan.Error, want *ErrorExpectation) {
	t.Helper()

	if want.messageContains != "" && !strings.Contains(got.Message, want.messageContains) {
		t.Errorf("error %q: message %q does not contain %q", want.resourceID, got.Message, want.messageContains)
	}

	for _, wantVF := range want.validationFailures {
		i := slices.IndexFunc(got.ValidationFailures, func(v plan.ValidationFailure) bool {
			return v.APIVersion == wantVF.apiVersion && v.Kind == wantVF.kind && v.Name == wantVF.name &&
				(wantVF.namespace == "" || v.Namespace == wantVF.namespace)
		})
		if i < 0 {
			actual := make([]string, 0, len(got.ValidationFailures))
			for _, vf := range got.ValidationFailures {
				actual = append(actual, fmt.Sprintf("%s/%s %s/%s", vf.APIVersion, vf.Kind, vf.Namespace, vf.Name))
			}

			t.Errorf("error %q: expected validationFailure %s/%s %s/%s not found. Actual: %v",
				want.resourceID, wantVF.apiVersion, wantVF.kind, wantVF.namespace, wantVF.name, actual)

			continue
		}

		assertValidationFailureMatch(t, got.ValidationFailures[i], wantVF)
	}

	if len(want.validationFailures) > 0 && len(got.ValidationFailures) != len(want.validationFailures) {
		t.Errorf("error %q: expected %d validationFailures, got %d",
			want.resourceID, len(want.validationFailures), len(got.ValidationFailures))
	}
}

func assertValidationFailureMatch(t *testing.T, got plan.ValidationFailure, want *ValidationFailureExpectation) {
	t.Helper()

	label := fmt.Sprintf("validationFailure %s/%s %s", want.apiVersion, want.kind, want.name)

	if want.status != "" && got.Status != want.status {
		t.Errorf("%s: status: expected %q, got %q", label, want.status, got.Status)
	}

	for _, wantFE := range want.errors {
		i := slices.IndexFunc(got.Errors, func(fe plan.FieldValidationError) bool {
			return fe.Type == wantFE.errType && fe.Field == wantFE.field
		})
		if i < 0 {
			actual := make([]string, 0, len(got.Errors))
			for _, fe := range got.Errors {
				actual = append(actual, fmt.Sprintf("[%s] %s", fe.Type, fe.Field))
			}

			t.Errorf("%s: expected fieldError [%s] %s not found. Actual: %v", label, wantFE.errType, wantFE.field, actual)

			continue
		}

		gotFE := got.Errors[i]

		if wantFE.messageContains != "" && !strings.Contains(gotFE.Message, wantFE.messageContains) {
			t.Errorf("%s: fieldError [%s] %s: message %q does not contain %q",
				label, wantFE.errType, wantFE.field, gotFE.Message, wantFE.messageContains)
		}

		if wantFE.hasValue && !reflect.DeepEqual(gotFE.Value, wantFE.value) {
			t.Errorf("%s: fieldError [%s] %s: value: expected %v (%T), got %v (%T)",
				label, wantFE.errType, wantFE.field, wantFE.value, wantFE.value, gotFE.Value, gotFE.Value)
		}
	}

	if len(want.errors) > 0 && len(got.Errors) != len(want.errors) {
		t.Errorf("%s: expected %d field errors, got %d", label, len(want.errors), len(got.Errors))
	}
}

func assertSummary(t *testing.T, label string, want *expectedSummary, create, update, destroy, replace int) {
	t.Helper()

	// expectedSummary has no replace bucket, so a replace would otherwise go uncounted.
	if replace != 0 {
		t.Errorf("%s: %d replace action(s) can't be pinned by a summary of added/modified/removed", label, replace)
	}

	if create != want.added || update != want.modified || destroy != want.removed {
		t.Errorf("%s: expected {added:%d modified:%d removed:%d}, got {added:%d modified:%d removed:%d}",
			label, want.added, want.modified, want.removed, create, update, destroy)
	}
}

// changePlans returns the plans that change something, i.e. neither failed nor no-op, and satisfy f.
func changePlans(plans []*plan.ResourcePlan, f func(*plan.ResourcePlan) bool) []*plan.ResourcePlan {
	var out []*plan.ResourcePlan

	for _, p := range plans {
		if p.Error == "" && p.Action != plan.ActionNoOp && f(p) {
			out = append(out, p)
		}
	}

	return out
}

func groupedUnder(ref plan.ResourceRef) func(*plan.ResourcePlan) bool {
	return func(p *plan.ResourcePlan) bool { return p.Composite != nil && *p.Composite == ref }
}

func countActions(plans []*plan.ResourcePlan) (create, update, destroy, replace int) {
	for _, p := range plans {
		switch p.Action {
		case plan.ActionCreate:
			create++
		case plan.ActionUpdate:
			update++
		case plan.ActionDelete:
			destroy++
		case plan.ActionReplace:
			replace++
		case plan.ActionNoOp:
		}
	}

	return create, update, destroy, replace
}

// findPlan returns the plan for ref grouped under composite.
func findPlan(plans []*plan.ResourcePlan, ref plan.ResourceRef, composite *plan.ResourceRef) *plan.ResourcePlan {
	for _, p := range plans {
		if p.APIVersion == ref.APIVersion && p.Kind == ref.Kind && p.Name == ref.Name && p.Namespace == ref.Namespace &&
			reflect.DeepEqual(p.Composite, composite) {
			return p
		}
	}

	return nil
}

// findMatchingPlan returns the first plan with changeType's action, kind and (when given) namespace,
// whose name is any, matches namePattern, or equals name.
func findMatchingPlan(plans []*plan.ResourcePlan, changeType, kind, name, namespace string, namePattern *regexp.Regexp, anyName bool) *plan.ResourcePlan {
	for _, p := range plans {
		if p.Action != changeAction(changeType) {
			continue
		}

		ref := plan.ResourceRef{Kind: p.Kind, Name: p.Name, Namespace: p.Namespace}
		if refMatches(ref, kind, name, namespace, namePattern, anyName, false) {
			return p
		}
	}

	return nil
}

// refMatches reports whether ref has kind and the expected name (any, matching namePattern, or equal).
// An empty namespace matches any namespace unless exactNamespace is set.
func refMatches(ref plan.ResourceRef, kind, name, namespace string, namePattern *regexp.Regexp, anyName, exactNamespace bool) bool {
	if ref.Kind != kind || ((exactNamespace || namespace != "") && ref.Namespace != namespace) {
		return false
	}

	switch {
	case anyName:
		return true
	case namePattern != nil:
		return namePattern.MatchString(ref.Name)
	default:
		return ref.Name == name
	}
}

// findMatchingError returns the error whose resource is resourceID ("Kind/name").
func findMatchingError(errs []plan.Error, resourceID string) *plan.Error {
	for i := range errs {
		if errorID(errs[i]) == resourceID {
			return &errs[i]
		}
	}

	return nil
}

// errorID is crossplane-diff's resourceID for an error: its resource's Kind/name, empty for a run
// error no resource owns.
func errorID(e plan.Error) string {
	if e.Ref == nil {
		return ""
	}

	return e.Ref.Kind + "/" + e.Ref.Name
}

func errorIDs(errs []plan.Error) []string {
	ids := make([]string, 0, len(errs))
	for _, e := range errs {
		ids = append(ids, errorID(e))
	}

	return ids
}

func planIDs(plans []*plan.ResourcePlan) []string {
	ids := make([]string, 0, len(plans))
	for _, p := range plans {
		ids = append(ids, fmt.Sprintf("%s %s/%s (ns=%s)", p.Action, p.Kind, p.Name, p.Namespace))
	}

	return ids
}

func compositeIDs(cs []plan.CompositeResult) []string {
	ids := make([]string, 0, len(cs))
	for _, c := range cs {
		ids = append(ids, fmt.Sprintf("%s/%s (ns=%s, status=%s)", c.Ref.Kind, c.Ref.Name, c.Ref.Namespace, c.Status))
	}

	return ids
}

func warningMessages(ws []plan.Warning) []string {
	msgs := make([]string, 0, len(ws))
	for _, w := range ws {
		msgs = append(msgs, w.Message)
	}

	return msgs
}

func warningMatches[W any](ws []W, f func(W) warningMatch) []warningMatch {
	out := make([]warningMatch, 0, len(ws))
	for _, w := range ws {
		out = append(out, f(w))
	}

	return out
}

// pathToken is one step of a field path: a map key, or a list index.
type pathToken struct {
	key     string
	index   int
	isIndex bool
}

// parsePath splits a field path into its steps. It reads both plan.Result's form (keys joined by ".",
// other keys as ["key"], indexes as [N]) and the test expectations' ['key'] form.
func parsePath(path string) ([]pathToken, error) {
	var toks []pathToken

	for i := 0; i < len(path); {
		switch {
		case path[i] == '.':
			i++
		case strings.HasPrefix(path[i:], `["`):
			q, err := strconv.QuotedPrefix(path[i+1:])
			end := i + 1 + len(q)

			if err != nil || end >= len(path) || path[end] != ']' {
				return nil, fmt.Errorf("malformed quoted key at %d in %q", i, path)
			}

			k, _ := strconv.Unquote(q)
			toks = append(toks, pathToken{key: k})
			i = end + 1
		case strings.HasPrefix(path[i:], `['`):
			j := strings.Index(path[i+2:], `']`)
			if j < 0 {
				return nil, fmt.Errorf("unterminated quoted key at %d in %q", i, path)
			}

			toks = append(toks, pathToken{key: path[i+2 : i+2+j]})
			i += 2 + j + 2
		case path[i] == '[':
			j := strings.IndexByte(path[i:], ']')
			if j < 0 {
				return nil, fmt.Errorf("unterminated index at %d in %q", i, path)
			}

			n, err := strconv.Atoi(path[i+1 : i+j])
			if err != nil {
				return nil, fmt.Errorf("bad index at %d in %q: %w", i, path, err)
			}

			toks = append(toks, pathToken{index: n, isIndex: true})
			i += j + 1
		default:
			j := strings.IndexAny(path[i:], ".[")
			if j < 0 {
				j = len(path) - i
			}

			toks = append(toks, pathToken{key: path[i : i+j]})
			i += j
		}
	}

	return toks, nil
}

func mustParsePath(t *testing.T, path string) []pathToken {
	t.Helper()

	toks, err := parsePath(path)
	if err != nil {
		t.Fatalf("field path: %v", err)
	}

	return toks
}

// under returns the steps of toks below prefix, and whether toks is at or under prefix at all.
func under(toks, prefix []pathToken) ([]pathToken, bool) {
	if len(toks) < len(prefix) || !slices.Equal(toks[:len(prefix)], prefix) {
		return nil, false
	}

	return toks[len(prefix):], true
}

// changesUnder returns the changes at or under path.
func changesUnder(t *testing.T, changes []plan.FieldChange, path string) []plan.FieldChange {
	t.Helper()

	prefix := mustParsePath(t, path)

	var out []plan.FieldChange

	for _, c := range changes {
		if _, ok := under(mustParsePath(t, c.Path), prefix); ok {
			out = append(out, c)
		}
	}

	return out
}

// objectValueAt returns the value at path in u, and whether it is there at all.
func objectValueAt(t *testing.T, u *unstructured.Unstructured, path string) (any, bool) {
	t.Helper()

	if u == nil {
		return nil, false
	}

	var node any = u.Object

	for _, tok := range mustParsePath(t, path) {
		if tok.isIndex {
			l, ok := node.([]any)
			if !ok || tok.index >= len(l) {
				return nil, false
			}

			node = l[tok.index]

			continue
		}

		m, ok := node.(map[string]any)
		if !ok {
			return nil, false
		}

		if node, ok = m[tok.key]; !ok {
			return nil, false
		}
	}

	return node, true
}

// valueAt returns the value at path on one side of a plan's changes: the planned side, or else the
// actual. A leaf is its change's value; a non-leaf is re-assembled from the changed leaves under it.
// Nil means no change sets anything at or under path on that side.
func valueAt(t *testing.T, changes []plan.FieldChange, planned bool, path string) any {
	t.Helper()

	prefix := mustParsePath(t, path)

	var root any

	for _, c := range changes {
		v := c.Actual
		if planned {
			v = c.Planned
		}

		if v == nil {
			continue
		}

		rel, ok := under(mustParsePath(t, c.Path), prefix)
		if !ok {
			continue
		}

		if len(rel) == 0 {
			return v.Value
		}

		root = insert(root, rel, v.Value)
	}

	return assemble(root)
}

// insert sets v at toks below node, building maps as it goes. Lists are built as index maps, which
// assemble turns into slices.
func insert(node any, toks []pathToken, v any) any {
	if len(toks) == 0 {
		return v
	}

	if toks[0].isIndex {
		m, _ := node.(map[int]any)
		if m == nil {
			m = map[int]any{}
		}

		m[toks[0].index] = insert(m[toks[0].index], toks[1:], v)

		return m
	}

	m, _ := node.(map[string]any)
	if m == nil {
		m = map[string]any{}
	}

	m[toks[0].key] = insert(m[toks[0].key], toks[1:], v)

	return m
}

// assemble turns insert's index maps into slices.
func assemble(node any) any {
	switch n := node.(type) {
	case map[int]any:
		size := 0
		for i := range n {
			size = max(size, i+1)
		}

		out := make([]any, size)
		for i, v := range n {
			out[i] = assemble(v)
		}

		return out
	case map[string]any:
		for k, v := range n {
			n[k] = assemble(v)
		}

		return n
	default:
		return node
	}
}
