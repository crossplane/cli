package simtest

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/crossplane/crossplane-runtime/v2/pkg/fieldpath"

	dt "github.com/crossplane/cli/v2/internal/diff/result"
	"github.com/crossplane/cli/v2/internal/plan"
	"github.com/crossplane/cli/v2/internal/simulate"
)

// The assertions below read composition simulate's structured output, simulate.ProjectResult, behind the
// builder API in structured_assertions.go. The builders speak crossplane-diff's vocabulary
// (added/modified/removed changes, impactAnalysis[] entries); this file maps each onto its
// simulate.ProjectResult home:
//
//   - a change is a ResourcePlan, its type the plan's Action (create/update/delete);
//   - a field value or old/new pair is read from the plan's leaf FieldChanges, a non-leaf path being
//     re-assembled from the leaves under it, or, for a pair whose sides are equal, from the plan's
//     before and after objects;
//   - an impactAnalysis[] entry is a Composites entry, its changes the plans grouped under it;
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
// simulate.ProjectResult splits into deferred (Manual policy) and skipped (any other reason).
const statusFiltered = "filtered"

// AssertStructured checks composition simulate's -o json output against expected, running each
// section only if it was expected. Accepts any builder level, so callers don't need
// And()/AndXR()/AndComp().
func AssertStructured(t *testing.T, jsonOutput string, e Expectation) {
	t.Helper()

	expected := e.expectation()

	var res simulate.ProjectResult
	if err := json.Unmarshal([]byte(jsonOutput), &res); err != nil {
		t.Fatalf("failed to parse plan result JSON output: %v\nOutput was:\n%s", err, jsonOutput)
	}

	assertWarnings(t, res.Warnings, warningMatches(expected.warnings, func(w *WarningExpectation) warningMatch { return w.warningMatch }), expected.noWarnings)

	for _, id := range expected.errorIDs {
		if findMatchingError(res.Errors, id) == nil {
			t.Errorf("Expected top-level error with resourceID %q not found. Actual error IDs: %v", id, errorIDs(res.Errors))
		}
	}

	for _, want := range expected.compositions {
		assertComposition(t, res, want)
	}
}

func assertComposition(t *testing.T, res simulate.ProjectResult, want *CompositionExpectation) {
	t.Helper()

	i := slices.IndexFunc(res.Compositions, func(c simulate.CompositionResult) bool { return c.Ref.Name == want.name })
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

	composites := slices.DeleteFunc(slices.Clone(res.Composites), func(c simulate.CompositeResult) bool {
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
		}
	}

	for _, wantXR := range want.xrImpacts {
		assertXRImpact(t, res, label, composites, wantXR)
	}
}

// assertAffected checks the composition's affected-composite counts. Total counts every composite the
// composition reaches, filtered ones included.
func assertAffected(t *testing.T, label string, composites []simulate.CompositeResult, want *CompositionExpectation) {
	t.Helper()

	count := func(f func(simulate.CompositeResult) bool) int {
		n := 0
		for _, c := range composites {
			if f(c) {
				n++
			}
		}

		return n
	}
	status := func(s simulate.CompositeStatus) func(simulate.CompositeResult) bool {
		return func(c simulate.CompositeResult) bool { return c.Status == s }
	}

	check := func(name string, want *int, got int) {
		if want != nil && got != *want {
			t.Errorf("%s: AffectedResources.%s: expected %d, got %d", label, name, *want, got)
		}
	}

	check("Total", want.affectedTotal, len(composites))
	check("WithChanges", want.affectedWithChanges, count(status(simulate.CompositeChanged)))
	check("Unchanged", want.affectedUnchanged, count(status(simulate.CompositeUnchanged)))
	check("WithErrors", want.affectedWithErrors, count(status(simulate.CompositeError)))
	check("FilteredByDeletion", want.affectedFilteredByDeletion, count(func(c simulate.CompositeResult) bool {
		return c.Status == simulate.CompositeSkipped && c.Reason == simulate.CompositeReasonDeleting
	}))
}

func assertRevisionImpact(t *testing.T, label string, got *simulate.RevisionImpact, want *CompositionExpectation) {
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

func assertXRImpact(t *testing.T, res simulate.ProjectResult, label string, composites []simulate.CompositeResult, want *XRImpactExpectation) {
	t.Helper()

	i := slices.IndexFunc(composites, func(c simulate.CompositeResult) bool {
		return refMatches(c.Ref, want.kind, want.name, want.namespace)
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

	errs := slices.DeleteFunc(slices.Clone(res.Errors), func(e simulate.Error) bool { return e.Ref == nil || *e.Ref != got.Ref })

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
		p := findMatchingPlan(downstream, wantRes.changeType, wantRes.kind, wantRes.name, wantRes.namespace)
		if p == nil {
			t.Errorf("%s: expected downstream %s resource %s/%s not found. Actual: %v",
				label, wantRes.changeType, wantRes.kind, wantRes.name, planIDs(downstream))

			continue
		}

		id := fmt.Sprintf("%s: downstream %s %s/%s", label, wantRes.changeType, wantRes.kind, p.Name)
		assertDryRun(t, id, p.DryRun, wantRes.dryRun)
		assertFields(t, id, p, wantRes.changeType, wantRes.fieldValues, wantRes.fieldChanges)
	}
}

// statusMatches compares a composite's status with crossplane-diff's, whose "filtered" simulate.ProjectResult
// splits into deferred and skipped.
func statusMatches(got simulate.CompositeStatus, want string) bool {
	if want == statusFiltered {
		// Assert filterReason when it matters which of deferred or skipped applies.
		return got == simulate.CompositeDeferred || got == simulate.CompositeSkipped
	}

	return string(got) == want
}

// assertFields checks a plan's field expectations. values read the planned side, or the actual side
// of a delete, which carries what is removed. changes compare both sides.
func assertFields(t *testing.T, id string, p *plan.ResourcePlan, changeType string, values map[string]any, changes map[string][2]any) {
	t.Helper()

	planned := changeType != dt.DiffTypeWordRemoved

	for path, want := range values {
		// A field that is absent is not an explicit null, so WithField(path, nil) needs it present.
		if got, found := valueAt(t, p.Changes, planned, path); !found || !valuesEqual(got, want) {
			t.Errorf("%s: field %s: expected %v, got %v (present: %t)", id, path, want, got, found)
		}
	}

	for path, want := range changes {
		if want[0] == nil && want[1] == nil {
			t.Fatalf("%s: field %s: WithFieldChange(path, nil, nil) asserts nothing", id, path)
		}

		// FieldChanges are leaf changes only, so a field rendered identically on both sides is read from
		// the plan's objects instead, and must have no change at or under it.
		if want[0] != nil && valuesEqual(want[0], want[1]) {
			if c := changesUnder(t, p.Changes, path); len(c) > 0 {
				t.Errorf("%s: field %s: expected unchanged (%v), got changes %+v", id, path, want[0], c)
			}

			for i, u := range []*unstructured.Unstructured{p.Before, p.After} {
				if got, ok := objectValueAt(t, u, path); !ok || !valuesEqual(got, want[0]) {
					t.Errorf("%s: field %s in %s: expected %v, got %v (present: %t)", id, path, []string{"before", "after"}[i], want[0], got, ok)
				}
			}

			continue
		}

		if got, _ := valueAt(t, p.Changes, false, path); !valuesEqual(got, want[0]) {
			t.Errorf("%s: field %s old value: expected %v, got %v", id, path, want[0], got)
		}

		if got, _ := valueAt(t, p.Changes, true, path); !valuesEqual(got, want[1]) {
			t.Errorf("%s: field %s new value: expected %v, got %v", id, path, want[1], got)
		}
	}
}

// assertDryRun checks a plan's dry-run verification: either that its desired state was reported as NOT
// verified against the apiserver (for a reason, optionally with the cluster's own explanation), or that
// no such report is present at all.
func assertDryRun(t *testing.T, id string, got *plan.Verification, want dryRunExpectation) {
	t.Helper()

	if want.mustBePerformed && want.skipReason != "" {
		t.Errorf("%s: WithDryRunPerformed and WithDryRunSkipped(%q) contradict each other; set only one", id, want.skipReason)
		return
	}

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

	if want.detailSubstring != "" && !strings.Contains(got.Detail, want.detailSubstring) {
		t.Errorf("%s: dryRun.detail %q does not contain %q — the cluster's own explanation must be propagated, not swallowed",
			id, got.Detail, want.detailSubstring)
	}
}

// assertWarnings checks each expectation against got, then — if the test pinned any expectations, or
// asserted there are none — verifies no extras were emitted. Without the
// count check, warning duplication is untestable: a regression that emits the same advisory once per
// XR still satisfies every find-first-match expectation. No expectations and no noWarnings = no opinion.
func assertWarnings(t *testing.T, got []simulate.Warning, want []warningMatch, noWarnings bool) {
	t.Helper()

	if noWarnings && len(want) > 0 {
		t.Errorf("WithNoWarnings and WithWarning(%q) contradict each other; set only one", want[0].messageContains)
		return
	}

	// Each expectation consumes a distinct warning, so two identical expectations need two warnings.
	used := make([]bool, len(got))
	for _, w := range want {
		assertWarningExpectation(t, got, used, w)
	}

	if (len(want) > 0 || noWarnings) && len(got) != len(want) {
		t.Errorf("Expected %d warnings in structured output, got %d: %v", len(want), len(got), warningMessages(got))
	}
}

// assertWarningExpectation finds an unused warning whose message contains want's substring and, when the
// expectation pins one, whose context map is exactly want's, and marks it used. Both conditions select the
// warning, since several warnings legitimately share a message and differ only in context.
func assertWarningExpectation(t *testing.T, got []simulate.Warning, used []bool, want warningMatch) {
	t.Helper()

	var contextsSeen []map[string]string

	for i, w := range got {
		if used[i] || !strings.Contains(w.Message, want.messageContains) {
			continue
		}

		if want.context == nil || reflect.DeepEqual(want.context, w.Context) {
			used[i] = true
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

// findMatchingPlan returns the first plan with changeType's action, kind, name and (when given)
// namespace.
func findMatchingPlan(plans []*plan.ResourcePlan, changeType, kind, name, namespace string) *plan.ResourcePlan {
	for _, p := range plans {
		if p.Action != changeAction(changeType) {
			continue
		}

		ref := plan.ResourceRef{Kind: p.Kind, Name: p.Name, Namespace: p.Namespace}
		if refMatches(ref, kind, name, namespace) {
			return p
		}
	}

	return nil
}

// refMatches reports whether ref has kind, name and, unless namespace is empty, namespace.
func refMatches(ref plan.ResourceRef, kind, name, namespace string) bool {
	return ref.Kind == kind && ref.Name == name && (namespace == "" || ref.Namespace == namespace)
}

// findMatchingError returns the error whose resource is resourceID ("Kind/name").
func findMatchingError(errs []simulate.Error, resourceID string) *simulate.Error {
	for i := range errs {
		if errorID(errs[i]) == resourceID {
			return &errs[i]
		}
	}

	return nil
}

// errorID is crossplane-diff's resourceID for an error: its resource's Kind/name, empty for a run
// error no resource owns.
func errorID(e simulate.Error) string {
	if e.Ref == nil {
		return ""
	}

	return e.Ref.Kind + "/" + e.Ref.Name
}

func errorIDs(errs []simulate.Error) []string {
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

func compositeIDs(cs []simulate.CompositeResult) []string {
	ids := make([]string, 0, len(cs))
	for _, c := range cs {
		ids = append(ids, fmt.Sprintf("%s/%s (ns=%s, status=%s)", c.Ref.Kind, c.Ref.Name, c.Ref.Namespace, c.Status))
	}

	return ids
}

func warningMessages(ws []simulate.Warning) []string {
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

func mustParsePath(t *testing.T, path string) fieldpath.Segments {
	t.Helper()

	toks, err := fieldpath.Parse(path)
	if err != nil {
		t.Fatalf("field path: %v", err)
	}

	return toks
}

// under returns the steps of toks below prefix, and whether toks is at or under prefix at all.
func under(toks, prefix fieldpath.Segments) (fieldpath.Segments, bool) {
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

	return walk(u.Object, mustParsePath(t, path))
}

// walk returns the value at toks below node, and whether it is there at all.
func walk(node any, toks fieldpath.Segments) (any, bool) {
	for _, tok := range toks {
		if tok.Type == fieldpath.SegmentIndex {
			l, ok := node.([]any)
			if !ok || tok.Index >= uint(len(l)) {
				return nil, false
			}

			node = l[tok.Index]

			continue
		}

		m, ok := node.(map[string]any)
		if !ok {
			return nil, false
		}

		if node, ok = m[tok.Field]; !ok {
			return nil, false
		}
	}

	return node, true
}

// valuesEqual compares a and b by value, so int 3 equals float64 3. Both are normalised through JSON,
// which is how the plan's values arrive.
// NOTE: integers beyond 2^53 lose precision in the round trip; compare them differently if one is ever asserted.
func valuesEqual(a, b any) bool {
	// A marker state matches no concrete value, nil included, at any depth.
	if hasMarker(a) || hasMarker(b) {
		return false
	}

	na, errA := normalise(a)
	nb, errB := normalise(b)

	if errA != nil || errB != nil {
		return reflect.DeepEqual(a, b)
	}

	return reflect.DeepEqual(na, nb)
}

// hasMarker reports whether v is a marker or a map or slice holding one. It runs before normalisation,
// which would turn a marker into the plain string an expectation could spell out.
func hasMarker(v any) bool {
	switch v := v.(type) {
	case marker:
		return true
	case map[string]any:
		for _, e := range v {
			if hasMarker(e) {
				return true
			}
		}
	case []any:
		if slices.ContainsFunc(v, hasMarker) {
			return true
		}
	}

	return false
}

func normalise(v any) (any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}

	var out any

	return out, json.Unmarshal(raw, &out)
}

// marker stands in for a plan value that has no concrete value: unknown, sensitive or unresolved.
// valuesEqual never matches it, and it prints readably in failure messages.
type marker string

// leaf is the value a plan.Value contributes to a comparison.
func leaf(v *plan.Value) any {
	switch {
	case v.Unknown:
		return marker("(known after apply)")
	case v.Sensitive:
		return marker("(sensitive)")
	case v.Unresolved:
		return marker("(unresolved)")
	}

	return v.Value
}

// valueAt returns the value at path on one side of a plan's changes: the planned side, or else the
// actual. A leaf is its change's value; a non-leaf is re-assembled from the changed leaves under it.
// Found is false, and the value nil, when no change sets anything at or under path on that side, so an
// absent field differs from an explicit null. A change above path (one whose value is a whole object)
// is searched for path within its value.
func valueAt(t *testing.T, changes []plan.FieldChange, planned bool, path string) (any, bool) {
	t.Helper()

	prefix := mustParsePath(t, path)

	var (
		root  any
		found bool
	)

	for _, c := range changes {
		v := c.Actual
		if planned {
			v = c.Planned
		}

		if v == nil {
			continue
		}

		cp := mustParsePath(t, c.Path)

		// An enclosing change holds the value inside its own.
		if outer, ok := under(prefix, cp); ok && len(outer) > 0 {
			if m, ok := leaf(v).(marker); ok {
				return m, true
			}

			if inner, ok := walk(v.Value, outer); ok {
				return inner, true
			}

			continue
		}

		rel, ok := under(cp, prefix)
		if !ok {
			continue
		}

		if len(rel) == 0 {
			return leaf(v), true
		}

		root = insert(root, rel, leaf(v))
		found = true
	}

	return assemble(root), found
}

// insert sets v at toks below node, building maps as it goes. Lists are built as index maps, which
// assemble turns into slices.
func insert(node any, toks fieldpath.Segments, v any) any {
	if len(toks) == 0 {
		return v
	}

	if toks[0].Type == fieldpath.SegmentIndex {
		m, _ := node.(map[uint]any)
		if m == nil {
			m = map[uint]any{}
		}

		m[toks[0].Index] = insert(m[toks[0].Index], toks[1:], v)

		return m
	}

	m, _ := node.(map[string]any)
	if m == nil {
		m = map[string]any{}
	}

	m[toks[0].Field] = insert(m[toks[0].Field], toks[1:], v)

	return m
}

// assemble turns insert's index maps into slices.
func assemble(node any) any {
	switch n := node.(type) {
	case map[uint]any:
		size := uint(0)
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
