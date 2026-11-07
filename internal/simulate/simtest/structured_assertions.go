// Package simtest asserts on composition simulate's structured output, simulate.ProjectResult, through
// crossplane-diff's composition expectation builders.
package simtest

import (
	dt "github.com/crossplane/cli/v2/internal/diff/result"
)

// Expectation is implemented by every level of the builder hierarchy, so AssertStructured accepts
// any node and callers never need And()/AndXR()/AndComp() to climb back to the root.
type Expectation interface {
	expectation() *Expected
}

// Expected is a fluent builder for test expectations on a simulate.ProjectResult: its compositions,
// errors and warnings. Each section is asserted only if it was expected.
type Expected struct {
	// compositions are asserted by composition ref, with no count pinned.
	compositions []*CompositionExpectation
	// errorIDs are resourceIDs the top-level errors[] must contain (no count pinned); see WithErrorID.
	errorIDs []string

	warnings []*WarningExpectation
	// noWarnings asserts warnings[] is empty. Separate from len(warnings)==0, which means "no opinion".
	noWarnings bool
}

// warningMatch describes one expected entry in warnings[]. Matched
// by message substring rather than exact equality, since warning prose is not a stable contract;
// context keys are asserted exactly, because those ARE what a machine consumer reads.
type warningMatch struct {
	messageContains string
	context         map[string]string
}

// WarningExpectation describes one expected entry in warnings[].
type WarningExpectation struct {
	warningMatch

	parent *Expected
}

func (w *WarningExpectation) expectation() *Expected { return w.parent }

func (e *Expected) expectation() *Expected { return e }

// ResourceExpectation defines expectations for a single downstream resource change under an XR impact.
type ResourceExpectation struct {
	parent       *XRImpactExpectation
	changeType   string // "added", "modified", "removed"
	kind         string
	name         string
	namespace    string
	fieldValues  map[string]any    // For added/removed: field path -> expected value
	fieldChanges map[string][2]any // For modified: field path -> [old, new]

	dryRun dryRunExpectation
}

// dryRunExpectation holds a downstream change's dryRun assertions.
//
// skipReason, when non-empty, asserts the change carries a dryRun object with
// that skipReason. mustBePerformed asserts the opposite — that no dryRun object
// is present, i.e. the desired state went through the apiserver. The two are
// mutually exclusive; both unset asserts nothing, which keeps every pre-existing
// expectation valid.
type dryRunExpectation struct {
	skipReason      string
	detailSubstring string
	mustBePerformed bool
}

func (r *ResourceExpectation) expectation() *Expected { return r.parent.expectation() }

// Expect creates a new Expected builder.
func Expect() *Expected {
	return &Expected{compositions: make([]*CompositionExpectation, 0)}
}

// WithWarning asserts that structured output carries a warning whose message contains the supplied
// substring. Chain WithWarningContext to pin the machine-readable context pairs.
func (e *Expected) WithWarning(messageContains string) *WarningExpectation {
	w := &WarningExpectation{
		parent:       e,
		warningMatch: warningMatch{messageContains: messageContains},
	}
	e.warnings = append(e.warnings, w)

	return w
}

// WithNoWarnings asserts that structured output carries no advisories at all. Distinct from simply
// declaring no WithWarning expectations, which asserts nothing: this is how a test pins that a
// condition the tool used to warn about is correctly silent.
func (e *Expected) WithNoWarnings() *Expected {
	e.noWarnings = true
	return e
}

// WithWarningContext pins the expected context key/value pairs on the warning under construction.
func (w *WarningExpectation) WithWarningContext(context map[string]string) *WarningExpectation {
	w.context = context
	return w
}

// And returns to the parent builder.
func (w *WarningExpectation) And() *Expected { return w.parent }

// WithField asserts a specific field exists with exact value (for added/removed resources).
func (r *ResourceExpectation) WithField(path string, value any) *ResourceExpectation {
	r.fieldValues[path] = value
	return r
}

// WithFieldChange asserts a field changed from old to new value (for modified resources).
func (r *ResourceExpectation) WithFieldChange(path string, oldValue, newValue any) *ResourceExpectation {
	r.fieldChanges[path] = [2]any{oldValue, newValue}
	return r
}

// WithDryRunSkipped asserts this change reports that its desired state was NOT
// verified against the apiserver, for the given reason (one of the
// dt.DryRunSkipReason values: "disabled", "forbidden", "webhookUnavailable").
//
// Pass a detailSubstring to additionally require that the cluster's own
// explanation was propagated rather than swallowed; pass "" to skip that check.
func (r *ResourceExpectation) WithDryRunSkipped(reason, detailSubstring string) *ResourceExpectation {
	r.dryRun.skipReason = reason
	r.dryRun.detailSubstring = detailSubstring

	return r
}

// WithDryRunPerformed asserts this change carries NO dryRun object, i.e. its
// desired state did go through the apiserver. Worth asserting explicitly on the
// happy path: without it, a regression that silently stopped dry-running
// additions would only be caught by whichever field assertions happened to
// depend on server-side defaulting.
func (r *ResourceExpectation) WithDryRunPerformed() *ResourceExpectation {
	r.dryRun.mustBePerformed = true
	return r
}

// WithErrorID asserts the top-level errors[] contains an error with the given resourceID. It pins
// neither the error's details nor the error count.
func (e *Expected) WithErrorID(resourceID string) *Expected {
	e.errorIDs = append(e.errorIDs, resourceID)
	return e
}

// CompositionExpectation defines expectations for a single composition in the diff.
type CompositionExpectation struct {
	parent                     *Expected
	name                       string
	affectedTotal              *int
	affectedWithChanges        *int
	affectedUnchanged          *int
	affectedWithErrors         *int
	affectedFilteredByDeletion *int
	xrImpacts                  []*XRImpactExpectation
	// compositionChangeType, when set, is the composition's own change: "modified", the only one a
	// composition makes.
	compositionChangeType string
	// impactAnalysisSkipped, when set, asserts the composites were deliberately not evaluated.
	impactAnalysisSkipped *bool
	// revisionImpact, when set, asserts what applying the composition does to CompositionRevisions.
	revisionImpact *expectedRevisionImpact
	// predictedRevisionName, when set, asserts revisionImpact.predictedRevisionName. Set independently
	// of revisionImpact so a case can assert the name alone, and because the name is usually matched by
	// pattern (its suffix is a content hash) while the rest are exact.
	predictedRevisionName *expectedPredictedRevisionName
}

// expectedRevisionImpact is the expected revisionImpact object for a composition.
type expectedRevisionImpact struct {
	changeScope         string
	createsRevision     bool
	repointedComposites int
}

// expectedPredictedRevisionName is the expected revisionImpact.predictedRevisionName. A pattern rather
// than a literal because the name's suffix is the first 7 hex digits of the composition's hash, so
// pinning it exactly would make every fixture edit a test failure. The derivation itself is pinned
// exactly by the revisionIdentity unit test, which is where that belongs.
//
// An empty pattern asserts the field is absent, which is a meaningful outcome in its own right: the
// name was not predictable.
type expectedPredictedRevisionName struct {
	pattern string
}

func (c *CompositionExpectation) expectation() *Expected { return c.parent }

// XRImpactExpectation defines expectations for a single XR impact.
type XRImpactExpectation struct {
	parent              *CompositionExpectation
	kind                string
	name                string
	namespace           string
	status              string   // "changed", "unchanged", "error", "filtered"
	filterReason        string   // "manualPolicy", "revisionSelectorMismatch", "deleting"; only checked when set
	errorContains       string   // substring of an "error" impact's Error; only checked when set
	errorIDs            []string // resourceIDs the simulate.ProjectResult errors[] attributed to the XR must contain
	downstreamSummary   *expectedSummary
	downstreamResources []*ResourceExpectation
}

func (x *XRImpactExpectation) expectation() *Expected { return x.parent.parent }

// expectedSummary is the expected count of each change type.
type expectedSummary struct {
	added    int
	modified int
	removed  int
}

// WithComposition adds an expectation for a composition.
func (e *Expected) WithComposition(name string) *CompositionExpectation {
	c := &CompositionExpectation{
		parent:    e,
		name:      name,
		xrImpacts: make([]*XRImpactExpectation, 0),
	}
	e.compositions = append(e.compositions, c)

	return c
}

// WithAffectedResources sets the expected affected resources counts.
func (c *CompositionExpectation) WithAffectedResources(total, withChanges, unchanged, withErrors int) *CompositionExpectation {
	c.affectedTotal = &total
	c.affectedWithChanges = &withChanges
	c.affectedUnchanged = &unchanged
	c.affectedWithErrors = &withErrors

	return c
}

// WithFilteredByDeletion asserts the number of XRs excluded from impact analysis because they are
// being deleted (AffectedResourcesSummary.FilteredByDeletion). A single-purpose setter rather than a
// positional counterpart to WithAffectedResources, so a caller cannot transpose filter counters.
func (c *CompositionExpectation) WithFilteredByDeletion(count int) *CompositionExpectation {
	c.affectedFilteredByDeletion = &count

	return c
}

// WithImpactAnalysisSkipped asserts that the composites were deliberately not evaluated, which is
// what distinguishes "we did not look" from "we looked and found no affected composites" — both of
// which leave impactAnalysis empty.
func (c *CompositionExpectation) WithImpactAnalysisSkipped() *CompositionExpectation {
	skipped := true
	c.impactAnalysisSkipped = &skipped

	return c
}

// WithRevisionImpact asserts what applying the composition does to CompositionRevisions, independent
// of whether anything renders differently. changeScope is "none", "metadata" or "spec".
func (c *CompositionExpectation) WithRevisionImpact(changeScope string, createsRevision bool, repointedComposites int) *CompositionExpectation {
	c.revisionImpact = &expectedRevisionImpact{
		changeScope:         changeScope,
		createsRevision:     createsRevision,
		repointedComposites: repointedComposites,
	}

	return c
}

// WithPredictedRevisionNamePattern asserts that revisionImpact.predictedRevisionName matches pattern
// (an unanchored regexp unless the pattern anchors itself). Use for the usual case, where the name's
// hash suffix is not worth pinning in an end-to-end test.
func (c *CompositionExpectation) WithPredictedRevisionNamePattern(pattern string) *CompositionExpectation {
	c.predictedRevisionName = &expectedPredictedRevisionName{pattern: pattern}

	return c
}

// WithoutPredictedRevisionName asserts that revisionImpact carries no predictedRevisionName — i.e. the
// revision's identity could not be predicted. Distinct from simply not asserting on the field.
func (c *CompositionExpectation) WithoutPredictedRevisionName() *CompositionExpectation {
	c.predictedRevisionName = &expectedPredictedRevisionName{pattern: ""}

	return c
}

// WithCompositionModified asserts that the composition itself is modified.
func (c *CompositionExpectation) WithCompositionModified() *CompositionExpectation {
	c.compositionChangeType = dt.DiffTypeWordModified
	return c
}

// WithXRImpact adds an expectation for an XR impact.
func (c *CompositionExpectation) WithXRImpact(kind, name, namespace, status string) *XRImpactExpectation {
	x := &XRImpactExpectation{
		parent:    c,
		kind:      kind,
		name:      name,
		namespace: namespace,
		status:    status,
	}
	c.xrImpacts = append(c.xrImpacts, x)

	return x
}

// WithFilterReason pins the expected FilterReason on a filtered XR impact (e.g. "manualPolicy",
// "revisionSelectorMismatch", or "deleting"). Only asserted when set.
func (x *XRImpactExpectation) WithFilterReason(reason string) *XRImpactExpectation {
	x.filterReason = reason
	return x
}

// WithErrorContaining pins a substring of an "error" XR impact's Error, so a test can assert why
// the composite failed rather than only that it did. Only asserted when set.
func (x *XRImpactExpectation) WithErrorContaining(s string) *XRImpactExpectation {
	x.errorContains = s
	return x
}

// WithErrorResourceID asserts the entry's errors[] (v2 only) holds an error with the given resourceID.
func (x *XRImpactExpectation) WithErrorResourceID(resourceID string) *XRImpactExpectation {
	x.errorIDs = append(x.errorIDs, resourceID)
	return x
}

// WithDownstreamSummary sets the expected downstream changes summary.
func (x *XRImpactExpectation) WithDownstreamSummary(added, modified, removed int) *XRImpactExpectation {
	x.downstreamSummary = &expectedSummary{
		added:    added,
		modified: modified,
		removed:  removed,
	}

	return x
}

// WithDownstreamResource adds an expectation for a downstream resource change.
// Use this to assert on specific field-level changes in downstream resources.
func (x *XRImpactExpectation) WithDownstreamResource(changeType, kind, name, namespace string) *ResourceExpectation {
	r := &ResourceExpectation{
		parent:       x,
		changeType:   changeType,
		kind:         kind,
		name:         name,
		namespace:    namespace,
		fieldChanges: make(map[string][2]any),
		fieldValues:  make(map[string]any),
	}
	x.downstreamResources = append(x.downstreamResources, r)

	return r
}

// AndXR returns to the enclosing XR impact to chain more downstream resource expectations.
// Not needed at the end of a chain - AssertStructured accepts a ResourceExpectation directly.
func (r *ResourceExpectation) AndXR() *XRImpactExpectation { return r.parent }

// AndComp returns to parent to chain more XR impact expectations.
// Not needed at the end of a chain - AssertStructured accepts XRImpactExpectation directly.
func (x *XRImpactExpectation) AndComp() *CompositionExpectation {
	return x.parent
}

// AndComposition starts an expectation for another composition, from anywhere in the current
// composition's chain. It exists because the only climb a caller genuinely needs is the one back to
// the root to add a sibling — every other And()/AndXR()/AndComp() is avoidable, since the assert
// functions accept any builder level. Spelling the sibling case as one intention-named call keeps a
// multi-composition expectation readable instead of ending each composition with a run of climbs
// whose purpose is not obvious from reading them.
func (r *ResourceExpectation) AndComposition(name string) *CompositionExpectation {
	return r.expectation().WithComposition(name)
}
