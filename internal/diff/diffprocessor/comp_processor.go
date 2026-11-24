/*
Copyright 2025 The Crossplane Authors.

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

package diffprocessor

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	un "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	apiextensionsv1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1"

	xp "github.com/crossplane/cli/v2/internal/diff/client/crossplane"
	"github.com/crossplane/cli/v2/internal/diff/compare"
	"github.com/crossplane/cli/v2/internal/diff/ref"
	dt "github.com/crossplane/cli/v2/internal/diff/result"
	dtypes "github.com/crossplane/cli/v2/internal/diff/types"
)

// XRDiffResult captures the result of processing a single XR against a composition.
type XRDiffResult struct {
	// Diffs contains the downstream resource diffs for this XR (keyed by resource ID).
	// Empty map means no changes detected.
	Diffs map[string]*dt.ResourceDiff
	// Error contains any error that occurred while processing this XR.
	// nil means processing was successful.
	Error error
	// unchanged holds the unchanged renderings Diffs leaves out, for the overlap check only.
	unchanged map[string]*dt.ResourceDiff
	// health is the live health the render read, computed as the XR is diffed so that the live objects
	// are not retained until impact analysis.
	health dt.CompositeHealth
}

// HasChanges returns true if this XR has downstream resource changes.
func (r *XRDiffResult) HasChanges() bool {
	// Count only non-equal diffs as "having changes".
	// The diffs map may contain DiffTypeEqual entries (e.g., XR stored for removal detection).
	for _, diff := range r.Diffs {
		if diff.DiffType != dt.DiffTypeEqual {
			return true
		}
	}

	return false
}

// HasError returns true if this XR encountered a processing error.
func (r *XRDiffResult) HasError() bool {
	return r.Error != nil
}

// CompDiffProcessor defines the interface for composition diffing.
type CompDiffProcessor interface {
	// DiffComposition processes composition changes and their impact on existing XRs, and returns
	// the report for the caller to render. The report is non-nil whenever compositions were
	// processed, even with an error, so it can still be rendered.
	// When `resources` is non-empty, impact analysis is restricted to the named composites:
	// each ref is resolved against every supplied composition's (XR GVK, claim GVK) pair via a
	// preflight pass. If any ref is relevant to no supplied composition, the call fails before
	// processing any composition, with a nil report (CLI input error). When `resources` is empty,
	// behavior is unchanged.
	DiffComposition(ctx context.Context, compositions []*un.Unstructured, namespace string, resources []k8stypes.NamespacedName) (*dt.CompDiffOutput, error)
	Initialize(ctx context.Context) error
	// Cleaner releases any resources held by the processor (e.g., Docker containers).
	Cleaner
}

// DefaultCompDiffProcessor implements CompDiffProcessor.
type DefaultCompDiffProcessor struct {
	compositionClient xp.CompositionClient
	config            ProcessorConfig
	xrProc            DiffProcessor
}

// NewCompDiffProcessor creates a new DefaultCompDiffProcessor.
func NewCompDiffProcessor(xrProc DiffProcessor, compositionClient xp.CompositionClient, opts ...ProcessorOption) CompDiffProcessor {
	// Create default configuration
	config := ProcessorConfig{
		MaxNestedDepth:      DefaultMaxNestedDepth,
		MaxRenderIterations: DefaultMaxRenderIterations,
		Logger:              logging.NewNopLogger(),
	}

	// Apply all provided options
	for _, option := range opts {
		option(&config)
	}

	// Unset, advisories are only collected into the report; see NewDiffProcessor.
	if config.Advisories == nil {
		config.Advisories = NewAdvisoryCollector(nil)
	}

	// NOTE: We intentionally do NOT default config.RenderFunc here.
	//
	// WithRenderFunc is operation-scoped: the same opts slice flows into
	// NewDiffProcessor for our embedded xrProc, so a caller-supplied renderer is
	// already honored at the layer that actually drives rendering. Comp's copy of
	// config.RenderFunc just rides along unused, which is fine.
	//
	// What we must NOT do is *default* one here. NewEngineRenderFn allocates a
	// Docker bridge network and reserves function-runtime addresses on first use,
	// and DefaultCompDiffProcessor.Cleanup delegates to xrProc.Cleanup — it has no
	// hook for tearing down a comp-side engineRenderFn. Defaulting one would leak
	// the network for the lifetime of the process. The xr processor handles its
	// own default + cleanup in NewDiffProcessor, so comp piggybacking on
	// xrProc.Render is the correct (and only) path.

	// Set default factories if not provided
	config.SetDefaultFactories()

	return &DefaultCompDiffProcessor{
		compositionClient: compositionClient,
		config:            config,
		xrProc:            xrProc,
	}
}

// Initialize loads required resources.
func (p *DefaultCompDiffProcessor) Initialize(ctx context.Context) error {
	p.config.Logger.Debug("Initializing composition diff processor")

	// Initialize the injected XR processor
	if err := p.xrProc.Initialize(ctx); err != nil {
		return errors.Wrap(err, "cannot initialize XR diff processor")
	}

	p.config.Logger.Debug("Composition diff processor initialized")

	return nil
}

// Cleanup releases any resources held by the processor.
// Delegates to the underlying XR processor for cleanup.
func (p *DefaultCompDiffProcessor) Cleanup(ctx context.Context) error {
	return p.xrProc.Cleanup(ctx)
}

// DiffComposition processes composition changes and their impact on existing XRs, and returns the
// report for the caller to render. The report is non-nil whenever compositions were processed, even
// with an error.
func (p *DefaultCompDiffProcessor) DiffComposition(ctx context.Context, compositions []*un.Unstructured, namespace string, resources []k8stypes.NamespacedName) (*dt.CompDiffOutput, error) {
	p.config.Logger.Debug("Processing composition diff",
		"compositionCount", len(compositions),
		"namespace", namespace,
		"resourceCount", len(resources))

	if len(compositions) == 0 {
		return nil, errors.New("no compositions provided")
	}

	// When --resource is set, run a preflight pass that resolves every ref against every supplied
	// composition. If any ref is relevant to no supplied composition, fail loudly BEFORE processing
	// any composition (this is a CLI input error, not a downstream processing failure).
	preflightMatches, err := p.preflightResourceRefs(ctx, compositions, resources)
	if err != nil {
		return nil, err
	}

	output := &dt.CompDiffOutput{
		Compositions: make([]dt.CompositionDiff, 0, len(compositions)),
		Errors:       []dt.OutputError{},
	}

	var (
		compositionErrors int
		// composites are the successfully rendered composites of every composition, for the overlap check.
		composites []renderedComposite
		// run holds every Composition of the run, so a nested XR of another type renders with its own.
		run = p.runCompositions(ctx, compositions)
	)

	// Resolve every Composition's affected composites before processing any, so a supplied composite
	// can replace a live one discovered under another Composition of the run (see mergeSupplied).
	resolvedComps := p.resolveCompositions(ctx, compositions, namespace, resources, preflightMatches)

	live := p.suppliedLiveness(ctx, resolvedComps)

	// suppliedTo maps each named supplied composite's identity to the Composition it is assigned to. One
	// with only a generateName cannot be a live composite, so it is not matched against them.
	suppliedTo := make(map[suppliedIdentity]string)
	for name, us := range p.config.SuppliedComposites {
		for _, u := range us {
			if u.GetName() != "" {
				suppliedTo[identityOfSupplied(u)] = name
			}
		}
	}

	for _, r := range resolvedComps {
		comp, affectedXRs, discoveryErr := r.comp, r.affectedXRs, r.discoveryErr
		compositionID := comp.GetName()
		p.config.Logger.Debug("Processing composition", "name", compositionID)

		// The filtered XRs are recorded in ImpactAnalysis; scoped records which mode produced the set,
		// so the renderer can decide how to present them.
		scoped := len(resources) > 0

		var supplied map[string]suppliedState

		if discoveryErr == nil {
			// A live composite supplied under another Composition renders there, once (see mergeSupplied).
			affectedXRs = slices.DeleteFunc(slices.Clone(affectedXRs), func(u *un.Unstructured) bool {
				name, ok := suppliedTo[identityOfSupplied(u)]
				return ok && name != compositionID
			})
			affectedXRs, supplied = mergeSupplied(affectedXRs, p.config.SuppliedComposites[compositionID], live)
		}

		// Process this single composition and build the result
		var (
			compResult *dt.CompositionDiff
			rendered   []renderedComposite
			err        = discoveryErr
		)

		if err == nil {
			compResult, rendered, err = p.processSingleComposition(ctx, comp, affectedXRs, supplied, scoped, run)
		}

		composites = append(composites, rendered...)

		if err != nil {
			p.config.Logger.Debug("Failed to process composition", "composition", compositionID, "error", err)

			compositionErrors++

			// Include failed composition with error instead of skipping
			output.Compositions = append(output.Compositions, dt.CompositionDiff{
				Name:   compositionID,
				Scoped: scoped,
				Error:  new(NewOutputError(fmt.Sprintf("Composition/%s", compositionID), err)),
				AffectedResources: dt.AffectedResourcesSummary{
					Total:       0,
					WithChanges: 0,
					Unchanged:   0,
					WithErrors:  0,
				},
				ImpactAnalysis: []dt.XRImpact{},
			})
		} else {
			output.Compositions = append(output.Compositions, *compResult)
		}
	}

	// Collect XR errors with their resource IDs for top-level errors
	for _, comp := range output.Compositions {
		for _, impact := range comp.ImpactAnalysis {
			// The impact carries the OutputError built when it was created (see buildImpactAnalysis), so the
			// per-XR entry and this list hold the same error.
			if impact.Status == dt.XRStatusError && impact.Error != nil {
				output.Errors = append(output.Errors, *impact.Error)
			}
		}
	}

	// Release resources before draining advisories, so one raised during teardown (leftover function
	// containers) is collected rather than arriving after the report has been returned and rendered.
	CleanupDetached(ctx, p, p.config.Logger)

	// Attach any advisories raised during the run. An injected collector has already echoed them to
	// stderr when raised; this carries them into structured output.
	output.Warnings = p.config.Advisories.Advisories()

	// Counted before the run-level entries are added: they are not XR failures.
	totalXRErrors := len(output.Errors)

	// Renders that reach one resource in ways no single diff (or provider plan) can represent, checked
	// across every composition's composites. A run-level error: the report is still returned, so
	// structured output stays valid.
	overlaps := renderOverlapErrors(overlapGroups(composites))
	for _, err := range overlaps {
		output.Errors = append(output.Errors, dt.OutputError{Message: err.Error()})
	}

	output.Errors = withInterruption(ctx, output.Errors)

	// Return an error so CI/CD pipelines get a non-zero exit code. The report is returned with it, so
	// the caller still renders it (structured output stays valid). The renderer does not print errors,
	// so the returned error names each failing resource. A run in which only some compositions failed
	// still succeeds: each failure is in the report, under its composition. A render overlap fails the
	// run.
	var errs []error

	if totalXRErrors > 0 {
		errs = append(errs, errors.Wrapf(outputErrorsJoined(output.Errors[:totalXRErrors]), "impact analysis failed for %d XR(s)", totalXRErrors))
	}

	if compositionErrors > 0 && compositionErrors == len(output.Compositions) {
		failed := make([]dt.OutputError, 0, compositionErrors)
		for _, comp := range output.Compositions {
			failed = append(failed, *comp.Error)
		}

		errs = append(errs, errors.Wrap(outputErrorsJoined(failed), "failed to process all compositions"))
	}

	errs = append(errs, overlaps...)

	return output, errors.Join(errs...)
}

// outputErrorsJoined returns errs as one error that names each one's resource, the way PerformDiff
// names the resources it failed to process.
func outputErrorsJoined(errs []dt.OutputError) error {
	joined := make([]error, 0, len(errs))
	for _, e := range errs {
		joined = append(joined, errors.Errorf("unable to process resource %s: %s", e.ResourceID, e.Message))
	}

	return errors.Join(joined...)
}

// suppliedIdentity is the identity a supplied composite shares with the live one it replaces.
type suppliedIdentity struct{ group, kind, namespace, name string }

func identityOfSupplied(u *un.Unstructured) suppliedIdentity {
	gvk := u.GroupVersionKind()
	return suppliedIdentity{gvk.Group, gvk.Kind, u.GetNamespace(), u.GetName()}
}

// suppliedState is whether a supplied composite is live, i.e. the cluster has it, or why that could not
// be told.
type suppliedState struct {
	live bool
	err  error
}

// suppliedLiveness returns the state of every composite known to be live, or whose liveness could not
// be told: those discovered or scoped under any Composition of the run, and each named supplied composite
// the cluster has. The cluster is asked about each supplied one not discovered, since discovery misses
// live composites that --resource, --namespace or a Composition outside the run leave out. A composite
// with only a generateName is new, so it is not looked up.
func (p *DefaultCompDiffProcessor) suppliedLiveness(ctx context.Context, resolvedComps []resolvedComposition) map[suppliedIdentity]suppliedState {
	live := make(map[suppliedIdentity]suppliedState)
	for _, r := range resolvedComps {
		for _, u := range r.affectedXRs {
			live[identityOfSupplied(u)] = suppliedState{live: true}
		}
	}

	for _, us := range p.config.SuppliedComposites {
		for _, u := range us {
			id := identityOfSupplied(u)
			if _, ok := live[id]; ok || u.GetName() == "" {
				continue
			}

			getter, ok := p.xrProc.(ClusterCompositeGetter)
			if !ok {
				live[id] = suppliedState{err: errors.Errorf("cannot tell whether %s %q exists in the cluster", u.GetKind(), u.GetName())}
				continue
			}

			cluster, err := getter.ClusterComposite(ctx, u)
			switch {
			case err != nil:
				live[id] = suppliedState{err: errors.Wrapf(err, "cannot get %s %q from the cluster", u.GetKind(), u.GetName())}
			case cluster != nil:
				live[id] = suppliedState{live: true}
			}
		}
	}

	return live
}

// mergeSupplied returns the affected composites with the supplied ones added. A named supplied composite
// with the same group, kind, namespace and name as an affected one replaces it in place: it is the
// desired state, and still diffs against the live object.
//
// It also returns the diff key of every supplied composite, mapped to its state in live (see
// suppliedLiveness). One that is not live would be created by applying it, so it adopts no revision.
func mergeSupplied(affected, supplied []*un.Unstructured, live map[suppliedIdentity]suppliedState) ([]*un.Unstructured, map[string]suppliedState) {
	if len(supplied) == 0 {
		return affected, nil
	}

	at := make(map[suppliedIdentity]int, len(affected))
	out := make([]*un.Unstructured, 0, len(affected)+len(supplied))
	keys := make(map[string]suppliedState, len(supplied))

	for _, u := range affected {
		at[identityOfSupplied(u)] = len(out)
		out = append(out, u)
	}

	for _, u := range supplied {
		id := identityOfSupplied(u)
		keys[dt.MakeDiffKeyFromResource(u)] = live[id]

		if u.GetName() == "" {
			out = append(out, u)
			continue
		}

		if i, ok := at[id]; ok {
			out[i] = u
			continue
		}

		at[id] = len(out)
		out = append(out, u)
	}

	return out, keys
}

// preflightResourceRefs resolves user --resource refs against every supplied composition before
// any rendering happens. Returns the per-composition matched set keyed by composition name.
// If any ref is relevant to no supplied composition, it returns an error naming the unmatched
// refs (CLI input error). When `refs` is empty, returns (nil, nil) and the caller falls back to
// default-discovery mode.
func (p *DefaultCompDiffProcessor) preflightResourceRefs(ctx context.Context, compositions []*un.Unstructured, refs []k8stypes.NamespacedName) (map[string][]*un.Unstructured, error) {
	if len(refs) == 0 {
		return nil, nil
	}

	perComp := make(map[string][]*un.Unstructured, len(compositions))
	matchedAtLeastOnce := make(map[string]bool, len(refs))

	for _, comp := range compositions {
		if comp.GetKind() != "Composition" {
			continue
		}

		// FindComposites takes the unstructured composition; it converts to typed internally
		// in refs mode (resolveCompositeTypes needs spec.compositeTypeRef).
		matched, err := p.compositionClient.FindComposites(ctx, comp, dtypes.FindCompositesOptions{Refs: refs})
		if err != nil {
			return nil, errors.Wrapf(err, "preflight: cannot resolve resource refs for composition %s", comp.GetName())
		}

		perComp[comp.GetName()] = matched

		// A ref is matched globally if any composition's matched-set contains a composite whose
		// (namespace, name) equals the ref.
		for _, m := range matched {
			for _, ref := range refs {
				if m.GetName() == ref.Name && m.GetNamespace() == ref.Namespace {
					matchedAtLeastOnce[ref.String()] = true
				}
			}
		}
	}

	var globallyUnmatched []k8stypes.NamespacedName

	for _, ref := range refs {
		if !matchedAtLeastOnce[ref.String()] {
			globallyUnmatched = append(globallyUnmatched, ref)
		}
	}

	if len(globallyUnmatched) > 0 {
		names := make([]string, 0, len(globallyUnmatched))
		for _, r := range globallyUnmatched {
			names = append(names, ref.Format(r))
		}

		return nil, errors.Errorf("resource ref(s) not relevant to any supplied composition: %v (resource not found, or it doesn't reference one of the supplied compositions)", names)
	}

	return perComp, nil
}

// processSingleComposition processes a single composition and builds the result.
// `affectedXRs` is the pre-resolved set of XRs to evaluate (caller decides via DiffComposition's
// switch whether this comes from the --resource preflight or default-discovery via FindComposites).
// `supplied` maps the diff key of each composite from --resource-file inputs to its state (see
// mergeSupplied). One whose liveness could not be told is reported with that error, unrendered.
// `scoped` records that the set came from --resource rather than discovery (see CompositionDiff.Scoped).
// `run` holds the run's Compositions by name, for nested XRs of other types (see runCompositions).
//
// It also returns each composite it rendered without error, with its group of diffs and unchanged
// renderings (which the impact analysis leaves out), for DiffComposition's render-overlap check.
func (p *DefaultCompDiffProcessor) processSingleComposition(ctx context.Context, newComp *un.Unstructured, affectedXRs []*un.Unstructured, supplied map[string]suppliedState, scoped bool, run map[string]runComposition) (*dt.CompositionDiff, []renderedComposite, error) {
	result := &dt.CompositionDiff{
		Name:           newComp.GetName(),
		Scoped:         scoped,
		ImpactAnalysis: []dt.XRImpact{},
		AffectedResources: dt.AffectedResourcesSummary{
			Total:            0,
			WithChanges:      0,
			Unchanged:        0,
			WithErrors:       0,
			FilteredByPolicy: 0,
		},
	}

	// The composition diff itself, the typed composition and the identity of the revision it would
	// produce come from the run (see runCompositions), so a nested XR of this composition's type renders
	// under another composition's parent with the same revision this composition's own XRs get.
	rc, ok := run[newComp.GetName()]
	if !ok {
		return nil, nil, errors.Errorf("composition %q is not in the run", newComp.GetName())
	}

	if rc.err != nil {
		return nil, nil, rc.err
	}

	comparison, cliComp, pred := rc.comparison, rc.comp, rc.pred

	result.CompositionDiff = comparison.diff
	result.MaskedChangesOnly = comparison.diff == nil && comparison.changed()

	// Report what applying this composition does to CompositionRevisions regardless of whether the
	// composites are evaluated below. This is the mutative consequence of the apply, and it is
	// independent of whether anything renders differently — so it is reported at every --analyze-on
	// setting, and carried as a typed per-composition field rather than a warning, because a CI
	// consumer may want to gate on it.
	result.RevisionImpact = dt.RevisionImpact{
		ChangeScope:     string(comparison.scope),
		CreatesRevision: comparison.changed(),
	}

	result.RevisionImpact.PredictedRevisionName = rc.revisionName()

	// Partition XRs by whether they would adopt the diffed composition's resulting revision. This is
	// local (no renders), and every path below needs it: the composites that would adopt the new
	// revision are exactly the ones that would re-point at it, and the ones that would not are a
	// consequence of applying this composition in their own right. The same classification decides, in
	// cliCompositionRevision, which composites are rendered with the new revision's name: a Manual
	// composite pinned to a revision and kept by --include-pinned stays pinned, so it never is.
	// A supplied composite the cluster lacks would be created, and a new composite takes the latest
	// revision whatever its update policy, so it is not deferred as Manual (see classifyXR).
	netNew := netNewKeys(supplied)

	// A supplied composite whose liveness is unknown has no known update policy: it is not classified,
	// but kept, to be reported with its error.
	unknown := slices.DeleteFunc(slices.Clone(affectedXRs), func(xr *un.Unstructured) bool { return lookupErr(supplied, xr) == nil })

	keptXRs, droppedXRs, repointingXRs, unresolvedXRs, err := p.partitionXRsByUpdatePolicy(ctx, slices.DeleteFunc(slices.Clone(affectedXRs), func(xr *un.Unstructured) bool { return lookupErr(supplied, xr) != nil }), newComp, pred, netNew)
	if err != nil {
		return nil, nil, err
	}

	keptXRs = append(keptXRs, unknown...)

	// A composition whose pipeline calls a changed function renders differently without creating a
	// revision, so it is analysed as at least a metadata-scope change: any-change analyses it,
	// spec-change still skips it. Only the decision is raised; RevisionImpact still reports the real scope.
	analysisScope := comparison.scope
	if analysisScope == ChangeScopeNone && callsChangedFunction(cliComp.Spec.Pipeline, p.config.ChangedFunctions) {
		analysisScope = ChangeScopeMetadata
	}

	// A Manual XR pinned to a revision that calls a changed function is not deferred when the analysis
	// runs: it is rendered against that revision. See pinnedFunctionChangeXRs.
	// This holds even when the diffed composition itself calls no changed function and matches the
	// cluster: an XR pinned to an older revision that does is analysed as a metadata-scope change.
	pinnedXRs, pinnedKept, pinnedDropped := p.pinnedFunctionChangeXRs(ctx, keptXRs, droppedXRs)
	// A claim not known to be pinned or not carries that error the same way, as its own result.
	if len(unresolvedXRs) > 0 {
		if pinnedXRs == nil {
			pinnedXRs = make(map[string]pinnedResolution, len(unresolvedXRs))
		}

		maps.Copy(pinnedXRs, unresolvedXRs)
	}
	// A failed lookup does not raise the scope: it says nothing about whether the revision calls a
	// changed function, and raising it would report unrelated drift as this composition's impact. The
	// XR still carries its error when the analysis runs for another reason.
	if analysisScope == ChangeScopeNone && anyResolved(pinnedXRs) {
		analysisScope = ChangeScopeMetadata
	}

	if analysisScope.triggersAnalysis(p.config.AnalyzeOn) {
		keptXRs, droppedXRs = pinnedKept, pinnedDropped
	} else {
		pinnedXRs = nil
	}

	// A claim renders through its backing XR, so the claim's entry carries the XR's changes. When both
	// are in scope, rendering the XR too would report one change twice, and — when the claim is edited,
	// which Crossplane syncs into the XR — report the XR's stale rendering as a conflict with its claim's.
	// This runs after the pinned step, so a pinned backing XR whose Manual claim stays filtered is kept.
	keptXRs, droppedXRs = withoutClaimedXRs(keptXRs, droppedXRs, cliComp.Spec.CompositeTypeRef.Kind)
	inScope := len(keptXRs) + len(droppedXRs)

	// A dropped backing XR is reported under its claim, so it does not re-point on its own. Nor does its
	// pin apply to the claim's render, which is handed the backing XR (see collectXRDiffs): the claim's
	// spec, policy included, is what Crossplane syncs into that XR, so only the claim's own pin, resolved
	// for that synced XR, does.
	maps.DeleteFunc(repointingXRs, func(key string, _ bool) bool { return !slices.ContainsFunc(keptXRs, isKey(key)) })
	maps.DeleteFunc(pinnedXRs, func(key string, _ pinnedResolution) bool { return !slices.ContainsFunc(keptXRs, isKey(key)) })

	// A composite that would be created adopts nothing: it is not counted as re-pointing.
	if comparison.changed() {
		result.RevisionImpact.RepointedComposites = countRepointed(repointingXRs, netNew)
	}

	counts := countFilterReasons(droppedXRs)

	p.config.Logger.Debug("Filtered XRs by deletion state, update policy, and revision selector",
		"composition", newComp.GetName(),
		"originalCount", len(affectedXRs),
		"keptCount", len(keptXRs),
		"repointingCount", len(repointingXRs),
		"droppedCount", len(droppedXRs),
		"filteredByPolicy", counts.byPolicy,
		"filteredBySelector", counts.bySelector,
		"filteredByDeletion", counts.byDeletion,
		"includeManual", p.config.IncludeManual)

	// Record every filtered composite as XRStatusFiltered, with its reason, in either mode. Whether to
	// list them or only count them is the renderer's decision (see CompositionDiff.Scoped).
	for _, d := range droppedXRs {
		result.ImpactAnalysis = append(result.ImpactAnalysis, dt.XRImpact{
			ComposedBy:   parentCompositeRef(d.xr, nil),
			APIVersion:   d.xr.GetAPIVersion(),
			Kind:         d.xr.GetKind(),
			Name:         d.xr.GetName(),
			Namespace:    d.xr.GetNamespace(),
			Status:       dt.XRStatusFiltered,
			FilterReason: d.reason,
			FilterDetail: d.detail,
			Supplied:     isSupplied(supplied, d.xr),
		})
	}

	// Which composites were discovered, and which of them would not adopt the resulting revision, is
	// settled above without rendering anything. So it is recorded before the skip below, which
	// suppresses only the per-XR renders: --analyze-on is a cost knob, and a flag may change how much
	// work is done but never whether a real consequence is reported. Reporting a total of 0 here while
	// RevisionImpact.RepointedComposites said otherwise was issue #478.
	result.AffectedResources.Total = inScope
	counts.applyTo(&result.AffectedResources)

	// Skip the per-XR work — one function render per XR, the dominant cost of comp — when the change
	// is smaller than the user asked to analyse.
	//
	// At the default (any-change) this skips only a composition identical in everything Crossplane
	// hashes: no new CompositionRevision is created, so no XR adopts anything it hasn't already, and
	// any downstream delta computed here would be caused by something else — drift, convergence lag,
	// or a modeling artifact of this tool — which we cannot tell apart, so reporting it as this
	// composition's "impact" would attribute cluster state to a change that does not exist.
	// See issues #453 and #472.
	//
	// That does not hold for a composite from --resource-file, which the user asked to preview: it is
	// rendered whatever the scope, and only the discovered ones are skipped.
	// skippedErrors counts the discovered composites the skip reports as errors, which the supplied
	// ones' summary below would otherwise drop.
	skippedErrors := 0

	if !analysisScope.triggersAnalysis(p.config.AnalyzeOn) {
		p.config.Logger.Debug("Skipping impact analysis",
			"composition", newComp.GetName(),
			"changeScope", string(analysisScope),
			"analyzeOn", string(p.config.AnalyzeOn),
			"affectedXRCount", len(affectedXRs))

		result.ImpactAnalysisSkipped = true

		// A kept claim whose pin could not be read is an error the run already earned, not a verdict
		// the skip withholds, so it is reported as one: without it the claim would vanish from the
		// report, the error from output.Errors, and RepointedComposites would undercount silently.
		// A supplied composite is rendered below, so it reports its own error there.
		discovered := slices.DeleteFunc(slices.Clone(keptXRs), func(xr *un.Unstructured) bool { return isSupplied(supplied, xr) })
		impacts, summary := p.unresolvedImpacts(discovered, unresolvedXRs)
		result.ImpactAnalysis = append(result.ImpactAnalysis, impacts...)
		result.AffectedResources.WithErrors = summary.WithErrors
		skippedErrors = summary.WithErrors

		keptXRs = slices.DeleteFunc(keptXRs, func(xr *un.Unstructured) bool { return !isSupplied(supplied, xr) })
		if len(keptXRs) == 0 {
			return result, nil, nil
		}
	}

	p.config.Logger.Debug("Processing affected XRs", "composition", newComp.GetName(), "count", len(affectedXRs), "scoped", scoped)

	if len(keptXRs) == 0 {
		// All XRs were filtered; the summary recorded above is already complete.
		return result, nil, nil
	}

	// Render the composites with the revision they would actually be pointing at. Without this the
	// render sees each composite's *existing* compositionRevisionRef, so a composition template reading
	// the revision name produces the stale one and a real change goes unreported. See issue #474.
	//
	// When the name is not predictable we render with the refs the composites have — the pre-#474
	// behaviour — and say so, rather than guess one. Rendering with a name we invented would manufacture
	// a downstream diff on a converged cluster for exactly the compositions this feature exists to serve.
	revisionName := rc.revisionName()

	if revisionName == "" && comparison.changed() {
		p.config.Advisories.Raise(dt.OutputWarning{
			Message: "Could not predict the name of the CompositionRevision this composition would create, because it differs from the cluster's only by the annotation a client-side `kubectl apply` writes — whose post-apply value depends on how you apply, not on the file. Composites were rendered with their existing compositionRevisionRef, so if any of your composition templates read the revision name, a resulting change was not detected. Apply with `--server-side` (or via Argo/Flux) to make it predictable.",
			Context: map[string]string{"composition": newComp.GetName()},
		})
	}

	// Process kept XRs and collect diffs to determine which ones have changes
	p.config.Logger.Debug("Processing XRs to collect diff information", "count", len(keptXRs))

	xrResults := p.collectXRDiffs(ctx, slices.DeleteFunc(slices.Clone(keptXRs), func(xr *un.Unstructured) bool { return lookupErr(supplied, xr) != nil }), cliComp, revisionName, rc.revisionLabels, pinnedXRs, netNew, run)
	for _, xr := range unknown {
		key := dt.MakeDiffKeyFromResource(xr)
		xrResults[key] = &XRDiffResult{Diffs: make(map[string]*dt.ResourceDiff), Error: errors.Wrapf(lookupErr(supplied, xr), "unable to process resource %s", key)}
	}

	composites := make([]renderedComposite, 0, len(keptXRs))
	for _, xr := range keptXRs {
		if r := xrResults[dt.MakeDiffKeyFromResource(xr)]; r != nil && !r.HasError() {
			group := dt.XRDiffGroup{XR: objectRef(xr), Diffs: r.Diffs}
			composites = append(composites, renderedComposite{xr: xr, group: withUnchanged([]dt.XRDiffGroup{group}, []map[string]*dt.ResourceDiff{r.unchanged})[0]})
		}
	}

	// Build impact analysis and counts from results for the kept set, then merge in any
	// already-appended filtered entries.
	keptImpacts, keptSummary := p.buildImpactAnalysis(keptXRs, xrResults)
	// buildImpactAnalysis keeps keptXRs' order. A kept composite that does not re-point is one
	// --include-pinned kept although its Manual policy pins it, or a claim whose pin could not be read,
	// which is not marked Pinned because whether it is pinned is unknown.
	for i, xr := range keptXRs {
		key := dt.MakeDiffKeyFromResource(xr)
		_, unresolved := unresolvedXRs[key]
		keptImpacts[i].Pinned = !repointingXRs[key] && !unresolved && lookupErr(supplied, xr) == nil
		keptImpacts[i].Supplied = isSupplied(supplied, xr)

		if pinned, ok := pinnedXRs[key]; ok && pinned.err == nil {
			keptImpacts[i].RenderedRevision = pinned.resolved.RevisionName
		}
	}

	result.ImpactAnalysis = append(result.ImpactAnalysis, keptImpacts...)
	// keptSummary.Total counts only kept; widen to include filtered so totals stay consistent.
	keptSummary.Total = inScope
	counts.applyTo(&keptSummary)
	keptSummary.WithErrors += skippedErrors
	result.AffectedResources = keptSummary

	return result, composites, nil
}

// isSupplied reports whether xr came from --resource-file inputs (see mergeSupplied).
func isSupplied(supplied map[string]suppliedState, xr *un.Unstructured) bool {
	_, ok := supplied[dt.MakeDiffKeyFromResource(xr)]
	return ok
}

// lookupErr is the error telling whether supplied composite xr is live, or nil.
func lookupErr(supplied map[string]suppliedState, xr *un.Unstructured) error {
	return supplied[dt.MakeDiffKeyFromResource(xr)].err
}

// renderedComposite is one composite a composition run rendered successfully, for the overlap check:
// the discovered object, and its diff group with its unchanged renderings added (see withUnchanged).
type renderedComposite struct {
	xr    *un.Unstructured
	group dt.XRDiffGroup
}

// isKey returns a predicate matching the resource whose dt.MakeDiffKeyFromResource is key.
func isKey(key string) func(*un.Unstructured) bool {
	return func(res *un.Unstructured) bool { return dt.MakeDiffKeyFromResource(res) == key }
}

// objectRef is res's object reference, as an impact or group names it.
func objectRef(res *un.Unstructured) corev1.ObjectReference {
	return corev1.ObjectReference{
		APIVersion: res.GetAPIVersion(),
		Kind:       res.GetKind(),
		Name:       res.GetName(),
		Namespace:  res.GetNamespace(),
	}
}

// withoutClaimedXRs returns kept and dropped without each XR (of kind xrKind) bound to a claim (of
// another kind) in scope, read from the binding Crossplane records on either side (see managedBy), so the
// change is reported once, under the claim. A filtered XR is removed whenever its claim is in scope. A
// kept XR is removed only when its claim is kept too: one whose claim is filtered, such as a Manual claim
// naming a revision that calls no changed function while its backing XR is still pinned to one that
// does, is still rendered, since nothing else reports it.
func withoutClaimedXRs(kept []*un.Unstructured, dropped []filteredXR, xrKind string) ([]*un.Unstructured, []filteredXR) {
	boundIn := func(claims []*un.Unstructured) func(*un.Unstructured) bool {
		return func(xr *un.Unstructured) bool {
			return xr.GetKind() == xrKind && slices.ContainsFunc(claims, func(claim *un.Unstructured) bool {
				return claim.GetKind() != xrKind && managedBy(dt.XRDiffGroup{}, xr, dt.XRDiffGroup{}, claim) == claimedBy
			})
		}
	}

	inScope := slices.Clone(kept)
	for _, d := range dropped {
		inScope = append(inScope, d.xr)
	}

	claimedInKept, claimedInScope := boundIn(kept), boundIn(inScope)

	return slices.DeleteFunc(slices.Clone(kept), claimedInKept),
		slices.DeleteFunc(slices.Clone(dropped), func(d filteredXR) bool { return claimedInScope(d.xr) })
}

// overlapGroups returns the groups of composites for the overlap check, without each composite another
// one in the run manages (see managedBy). A nested XR, controlled by its parent, is still rendered and
// reported, but the parent's rendering of what they share wins: it renders the nested XR as the parent
// would write it, with the nested XR's Composition from the run when the run has it (see
// runCompositionFor), where the nested XR's own render starts from the spec it has now.
//
// A kept claim's backing XR is dropped (see withoutClaimedXRs), so it cannot exclude an XR nested under
// it here. Such a nested XR carries the crossplane.io/claim-name and claim-namespace labels Crossplane
// copies onto composed resources, so it is bound to the claim (see boundToClaim): withoutClaimedXRs
// drops one of the backing XR's own type before it is rendered, and one of another type, rendered for
// its own Composition, is left out here. One lacking those labels reaches this check, controlled by no
// rendered composite.
func overlapGroups(composites []renderedComposite) []dt.XRDiffGroup {
	groups := make([]dt.XRDiffGroup, 0, len(composites))

	for b, nested := range composites {
		managed := slices.ContainsFunc(composites, func(parent renderedComposite) bool {
			return parent.xr != nested.xr && managedBy(nested.group, nested.xr, parent.group, parent.xr) != notManaged
		})

		if !managed {
			groups = append(groups, composites[b].group)
		}
	}

	return groups
}

// collectXRDiffs processes XRs and collects their diffs, returning results for each XR.
//
// revisionName is the name of the CompositionRevision applying cliComp would leave its composites on,
// or empty when that is not predictable; revisionLabels is the label set that revision would carry. See
// cliCompositionRevision for which composites are rendered with that name.
//
// pinned, keyed by dt.MakeDiffKeyFromResource, holds the composites rendered against their own pinned
// revision instead of cliComp, each with the key of the composite its render resolves the composition
// for; see pinnedFunctionChangeXRs.
//
// run holds the run's Compositions by name; a nested XR of another type is rendered with its own from
// there when the run has it (see runCompositionFor).
func (p *DefaultCompDiffProcessor) collectXRDiffs(ctx context.Context, xrs []*un.Unstructured, cliComp *apiextensionsv1.Composition, revisionName string, revisionLabels map[string]string, pinned map[string]pinnedResolution, netNew map[string]bool, run map[string]runComposition) map[string]*XRDiffResult {
	// Extract the target GVK from the CLI composition's compositeTypeRef
	cliCompTargetAPIVersion := cliComp.Spec.CompositeTypeRef.APIVersion
	cliCompTargetKind := cliComp.Spec.CompositeTypeRef.Kind

	// Only the group is compared: an XR may use a served version that is not the referenceable one the
	// compositeTypeRef names, and Crossplane still composes it with this composition. A malformed
	// apiVersion matches nothing, as in pinnedRevisionCallsChangedFunction.
	cliCompTargetGV, cliCompTargetGVErr := schema.ParseGroupVersion(cliCompTargetAPIVersion)

	p.config.Logger.Debug("CLI composition targets",
		"apiVersion", cliCompTargetAPIVersion,
		"kind", cliCompTargetKind)

	// Build a set of root-level resource keys (apiVersion/kind/namespace/name) for quick lookup.
	// Root-level resources are XRs and Claims supplied as `affectedXRs` to processSingleComposition
	// (resolved by DiffComposition via either preflight or FindComposites default-discovery).
	// These should always use the CLI composition. Namespace is included in the key to avoid
	// collisions between resources with the same name in different namespaces.
	rootResourceKeys := make(map[string]bool)

	for _, xr := range xrs {
		key := dt.MakeDiffKeyFromResource(xr)
		rootResourceKeys[key] = true
	}

	// The provider is handed the composite the pin was resolved for, which for a claim is its backing
	// XR, so it looks pins up under that composite's key.
	pinnedComposites := make(map[string]dtypes.ResolvedComposition, len(pinned))
	for _, pin := range pinned {
		if pin.err == nil {
			pinnedComposites[pin.composite] = pin.resolved
		}
	}

	// Likewise a net-new root claim reaches the provider as its backing XR, so that XR is net-new too.
	netNew = p.withNetNewComposites(ctx, xrs, netNew, cliCompTargetKind)

	// Composition provider that returns CLI composition for:
	// 1. Root-level resources (XRs and Claims that use this composition)
	// 2. XRs whose type matches the CLI composition's compositeTypeRef
	//
	// A nested XR of another type is rendered with its Composition from the run when the run has it,
	// and otherwise with the cluster's (see runCompositionFor).
	//
	// The provider is handed the effective composite (see CompositeResolver), so a root claim reaches it
	// as its backing XR, whose key is not in rootResourceKeys: Check 2 matches it by type instead.
	compositionProvider := func(ctx context.Context, res *un.Unstructured) (dtypes.ResolvedComposition, error) {
		resGVK := res.GroupVersionKind()
		resAPIVersion := resGVK.GroupVersion().String()
		resKind := resGVK.Kind
		resourceID := fmt.Sprintf("%s/%s", res.GetKind(), res.GetName())

		// Check 1: Is this a root-level resource (XR or Claim supplied as affectedXRs to this composition)?
		// Root-level resources always use the CLI composition, even claims whose GVK differs from the XR type.
		key := dt.MakeDiffKeyFromResource(res)
		if pin, ok := pinnedComposites[key]; ok {
			p.config.Logger.Debug("Resource is pinned to a revision calling a changed function, using that revision",
				"resource", resourceID,
				"revision", pin.RevisionName)

			return pin, nil
		}

		if rootResourceKeys[key] {
			p.config.Logger.Debug("Resource is root-level (uses this composition), using CLI composition",
				"resource", resourceID,
				"composition", cliComp.GetName())

			return p.cliCompositionRevision(ctx, res, cliComp, revisionName, revisionLabels, netNew)
		}

		// Check 2: Does this resource's type match the CLI composition's target type?
		// This handles XRs encountered during rendering that match the composition's type, whichever
		// served version they use.
		if cliCompTargetGVErr == nil && resGVK.Group == cliCompTargetGV.Group && resKind == cliCompTargetKind {
			p.config.Logger.Debug("Resource matches CLI composition type, using CLI composition",
				"resource", resourceID,
				"composition", cliComp.GetName())

			return p.cliCompositionRevision(ctx, res, cliComp, revisionName, revisionLabels, netNew)
		}

		// This is a nested XR with a different type: its Composition comes from the run, or the cluster.
		p.config.Logger.Debug("Resource does not match CLI composition type, looking up its own composition",
			"resource", resourceID,
			"resourceAPIVersion", resAPIVersion,
			"resourceKind", resKind,
			"cliCompTargetAPIVersion", cliCompTargetAPIVersion,
			"cliCompTargetKind", cliCompTargetKind)

		return p.runCompositionFor(ctx, res, run)
	}

	results := make(map[string]*XRDiffResult)

	for i, xr := range xrs {
		resourceID := dt.MakeDiffKeyFromResource(xr)
		// The composition prefix tells apart composites of the same name when
		// several compositions are simulated in one run.
		p.config.Progress.Report(i, len(xrs), cliComp.GetName()+": "+resourceID)

		if pin, ok := pinned[resourceID]; ok && pin.err != nil {
			results[resourceID] = &XRDiffResult{
				Diffs: make(map[string]*dt.ResourceDiff),
				Error: errors.Wrapf(pin.err, "unable to process resource %s", resourceID),
			}

			continue
		}

		xrCtx, cancel := p.config.xrContext(ctx)
		diffs, rendered, err := p.xrProc.DiffSingleResource(xrCtx, xr, compositionProvider)
		err = p.config.afterXR(ctx, xrCtx, diffs, err)
		cancel()
		if err != nil {
			p.config.Logger.Debug("Failed to process resource", "resource", resourceID, "error", err)

			// Store the error in the result
			results[resourceID] = &XRDiffResult{
				Diffs: make(map[string]*dt.ResourceDiff),
				Error: errors.Wrapf(err, "unable to process resource %s", resourceID),
			}
		} else {
			// Store successful result with diffs
			results[resourceID] = &XRDiffResult{
				Diffs:     diffs,
				unchanged: rendered.Unchanged(),
				health:    rendered.Health(),
			}
		}
	}

	return results
}

// withNetNewComposites returns netNew plus the key of the composite the render resolves the composition
// for, for each net-new root of another kind than xrKind: a claim, rendered through its backing XR.
func (p *DefaultCompDiffProcessor) withNetNewComposites(ctx context.Context, xrs []*un.Unstructured, netNew map[string]bool, xrKind string) map[string]bool {
	out := make(map[string]bool, len(netNew))
	maps.Copy(out, netNew)

	for _, xr := range xrs {
		if !netNew[dt.MakeDiffKeyFromResource(xr)] || xr.GetKind() == xrKind {
			continue
		}

		// A failed resolve is the render's to report: it resolves the same composite.
		if composite, err := p.xrProc.ResolveComposite(ctx, xr); err == nil {
			out[dt.MakeDiffKeyFromResource(composite)] = true
		}
	}

	return out
}

// pinnedResolution is the composition a Manual XR is pinned to, or why it could not be resolved.
type pinnedResolution struct {
	resolved dtypes.ResolvedComposition
	err      error

	// composite is the dt.MakeDiffKeyFromResource of the composite the render resolves the composition
	// for: the XR itself or, for a claim, its backing XR (see DiffProcessor.ResolveComposite).
	composite string
}

// anyResolved reports whether any pinned lookup succeeded.
func anyResolved(m map[string]pinnedResolution) bool {
	for _, r := range m {
		if r.err == nil {
			return true
		}
	}

	return false
}

// pinnedFunctionChangeXRs moves out of dropped the Manual XRs that stay pinned to a revision whose
// pipeline calls a changed function. Such an XR is not unaffected by applying the project: the revision
// it runs is unchanged, but the function it calls is not, so it is kept and rendered against that
// revision instead of the diffed composition. It returns those XRs' resolutions (keyed by
// dt.MakeDiffKeyFromResource), kept with the moved XRs appended, and dropped without them.
//
// The revision is looked up for the composite the render resolves the composition for: the XR itself
// or, for a Manual claim, its backing XR with the claim synced in, whose pin is the claim's.
//
// It does nothing when no function changed or under IncludeManual, which keeps Manual XRs and renders
// them against the diffed composition. A Manual XR whose revision calls no changed function stays dropped.
// A failed lookup is that XR's own error (a kept XR whose result is the error), not the run's. So with
// changed functions set, a failed revision lookup makes every Manual XR, claims included, a per-XR error
// where upstream would filter it.
func (p *DefaultCompDiffProcessor) pinnedFunctionChangeXRs(ctx context.Context, kept []*un.Unstructured, dropped []filteredXR) (map[string]pinnedResolution, []*un.Unstructured, []filteredXR) {
	if len(p.config.ChangedFunctions) == 0 || p.config.IncludeManual {
		return nil, kept, dropped
	}

	var (
		pinned    map[string]pinnedResolution
		remaining []filteredXR
	)

	for _, d := range dropped {
		if d.reason != dt.FilterReasonManualPolicy {
			remaining = append(remaining, d)
			continue
		}

		var resolved dtypes.ResolvedComposition

		composite, err := p.xrProc.ResolveComposite(ctx, d.xr)
		if err == nil {
			resolved, err = p.compositionClient.FindMatchingComposition(ctx, composite)
		}

		if err == nil && !pinnedRevisionCallsChangedFunction(composite, resolved, p.config.ChangedFunctions) {
			remaining = append(remaining, d)
			continue
		}

		if pinned == nil {
			pinned = make(map[string]pinnedResolution)
		}

		pin := pinnedResolution{resolved: resolved, err: errors.Wrapf(err, "cannot resolve the revision %s is pinned to", d.xr.GetName())}
		if err == nil {
			pin.composite = dt.MakeDiffKeyFromResource(composite)
		}

		pinned[dt.MakeDiffKeyFromResource(d.xr)] = pin
		kept = append(kept, d.xr)
	}

	return pinned, kept, remaining
}

// pinnedRevisionCallsChangedFunction reports whether resolved is the pinned revision of composite and
// calls a changed function. composite is the one the render resolves the composition for, so for a
// claim it is the backing XR, whose type the revision's compositeTypeRef names. A resolution with no
// revision name (the composition itself) or for another type is not.
func pinnedRevisionCallsChangedFunction(composite *un.Unstructured, resolved dtypes.ResolvedComposition, changed sets.Set[string]) bool {
	if resolved.Composition == nil || resolved.RevisionName == "" {
		return false
	}

	// Group and kind only: the XR may use a served version that is not the referenceable one.
	ref := resolved.Composition.Spec.CompositeTypeRef

	refGV, err := schema.ParseGroupVersion(ref.APIVersion)
	if err != nil || refGV.Group != composite.GroupVersionKind().Group || ref.Kind != composite.GetKind() {
		return false
	}

	return callsChangedFunction(resolved.Composition.Spec.Pipeline, changed)
}

// cliCompositionRevision resolves a composite that is rendered against cliComp, the diffed composition.
// Its revision is the one applying that composition leaves it on, named revisionName, for a composite
// that would re-point at it as classifyXR decides; revisionLabels is the label set that revision would
// carry, which a compositionRevisionSelector is matched against. For any other composite the revision
// is left unknown, so it keeps the ref it has: one a Manual policy pins to a revision (#479), being deleted, or whose
// compositionRevisionSelector rejects that revision. So does every composite when revisionName is
// empty, i.e. not predictable (#474).
func (p *DefaultCompDiffProcessor) cliCompositionRevision(ctx context.Context, res *un.Unstructured, cliComp *apiextensionsv1.Composition, revisionName string, revisionLabels map[string]string, netNew map[string]bool) (dtypes.ResolvedComposition, error) {
	resolved := dtypes.ResolvedComposition{Composition: cliComp}
	if revisionName == "" {
		return resolved, nil
	}

	disposition, err := p.classifyXR(ctx, res, cliComp.Spec.CompositeTypeRef.Kind, revisionLabels, cliComp.GetLabels(), netNew[dt.MakeDiffKeyFromResource(res)])
	if err != nil {
		return dtypes.ResolvedComposition{}, err
	}

	if disposition.filtered == nil && !disposition.pinnedToRevision && disposition.unresolved == nil {
		resolved.RevisionName = revisionName
	}

	return resolved, nil
}

// runComposition is one Composition of the run: its comparison with the cluster's, its typed form and
// the predicted identity and labels of the revision it would produce, or the error that kept it from
// being read. It is the one source of these for the Composition's own processing (see
// processSingleComposition) and for a nested XR of its type under another Composition's parent (see
// runCompositionFor).
type runComposition struct {
	comparison     compositionComparison
	comp           *apiextensionsv1.Composition
	pred           predictedRevision
	revisionLabels map[string]string
	err            error
}

// revisionName is the predicted name of the revision rc would produce, or empty when it cannot be
// predicted (see compositionComparison.revisionNamePredictable).
func (rc runComposition) revisionName() string {
	if !rc.comparison.revisionNamePredictable {
		return ""
	}

	return rc.pred.name
}

// runCompositions returns the run's Compositions by name. Names are unique within a run (see
// simulate.RejectDuplicateNames). A Composition that cannot be read carries its error, which its own
// processing reports.
func (p *DefaultCompDiffProcessor) runCompositions(ctx context.Context, compositions []*un.Unstructured) map[string]runComposition {
	run := make(map[string]runComposition, len(compositions))

	for _, u := range compositions {
		if u.GetKind() != "Composition" {
			continue
		}

		// The comparison strips cluster metadata from u, so it runs before the conversion.
		comparison, err := p.calculateCompositionDiff(ctx, u)
		if err != nil {
			run[u.GetName()] = runComposition{err: errors.Wrap(err, "cannot calculate composition diff")}
			continue
		}

		comp := &apiextensionsv1.Composition{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, comp); err != nil {
			run[u.GetName()] = runComposition{err: errors.Wrapf(err, "cannot convert composition %q to typed", u.GetName())}
			continue
		}

		// The identity of the revision this composition would produce, used to evaluate
		// compositionRevisionSelectors against the label set the revision would carry, and to seed the
		// composites so a template observing the revision name renders the value it would really get.
		pred := predictRevision(comp)
		run[u.GetName()] = runComposition{comparison: comparison, comp: comp, pred: pred, revisionLabels: predictedRevisionLabels(u, pred)}
	}

	return run
}

// runCompositionFor resolves the composition of res, a nested XR of a type other than the diffed
// composition's. The run's Composition is used when it is the one res names in compositionRef, or, when
// res names none, the one the cluster selects for it (by compositionSelector or type), and res would
// adopt the revision it produces (see adoptsRunRevision). res then renders with that revision's predicted
// name, as a composite of the diffed composition does (see cliCompositionRevision). Otherwise res gets the
// cluster's composition and revision: a Manual res stays on the revision it is pinned to, as Crossplane
// would keep it.
//
// TODO(jcogilvie): selection by compositionSelector or type runs over the cluster's Compositions, so a run
// Composition the cluster does not have yet, or whose labels the run changes, is not selected for a
// nested XR without a compositionRef. Crossplane writes compositionRef on every XR it reconciles, so
// only a nested XR the run would create is affected; overlay the run onto the cluster's candidates if
// that case matters.
func (p *DefaultCompDiffProcessor) runCompositionFor(ctx context.Context, res *un.Unstructured, run map[string]runComposition) (dtypes.ResolvedComposition, error) {
	name, err := xp.XRCompositionRef(res.Object, res.GetAPIVersion())
	if err != nil {
		return dtypes.ResolvedComposition{}, errors.Wrapf(err, "cannot read compositionRef for %s/%s", res.GetKind(), res.GetName())
	}

	// cluster is the cluster's resolution, looked up only when res names no composition or the run's
	// does not apply to it.
	var cluster dtypes.ResolvedComposition

	if name == "" {
		if cluster, err = p.compositionClient.FindMatchingComposition(ctx, res); err != nil {
			return dtypes.ResolvedComposition{}, err
		}

		name = cluster.Composition.GetName()
	}

	rc, inRun := run[name]
	if inRun {
		if inRun, err = p.adoptsRunRevision(ctx, res, name, rc); err != nil {
			return dtypes.ResolvedComposition{}, err
		}
	}

	if !inRun {
		if cluster.Composition != nil {
			return cluster, nil
		}

		return p.compositionClient.FindMatchingComposition(ctx, res)
	}

	p.config.Logger.Debug("Nested XR's composition is in the run, using it",
		"resource", fmt.Sprintf("%s/%s", res.GetKind(), res.GetName()),
		"composition", rc.comp.GetName())

	// classifyXR kept res, so it adopts the revision, as cliCompositionRevision would find.
	return dtypes.ResolvedComposition{Composition: rc.comp, RevisionName: rc.revisionName()}, nil
}

// adoptsRunRevision reports whether res, a nested XR naming (or selected for) the run's Composition rc
// called name, would compose with the revision rc produces (see classifyXR). A Manual res pinned by a
// compositionRevisionRef does not, and nothing about rc matters to it, so a run entry that cannot be used
// does not fail it. For any other res, an rc that cannot be read, or composes another type than res, is
// an error.
func (p *DefaultCompDiffProcessor) adoptsRunRevision(ctx context.Context, res *un.Unstructured, name string, rc runComposition) (bool, error) {
	policy, err := xp.XRUpdatePolicy(res.Object, res.GetAPIVersion())
	if err != nil {
		return false, errors.Wrapf(err, "cannot read compositionUpdatePolicy for XR %q", res.GetName())
	}

	if policy == compositionUpdatePolicyManual {
		revision, err := xp.XRCompositionRevisionRef(res.Object, res.GetAPIVersion())
		if err != nil {
			return false, errors.Wrapf(err, "cannot read compositionRevisionRef for XR %q", res.GetName())
		}

		if revision != "" {
			return false, nil
		}
	}

	if rc.err != nil {
		return false, errors.Wrapf(rc.err, "cannot use composition %q from the run", name)
	}

	if ref := rc.comp.Spec.CompositeTypeRef; ref.APIVersion != res.GetAPIVersion() || ref.Kind != res.GetKind() {
		return false, errors.Errorf("composition %q in the run composes %s %s, but nested XR %q is a %s %s", rc.comp.GetName(), ref.APIVersion, ref.Kind, res.GetName(), res.GetAPIVersion(), res.GetKind())
	}

	disposition, err := p.classifyXR(ctx, res, rc.comp.Spec.CompositeTypeRef.Kind, rc.revisionLabels, rc.comp.GetLabels(), false)
	if err != nil {
		return false, err
	}

	return disposition.filtered == nil && !disposition.pinnedToRevision, nil
}

// compositionComparison is the result of comparing a proposed composition against its in-cluster
// version.
//
// The two fields come apart whenever a mask hides the only difference: `diff` is what the user is
// shown (nil when the compositions are equal after masking), while `changed` records whether the
// composition differs at all. Impact analysis gates on `changed`, never on `diff`.
//
// Both kinds of mask can hide a real difference, so neither may gate the analysis:
//   - the user's --ignore-paths, which could cover something load-bearing for rendering (anything
//     under spec.pipeline[].input, say);
//   - the comparison's display-only suppressions, which cover fields Crossplane nonetheless hashes
//     into a composition's identity, and so into whether a new CompositionRevision is created.
//
// `scope` is therefore computed with every mask lifted. See issue #453.
//
// `revisionNamePredictable` is separate from both, and answers a third question: can we say what the
// resulting CompositionRevision will be *called*? See the field comment.
type compositionComparison struct {
	diff  *dt.ResourceDiff
	scope ChangeScope

	// revisionNamePredictable reports whether the resulting revision's name can be derived from the
	// composition as supplied. It is false in exactly one situation: the composition differs from the
	// cluster's *only* by kubectl's last-applied-configuration annotation.
	//
	// The name is "<composition>-<hash[:7]>" and Composition.Hash() covers annotations, so the name
	// depends on that annotation's post-apply value — which a client-side `kubectl apply` derives from
	// the file being applied (see kubectl's GetModifiedConfiguration), not from what is in the file's
	// own annotations. So the value the cluster will end up holding is a function of the user's apply
	// mode, and this tool cannot know it: under server-side apply the annotation is left alone, under
	// client-side apply it is rewritten. Predicting from the file as supplied would be a guess.
	//
	// Note what is NOT claimed when this is false. The delta is real, so the composition may well
	// produce a revision and CreatesRevision stays true; a stale annotation genuinely would be
	// rewritten, minting one. What is unknowable is the identity, so we decline to seed it and say so
	// rather than seed a name we invented — which, for a template reading the name, would manufacture
	// a downstream diff on a converged cluster. See issue #474.
	//
	// The exception is a live annotation that already describes the file: applying it then writes
	// nothing, so the composition is reported unchanged rather than unpredictable. See
	// lastAppliedDescribes and issue #500.
	revisionNamePredictable bool
}

// changed reports whether the composition differs at all in the fields Crossplane hashes.
func (c compositionComparison) changed() bool {
	return c.scope != ChangeScopeNone
}

// calculateCompositionDiff calculates the diff between the cluster composition and the file composition.
func (p *DefaultCompDiffProcessor) calculateCompositionDiff(ctx context.Context, newComp *un.Unstructured) (compositionComparison, error) {
	p.config.Logger.Debug("Calculating composition diff", "composition", newComp.GetName())

	var originalCompUnstructured *un.Unstructured

	// Get the original composition from the cluster
	originalComp, err := p.compositionClient.GetComposition(ctx, newComp.GetName())
	if err != nil {
		p.config.Logger.Debug("Original composition not found in cluster, treating as new composition",
			"composition", newComp.GetName(), "error", err)
		// originalCompUnstructured remains nil for new compositions
	} else {
		p.config.Logger.Debug("Retrieved original composition from cluster", "name", originalComp.GetName(), "composition", originalComp)

		// Convert original composition to unstructured for comparison
		unstructuredObj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(originalComp)
		if err != nil {
			return compositionComparison{}, errors.Wrap(err, "cannot convert original composition to unstructured")
		}

		originalCompUnstructured = &un.Unstructured{Object: unstructuredObj}
	}

	newCompUnstructured := newComp

	// Clean up managed fields and other cluster metadata before diff calculation
	cleanupClusterMetadata := func(obj *un.Unstructured) {
		if obj == nil {
			return
		}

		obj.SetManagedFields(nil)
		obj.SetResourceVersion("")
		obj.SetUID("")
		obj.SetGeneration(0)
		obj.SetCreationTimestamp(metav1.Time{})
	}

	cleanupClusterMetadata(originalCompUnstructured)
	cleanupClusterMetadata(newCompUnstructured)

	// Calculate the composition diff directly without dry-run apply
	// (compositions are static YAML documents that don't need server-side processing)
	diffOptions := p.config.GetCompareOptions()

	compDiff, err := compare.GenerateDiff(ctx, originalCompUnstructured, newCompUnstructured, p.config.Logger, diffOptions)
	if err != nil {
		return compositionComparison{}, errors.Wrap(err, "cannot calculate composition diff")
	}

	p.config.Logger.Debug("Calculated composition diff",
		"composition", newComp.GetName(),
		"hasChanges", compDiff.DiffType != dt.DiffTypeEqual,
		"isNewComposition", originalCompUnstructured == nil)

	if compDiff.DiffType != dt.DiffTypeEqual {
		// A displayable difference means the compositions differ in something other than the annotation
		// (which is never displayed), so the hash difference is driven by real content and the resulting
		// revision's name follows from the composition as supplied.
		return compositionComparison{
			diff:                    compDiff,
			scope:                   compositionChangeScope(originalCompUnstructured, newCompUnstructured, true),
			revisionNamePredictable: true,
		}, nil
	}

	// Nothing to display. That is not the same as unchanged: both the user's --ignore-paths and the
	// comparison's display-only suppressions could be hiding a real difference. Re-compare with every
	// mask lifted (ForVerdict) to find out.
	//
	// The masks are display preferences; whether the composition changed is a fact about the object.
	// Crossplane's own answer to that question is Composition.Hash(), which covers labels,
	// annotations and spec — so a metadata-only difference genuinely produces a new
	// CompositionRevision that composites re-point to, and that a template can observe through the
	// XR's compositionRevisionRef. This comparison mirrors that scope. It is local (no API calls).
	diffOptions.IgnorePaths = nil
	diffOptions.ForVerdict = true

	unmasked, err := compare.GenerateDiff(ctx, originalCompUnstructured, newCompUnstructured, p.config.Logger, diffOptions)
	if err != nil {
		return compositionComparison{}, errors.Wrap(err, "cannot calculate composition diff with masks lifted")
	}

	anyChange := unmasked.DiffType != dt.DiffTypeEqual
	scope := compositionChangeScope(originalCompUnstructured, newCompUnstructured, anyChange)

	// One more local comparison, to find out whether the difference just detected is *only* kubectl's
	// last-applied-configuration annotation: same mask-lifted view, but with that one path ignored. If
	// the objects become equal, it was the sole delta, and the resulting revision's name is not
	// predictable — see compositionComparison.revisionNamePredictable. Ordering matters: the annotation
	// must stay in the comparison that decides `scope` (a real difference, and Crossplane hashes it) and
	// be excluded only from this narrower question about identity.
	revisionNamePredictable := true

	if anyChange {
		diffOptions.IgnorePaths = []string{compare.PathLastAppliedConfiguration}

		withoutLastApplied, err := compare.GenerateDiff(ctx, originalCompUnstructured, newCompUnstructured, p.config.Logger, diffOptions)
		if err != nil {
			return compositionComparison{}, errors.Wrap(err, "cannot calculate composition diff excluding last-applied-configuration")
		}

		revisionNamePredictable = withoutLastApplied.DiffType != dt.DiffTypeEqual

		// The annotation is the sole delta. If the cluster's copy of it already describes this exact
		// file, applying the file is a no-op under either apply mode, so it is not a change at all. See
		// lastAppliedDescribes.
		if !revisionNamePredictable && lastAppliedDescribes(originalCompUnstructured, newCompUnstructured) {
			scope = ChangeScopeNone
			revisionNamePredictable = true
		}
	}

	p.config.Logger.Debug("No displayable changes in composition",
		"composition", newComp.GetName(),
		"changeScope", string(scope),
		"revisionNamePredictable", revisionNamePredictable)

	return compositionComparison{diff: nil, scope: scope, revisionNamePredictable: revisionNamePredictable}, nil
}

// lastAppliedDescribes reports whether the cluster object's kubectl last-applied-configuration is
// semantically the proposed file, in which case applying the file writes nothing (issue #500).
//
// A client-side `kubectl apply` sets that annotation to the file minus the annotation, JSON-encoded
// (kubectl's GetModifiedConfiguration), so the value is a function of the file alone: if the live value
// already equals it, the three-way patch is empty and kubectl issues no request. Server-side apply leaves
// an annotation the file does not mention untouched. Either way nothing changes, so no
// CompositionRevision is created. Equal maps encode to equal JSON, so semantic equality suffices.
//
// Only a file that does not carry the annotation itself qualifies: one that does (say, a fetched object
// checked into git) would have it written verbatim under server-side apply, so its outcome again depends
// on the apply mode. kubectl keeps the emptied annotations map in what it encodes, so an empty
// annotations map is treated as absent on both sides. Neither object is mutated.
func lastAppliedDescribes(original, proposed *un.Unstructured) bool {
	if original == nil {
		return false
	}

	if _, ok := proposed.GetAnnotations()[corev1.LastAppliedConfigAnnotation]; ok {
		return false
	}

	live, ok := original.GetAnnotations()[corev1.LastAppliedConfigAnnotation]
	if !ok {
		return false
	}

	lastApplied := &un.Unstructured{}
	if err := lastApplied.UnmarshalJSON([]byte(live)); err != nil {
		// Not something kubectl wrote, so we cannot say what applying would do; keep the difference.
		return false
	}

	return equality.Semantic.DeepEqual(withoutLastApplied(lastApplied).Object, withoutLastApplied(proposed).Object)
}

// withoutLastApplied returns a copy of obj without kubectl's last-applied-configuration annotation,
// dropping the annotations map entirely if that leaves it empty.
func withoutLastApplied(obj *un.Unstructured) *un.Unstructured {
	out := obj.DeepCopy()

	annotations := out.GetAnnotations()
	delete(annotations, corev1.LastAppliedConfigAnnotation)

	if len(annotations) == 0 {
		un.RemoveNestedField(out.Object, "metadata", "annotations")
	} else {
		out.SetAnnotations(annotations)
	}

	return out
}

// callsChangedFunction reports whether any pipeline step references a function in changed.
func callsChangedFunction(steps []apiextensionsv1.PipelineStep, changed sets.Set[string]) bool {
	return slices.ContainsFunc(steps, func(s apiextensionsv1.PipelineStep) bool { return changed.Has(s.FunctionRef.Name) })
}

// compositionChangeScope classifies how much of a composition differs from its in-cluster version,
// in the terms Crossplane uses to decide whether a new CompositionRevision is needed. `anyChange`
// says whether the two differ at all with every display mask lifted; this function only has to
// decide whether the difference reaches the spec.
//
// A nil original means the composition does not exist in the cluster yet, which is a spec change by
// construction — there is no prior spec.
func compositionChangeScope(original, proposed *un.Unstructured, anyChange bool) ChangeScope {
	if original == nil {
		return ChangeScopeSpec
	}

	if !anyChange {
		return ChangeScopeNone
	}

	// NestedMap deep-copies, so the missing-spec case (nil) compares equal on both sides rather than
	// tripping on one being an empty map and the other nil.
	originalSpec, _, _ := un.NestedMap(original.Object, "spec")
	proposedSpec, _, _ := un.NestedMap(proposed.Object, "spec")

	if !equality.Semantic.DeepEqual(originalSpec, proposedSpec) {
		return ChangeScopeSpec
	}

	return ChangeScopeMetadata
}

// predictedRevision is the identity of the CompositionRevision that applying a composition would
// produce. Both fields derive from Crossplane's own Composition.Hash(), so neither is guessed.
type predictedRevision struct {
	// name is what the revision would be called: "<composition>-<hash[:7]>".
	name string
	// hash is the value of the crossplane.io/composition-hash label, i.e. the hash truncated to the
	// 63-character limit on label values.
	hash string
}

// revisionIdentity derives a revision's name and hash label from a composition's name and hash.
//
// This is the one and only thing this tool mirrors from upstream rather than calling: the derivation
// lives in NewCompositionRevision, in an internal/ package we cannot import, while the hash itself
// comes from the exported Composition.Hash(). The two truncations are different and both deliberate —
// 63 characters for the label value (Kubernetes' limit), 7 for the name suffix — and the >= guards
// are upstream's, kept verbatim so the degenerate hash Composition.Hash() returns on a marshal error
// ("unknown", shorter than either bound) produces the same result here as it would in the cluster.
func revisionIdentity(compName, hash string) predictedRevision {
	if len(hash) >= 63 {
		hash = hash[0:63]
	}

	nameSuffix := hash
	if len(nameSuffix) >= 7 {
		nameSuffix = nameSuffix[0:7]
	}

	return predictedRevision{
		name: fmt.Sprintf("%s-%s", compName, nameSuffix),
		hash: hash,
	}
}

// predictRevision computes the identity of the CompositionRevision applying comp would produce, by
// calling Crossplane's own exported Composition.Hash().
func predictRevision(comp *apiextensionsv1.Composition) predictedRevision {
	return revisionIdentity(comp.GetName(), comp.Hash())
}

// predictedRevisionLabels returns the label set the CompositionRevision resulting from this
// composition would carry, for evaluating an XR's compositionRevisionSelector. Crossplane stamps
// every revision with the composition's own metadata.labels plus crossplane.io/composition-name and
// crossplane.io/composition-hash (and appends them before matching a selector), so we mirror that
// here — see NewCompositionRevision. Omitting the hash label used to drop an XR whose selector keys on
// it as a selector mismatch, with a detail message that pointed the user at their own labels instead.
//
// The hash label is stamped even when compositionComparison.revisionNamePredictable is false, i.e. when
// the eventual hash depends on the user's apply mode. That is deliberate, and is why this takes the
// prediction rather than consulting predictability: withholding it would put us back to failing every
// selector that merely requires the label to Exist, which is the common shape and is now always answered
// correctly. A selector matching an exact hash *value* is the only case the caveat reaches, and there the
// best available prediction beats a guaranteed mismatch. newComp is not mutated.
func predictedRevisionLabels(newComp *un.Unstructured, pred predictedRevision) map[string]string {
	compLabels := newComp.GetLabels()

	labels := make(map[string]string, len(compLabels)+2)
	maps.Copy(labels, compLabels)

	labels[xp.LabelCompositionName] = newComp.GetName()
	labels[xp.LabelCompositionHash] = pred.hash

	return labels
}

// filteredXR pairs a dropped XR with the reason it was excluded from impact analysis and an
// optional human-readable detail (e.g. which selector failed to match which labels).
type filteredXR struct {
	xr     *un.Unstructured
	reason dt.FilterReason
	detail string
}

// partitionXRsByUpdatePolicy splits XRs into a kept set and a dropped set, based on whether each XR
// would adopt the CompositionRevision resulting from newComp. See classifyXR for the per-XR rules,
// which also cover exclusions unrelated to update policy (e.g. XRs being deleted). A malformed
// compositionRevisionSelector is a hard error (accuracy over guessing).
//
// `repointing` is the set of kept XRs (keyed by dt.MakeDiffKeyFromResource) that would genuinely
// re-point at the resulting revision, which is not the same as the kept set: --include-pinned keeps a Manual-policy XR for analysis even though it
// stays pinned to whatever compositionRevisionRef already names. See classifyXR and issue #479.
//
// `unresolved`, keyed the same way, holds the kept Manual claims whose backing XR could not be resolved
// to tell whether they are pinned (see xrDisposition.unresolved), each with its error: that claim's own
// result, not the run's.
func (p *DefaultCompDiffProcessor) partitionXRsByUpdatePolicy(ctx context.Context, xrs []*un.Unstructured, newComp *un.Unstructured, pred predictedRevision, netNew map[string]bool) (kept []*un.Unstructured, dropped []filteredXR, repointing map[string]bool, unresolved map[string]pinnedResolution, err error) {
	// The selector is matched against the label set the new revision would carry (composition labels
	// plus the stamped crossplane.io/composition-name and crossplane.io/composition-hash), while
	// mismatch messages display the user's own composition labels; see predictedRevisionLabels and
	// xp.XRRevisionSelectorMatch.
	targetLabels := predictedRevisionLabels(newComp, pred)
	repointing = make(map[string]bool, len(xrs))
	compLabels := newComp.GetLabels()
	xrKind, _, _ := un.NestedString(newComp.Object, "spec", "compositeTypeRef", "kind")

	for _, xr := range xrs {
		disposition, classifyErr := p.classifyXR(ctx, xr, xrKind, targetLabels, compLabels, netNew[dt.MakeDiffKeyFromResource(xr)])
		if classifyErr != nil {
			return nil, nil, nil, nil, classifyErr
		}

		if disposition.filtered != nil {
			dropped = append(dropped, *disposition.filtered)
			continue
		}

		kept = append(kept, xr)
		key := dt.MakeDiffKeyFromResource(xr)

		switch {
		case disposition.unresolved != nil:
			if unresolved == nil {
				unresolved = make(map[string]pinnedResolution)
			}

			unresolved[key] = pinnedResolution{err: disposition.unresolved}
		case !disposition.pinnedToRevision:
			repointing[key] = true
		}
	}

	return kept, dropped, repointing, unresolved, nil
}

// xrDisposition is what classifyXR decided about one XR.
type xrDisposition struct {
	// filtered is non-nil when the XR is dropped from impact analysis, carrying the reason.
	filtered *filteredXR

	// pinnedToRevision marks a *kept* XR that nonetheless would not adopt the CompositionRevision this
	// composition would produce, because a Manual compositionUpdatePolicy pins it to whatever
	// compositionRevisionRef already names. Only reachable under --include-pinned, which keeps such an
	// XR so its would-be rendered output can be inspected — without changing the fact that it does not
	// re-point. Counting it as re-pointing was issue #479.
	pinnedToRevision bool

	// unresolved is set, with nothing else, for a Manual claim naming no revision itself whose backing
	// XR could not be resolved to read the revision it is pinned to. Whether it is pinned is unknown, so
	// the claim is kept, not re-pointing, and reported as this error.
	unresolved error
}

// classifyXR decides whether a single XR should be kept for impact analysis or dropped (and why),
// based on whether it would adopt the CompositionRevision resulting from the diffed composition.
// xrKind is the diffed composition's compositeTypeRef kind: a composite of another kind is a claim.
// targetLabels is the predicted revision label set (used for selector matching); compLabels is the
// composition's own metadata.labels (used for the user-facing mismatch detail).
//
// Rules:
//   - Being deleted (metadata.deletionTimestamp set): dropped (reason deleting). Checked first,
//     and NOT overridden by IncludeManual, because deletion supersedes the policy rules entirely.
//   - Manual compositionUpdatePolicy with a compositionRevisionRef: dropped (reason manual_policy),
//     pinned to that revision, unless IncludeManual is set, in which case it is kept but marked
//     pinnedToRevision: opting into analysing it does not make it adopt the new revision. A claim
//     that names no revision itself is pinned to the one its backing XR names (see pinnedRevision).
//   - Manual policy with no compositionRevisionRef: kept, and re-points. Crossplane pins only a
//     composite that already names a revision; one that names none gets the latest revision, the
//     one the diffed composition produces, and is pinned to it from then on (crossplane v2.4.3
//     internal/controller/apiextensions/composite/api.go:163-189). Its compositionRevisionSelector
//     is ignored, as Crossplane ignores it under Manual (api.go:203).
//   - Automatic policy with a compositionRevisionSelector that does not match: dropped (reason
//     revision_selector_mismatch). NOT overridden by IncludeManual, since the XR genuinely would
//     not select the resulting revision.
//   - Automatic policy with no selector, or a matching selector: kept, and re-points.
//
// netNew marks a composite the cluster lacks, from --resource-file: Crossplane gives a new composite
// the latest revision whatever its policy, so a Manual one is classified as Automatic.
func (p *DefaultCompDiffProcessor) classifyXR(ctx context.Context, xr *un.Unstructured, xrKind string, targetLabels, compLabels map[string]string, netNew bool) (xrDisposition, error) {
	// An XR with a deletionTimestamp is on Crossplane's teardown path: the composite reconciler
	// deletes its composed resources instead of composing them. It will never adopt the revision
	// this composition would produce, and rendering it yields no composed resources to diff — so
	// including it produces a meaningless (or outright failing) impact analysis. See issue #452.
	//
	// The timestamp is normalized to UTC/RFC3339 for display: the accessor returns a metav1.Time whose
	// default rendering is machine-local, which would make the surfaced detail differ between runs on
	// differently-configured machines.
	if deletedAt := xr.GetDeletionTimestamp(); deletedAt != nil {
		stamp := deletedAt.UTC().Format(time.RFC3339)

		p.config.Logger.Debug("Excluding XR that is being deleted",
			"xr", xr.GetName(),
			"kind", xr.GetKind(),
			"deletionTimestamp", stamp)

		return xrDisposition{filtered: &filteredXR{
			xr:     xr,
			reason: dt.FilterReasonDeleting,
			detail: fmt.Sprintf("deletionTimestamp: %s", stamp),
		}}, nil
	}

	policy, err := xp.XRUpdatePolicy(xr.Object, xr.GetAPIVersion())
	if err != nil {
		return xrDisposition{}, errors.Wrapf(err, "cannot read compositionUpdatePolicy for XR %q", xr.GetName())
	}

	p.config.Logger.Debug("Classifying XR",
		"xr", xr.GetName(),
		"kind", xr.GetKind(),
		"policy", policy)

	// Manual policy with a revision: pinned to it; excluded unless the user opts in. Opting in keeps
	// it for analysis but does not unpin it, so it is marked as not re-pointing. With no revision it
	// adopts the resulting one, as an Automatic composite does, so it falls through.
	if policy == compositionUpdatePolicyManual && !netNew {
		revision, err := xp.XRCompositionRevisionRef(xr.Object, xr.GetAPIVersion())
		if err != nil {
			return xrDisposition{}, errors.Wrapf(err, "cannot read compositionRevisionRef for XR %q", xr.GetName())
		}

		if revision == "" && xr.GetKind() != xrKind {
			// The backing XR read is cluster I/O for this claim alone, so it runs under the claim's own
			// XRTimeout budget: a slow read fails this claim, not the run.
			xrCtx, cancel := p.config.xrContext(ctx)
			revision, err = p.pinnedRevision(xrCtx, xr)
			cancel()

			if err != nil {
				return xrDisposition{unresolved: err}, nil //nolint:nilerr // The claim's own error, not the run's.
			}
		}

		if revision != "" {
			if p.config.IncludeManual {
				return xrDisposition{pinnedToRevision: true}, nil
			}

			return xrDisposition{filtered: &filteredXR{xr: xr, reason: dt.FilterReasonManualPolicy, detail: "compositionRevisionRef: " + revision}}, nil
		}
	}

	// Automatic (or default) policy, or Manual with no revision: honor compositionRevisionSelector,
	// which only an Automatic policy has (see xp.XRRevisionSelectorMatch). An XR whose selector does not
	// match the predicted revision labels would not select the resulting revision, so it is excluded —
	// regardless of IncludeManual.
	matches, detail, err := xp.XRRevisionSelectorMatch(xr, targetLabels, compLabels)
	if err != nil {
		return xrDisposition{}, errors.Wrapf(err, "cannot evaluate compositionRevisionSelector for XR %q", xr.GetName())
	}

	if !matches {
		return xrDisposition{filtered: &filteredXR{xr: xr, reason: dt.FilterReasonRevisionSelectorMismatch, detail: detail}}, nil
	}

	return xrDisposition{}, nil
}

// pinnedRevision returns the compositionRevisionRef of claim's effective composite (see
// DiffProcessor.ResolveComposite): its backing XR with the claim synced in. A Manual claim usually names
// no revision itself, because Crossplane writes the pin onto the backing XR alone and copies a claim's
// ref onto it only when the claim sets one (crossplane v2.4.3 internal/controller/apiextensions/claim/
// syncer_ssa.go:147-152 and :211). Empty when the backing XR names none either.
func (p *DefaultCompDiffProcessor) pinnedRevision(ctx context.Context, claim *un.Unstructured) (string, error) {
	composite, err := p.xrProc.ResolveComposite(ctx, claim)
	if err != nil {
		return "", errors.Wrapf(err, "cannot resolve the backing XR of claim %q to read the revision it is pinned to", claim.GetName())
	}

	revision, err := xp.XRCompositionRevisionRef(composite.Object, composite.GetAPIVersion())

	return revision, errors.Wrapf(err, "cannot read compositionRevisionRef of the backing XR of claim %q", claim.GetName())
}

// filterCounts tallies dropped XRs by filter reason. A struct rather than a growing list of
// positional return values, so adding a reason cannot silently transpose a caller's arguments.
type filterCounts struct {
	byPolicy   int
	bySelector int
	byDeletion int
}

// countFilterReasons tallies dropped XRs by filter reason for the affected-resources summary.
func countFilterReasons(dropped []filteredXR) filterCounts {
	var counts filterCounts

	for _, d := range dropped {
		switch d.reason {
		case dt.FilterReasonManualPolicy:
			counts.byPolicy++
		case dt.FilterReasonRevisionSelectorMismatch:
			counts.bySelector++
		case dt.FilterReasonDeleting:
			counts.byDeletion++
		}
	}

	return counts
}

// applyTo writes the per-reason tallies onto a summary. Centralized so every code path that builds
// an AffectedResourcesSummary reports the same set of reasons.
func (c filterCounts) applyTo(summary *dt.AffectedResourcesSummary) {
	summary.FilteredByPolicy = c.byPolicy
	summary.FilteredBySelector = c.bySelector
	summary.FilteredByDeletion = c.byDeletion
}

// unresolvedImpacts builds the XRStatusError impacts, in xrs' order, of the composites in xrs that
// unresolved holds (see partitionXRsByUpdatePolicy), without rendering anything.
func (p *DefaultCompDiffProcessor) unresolvedImpacts(xrs []*un.Unstructured, unresolved map[string]pinnedResolution) ([]dt.XRImpact, dt.AffectedResourcesSummary) {
	var errored []*un.Unstructured

	results := make(map[string]*XRDiffResult, len(unresolved))

	for _, xr := range xrs {
		key := dt.MakeDiffKeyFromResource(xr)
		if u, ok := unresolved[key]; ok {
			errored = append(errored, xr)
			results[key] = &XRDiffResult{Error: u.err}
		}
	}

	return p.buildImpactAnalysis(errored, results)
}

// buildImpactAnalysis builds the impact analysis and summary from XR results.
func (p *DefaultCompDiffProcessor) buildImpactAnalysis(xrs []*un.Unstructured, results map[string]*XRDiffResult) ([]dt.XRImpact, dt.AffectedResourcesSummary) {
	impacts := make([]dt.XRImpact, 0, len(xrs))
	summary := dt.AffectedResourcesSummary{
		Total: len(xrs),
	}

	// Parents referenced by nested XRs are usually in the batch; index them
	// so ComposedBy can report the parent's own namespace rather than
	// assuming the child's (a cluster-scoped parent has none).
	byIdentity := make(map[string]*un.Unstructured, len(xrs))
	for _, xr := range xrs {
		byIdentity[xrIdentity(xr.GetAPIVersion(), xr.GetKind(), xr.GetName())] = xr
	}

	for _, xr := range xrs {
		resourceID := dt.MakeDiffKeyFromResource(xr)
		result := results[resourceID]

		impact := dt.XRImpact{ObjectReference: objectRef(xr), ComposedBy: parentCompositeRef(xr, byIdentity)}

		switch {
		case result != nil && result.HasError():
			impact.Status = dt.XRStatusError
			// NewOutputError surfaces typed validation failures via OutputError.ValidationFailures
			// when result.Error wraps a SchemaValidationError carrying a structured Result.
			impact.Error = new(NewOutputError(fmt.Sprintf("%s/%s", impact.Kind, impact.Name), result.Error))
			summary.WithErrors++
		case result != nil && result.HasChanges():
			impact.Status = dt.XRStatusChanged
			impact.Diffs = result.Diffs
			impact.Health = result.health
			summary.WithChanges++
		default:
			impact.Status = dt.XRStatusUnchanged
			// Renderers ignore an unchanged impact's diffs; they are carried only for a caller that kept
			// equal diffs to refine them.
			if result != nil && p.config.KeepEqualDiffs != nil {
				impact.Diffs = result.Diffs
			}
			if result != nil {
				impact.Health = result.health
			}
			summary.Unchanged++
		}

		impacts = append(impacts, impact)
	}

	return impacts, summary
}

// parentCompositeRef returns a reference to the parent composite of a nested XR,
// derived from its controller owner reference; nil for a top-level XR. Only
// an XR that carries the composite label is considered nested.
//
// An owner reference is either in the owner's namespace (which, for owner
// references, is the child's) or cluster-scoped, and carries no namespace of
// its own. When the parent is in byIdentity its own namespace is
// authoritative; otherwise the child's namespace is used, which is correct
// for a namespaced parent and spurious only for a cluster-scoped parent
// outside the processed set.
func parentCompositeRef(xr *un.Unstructured, byIdentity map[string]*un.Unstructured) *corev1.ObjectReference {
	if _, ok := xr.GetLabels()[LabelComposite]; !ok {
		return nil
	}
	for _, ref := range xr.GetOwnerReferences() {
		if ref.Controller == nil || !*ref.Controller {
			continue
		}
		namespace := xr.GetNamespace()
		if parent, ok := byIdentity[xrIdentity(ref.APIVersion, ref.Kind, ref.Name)]; ok {
			namespace = parent.GetNamespace()
		}
		return &corev1.ObjectReference{
			APIVersion: ref.APIVersion,
			Kind:       ref.Kind,
			Name:       ref.Name,
			Namespace:  namespace,
		}
	}
	return nil
}

// xrIdentity keys an XR by apiVersion, kind, and name.
func xrIdentity(apiVersion, kind, name string) string {
	return apiVersion + "|" + kind + "|" + name
}

// netNewKeys returns the keys of the supplied composites the cluster lacks.
func netNewKeys(supplied map[string]suppliedState) map[string]bool {
	netNew := make(map[string]bool, len(supplied))
	for key, state := range supplied {
		if !state.live && state.err == nil {
			netNew[key] = true
		}
	}
	return netNew
}

// countRepointed counts the re-pointing composites that already exist, since a composite that would
// be created adopts nothing.
func countRepointed(repointing, netNew map[string]bool) int {
	n := 0
	for key := range repointing {
		if !netNew[key] {
			n++
		}
	}
	return n
}

// resolvedComposition is a Composition with its affected composites, or the error discovering them.
type resolvedComposition struct {
	comp         *un.Unstructured
	affectedXRs  []*un.Unstructured
	discoveryErr error
}

// resolveCompositions resolves the affected composites of every Composition, skipping other kinds.
// In --resource mode the preflight matches are used; otherwise the cluster is queried.
func (p *DefaultCompDiffProcessor) resolveCompositions(ctx context.Context, compositions []*un.Unstructured, namespace string, resources []k8stypes.NamespacedName, preflightMatches map[string][]*un.Unstructured) []resolvedComposition {
	resolvedComps := make([]resolvedComposition, 0, len(compositions))

	for _, comp := range compositions {
		// Skip non-Composition objects (e.g., GoTemplate objects extracted from pipeline steps)
		if comp.GetKind() != "Composition" {
			p.config.Logger.Debug("Skipping non-Composition object", "kind", comp.GetKind(), "apiVersion", comp.GetAPIVersion())
			continue
		}

		compositionID := comp.GetName() // Use actual name from unstructured

		// Resolve the affected XR set up-front. In --resource mode the preflight already produced it;
		// in default-discovery mode we query the cluster (a net-new composition yields empty; any other failure fails the composition).
		var (
			affectedXRs  []*un.Unstructured
			discoveryErr error
		)

		switch {
		case len(resources) > 0:
			affectedXRs = preflightMatches[compositionID]
		default:
			// Default-discovery only needs comp.GetName() — pass the unstructured directly.
			// FindComposites converts to typed internally only in refs mode (which we're not in here).
			discovered, findErr := p.compositionClient.FindComposites(ctx, comp, dtypes.FindCompositesOptions{Namespace: namespace})

			switch {
			case apierrors.IsNotFound(findErr):
				// Net-new composition (won't exist in cluster) → graceful empty result. Only NotFound
				// means that: a Forbidden or a timeout must not read as "no impact".
				p.config.Logger.Debug("Cannot find composites using composition (net-new composition)",
					"composition", compositionID, "error", findErr)

				affectedXRs = nil
			case findErr != nil:
				discoveryErr = findErr
			default:
				affectedXRs = discovered
			}
		}

		resolvedComps = append(resolvedComps, resolvedComposition{comp: comp, affectedXRs: affectedXRs, discoveryErr: discoveryErr})
	}

	return resolvedComps
}
