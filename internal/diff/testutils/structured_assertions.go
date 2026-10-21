// Package testutils provides test utilities for crossplane-diff.
package testutils

import (
	"regexp"

	dt "github.com/crossplane/cli/v2/internal/diff/result"
)

// DiffExpectation is an interface that allows AssertStructuredDiff to accept
// either *ExpectedDiff or *ResourceExpectation, eliminating the need for And().
type DiffExpectation interface {
	expectation() *ExpectedDiff
}

// ExpectedDiff is a fluent builder for test expectations on structured diff output.
type ExpectedDiff struct {
	summary   *expectedSummary
	resources []*ResourceExpectation
	errors    []*ErrorExpectation
	warnings  []*WarningExpectation
	// noWarnings asserts warnings[] is empty. Separate from len(warnings)==0, which means "no opinion".
	noWarnings bool
	xrs        []*XRExpectation
}

// warningMatch describes one expected entry in warnings[], shared by the xr and comp builders. Matched
// by message substring rather than exact equality, since warning prose is not a stable contract;
// context keys are asserted exactly, because those ARE what a machine consumer reads.
type warningMatch struct {
	messageContains string
	context         map[string]string
}

// WarningExpectation describes one expected entry in an xr diff's warnings[].
type WarningExpectation struct {
	warningMatch

	parent *ExpectedDiff
}

func (w *WarningExpectation) expectation() *ExpectedDiff { return w.parent }

func (e *ExpectedDiff) expectation() *ExpectedDiff { return e }

type expectedSummary struct {
	added    int
	modified int
	removed  int
}

// ResourceExpectation defines expectations for a single resource change.
type ResourceExpectation struct {
	parent             *ExpectedDiff
	xrParent           *XRExpectation // set when this change is scoped to an xrs[] entry
	changeType         string         // "added", "modified", "removed"
	kind               string
	name               string
	namePattern        *regexp.Regexp
	namespace          string
	fieldValues        map[string]any            // For added/removed: field path -> expected value
	fieldChanges       map[string][2]any         // For modified: field path -> [old, new]
	fieldValuePatterns map[string]*regexp.Regexp // For pattern matching field values
	specMatch          map[string]any            // For strict spec matching
	anyNameAllowed     bool                      // If true, any name is accepted

	dryRun dryRunExpectation
}

// dryRunExpectation holds the dryRun assertions. It is shared by the flat (xr)
// and downstream (comp) change builders because dryRun lives on ChangeDetail,
// which is the same per-resource wire shape for both commands — so asserting it
// two different ways would let the two drift apart.
//
// skipReason, when non-empty, asserts the change carries a dryRun object with
// that skipReason. mustBePerformed asserts the opposite — that no dryRun object
// is present, i.e. the desired state went through the apiserver. The two are
// mutually exclusive; both unset asserts nothing, which keeps every pre-existing
// expectation valid.
type dryRunExpectation struct {
	skipReason      string
	detailSubstring string
	storageVersion  string
	mustBePerformed bool
}

// expectation returns the root builder. Only meaningful for a flat top-level
// change (r.parent set); an XR-scoped change (r.xrParent set) is built as a
// standalone sub-expression passed to WithXRs and is never walked back to a
// root, so r.parent is nil there and that is fine.
func (r *ResourceExpectation) expectation() *ExpectedDiff {
	return r.parent
}

// ExpectDiff creates a new ExpectedDiff builder.
func ExpectDiff() *ExpectedDiff {
	return &ExpectedDiff{
		resources: make([]*ResourceExpectation, 0),
	}
}

// WithSummary sets the expected summary counts.
func (e *ExpectedDiff) WithSummary(added, modified, removed int) *ExpectedDiff {
	e.summary = &expectedSummary{
		added:    added,
		modified: modified,
		removed:  removed,
	}

	return e
}

// WithAddedResource adds an expectation for an added resource.
func (e *ExpectedDiff) WithAddedResource(kind, name, namespace string) *ResourceExpectation {
	r := &ResourceExpectation{
		parent:             e,
		changeType:         dt.DiffTypeWordAdded,
		kind:               kind,
		name:               name,
		namespace:          namespace,
		fieldValues:        make(map[string]any),
		fieldChanges:       make(map[string][2]any),
		fieldValuePatterns: make(map[string]*regexp.Regexp),
	}
	e.resources = append(e.resources, r)

	return r
}

// WithModifiedResource adds an expectation for a modified resource.
func (e *ExpectedDiff) WithModifiedResource(kind, name, namespace string) *ResourceExpectation {
	r := &ResourceExpectation{
		parent:             e,
		changeType:         dt.DiffTypeWordModified,
		kind:               kind,
		name:               name,
		namespace:          namespace,
		fieldValues:        make(map[string]any),
		fieldChanges:       make(map[string][2]any),
		fieldValuePatterns: make(map[string]*regexp.Regexp),
	}
	e.resources = append(e.resources, r)

	return r
}

// ErrorExpectation describes one expected entry in the structured
// errors[] payload. Match is by ResourceID; ValidationFailures, when
// non-nil, asserts the per-resource typed validation breakdown.
type ErrorExpectation struct {
	parent             *ExpectedDiff
	resourceID         string
	messageContains    string
	validationFailures []*ValidationFailureExpectation
}

func (e *ErrorExpectation) expectation() *ExpectedDiff { return e.parent }

// ValidationFailureExpectation describes one expected entry in
// errors[].validationFailures[]. Match is by APIVersion/Kind/Name;
// Errors asserts on the per-field validation entries within.
type ValidationFailureExpectation struct {
	parent     *ErrorExpectation
	apiVersion string
	kind       string
	name       string
	namespace  string
	status     string                   // optional; if "", no assertion
	errors     []*FieldErrorExpectation // optional; nil = no assertion on per-field errors
}

func (v *ValidationFailureExpectation) expectation() *ExpectedDiff { return v.parent.parent }

// FieldErrorExpectation describes one expected entry in
// errors[].validationFailures[].errors[]. Match is by Type+Field
// (Field may be empty for non-localized errors); messageContains and
// value are optional refinements.
type FieldErrorExpectation struct {
	parent          *ValidationFailureExpectation
	errType         string
	field           string
	messageContains string
	hasValue        bool
	value           any
}

func (f *FieldErrorExpectation) expectation() *ExpectedDiff { return f.parent.parent.parent }

// WithWarning asserts that structured output carries a warning whose message contains the supplied
// substring. Chain WithWarningContext to pin the machine-readable context pairs.
func (e *ExpectedDiff) WithWarning(messageContains string) *WarningExpectation {
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
func (e *ExpectedDiff) WithNoWarnings() *ExpectedDiff {
	e.noWarnings = true
	return e
}

// WithWarningContext pins the expected context key/value pairs on the warning under construction.
func (w *WarningExpectation) WithWarningContext(context map[string]string) *WarningExpectation {
	w.context = context
	return w
}

// And returns to the parent builder.
func (w *WarningExpectation) And() *ExpectedDiff { return w.parent }

// WithError attaches an expectation for an entry in the structured
// errors[] payload, matched by resourceID. Use the returned
// ErrorExpectation to add validationFailures expectations.
func (e *ExpectedDiff) WithError(resourceID string) *ErrorExpectation {
	er := &ErrorExpectation{
		parent:     e,
		resourceID: resourceID,
	}
	e.errors = append(e.errors, er)

	return er
}

// WithMessageContaining adds a substring assertion on the error's
// Message field. Optional — useful when machine consumers care about
// the human-readable message too.
func (e *ErrorExpectation) WithMessageContaining(s string) *ErrorExpectation {
	e.messageContains = s
	return e
}

// WithValidationFailure adds an expectation for one entry in the
// error's validationFailures[] slice, matched by APIVersion/Kind/Name.
// namespace is optional (empty string skips the namespace assertion);
// pass the actual namespace to lock down namespaced resources.
func (e *ErrorExpectation) WithValidationFailure(apiVersion, kind, name, namespace string) *ValidationFailureExpectation {
	v := &ValidationFailureExpectation{
		parent:     e,
		apiVersion: apiVersion,
		kind:       kind,
		name:       name,
		namespace:  namespace,
	}
	e.validationFailures = append(e.validationFailures, v)

	return v
}

// AndError returns to the parent ExpectedDiff to chain another
// WithError call, mirroring the AndXR / AndComp helpers elsewhere in
// this file.
func (e *ErrorExpectation) AndError() *ExpectedDiff {
	return e.parent
}

// WithStatus pins the validation status on this resource entry
// ("invalid", "missingSchema"). Optional; empty status skips the
// assertion.
func (v *ValidationFailureExpectation) WithStatus(status string) *ValidationFailureExpectation {
	v.status = status
	return v
}

// WithFieldError adds an expectation for one entry in the failure's
// errors[] slice, matched by errType ("schema", "cel",
// "unknownField", "defaulting") and field (empty matches
// non-localized errors like defaulting).
func (v *ValidationFailureExpectation) WithFieldError(errType, field string) *FieldErrorExpectation {
	f := &FieldErrorExpectation{
		parent:  v,
		errType: errType,
		field:   field,
	}
	v.errors = append(v.errors, f)

	return f
}

// AndValidationFailure returns to the parent ErrorExpectation to chain
// another WithValidationFailure.
func (v *ValidationFailureExpectation) AndValidationFailure() *ErrorExpectation {
	return v.parent
}

// WithMessageContaining adds a substring assertion on this field
// error's Message. Optional.
func (f *FieldErrorExpectation) WithMessageContaining(s string) *FieldErrorExpectation {
	f.messageContains = s
	return f
}

// WithValue pins the bad value on this field error. Pass the value as
// it would arrive after JSON decoding (e.g. float64 for numbers,
// string for strings); for "any number is fine" omit this method.
func (f *FieldErrorExpectation) WithValue(v any) *FieldErrorExpectation {
	f.hasValue = true
	f.value = v

	return f
}

// AndFieldError returns to the parent ValidationFailureExpectation to
// chain another WithFieldError.
func (f *FieldErrorExpectation) AndFieldError() *ValidationFailureExpectation {
	return f.parent
}

// WithRemovedResource adds an expectation for a removed resource.
func (e *ExpectedDiff) WithRemovedResource(kind, name, namespace string) *ResourceExpectation {
	r := &ResourceExpectation{
		parent:             e,
		changeType:         dt.DiffTypeWordRemoved,
		kind:               kind,
		name:               name,
		namespace:          namespace,
		fieldValues:        make(map[string]any),
		fieldChanges:       make(map[string][2]any),
		fieldValuePatterns: make(map[string]*regexp.Regexp),
	}
	e.resources = append(e.resources, r)

	return r
}

// WithField asserts a specific field exists with exact value (for added/removed resources).
func (r *ResourceExpectation) WithField(path string, value any) *ResourceExpectation {
	r.fieldValues[path] = value
	return r
}

// WithFieldAbsent asserts the path carries no value at all (for added/removed resources).
//
// Named rather than spelled WithField(path, nil) at call sites, because "this field must not be
// here" is a different claim from "this field equals nothing" and reads as a typo otherwise.
func (r *ResourceExpectation) WithFieldAbsent(path string) *ResourceExpectation {
	r.fieldValues[path] = nil
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

// WithDryRunStorageVersion asserts this change, reported as not verified (see
// WithDryRunSkipped), records version as the version its CRD stores it at:
// one other than the version it was requested at, whose defaults and
// conversion its locally predicted diff does not model.
func (r *ResourceExpectation) WithDryRunStorageVersion(version string) *ResourceExpectation {
	r.dryRun.storageVersion = version
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

// WithFieldAdded asserts a field was added (for modified resources).
// Equivalent to WithFieldChange(path, nil, value).
func (r *ResourceExpectation) WithFieldAdded(path string, value any) *ResourceExpectation {
	return r.WithFieldChange(path, nil, value)
}

// WithFieldRemoved asserts a field was removed (for modified resources).
// Equivalent to WithFieldChange(path, value, nil).
func (r *ResourceExpectation) WithFieldRemoved(path string, value any) *ResourceExpectation {
	return r.WithFieldChange(path, value, nil)
}

// WithFieldValuePattern asserts a field value matches a regex pattern.
// Use this for fields with generated/dynamic values like resource names with random suffixes.
func (r *ResourceExpectation) WithFieldValuePattern(path, pattern string) *ResourceExpectation {
	r.fieldValuePatterns[path] = regexp.MustCompile(pattern)
	return r
}

// WithSpec asserts the entire spec matches (for strict equality).
func (r *ResourceExpectation) WithSpec(spec map[string]any) *ResourceExpectation {
	r.specMatch = spec
	return r
}

// WithNamePattern matches resource name against a regex pattern instead of exact name.
func (r *ResourceExpectation) WithNamePattern(pattern string) *ResourceExpectation {
	r.namePattern = regexp.MustCompile(pattern)
	r.anyNameAllowed = false

	return r
}

// WithAnyName allows any resource name (useful for generated names).
func (r *ResourceExpectation) WithAnyName() *ResourceExpectation {
	r.anyNameAllowed = true
	return r
}

// And returns to the root ExpectedDiff to chain more expectations.
// Not needed at the end of a chain - AssertStructuredDiff accepts a
// ResourceExpectation directly.
func (r *ResourceExpectation) And() *ExpectedDiff {
	return r.expectation()
}

// Change opens a sibling change on the same enclosing XR entry (only valid on a
// change opened via XRExpectation.Change). Lets an XR's changes chain without a
// scope-closing call: xr.Change(...).FieldChange(...).Change(...). Panics if
// called on a flat top-level change (which has no enclosing XR).
func (r *ResourceExpectation) Change(changeType, kind, name, namespace string) *ResourceExpectation {
	if r.xrParent == nil {
		panic("Change chained on a flat (non-XR) change; open the change via XR(...).Change(...)")
	}

	return r.xrParent.Change(changeType, kind, name, namespace)
}

// XRExpectation defines expectations for a single entry in the xrs[] grouped
// view: the input XR's identity, its status, its per-XR summary, and its
// per-XR changes. Build one with XR(...), configure it with the methods below,
// then pass it (with any siblings) to ExpectedDiff.WithXRs.
type XRExpectation struct {
	kind        string
	name        string
	namePattern *regexp.Regexp
	namespace   string
	anyName     bool
	status      string
	summary     *expectedSummary
	changes     []*ResourceExpectation
	errorIDs    []string
}

// XR begins a standalone expectation for one xrs[] entry, keyed by the input
// XR's kind/name/namespace. Pass the result(s) to ExpectedDiff.WithXRs.
func XR(kind, name, namespace string) *XRExpectation {
	return &XRExpectation{kind: kind, name: name, namespace: namespace}
}

// xrSpec is satisfied by the two things a WithXRs argument can end on: an
// XRExpectation itself, or a ResourceExpectation returned by an XR's Change(…)
// sub-chain (which resolves back to its enclosing XR). This lets a per-XR
// sub-expression terminate on either a status/summary call or a field call
// without an explicit scope-closing step.
type xrSpec interface {
	xrExpectation() *XRExpectation
}

func (x *XRExpectation) xrExpectation() *XRExpectation { return x }

func (r *ResourceExpectation) xrExpectation() *XRExpectation { return r.xrParent }

// WithXRs attaches per-input-XR expectations (built via XR(...)) to assert
// against the xrs[] grouped view, in order.
func (e *ExpectedDiff) WithXRs(xrs ...xrSpec) *ExpectedDiff {
	for _, x := range xrs {
		e.xrs = append(e.xrs, x.xrExpectation())
	}

	return e
}

// Build terminates the chain and returns the root expectation. Optional —
// AssertStructuredDiff accepts an *ExpectedDiff directly — but it reads as an
// explicit "done building" marker.
func (e *ExpectedDiff) Build() *ExpectedDiff { return e }

// Status asserts the entry's status (changed/unchanged/error).
func (x *XRExpectation) Status(status string) *XRExpectation {
	x.status = status
	return x
}

// Summary asserts the entry's per-XR summary counts.
func (x *XRExpectation) Summary(added, modified, removed int) *XRExpectation {
	x.summary = &expectedSummary{added: added, modified: modified, removed: removed}
	return x
}

// AnyName matches the XR by kind/namespace only, accepting any name (useful for
// generated XR names).
func (x *XRExpectation) AnyName() *XRExpectation {
	x.anyName = true
	return x
}

// NamePattern matches the XR name against a regex instead of an exact name.
func (x *XRExpectation) NamePattern(pattern string) *XRExpectation {
	x.namePattern = regexp.MustCompile(pattern)
	return x
}

// Err asserts the entry carries an error with the given resourceID.
func (x *XRExpectation) Err(resourceID string) *XRExpectation {
	x.errorIDs = append(x.errorIDs, resourceID)
	return x
}

// Change opens a change expectation scoped to this XR entry's changes[].
// changeType is "added", "modified", or "removed". Returns a
// ResourceExpectation whose field methods (WithField/WithFieldChange/…) and
// chained Change(...) let a change and its siblings be expressed inline.
func (x *XRExpectation) Change(changeType, kind, name, namespace string) *ResourceExpectation {
	r := &ResourceExpectation{
		xrParent:           x,
		changeType:         changeType,
		kind:               kind,
		name:               name,
		namespace:          namespace,
		fieldValues:        make(map[string]any),
		fieldChanges:       make(map[string][2]any),
		fieldValuePatterns: make(map[string]*regexp.Regexp),
	}
	x.changes = append(x.changes, r)

	return r
}

// CompDiffExpectation is an interface that allows AssertStructuredCompDiff to accept
// any level of the builder hierarchy, eliminating the need for And()/AndXR()/AndComp().
type CompDiffExpectation interface {
	compExpectation() *ExpectedCompDiff
}

// ExpectedCompDiff is a fluent builder for test expectations on composition diff output.
type ExpectedCompDiff struct {
	compositions []*CompositionExpectation
	warnings     []*CompWarningExpectation
	// noWarnings asserts warnings[] is empty. Separate from len(warnings)==0, which means "no opinion".
	noWarnings bool
	// errorIDs are resourceIDs the top-level errors[] must contain.
	errorIDs []string
}

func (e *ExpectedCompDiff) compExpectation() *ExpectedCompDiff { return e }

// CompWarningExpectation describes one expected entry in a comp diff's warnings[].
type CompWarningExpectation struct {
	warningMatch

	parent *ExpectedCompDiff
}

func (w *CompWarningExpectation) compExpectation() *ExpectedCompDiff { return w.parent }

// WithWarning asserts that structured output carries a warning whose message contains the supplied
// substring. Chain WithWarningContext to pin the machine-readable context pairs.
func (e *ExpectedCompDiff) WithWarning(messageContains string) *CompWarningExpectation {
	w := &CompWarningExpectation{
		parent:       e,
		warningMatch: warningMatch{messageContains: messageContains},
	}
	e.warnings = append(e.warnings, w)

	return w
}

// WithError asserts the top-level errors[] contains an error with the given resourceID.
func (e *ExpectedCompDiff) WithError(resourceID string) *ExpectedCompDiff {
	e.errorIDs = append(e.errorIDs, resourceID)
	return e
}

// WithNoWarnings asserts that structured output carries no advisories at all. Distinct from simply
// declaring no WithWarning expectations, which asserts nothing: this is how a test pins that a
// condition the tool warns about elsewhere is correctly silent here.
func (e *ExpectedCompDiff) WithNoWarnings() *ExpectedCompDiff {
	e.noWarnings = true
	return e
}

// WithWarningContext pins the expected context key/value pairs on the warning under construction.
func (w *CompWarningExpectation) WithWarningContext(context map[string]string) *CompWarningExpectation {
	w.context = context
	return w
}

// And returns to the parent builder.
func (w *CompWarningExpectation) And() *ExpectedCompDiff { return w.parent }

// CompositionExpectation defines expectations for a single composition in the diff.
type CompositionExpectation struct {
	parent                     *ExpectedCompDiff
	name                       string
	affectedTotal              *int
	affectedWithChanges        *int
	affectedUnchanged          *int
	affectedWithErrors         *int
	affectedFilteredByDeletion *int
	xrImpacts                  []*XRImpactExpectation
	// Composition changes expectations
	compositionChangeType   string            // "modified" - composition changes are always modifications
	compositionFieldChanges map[string][2]any // For modified: field path -> [old, new]
	// impactAnalysisSkipped, when set, asserts the composites were deliberately not evaluated.
	impactAnalysisSkipped *bool
	// revisionImpact, when set, asserts what applying the composition does to CompositionRevisions.
	revisionImpact *expectedRevisionImpact
	// predictedRevisionName, when set, asserts revisionImpact.predictedRevisionName. Set independently
	// of revisionImpact so a case can assert the name alone, and because the name is usually matched by
	// pattern (its suffix is a content hash) while the rest are exact.
	predictedRevisionName *expectedPredictedRevisionName
	// errorContains, when set, is a substring the composition's error must contain: v1's error string,
	// or a message in v2's errors[].
	errorContains string
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

func (c *CompositionExpectation) compExpectation() *ExpectedCompDiff { return c.parent }

// XRImpactExpectation defines expectations for a single XR impact.
type XRImpactExpectation struct {
	parent              *CompositionExpectation
	kind                string
	name                string
	namespace           string
	anyNameAllowed      bool
	status              string   // "changed", "unchanged", "error", "filtered"
	filterReason        string   // "manual_policy", "revision_selector_mismatch", "deleting"; only checked when set
	errorContains       string   // substring of an "error" impact's Error; only checked when set
	errorIDs            []string // resourceIDs the entry's v2 errors[] must contain
	downstreamSummary   *expectedSummary
	downstreamResources []*DownstreamResourceExpectation
}

func (x *XRImpactExpectation) compExpectation() *ExpectedCompDiff { return x.parent.parent }

// ExpectCompDiff creates a new ExpectedCompDiff builder.
func ExpectCompDiff() *ExpectedCompDiff {
	return &ExpectedCompDiff{
		compositions: make([]*CompositionExpectation, 0),
	}
}

// WithComposition adds an expectation for a composition.
func (e *ExpectedCompDiff) WithComposition(name string) *CompositionExpectation {
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
	if c.compositionFieldChanges == nil {
		c.compositionFieldChanges = make(map[string][2]any)
	}

	return c
}

// WithCompositionFieldChange asserts a specific field in the composition changed from old to new value.
func (c *CompositionExpectation) WithCompositionFieldChange(path string, oldValue, newValue any) *CompositionExpectation {
	if c.compositionFieldChanges == nil {
		c.compositionFieldChanges = make(map[string][2]any)
	}

	c.compositionFieldChanges[path] = [2]any{oldValue, newValue}

	return c
}

// WithCompositionErrorContaining asserts the composition failed with an error containing s: v1's
// error string, or a message in v2's errors[].
func (c *CompositionExpectation) WithCompositionErrorContaining(s string) *CompositionExpectation {
	c.errorContains = s
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

// WithAnyName allows any XR name (useful for generated names).
func (x *XRImpactExpectation) WithAnyName() *XRImpactExpectation {
	x.anyNameAllowed = true
	return x
}

// WithFilterReason pins the expected FilterReason on a filtered XR impact (e.g. "manual_policy",
// "revision_selector_mismatch", or "deleting"). Only asserted when set.
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
func (x *XRImpactExpectation) WithDownstreamResource(changeType, kind, name, namespace string) *DownstreamResourceExpectation {
	r := &DownstreamResourceExpectation{
		parent:             x,
		changeType:         changeType,
		kind:               kind,
		name:               name,
		namespace:          namespace,
		fieldChanges:       make(map[string][2]any),
		fieldValues:        make(map[string]any),
		fieldValuePatterns: make(map[string]*regexp.Regexp),
	}
	x.downstreamResources = append(x.downstreamResources, r)

	return r
}

// DownstreamResourceExpectation defines expectations for a downstream resource change.
type DownstreamResourceExpectation struct {
	parent             *XRImpactExpectation
	changeType         string // "added", "modified", "removed"
	kind               string
	name               string
	namePattern        *regexp.Regexp
	namespace          string
	anyNameAllowed     bool
	fieldChanges       map[string][2]any         // For modified: field path -> [old, new]
	fieldValues        map[string]any            // For added/removed: field path -> expected value
	fieldValuePatterns map[string]*regexp.Regexp // For pattern matching field values
	dryRun             dryRunExpectation
}

func (d *DownstreamResourceExpectation) compExpectation() *ExpectedCompDiff {
	return d.parent.parent.parent
}

// WithFieldChange asserts a field changed from old to new value (for modified resources).
func (d *DownstreamResourceExpectation) WithFieldChange(path string, oldValue, newValue any) *DownstreamResourceExpectation {
	d.fieldChanges[path] = [2]any{oldValue, newValue}
	return d
}

// WithFieldAdded asserts a field was added (for modified resources).
// Equivalent to WithFieldChange(path, nil, value).
func (d *DownstreamResourceExpectation) WithFieldAdded(path string, value any) *DownstreamResourceExpectation {
	return d.WithFieldChange(path, nil, value)
}

// WithFieldRemoved asserts a field was removed (for modified resources).
// Equivalent to WithFieldChange(path, value, nil).
func (d *DownstreamResourceExpectation) WithFieldRemoved(path string, value any) *DownstreamResourceExpectation {
	return d.WithFieldChange(path, value, nil)
}

// WithField asserts a specific field exists with exact value (for added/removed resources).
func (d *DownstreamResourceExpectation) WithField(path string, value any) *DownstreamResourceExpectation {
	d.fieldValues[path] = value
	return d
}

// WithFieldValuePattern asserts a field value matches a regex pattern.
// Use this for fields with generated/dynamic values like resource names with random suffixes.
func (d *DownstreamResourceExpectation) WithFieldValuePattern(path, pattern string) *DownstreamResourceExpectation {
	d.fieldValuePatterns[path] = regexp.MustCompile(pattern)
	return d
}

// WithDryRunSkipped asserts this downstream change reports that its desired state
// was NOT verified against the apiserver, for the given reason (one of the
// dt.DryRunSkipReason values: "disabled", "forbidden", "webhookUnavailable").
//
// Pass a detailSubstring to additionally require that the cluster's own
// explanation was propagated rather than swallowed; pass "" to skip that check.
func (d *DownstreamResourceExpectation) WithDryRunSkipped(reason, detailSubstring string) *DownstreamResourceExpectation {
	d.dryRun.skipReason = reason
	d.dryRun.detailSubstring = detailSubstring

	return d
}

// WithDryRunPerformed asserts this downstream change carries NO dryRun object,
// i.e. its desired state did go through the apiserver. See the ResourceExpectation
// counterpart for why the happy path is worth asserting explicitly.
func (d *DownstreamResourceExpectation) WithDryRunPerformed() *DownstreamResourceExpectation {
	d.dryRun.mustBePerformed = true
	return d
}

// WithAnyName allows any resource name (useful for generated names).
func (d *DownstreamResourceExpectation) WithAnyName() *DownstreamResourceExpectation {
	d.anyNameAllowed = true
	return d
}

// WithNamePattern matches resource name against a regex pattern.
func (d *DownstreamResourceExpectation) WithNamePattern(pattern string) *DownstreamResourceExpectation {
	d.namePattern = regexp.MustCompile(pattern)
	return d
}

// AndXR returns to parent to chain more downstream resource expectations.
// Not needed at the end of a chain - AssertStructuredCompDiff accepts DownstreamResourceExpectation directly.
func (d *DownstreamResourceExpectation) AndXR() *XRImpactExpectation {
	return d.parent
}

// AndComp returns to parent to chain more XR impact expectations.
// Not needed at the end of a chain - AssertStructuredCompDiff accepts XRImpactExpectation directly.
func (x *XRImpactExpectation) AndComp() *CompositionExpectation {
	return x.parent
}

// And returns to parent to chain more composition expectations.
// Not needed at the end of a chain - AssertStructuredCompDiff accepts CompositionExpectation directly.
func (c *CompositionExpectation) And() *ExpectedCompDiff {
	return c.parent
}

// AndComposition starts an expectation for another composition, from anywhere in the current
// composition's chain. It exists because the only climb a caller genuinely needs is the one back to
// the root to add a sibling — every other And()/AndXR()/AndComp() is avoidable, since the assert
// functions accept any builder level. Spelling the sibling case as one intention-named call keeps a
// multi-composition expectation readable instead of ending each composition with a run of climbs
// whose purpose is not obvious from reading them.
func (d *DownstreamResourceExpectation) AndComposition(name string) *CompositionExpectation {
	return d.compExpectation().WithComposition(name)
}

// AndComposition starts an expectation for another composition. See
// DownstreamResourceExpectation.AndComposition.
func (x *XRImpactExpectation) AndComposition(name string) *CompositionExpectation {
	return x.compExpectation().WithComposition(name)
}

// AndComposition starts an expectation for another composition. See
// DownstreamResourceExpectation.AndComposition.
func (c *CompositionExpectation) AndComposition(name string) *CompositionExpectation {
	return c.compExpectation().WithComposition(name)
}
