//go:build integration

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

package composition_test

import (
	"strings"
	"testing"

	dp "github.com/crossplane/cli/v2/internal/diff/diffprocessor"
	tu "github.com/crossplane/cli/v2/internal/diff/testutils"
	"github.com/crossplane/cli/v2/internal/simulate/simtest"
	"github.com/crossplane/cli/v2/internal/testutils/integration"
	"github.com/crossplane/cli/v2/internal/testutils/integration/simharness"
)

// TestCompDiffIntegration runs an integration test for the composition diff command.
//
//nolint:tparallel // simharness.RunIntegrationTest calls t.Parallel in each subtest.
func TestCompDiffIntegration(t *testing.T) {
	t.Parallel()

	tests := map[string]simharness.IntegrationTestCase{
		"CompositionChangeImpactsXRs": {
			Reason: "Validates composition change impacts existing XRs",
			// Set up existing XRs that use the original composition
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/comp/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/original-composition.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
			},
			// Add existing XRs that use the composition
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"../../../internal/testutils/integration/testdata/comp/resources/existing-xr-1.yaml",
					"../../../internal/testutils/integration/testdata/comp/resources/existing-downstream-1.yaml",
				),
				integration.Composes(
					"../../../internal/testutils/integration/testdata/comp/resources/existing-xr-2.yaml",
					"../../../internal/testutils/integration/testdata/comp/resources/existing-downstream-2.yaml",
				),
			},
			// New composition files that will be diffed
			InputFiles: []string{"../../../internal/testutils/integration/testdata/comp/updated-composition.yaml"},
			Namespace:  "default",
			ExpectedOutput: `
=== Composition Changes ===

~~~ Composition/xnopresources.diff.example.org
  apiVersion: apiextensions.crossplane.io/v1
  kind: Composition
  metadata:
    name: xnopresources.diff.example.org
  spec:
    compositeTypeRef:
      apiVersion: ns.diff.example.org/v1alpha1
      kind: XNopResource
    mode: Pipeline
    pipeline:
    - functionRef:
        name: function-go-templating
      input:
        apiVersion: template.fn.crossplane.io/v1beta1
        inline:
          template: |
            apiVersion: ns.nop.example.org/v1alpha1
            kind: XDownstreamResource
            metadata:
              name: {{ .observed.composite.resource.metadata.name }}
              namespace: {{ .observed.composite.resource.metadata.namespace }}
              annotations:
                gotemplating.fn.crossplane.io/composition-resource-name: nop-resource
            spec:
              forProvider:
-               configData: {{ .observed.composite.resource.spec.coolField }}
-               resourceTier: basic
+               configData: updated-{{ .observed.composite.resource.spec.coolField }}
+               resourceTier: premium
        kind: GoTemplate
        source: Inline
      step: generate-resources
    - functionRef:
        name: function-auto-ready
      step: automatically-detect-ready-composed-resources

---

Summary: 1 modified

Applying this composition creates a new CompositionRevision, which 2 composites would adopt.

=== Affected Composite Resources ===

  ⚠ XNopResource/another-resource (namespace: default)
  ⚠ XNopResource/test-resource (namespace: default)

Summary: 2 resources with changes

=== Impact Analysis ===

~~~ XDownstreamResource/another-resource
  apiVersion: ns.nop.example.org/v1alpha1
  kind: XDownstreamResource
  metadata:
    annotations:
      crossplane.io/composition-resource-name: nop-resource
    labels:
      crossplane.io/composite: another-resource
    name: another-resource
    namespace: default
  spec:
    forProvider:
-     configData: another-existing-value
-     resourceTier: basic
+     configData: updated-another-existing-value
+     resourceTier: premium

---
~~~ XDownstreamResource/test-resource
  apiVersion: ns.nop.example.org/v1alpha1
  kind: XDownstreamResource
  metadata:
    annotations:
      crossplane.io/composition-resource-name: nop-resource
    labels:
      crossplane.io/composite: test-resource
    name: test-resource
    namespace: default
  spec:
    forProvider:
-     configData: existing-value
-     resourceTier: basic
+     configData: updated-existing-value
+     resourceTier: premium

---

Summary: 2 modified`,
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			NoColor:          true,
		},
		// Issue #452: an XR with a deletionTimestamp is on Crossplane's teardown path and will never
		// adopt the diffed composition's revision. Rendering it yields no composed resources (the
		// composite reconciler takes its deletion path), so including it produces a meaningless
		// impact analysis. It must be excluded and counted, leaving only the live XR evaluated.
		"CompositionDiffExcludesDeletingXRs": {
			Reason: "A deleting XR is excluded from impact analysis and counted as filteredByDeletion; the live XR is still evaluated",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/comp/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/original-composition.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				{
					OwnerFile: "../../../internal/testutils/integration/testdata/comp/resources/existing-xr-1.yaml",
					OwnedFiles: map[string]*integration.HierarchicalOwnershipRelation{
						"../../../internal/testutils/integration/testdata/comp/resources/existing-downstream-1.yaml": nil,
					},
				},
				{
					OwnerFile: "testdata/comp/resources/existing-xr-deleting.yaml",
					OwnedFiles: map[string]*integration.HierarchicalOwnershipRelation{
						"testdata/comp/resources/existing-downstream-deleting.yaml": nil,
					},
				},
			},
			DeleteAfterSetup: []string{"testdata/comp/resources/existing-xr-deleting.yaml"},
			InputFiles:       []string{"../../../internal/testutils/integration/testdata/comp/updated-composition.yaml"},
			Namespace:        "default",
			OutputFormat:     "json",
			ExpectedExitCode: dp.ExitCodeDiffDetected, // the composition itself changed
			// Total counts both discovered XRs; only the live one is evaluated (1 with changes), and
			// the deleting one is reported via filteredByDeletion. In default-discovery mode filtered
			// XRs are counted but not surfaced as impact entries, so only test-resource appears.
			ExpectedStructured: simtest.Expect().
				WithComposition("xnopresources.diff.example.org").
				WithCompositionModified().
				WithAffectedResources(2, 1, 0, 0).
				WithFilteredByDeletion(1).
				WithXRImpact("XNopResource", "test-resource", "default", "changed"),
		},
		// Issue #452, --resource mode: filtered XRs are surfaced explicitly there, so a user who names
		// a deleting composite is told why it was skipped rather than getting silence.
		"ResourceFilterSurfacesDeletingXR": {
			Reason: "--resource matching a deleting composite surfaces it as filtered with reason deleting",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/comp/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/original-composition.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				{
					OwnerFile: "testdata/comp/resources/existing-xr-deleting.yaml",
					OwnedFiles: map[string]*integration.HierarchicalOwnershipRelation{
						"testdata/comp/resources/existing-downstream-deleting.yaml": nil,
					},
				},
			},
			DeleteAfterSetup: []string{"testdata/comp/resources/existing-xr-deleting.yaml"},
			InputFiles:       []string{"../../../internal/testutils/integration/testdata/comp/updated-composition.yaml"},
			Resources:        []string{"default/deleting-resource"},
			OutputFormat:     "json",
			ExpectedExitCode: dp.ExitCodeDiffDetected, // the composition itself changed
			ExpectedStructured: simtest.Expect().
				WithComposition("xnopresources.diff.example.org").
				WithCompositionModified().
				WithFilteredByDeletion(1).
				WithXRImpact("XNopResource", "deleting-resource", "default", "filtered").
				WithFilterReason("deleting"),
		},
		// Issue #495, `comp` side: impact analysis renders each affected XR through the same observed-
		// resource fetch as `xr`, so a composed resource deleted out of band is tolerated with the same
		// warning. Being genuinely absent, it is then reported as something the XR will create.
		"CompositionDiffComposedResourceDeletedOutOfBandWarns": {
			Reason: "comp against an XR whose resourceRefs name a deleted composed resource warns and reports it as an addition",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/comp/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/original-composition.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				{
					OwnerFile: "../../../internal/testutils/integration/testdata/comp/resources/existing-xr-1.yaml",
					OwnedFiles: map[string]*integration.HierarchicalOwnershipRelation{
						"../../../internal/testutils/integration/testdata/comp/resources/existing-downstream-1.yaml": nil,
					},
				},
			},
			DeleteAfterSetup: []string{"../../../internal/testutils/integration/testdata/comp/resources/existing-downstream-1.yaml"},
			InputFiles:       []string{"../../../internal/testutils/integration/testdata/comp/updated-composition.yaml"},
			Namespace:        "default",
			OutputFormat:     "json",
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			ExpectedStructured: simtest.Expect().
				WithWarning("was not found in the cluster").
				WithWarningContext(map[string]string{
					"resource":  "XDownstreamResource/test-resource",
					"namespace": "default",
				}).
				And().
				WithComposition("xnopresources.diff.example.org").
				WithCompositionModified().
				WithAffectedResources(1, 1, 0, 0).
				WithXRImpact("XNopResource", "test-resource", "default", "changed").
				WithDownstreamSummary(1, 0, 0).
				WithDownstreamResource("added", "XDownstreamResource", "test-resource", "default"),
		},
		// The comp side of TestDiffIntegration/ResourceRemovalSkipsResourcesTheXRDoesNotControl (#552):
		// the updated composition stops rendering resource2, which the XR controls, so it is removed;
		// the XR's resourceRefs also name a resource another XR controls and one with no controller,
		// which the composition doesn't render either, and neither of those is removed.
		"CompositionDropsResourceRemovingOnlyWhatTheXRControls": {
			Reason: "A composition that stops rendering resources removes only those the XR controls",
			// What this case pins: test-resource's resourceRefs still name two resources it doesn't control.
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/removal-test-original-composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/composition.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
				// Deliberately not in crossplaneManagedResources: its controller reference was removed, so
				// no XR controls it, though test-resource's resourceRefs still name it.
				"../../../internal/testutils/integration/testdata/diff/resources/removal-test-ns-downstream-without-controller.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/comp/resources/existing-xr-listing-uncontrolled-resources.yaml",
					"../../../internal/testutils/integration/testdata/diff/resources/removal-test-ns-downstream-resource1.yaml", // still rendered: kept
					"../../../internal/testutils/integration/testdata/diff/resources/removal-test-ns-downstream-resource2.yaml", // no longer rendered: removed
				),
				integration.Composes(
					"../../../internal/testutils/integration/testdata/diff/resources/existing-other-xr.yaml",
					"../../../internal/testutils/integration/testdata/diff/resources/existing-other-xr-downstream.yaml",
				),
			},
			InputFiles:       []string{"../../../internal/testutils/integration/testdata/diff/resources/removal-test-composition.yaml"},
			Namespace:        "default",
			OutputFormat:     "json",
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			ExpectedStructured: simtest.Expect().
				WithComposition("removal-test-composition").
				WithCompositionModified().
				WithAffectedResources(1, 1, 0, 0).
				WithXRImpact("XNopResource", "test-resource", "default", "changed").
				WithDownstreamSummary(0, 0, 1).
				WithDownstreamResource("removed", "XDownstreamResource", "resource-to-be-removed", "default"),
		},
		"CompositionDiffIgnorePaths": {
			Reason: "Validates that ArgoCD annotations are ignored in composition diffs",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/comp/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/original-composition.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
			},
			// Add existing XR with ArgoCD annotations
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/comp/resources/existing-xr-with-argocd.yaml",
					"testdata/comp/resources/existing-downstream-with-argocd.yaml",
				),
			},
			InputFiles: []string{"testdata/comp/composition-no-changes.yaml"},
			Namespace:  "default",
			// The composition is unchanged, so impact analysis would be skipped by default (issue
			// #453). This case is about XR/downstream-level ignore paths, so it opts back in.
			AnalyzeUnchanged: true,
			IgnorePaths: []string{
				"metadata.annotations[argocd.argoproj.io/tracking-id]",
				"metadata.labels[argocd.argoproj.io/instance]",
			},
			ExpectedOutput: `
=== Composition Changes ===

No changes detected in composition xnopresources.diff.example.org

=== Affected Composite Resources ===

  ✓ XNopResource/test-resource (namespace: default)

Summary: 1 resource unchanged

=== Impact Analysis ===

All composite resources are up-to-date. No downstream resource changes detected.

`,
			ExpectedError: false,
			NoColor:       true,
		},
		// Issue #453: any revision an unchanged composition produced would carry the same spec, so no XR
		// renders differently and the affected-XR / impact-analysis sections are replaced by a skip note.
		//
		// These fixtures are deliberately ones that DO produce a downstream delta when evaluated (see
		// UnchangedCompositionAnalyzeUnchangedEvaluatesXRs, which asserts exactly that against the same
		// setup). So this case proves two things at once: the skip suppresses a report that would
		// otherwise be emitted, and the exit code is 0 rather than the 3 that report would have caused
		// — a delta not attributable to any composition change must not gate a CI pipeline.
		"UnchangedCompositionSkipsImpactAnalysis": {
			Reason: "An unchanged composition skips impact analysis entirely and says so",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/comp/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/original-composition.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"../../../internal/testutils/integration/testdata/comp/resources/existing-xr-1.yaml",
					"../../../internal/testutils/integration/testdata/comp/resources/existing-downstream-1.yaml",
				),
			},
			InputFiles: []string{"testdata/comp/composition-no-changes.yaml"},
			Namespace:  "default",
			ExpectedOutput: `
=== Composition Changes ===

No changes detected in composition xnopresources.diff.example.org

Impact analysis skipped: this composition is identical to the cluster's, so applying it creates no new CompositionRevision and no composite resource could change as a result. Pass --analyze-on=always to evaluate them anyway.

`,
			ExpectedError: false,
			// No composition change and no evaluated XRs, so nothing was detected.
			ExpectedExitCode: dp.ExitCodeSuccess,
			NoColor:          true,
		},
		// Issue #467: the cluster composition was applied with a client-side `kubectl apply`, so it
		// carries kubectl.kubernetes.io/last-applied-configuration; the file being diffed never does.
		// That annotation is suppressed from the rendered diff — it is a multi-KB serialization of the
		// object, useless to show — but Crossplane's Composition.Hash() covers annotations, so applying
		// this DOES create a new CompositionRevision that composites re-point to, and whose name a
		// template can read off the XR's compositionRevisionRef. A suppression made for readability
		// must not decide that away, so the composites are evaluated. Contrast the sibling above, which
		// is byte-identical and needs --analyze-unchanged to evaluate anything.
		//
		// This runs the real CLI wiring, which the unit coverage in
		// TestDefaultCompDiffProcessor_calculateCompositionDiff cannot reach: it pins that
		// defaultProcessorOptions does not fold the annotation into --ignore-paths, and that the
		// display-only suppression does not leak into the change verdict.
		//
		// The evaluated composite renders unchanged, because the new revision's spec is identical, so
		// nothing renders differently and the exit code is 0 (as with AnalyzeOnSpecChangeSkipsMetadataOnlyChange).
		// What proves evaluation is that the composite is counted, rather than impact analysis being skipped.
		"CompositionAppliedWithKubectlEvaluatesXRs": {
			Reason: "A composition differing only in kubectl's last-applied-configuration still counts as changed, because applying it creates a new CompositionRevision",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/original-composition-kubectl-applied.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/comp/resources/existing-xr-1-kubectl-applied.yaml",
					"../../../internal/testutils/integration/testdata/comp/resources/existing-downstream-1.yaml",
				),
			},
			InputFiles:       []string{"testdata/comp/composition-no-changes.yaml"},
			Namespace:        "default",
			OutputFormat:     "json",
			ExpectedExitCode: dp.ExitCodeSuccess,
			ExpectedStderrContains: []string{
				// Issue #474: the composites are still evaluated, but they are rendered with their existing
				// compositionRevisionRef, because the name of the revision this would create cannot be
				// predicted for a client-side-applied composition. Saying so is the honest position: if this
				// composition's template read the revision name, the resulting change would go undetected.
				"Could not predict the name of the CompositionRevision",
			},
			ExpectedStructured: simtest.Expect().
				// The same advisory reaches warnings[] for machine consumers, keyed by composition so that two
				// compositions declining to predict stay two warnings rather than being merged as duplicates.
				WithWarning("Could not predict the name of the CompositionRevision").
				WithWarningContext(map[string]string{"composition": "xnopresources.diff.example.org"}).
				And().
				WithComposition("xnopresources.diff.example.org").
				// createsRevision stays true while the name is withheld: the annotation delta is real, and a
				// stale annotation genuinely would be rewritten and mint a revision. Only the identity is
				// unknowable, which is why the guard makes the weak claim rather than "no revision".
				WithRevisionImpact("metadata", true, 1).
				WithoutPredictedRevisionName().
				WithAffectedResources(1, 0, 1, 0).
				WithXRImpact("XNopResource", "test-resource", "default", "unchanged"),
		},
		// Issue #500: the same asymmetry, but the cluster's last-applied-configuration is the one a
		// client-side `kubectl apply` of this very file would write. Re-applying it computes the same
		// annotation, the patch is empty and kubectl writes nothing, so no CompositionRevision is created.
		// The sibling above differs only in that its annotation is stale, which a re-apply does rewrite.
		"CompositionReappliedWithKubectlCreatesNoRevision": {
			Reason: "A composition whose live last-applied-configuration already describes the file is unchanged, because re-applying it writes nothing",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/original-composition-kubectl-applied-current.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/comp/resources/existing-xr-1-kubectl-applied-current.yaml",
					"../../../internal/testutils/integration/testdata/comp/resources/existing-downstream-1.yaml",
				),
			},
			InputFiles:       []string{"testdata/comp/composition-no-changes.yaml"},
			Namespace:        "default",
			OutputFormat:     "json",
			ExpectedExitCode: dp.ExitCodeSuccess,
			ExpectedStructured: simtest.Expect().
				WithNoWarnings().
				WithComposition("xnopresources.diff.example.org").
				WithImpactAnalysisSkipped().
				WithRevisionImpact("none", false, 0),
		},
		// Issue #474: the case that makes "creates a revision but renders identically" a claim to be
		// verified rather than assumed. The composition edit is metadata-only — one added label, identical
		// spec — so the CompositionRevision it mints carries a byte-identical spec. But the template reads
		// the revision's own name off the composite and propagates it into the composed resource, so the
		// rendered output genuinely changes.
		//
		// Before the fix, the composite was rendered with its EXISTING compositionRevisionRef: configData
		// came out as the old revision's name, matched the cluster, and the tool reported unchanged with
		// exit 0. Seeding the predicted ref is what turns that into the exit 3 below. This is the
		// regression test for the whole feature — stop comp's composition provider resolving the predicted
		// revision, or stop the processor writing the resolved revision to the ref, and this is what fails.
		//
		// Note also what is NOT in the output: the composite's own
		// spec.crossplane.compositionRevisionRef diff. The predicted ref is written only to the render input,
		// never to the composite's dry-run payload, which is why the exit code reflects rendering rather than
		// bookkeeping; predictedRevisionName is what keeps the downstream change interpretable without it.
		"RevisionNamePropagatesToComposedResource": {
			Reason: "A metadata-only composition edit whose template reads the CompositionRevision name changes rendered output, and is detected rather than assumed away",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/comp/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/revision-templating-composition.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/revision-templating-composition-revision.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"../../../internal/testutils/integration/testdata/comp/resources/existing-xr-revision-ref.yaml",
					"../../../internal/testutils/integration/testdata/comp/resources/existing-downstream-revision-ref.yaml",
				),
			},
			InputFiles:       []string{"testdata/comp/revision-templating-updated-composition.yaml"},
			Namespace:        "default",
			OutputFormat:     "json",
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			ExpectedStructured: simtest.Expect().
				// The name was predictable, so the could-not-predict advisory must stay silent.
				WithNoWarnings().
				WithComposition("xrevisionrefs.diff.example.org").
				WithRevisionImpact("metadata", true, 1).
				// Matched by pattern, not literally: the suffix is the first 7 hex digits of the
				// composition's hash, so pinning it would make any edit to the fixture a test failure. The
				// derivation is pinned exactly by TestRevisionIdentity, which is where that belongs.
				WithPredictedRevisionNamePattern(`^xrevisionrefs\.diff\.example\.org-[0-9a-f]{7}$`).
				WithAffectedResources(1, 1, 0, 0).
				WithXRImpact("XNopResource", "revision-ref-resource", "default", "changed").
				WithDownstreamSummary(0, 1, 0).
				WithDownstreamResource("modified", "XDownstreamResource", "revision-ref-resource", "default").
				// The old value is the revision the composite currently tracks; the new one is the predicted
				// revision, i.e. the thing this whole feature exists to surface. Both are real revision names,
				// so both match the pattern above: only the exact values tell them apart.
				WithFieldChange("spec.forProvider.configData",
					"xrevisionrefs.diff.example.org-e36719b", "xrevisionrefs.diff.example.org-ec5496c"),
		},
		// Issue #536 / #479: the same edit with --include-manual, which keeps a Manual composite for analysis
		// without making it adopt the new revision. Only the Automatic composite is rendered with the
		// predicted revision name; the Manual one stays pinned, so its template-read name, and with it its
		// composed resource, is unchanged. Giving every composite the name would report the pinned one as
		// changing to a revision it never uses.
		"RevisionNameReachesOnlyRepointingCompositesWithIncludeManual": {
			Reason: "Under --include-manual, the predicted revision name reaches an Automatic composite but not a Manual one pinned to an older revision",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/comp/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/revision-templating-composition.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/revision-templating-composition-revision.yaml",
				"testdata/comp/resources/revision-templating-composition-older-revision.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"../../../internal/testutils/integration/testdata/comp/resources/existing-xr-revision-ref.yaml",
					"../../../internal/testutils/integration/testdata/comp/resources/existing-downstream-revision-ref.yaml",
				),
				integration.Composes(
					"testdata/comp/resources/existing-xr-pinned-revision-ref.yaml",
					"testdata/comp/resources/existing-downstream-pinned-revision-ref.yaml",
				),
			},
			InputFiles:       []string{"testdata/comp/revision-templating-updated-composition.yaml"},
			Namespace:        "default",
			IncludeManual:    true,
			OutputFormat:     "json",
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			ExpectedStructured: simtest.Expect().
				WithNoWarnings().
				WithComposition("xrevisionrefs.diff.example.org").
				// One composite re-points; the Manual one is kept but adopts nothing.
				WithRevisionImpact("metadata", true, 1).
				WithPredictedRevisionNamePattern(`^xrevisionrefs\.diff\.example\.org-[0-9a-f]{7}$`).
				WithAffectedResources(2, 1, 1, 0).
				WithXRImpact("XNopResource", "pinned-revision-ref-resource", "default", "unchanged").
				WithDownstreamSummary(0, 0, 0).
				AndComp().
				WithXRImpact("XNopResource", "revision-ref-resource", "default", "changed").
				WithDownstreamSummary(0, 1, 0).
				WithDownstreamResource("modified", "XDownstreamResource", "revision-ref-resource", "default").
				WithFieldChange("spec.forProvider.configData",
					"xrevisionrefs.diff.example.org-e36719b", "xrevisionrefs.diff.example.org-ec5496c"),
		},
		// Issue #498, the claim variant of RevisionNamePropagatesToComposedResource. A claim is rendered from its backing XR with the
		// claim's spec synced in. Under Automatic the claim's own ref is never propagated (the XR controller
		// owns the backing XR's), so seeding the claim would reach nothing: comp's composition provider is
		// handed the backing XR the render consumes, and resolves that to the predicted revision. Without it,
		// the backing XR's stale ref renders and the change goes unreported.
		"ClaimRevisionNamePropagatesToComposedResource": {
			Reason: "A metadata-only composition edit is rendered for an Automatic claim with the revision its backing XR would re-point to",
			SetupFiles: []string{
				"testdata/comp/resources/claim-xrd.yaml",
				"testdata/comp/resources/claim-revision-templating-composition.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
				"testdata/comp/resources/test-namespace.yaml",
				"testdata/comp/resources/existing-claim-revision-ref.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/comp/resources/existing-claim-revision-ref-xr.yaml",
					"testdata/comp/resources/existing-claim-revision-ref-downstream.yaml",
				),
			},
			XrdAPIVersion:    integration.XRDV1, // the XRD is v1, so Crossplane writes the XR's resourceRefs at spec.resourceRefs
			InputFiles:       []string{"testdata/comp/claim-revision-templating-updated-composition.yaml"},
			Namespace:        "test-namespace",
			OutputFormat:     "json",
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			ExpectedStructured: simtest.Expect().
				// The claim's composed resource is labelled with its backing XR's name, which is the claim's
				// owner, so no "already belongs to another composite" advisory is raised (#534).
				WithNoWarnings().
				WithComposition("xrevisionrefclaims.diff.example.org").
				WithRevisionImpact("metadata", true, 1).
				WithPredictedRevisionNamePattern(`^xrevisionrefclaims\.diff\.example\.org-[0-9a-f]{7}$`).
				WithAffectedResources(1, 1, 0, 0).
				WithXRImpact("NopClaim", "revision-ref-claim", "test-namespace", "changed").
				WithDownstreamSummary(0, 1, 0).
				WithDownstreamResource("modified", "XDownstreamResource", "revision-ref-claim-xr", "").
				// The old value is the revision the backing XR tracks now; the new one is the predicted name.
				WithFieldChange("spec.forProvider.configData",
					"xrevisionrefclaims.diff.example.org-98b484d", "xrevisionrefclaims.diff.example.org-55c3a3b"),
		},
		// Issue #498, the other half: when nothing is seeded, an Automatic claim renders against the
		// revision its backing XR tracks now, not against no revision at all. Nothing is seeded here
		// because the composition differs from the cluster's only by a stale kubectl annotation, so the
		// name of the revision that would mint is unknowable (see CompositionAppliedWithKubectlEvaluatesXRs);
		// the claim is still rendered, so the backing XR's ref is what the template reads.
		"ClaimRendersWithBackingXRRevisionWhenNothingIsSeeded": {
			Reason: "An Automatic claim with nothing seeded renders with its backing XR's compositionRevisionRef, so a template reading the revision name shows no spurious change",
			SetupFiles: []string{
				"testdata/comp/resources/claim-xrd.yaml",
				"testdata/comp/resources/claim-revision-templating-composition-kubectl-applied.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
				"testdata/comp/resources/test-namespace.yaml",
				"testdata/comp/resources/existing-claim-revision-ref-kubectl-applied.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/comp/resources/existing-claim-revision-ref-kubectl-applied-xr.yaml",
					"testdata/comp/resources/existing-claim-revision-ref-kubectl-applied-downstream.yaml",
				),
			},
			XrdAPIVersion:          integration.XRDV1, // the XRD is v1, so Crossplane writes the XR's resourceRefs at spec.resourceRefs
			InputFiles:             []string{"testdata/comp/resources/claim-revision-templating-composition.yaml"},
			Namespace:              "test-namespace",
			OutputFormat:           "json",
			ExpectedExitCode:       dp.ExitCodeSuccess,
			ExpectedStderrContains: []string{"Could not predict the name of the CompositionRevision"},
			ExpectedStructured: simtest.Expect().
				// The could-not-predict advisory is the only warning: the claim's composed resource is labelled
				// with its backing XR's name, so it raises no ownership advisory (#534).
				WithWarning("Could not predict the name of the CompositionRevision").
				WithWarningContext(map[string]string{"composition": "xrevisionrefclaims.diff.example.org"}).
				And().
				WithComposition("xrevisionrefclaims.diff.example.org").
				WithRevisionImpact("metadata", true, 1).
				WithoutPredictedRevisionName().
				WithAffectedResources(1, 0, 1, 0).
				WithXRImpact("NopClaim", "revision-ref-claim", "test-namespace", "unchanged").
				WithDownstreamSummary(0, 0, 0),
		},
		// Issue #472: the same metadata-only change, with the user opting out of paying a render per
		// composite for it. The composites go unevaluated — but the mutative consequence is still
		// reported, which is what keeps --analyze-on a cost knob rather than a correctness mode. So the
		// skip is recorded, and revisionImpact says a CompositionRevision is created and one composite
		// would adopt it. Exit code 0: nothing renders differently, and a GitOps loop re-applying the
		// same manifests must not fail its gate forever.
		"AnalyzeOnSpecChangeSkipsMetadataOnlyChange": {
			Reason: "--analyze-on=spec-change leaves a metadata-only change unevaluated, while still reporting that it creates a CompositionRevision",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/original-composition-kubectl-applied.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/comp/resources/existing-xr-1-kubectl-applied.yaml",
					"../../../internal/testutils/integration/testdata/comp/resources/existing-downstream-1.yaml",
				),
			},
			InputFiles:       []string{"testdata/comp/composition-no-changes.yaml"},
			Namespace:        "default",
			AnalyzeOn:        "spec-change",
			OutputFormat:     "json",
			ExpectedExitCode: dp.ExitCodeSuccess,
			ExpectedStructured: simtest.Expect().
				WithComposition("xnopresources.diff.example.org").
				WithImpactAnalysisSkipped().
				WithRevisionImpact("metadata", true, 1).
				// Issue #474: no predicted name here, and that absence is the point. The compositions
				// differ by nothing but kubectl's last-applied-configuration, whose post-apply value is a
				// function of how the user applies rather than of the file — so the hash, and therefore the
				// revision's name, is unknowable. createsRevision stays true; only the identity is withheld.
				WithoutPredictedRevisionName(),
		},
		// The same setup with --analyze-unchanged evaluates the XRs after all (the pre-edit
		// convergence-baseline workflow). Here the composed resource has drifted out of band, so the
		// evaluation reports a downstream modification even though the composition is byte-identical
		// to the cluster's. That delta is not caused by this composition — it is exactly the class of
		// finding the default skip keeps out of the "impact of your composition change" report, and why
		// opting in is explicit.
		"UnchangedCompositionAnalyzeUnchangedEvaluatesXRs": {
			Reason: "--analyze-unchanged evaluates affected XRs even though the composition is unchanged, surfacing deltas not caused by it",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/comp/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/original-composition.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"../../../internal/testutils/integration/testdata/comp/resources/existing-xr-1.yaml",
					"testdata/comp/resources/existing-downstream-drifted.yaml",
				),
			},
			InputFiles:       []string{"testdata/comp/composition-no-changes.yaml"},
			Namespace:        "default",
			AnalyzeUnchanged: true,
			OutputFormat:     "json",
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			ExpectedStructured: simtest.Expect().
				WithComposition("xnopresources.diff.example.org").
				WithAffectedResources(1, 1, 0, 0).
				WithXRImpact("XNopResource", "test-resource", "default", "changed").
				WithDownstreamSummary(0, 1, 0).
				WithDownstreamResource("modified", "XDownstreamResource", "test-resource", "default").
				WithFieldChange("spec.forProvider.configData", "drifted-value", "existing-value"),
		},
		"CompositionDiffCustomNamespace": {
			Reason: "Validates composition diff with custom namespace",
			// Set up existing XRs in both custom and default namespaces to validate filtering
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/comp/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/original-composition.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
				// Create the custom namespace first
				"testdata/comp/resources/custom-namespace.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				// Add existing XRs in custom namespace (should be included in output)
				integration.Composes(
					"testdata/comp/resources/existing-custom-ns-xr.yaml",
					"testdata/comp/resources/existing-custom-ns-downstream.yaml",
				),
				// Add existing XRs in default namespace (should be filtered out)
				integration.Composes(
					"../../../internal/testutils/integration/testdata/comp/resources/existing-xr-1.yaml",
					"../../../internal/testutils/integration/testdata/comp/resources/existing-downstream-1.yaml",
				),
			},
			// New composition files that will be diffed
			InputFiles: []string{"../../../internal/testutils/integration/testdata/comp/updated-composition.yaml"},
			Namespace:  "custom-namespace",
			// Expected output should only show custom-namespace resources, NOT default namespace resources
			// This validates that namespace filtering works correctly
			ExpectedOutput: `
=== Composition Changes ===

~~~ Composition/xnopresources.diff.example.org
  apiVersion: apiextensions.crossplane.io/v1
  kind: Composition
  metadata:
    name: xnopresources.diff.example.org
  spec:
    compositeTypeRef:
      apiVersion: ns.diff.example.org/v1alpha1
      kind: XNopResource
    mode: Pipeline
    pipeline:
    - functionRef:
        name: function-go-templating
      input:
        apiVersion: template.fn.crossplane.io/v1beta1
        inline:
          template: |
            apiVersion: ns.nop.example.org/v1alpha1
            kind: XDownstreamResource
            metadata:
              name: {{ .observed.composite.resource.metadata.name }}
              namespace: {{ .observed.composite.resource.metadata.namespace }}
              annotations:
                gotemplating.fn.crossplane.io/composition-resource-name: nop-resource
            spec:
              forProvider:
-               configData: {{ .observed.composite.resource.spec.coolField }}
-               resourceTier: basic
+               configData: updated-{{ .observed.composite.resource.spec.coolField }}
+               resourceTier: premium
        kind: GoTemplate
        source: Inline
      step: generate-resources
    - functionRef:
        name: function-auto-ready
      step: automatically-detect-ready-composed-resources

---

Summary: 1 modified

Applying this composition creates a new CompositionRevision, which 1 composite would adopt.

=== Affected Composite Resources ===

  ⚠ XNopResource/custom-namespace-resource (namespace: custom-namespace)

Summary: 1 resource with changes

=== Impact Analysis ===

~~~ XDownstreamResource/custom-namespace-resource
  apiVersion: ns.nop.example.org/v1alpha1
  kind: XDownstreamResource
  metadata:
    annotations:
      crossplane.io/composition-resource-name: nop-resource
    labels:
      crossplane.io/composite: custom-namespace-resource
    name: custom-namespace-resource
    namespace: custom-namespace
  spec:
    forProvider:
-     configData: custom-ns-existing-value
-     resourceTier: basic
+     configData: updated-custom-ns-existing-value
+     resourceTier: premium

---

Summary: 1 modified`,
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			NoColor:          true,
		},
		// Two compositions that BOTH genuinely change, each with its own affected XR. This is the
		// aggregation case: one CompositionDiff entry per input, each carrying its own impact analysis,
		// with hasDiffs ORed across them. MultipleCompositionDiffImpact below covers the mixed
		// changed/unchanged variant; before this case existed, nothing covered two compositions that
		// both report impacts — the second composition there has no affected XR at all.
		"MultipleChangedCompositionsBothReportImpact": {
			Reason: "Two changed compositions each report their own affected XR's downstream changes",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/comp/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/original-composition.yaml",
				"testdata/comp/resources/original-composition-2.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				// XR using composition 1
				integration.Composes(
					"../../../internal/testutils/integration/testdata/comp/resources/existing-xr-1.yaml",
					"../../../internal/testutils/integration/testdata/comp/resources/existing-downstream-1.yaml",
				),
				// XR using composition 2
				integration.Composes(
					"testdata/comp/resources/existing-xr-v2.yaml",
					"testdata/comp/resources/existing-downstream-v2.yaml",
				),
			},
			InputFiles: []string{
				"../../../internal/testutils/integration/testdata/comp/updated-composition.yaml",
				"testdata/comp/updated-composition-2-changed.yaml",
			},
			Namespace:        "default",
			OutputFormat:     "json",
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			ExpectedStructured: simtest.Expect().
				WithComposition("xnopresources.diff.example.org").
				WithCompositionModified().
				WithAffectedResources(1, 1, 0, 0).
				WithXRImpact("XNopResource", "test-resource", "default", "changed").
				WithDownstreamSummary(0, 1, 0).
				WithDownstreamResource("modified", "XDownstreamResource", "test-resource", "default").
				WithFieldChange("spec.forProvider.configData", "existing-value", "updated-existing-value").
				WithFieldChange("spec.forProvider.resourceTier", "basic", "premium").
				AndComposition("xnopresources-v2.diff.example.org").
				WithCompositionModified().
				WithAffectedResources(1, 1, 0, 0).
				WithXRImpact("XNopResource", "v2-resource", "default", "changed").
				WithDownstreamSummary(0, 1, 0).
				WithDownstreamResource("modified", "XDownstreamResource", "v2-resource", "default").
				WithFieldChange("spec.forProvider.configData", "v2-updated-v2-existing-value", "v2-changed-v2-existing-value").
				WithFieldChange("spec.forProvider.resourceTier", "enterprise", "premium-plus"),
		},
		// Note the second input composition (xnopresources-v2.diff.example.org) is byte-identical to its
		// in-cluster version, so this case doubles as the mixed changed/unchanged scenario: the changed
		// composition reports its impact while the unchanged one is skipped per-composition (issue
		// #453). It is not a broken fixture.
		"MultipleCompositionDiffImpact": {
			Reason: "Validates multiple compositions in one invocation: the changed one reports impact on existing XRs, the unchanged one is skipped",
			// Set up existing XRs that use both compositions
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/comp/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/original-composition.yaml",
				"testdata/comp/resources/original-composition-2.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
			},
			// Add existing XRs that use the compositions
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"../../../internal/testutils/integration/testdata/comp/resources/existing-xr-1.yaml",
					"../../../internal/testutils/integration/testdata/comp/resources/existing-downstream-1.yaml",
				),
				integration.Composes(
					"../../../internal/testutils/integration/testdata/comp/resources/existing-xr-2.yaml",
					"../../../internal/testutils/integration/testdata/comp/resources/existing-downstream-2.yaml",
				),
			},
			// Multiple composition files that will be diffed
			InputFiles: []string{
				"../../../internal/testutils/integration/testdata/comp/updated-composition.yaml",
				"testdata/comp/updated-composition-2.yaml",
			},
			Namespace: "default",
			ExpectedOutput: `
=== Composition Changes ===

~~~ Composition/xnopresources.diff.example.org
  apiVersion: apiextensions.crossplane.io/v1
  kind: Composition
  metadata:
    name: xnopresources.diff.example.org
  spec:
    compositeTypeRef:
      apiVersion: ns.diff.example.org/v1alpha1
      kind: XNopResource
    mode: Pipeline
    pipeline:
    - functionRef:
        name: function-go-templating
      input:
        apiVersion: template.fn.crossplane.io/v1beta1
        inline:
          template: |
            apiVersion: ns.nop.example.org/v1alpha1
            kind: XDownstreamResource
            metadata:
              name: {{ .observed.composite.resource.metadata.name }}
              namespace: {{ .observed.composite.resource.metadata.namespace }}
              annotations:
                gotemplating.fn.crossplane.io/composition-resource-name: nop-resource
            spec:
              forProvider:
-               configData: {{ .observed.composite.resource.spec.coolField }}
-               resourceTier: basic
+               configData: updated-{{ .observed.composite.resource.spec.coolField }}
+               resourceTier: premium
        kind: GoTemplate
        source: Inline
      step: generate-resources
    - functionRef:
        name: function-auto-ready
      step: automatically-detect-ready-composed-resources

---

Summary: 1 modified

Applying this composition creates a new CompositionRevision, which 2 composites would adopt.

=== Affected Composite Resources ===

  ⚠ XNopResource/another-resource (namespace: default)
  ⚠ XNopResource/test-resource (namespace: default)

Summary: 2 resources with changes

=== Impact Analysis ===

~~~ XDownstreamResource/another-resource
  apiVersion: ns.nop.example.org/v1alpha1
  kind: XDownstreamResource
  metadata:
    annotations:
      crossplane.io/composition-resource-name: nop-resource
    labels:
      crossplane.io/composite: another-resource
    name: another-resource
    namespace: default
  spec:
    forProvider:
-     configData: another-existing-value
-     resourceTier: basic
+     configData: updated-another-existing-value
+     resourceTier: premium

---
~~~ XDownstreamResource/test-resource
  apiVersion: ns.nop.example.org/v1alpha1
  kind: XDownstreamResource
  metadata:
    annotations:
      crossplane.io/composition-resource-name: nop-resource
    labels:
      crossplane.io/composite: test-resource
    name: test-resource
    namespace: default
  spec:
    forProvider:
-     configData: existing-value
-     resourceTier: basic
+     configData: updated-existing-value
+     resourceTier: premium

---

Summary: 2 modified

================================================================================

=== Composition Changes ===

No changes detected in composition xnopresources-v2.diff.example.org

Impact analysis skipped: this composition is identical to the cluster's, so applying it creates no new CompositionRevision and no composite resource could change as a result. Pass --analyze-on=always to evaluate them anyway.
`,
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			NoColor:          true,
		},
		"CompositionDiffFiltersManualXRs": {
			Reason: "Validates composition diff filters Manual policy XRs by default",
			// Set up existing XRs - one with Automatic policy and one with Manual policy
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/comp/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/original-composition.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				// Add existing XR with Automatic policy (should be included)
				integration.Composes(
					"../../../internal/testutils/integration/testdata/comp/resources/existing-xr-1.yaml",
					"../../../internal/testutils/integration/testdata/comp/resources/existing-downstream-1.yaml",
				),
				// Add existing XR with Manual policy (should be filtered out by default)
				integration.Composes(
					"../../../internal/testutils/integration/testdata/comp/resources/existing-xr-manual.yaml",
					"../../../internal/testutils/integration/testdata/comp/resources/existing-downstream-manual.yaml",
				),
			},
			// Updated composition
			InputFiles: []string{"../../../internal/testutils/integration/testdata/comp/updated-composition.yaml"},
			Namespace:  "default",
			// Expected output should only show test-resource (Automatic), NOT manual-resource (Manual)
			ExpectedOutput: `
=== Composition Changes ===

~~~ Composition/xnopresources.diff.example.org
  apiVersion: apiextensions.crossplane.io/v1
  kind: Composition
  metadata:
    name: xnopresources.diff.example.org
  spec:
    compositeTypeRef:
      apiVersion: ns.diff.example.org/v1alpha1
      kind: XNopResource
    mode: Pipeline
    pipeline:
    - functionRef:
        name: function-go-templating
      input:
        apiVersion: template.fn.crossplane.io/v1beta1
        inline:
          template: |
            apiVersion: ns.nop.example.org/v1alpha1
            kind: XDownstreamResource
            metadata:
              name: {{ .observed.composite.resource.metadata.name }}
              namespace: {{ .observed.composite.resource.metadata.namespace }}
              annotations:
                gotemplating.fn.crossplane.io/composition-resource-name: nop-resource
            spec:
              forProvider:
-               configData: {{ .observed.composite.resource.spec.coolField }}
-               resourceTier: basic
+               configData: updated-{{ .observed.composite.resource.spec.coolField }}
+               resourceTier: premium
        kind: GoTemplate
        source: Inline
      step: generate-resources
    - functionRef:
        name: function-auto-ready
      step: automatically-detect-ready-composed-resources

---

Summary: 1 modified

Applying this composition creates a new CompositionRevision, which 1 composite would adopt.

=== Affected Composite Resources ===

  ⚠ XNopResource/test-resource (namespace: default)

Summary: 1 resource with changes

=== Impact Analysis ===

~~~ XDownstreamResource/test-resource
  apiVersion: ns.nop.example.org/v1alpha1
  kind: XDownstreamResource
  metadata:
    annotations:
      crossplane.io/composition-resource-name: nop-resource
    labels:
      crossplane.io/composite: test-resource
    name: test-resource
    namespace: default
  spec:
    forProvider:
-     configData: existing-value
-     resourceTier: basic
+     configData: updated-existing-value
+     resourceTier: premium

---

Summary: 1 modified`,
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			NoColor:          true,
		},
		"CompositionUpgradesResourceAPIVersion": {
			// NOTE: This test validates that resources are correctly matched across API version changes,
			// avoiding delete/recreate. The composition template changes from v1beta1 to v1beta2, but
			// Kubernetes automatically converts resources between served API versions. When we query for
			// the v1beta2 resource, Kubernetes finds the v1beta1 resource and returns it auto-converted
			// to v1beta2. From Kubernetes' perspective, the resource exists as both versions simultaneously,
			// so there's no apiVersion field change to show in the diff. The important thing is that the
			// resource is matched, preventing delete/recreate operations: since the stored object is
			// identical at either version it renders unchanged, where an unmatched one would show as a
			// +++ addition and a --- removal. The composition diff itself WILL show the template change
			// from v1beta1 to v1beta2.
			//
			// As in TestDiffIntegration/CompositionRevisionUpgradesResourceAPIVersion, the XR composes the
			// resource as Crossplane leaves it, named in its resourceRefs at v1beta1, so this also checks
			// that it is not reported as removed (#552).
			Reason: "Validates composition upgrade that changes resource API version shows as update not remove/add",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/comp/resources/xrd.yaml",
				// NOTE: xapimigrate CRD is auto-loaded from testdata/comp/crds/
				// We apply no XRD for XApiMigrateResource because it is a regular managed resource
				// (not a composite), and an XRD would make it composite, causing infinite recursion.
				"testdata/comp/resources/api-version-original-composition.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				{
					OwnerFile: "testdata/comp/resources/existing-api-version-xr.yaml",
					OwnedFiles: map[string]*integration.HierarchicalOwnershipRelation{
						"testdata/comp/resources/existing-api-version-downstream-v1beta1.yaml": nil,
					},
				},
			},
			InputFiles: []string{"testdata/comp/api-version-updated-composition.yaml"},
			Namespace:  "default",
			ExpectedOutput: `
=== Composition Changes ===

~~~ Composition/xapimigrateresources.example.org
  apiVersion: apiextensions.crossplane.io/v1
  kind: Composition
  metadata:
    name: xapimigrateresources.example.org
  spec:
    compositeTypeRef:
      apiVersion: ns.diff.example.org/v1alpha1
      kind: XNopResource
    mode: Pipeline
    pipeline:
    - functionRef:
        name: function-go-templating
      input:
        apiVersion: template.fn.crossplane.io/v1beta1
        inline:
          template: |
-           apiVersion: comp.example.org/v1beta1
+           apiVersion: comp.example.org/v1beta2
            kind: XApiMigrateResource
            metadata:
              name: {{ .observed.composite.resource.metadata.name }}-api-resource
              namespace: {{ .observed.composite.resource.metadata.namespace }}
              annotations:
                gotemplating.fn.crossplane.io/composition-resource-name: api-migrate-resource
            spec:
              forProvider:
                configData: {{ .observed.composite.resource.spec.coolField }}
        kind: GoTemplate
        source: Inline
      step: generate-api-versioned-resources
    - functionRef:
        name: function-auto-ready
      step: automatically-detect-ready-composed-resources

---

Summary: 1 modified

Applying this composition creates a new CompositionRevision, which 1 composite would adopt.

=== Affected Composite Resources ===

  ✓ XNopResource/test-api-version (namespace: default)

Summary: 1 resource unchanged

=== Impact Analysis ===

All composite resources are up-to-date. No downstream resource changes detected.
`,
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			NoColor:          true,
		},
		"NetNewCompositionNoImpact": {
			Reason: "Validates net-new composition with no downstream impact",
			// Set up cluster with existing resources but no composition that matches the new one
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/comp/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/original-composition.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
			},
			// Add existing XRs that use different compositions (won't be affected)
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"../../../internal/testutils/integration/testdata/comp/resources/existing-xr-1.yaml",
					"../../../internal/testutils/integration/testdata/comp/resources/existing-downstream-1.yaml",
				),
			},
			// Net-new composition file that doesn't exist in cluster and targets different XR type
			InputFiles: []string{"testdata/comp/net-new-composition.yaml"},
			Namespace:  "default",
			ExpectedOutput: `
=== Composition Changes ===

+++ Composition/xnewresources.diff.example.org
+ apiVersion: apiextensions.crossplane.io/v1
+ kind: Composition
+ metadata:
+   labels:
+     environment: staging
+     provider: aws
+   name: xnewresources.diff.example.org
+ spec:
+   compositeTypeRef:
+     apiVersion: staging.diff.example.org/v1alpha1
+     kind: XNewResource
+   mode: Pipeline
+   pipeline:
+   - functionRef:
+       name: function-go-templating
+     input:
+       apiVersion: template.fn.crossplane.io/v1beta1
+       inline:
+         template: |
+           apiVersion: staging.nop.example.org/v1alpha1
+           kind: XStagingResource
+           metadata:
+             name: {{ .observed.composite.resource.metadata.name }}
+             namespace: {{ .observed.composite.resource.metadata.namespace }}
+             annotations:
+               gotemplating.fn.crossplane.io/composition-resource-name: staging-resource
+           spec:
+             forProvider:
+               environment: staging
+               configData: new-{{ .observed.composite.resource.spec.newField }}
+               resourceTier: premium
+       kind: GoTemplate
+       source: Inline
+     step: generate-new-resources
+   - functionRef:
+       name: function-auto-ready
+     step: automatically-detect-ready-composed-resources

---

Summary: 1 added

No XRs found using composition xnewresources.diff.example.org`,
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			NoColor:          true,
		},
		"CompositionChangeWithUnchangedDownstreamResources": {
			Reason: "Validates status indicators for unchanged downstream resources",
			// Set up existing XRs that use the original composition
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/status-indicator-composition.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
			},
			// Add existing XRs that use the composition
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/comp/resources/status-xr-1.yaml",
					"testdata/comp/resources/status-downstream-1.yaml",
				),
				integration.Composes(
					"testdata/comp/resources/status-xr-2.yaml",
					"testdata/comp/resources/status-downstream-2.yaml",
				),
			},
			// Updated composition with only metadata change (no downstream impact)
			InputFiles: []string{"testdata/comp/status-indicator-updated-composition.yaml"},
			Namespace:  "default",
			ExpectedOutput: `
=== Composition Changes ===

~~~ Composition/xstatus.diff.example.org
  apiVersion: apiextensions.crossplane.io/v1
  kind: Composition
  metadata:
+   labels:
+     environment: production
    name: xstatus.diff.example.org
  spec:
    compositeTypeRef:
      apiVersion: ns.diff.example.org/v1alpha1
      kind: XNopResource
    mode: Pipeline
    pipeline:
    - functionRef:
        name: function-go-templating
      input:
        apiVersion: template.fn.crossplane.io/v1beta1
        inline:
          template: |
            apiVersion: ns.nop.example.org/v1alpha1
            kind: XDownstreamResource
            metadata:
              name: {{ .observed.composite.resource.metadata.name }}
              namespace: {{ .observed.composite.resource.metadata.namespace }}
              annotations:
                gotemplating.fn.crossplane.io/composition-resource-name: nop-resource
            spec:
              forProvider:
                configData: stable-value
                resourceTier: standard
        kind: GoTemplate
        source: Inline
      step: generate-resources
    - functionRef:
        name: function-auto-ready
      step: automatically-detect-ready-composed-resources

---

Summary: 1 modified

Applying this composition creates a new CompositionRevision, which 2 composites would adopt.

=== Affected Composite Resources ===

  ✓ XNopResource/status-test-xr-1 (namespace: default)
  ✓ XNopResource/status-test-xr-2 (namespace: default)

Summary: 2 resources unchanged

=== Impact Analysis ===

All composite resources are up-to-date. No downstream resource changes detected.`,
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			NoColor:          true,
		},
		"CompositionChangeWithMixedStatusAndColors": {
			Reason: "Validates status indicators with colorization for mixed changed/unchanged XRs",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/mixed-status-composition.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				// XR 1 with downstream resources (standard tier - will change)
				integration.Composes(
					"testdata/comp/resources/mixed-xr-1.yaml",
					"testdata/comp/resources/mixed-downstream-db-1.yaml",
					"testdata/comp/resources/mixed-downstream-storage-1.yaml",
					"testdata/comp/resources/mixed-downstream-network-1.yaml",
				),
				// XR 2 with downstream resources (standard tier - will change)
				integration.Composes(
					"testdata/comp/resources/mixed-xr-2.yaml",
					"testdata/comp/resources/mixed-downstream-db-2.yaml",
					"testdata/comp/resources/mixed-downstream-storage-2.yaml",
					"testdata/comp/resources/mixed-downstream-network-2.yaml",
				),
				// XR 3 with downstream resources (already premium tier - no change)
				integration.Composes(
					"testdata/comp/resources/mixed-xr-3.yaml",
					"testdata/comp/resources/mixed-downstream-db-3.yaml",
					"testdata/comp/resources/mixed-downstream-storage-3.yaml",
					"testdata/comp/resources/mixed-downstream-network-3.yaml",
				),
			},
			InputFiles: []string{"testdata/comp/mixed-status-updated-composition.yaml"},
			Namespace:  "default",
			ExpectedOutput: strings.Join([]string{
				`
=== Composition Changes ===

~~~ Composition/xmixed.diff.example.org
  apiVersion: apiextensions.crossplane.io/v1
  kind: Composition
  metadata:
    name: xmixed.diff.example.org
  spec:
    compositeTypeRef:
      apiVersion: ns.diff.example.org/v1alpha1
      kind: XNopResource
    mode: Pipeline
    pipeline:
    - functionRef:
        name: function-go-templating
      input:
        apiVersion: template.fn.crossplane.io/v1beta1
        inline:
          template: |
            ---
            apiVersion: ns.nop.example.org/v1alpha1
            kind: XDownstreamResource
            metadata:
              name: {{ .observed.composite.resource.metadata.name }}-database
              namespace: {{ .observed.composite.resource.metadata.namespace }}
              annotations:
                gotemplating.fn.crossplane.io/composition-resource-name: database
            spec:
              forProvider:
                resourceType: database
`, tu.Red(`-               tier: standard`), `
`, tu.Green(`+               tier: premium`), `
`, tu.Green(`+               backupEnabled: true`), `
            ---
            apiVersion: ns.nop.example.org/v1alpha1
            kind: XDownstreamResource
            metadata:
              name: {{ .observed.composite.resource.metadata.name }}-storage
              namespace: {{ .observed.composite.resource.metadata.namespace }}
              annotations:
                gotemplating.fn.crossplane.io/composition-resource-name: storage
            spec:
              forProvider:
                resourceType: storage
                tier: standard
            ---
            apiVersion: ns.nop.example.org/v1alpha1
            kind: XDownstreamResource
            metadata:
              name: {{ .observed.composite.resource.metadata.name }}-network
              namespace: {{ .observed.composite.resource.metadata.namespace }}
              annotations:
                gotemplating.fn.crossplane.io/composition-resource-name: network
            spec:
              forProvider:
                resourceType: network
                tier: standard
        kind: GoTemplate
        source: Inline
      step: generate-resources
    - functionRef:
        name: function-auto-ready
      step: automatically-detect-ready-composed-resources

---

Summary: 1 modified

Applying this composition creates a new CompositionRevision, which 3 composites would adopt.

=== Affected Composite Resources ===

`, tu.Yellow(`  ⚠ XNopResource/mixed-test-xr-1 (namespace: default)`), `
`, tu.Yellow(`  ⚠ XNopResource/mixed-test-xr-2 (namespace: default)`), `
`, tu.Green(`  ✓ XNopResource/mixed-test-xr-3 (namespace: default)`), `

Summary: 2 resources with changes, 1 resource unchanged

=== Impact Analysis ===

~~~ XDownstreamResource/mixed-test-xr-1-database
  apiVersion: ns.nop.example.org/v1alpha1
  kind: XDownstreamResource
  metadata:
    annotations:
      crossplane.io/composition-resource-name: database
    labels:
      crossplane.io/composite: mixed-test-xr-1
    name: mixed-test-xr-1-database
    namespace: default
  spec:
    forProvider:
`, tu.Red(`-     resourceType: database`), `
`, tu.Red(`-     tier: standard`), `
`, tu.Green(`+     backupEnabled: true`), `
`, tu.Green(`+     resourceType: database`), `
`, tu.Green(`+     tier: premium`), `
  
  

---
~~~ XDownstreamResource/mixed-test-xr-2-database
  apiVersion: ns.nop.example.org/v1alpha1
  kind: XDownstreamResource
  metadata:
    annotations:
      crossplane.io/composition-resource-name: database
    labels:
      crossplane.io/composite: mixed-test-xr-2
    name: mixed-test-xr-2-database
    namespace: default
  spec:
    forProvider:
`, tu.Red(`-     resourceType: database`), `
`, tu.Red(`-     tier: standard`), `
`, tu.Green(`+     backupEnabled: true`), `
`, tu.Green(`+     resourceType: database`), `
`, tu.Green(`+     tier: premium`), `
  
  

---

Summary: 2 modified
`,
			}, ""),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			NoColor:          false,
		},
		"CompositionChangeImpactsClaims": {
			Reason: "Validates composition change impacts existing Claims (issue #120)",
			// Set up existing Claims and their XRs that use the original composition
			SetupFiles: []string{
				// XRD, composition, and functions
				"testdata/comp/resources/claim-xrd.yaml",
				"testdata/comp/resources/claim-composition.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
				// Test namespace
				"testdata/comp/resources/test-namespace.yaml",
				// Existing Claims and their corresponding XRs
				"testdata/comp/resources/existing-claim-1.yaml",
				"testdata/comp/resources/existing-claim-2.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/comp/resources/existing-claim-1-xr.yaml",
					"testdata/comp/resources/existing-claim-downstream-1.yaml",
				),
				integration.Composes(
					"testdata/comp/resources/existing-claim-2-xr.yaml",
					"testdata/comp/resources/existing-claim-downstream-2.yaml",
				),
			},
			XrdAPIVersion: integration.XRDV1, // the XRD is v1, so Crossplane writes the XR's resourceRefs at spec.resourceRefs
			// Updated composition that will be diffed
			InputFiles: []string{"testdata/comp/updated-claim-composition.yaml"},
			Namespace:  "test-namespace",
			ExpectedOutput: `
=== Composition Changes ===

~~~ Composition/nopclaims.diff.example.org
  apiVersion: apiextensions.crossplane.io/v1
  kind: Composition
  metadata:
    name: nopclaims.diff.example.org
  spec:
    compositeTypeRef:
      apiVersion: diff.example.org/v1alpha1
      kind: XNop
    mode: Pipeline
    pipeline:
    - functionRef:
        name: function-go-templating
      input:
        apiVersion: template.fn.crossplane.io/v1beta1
        inline:
          template: |
            apiVersion: nop.example.org/v1alpha1
            kind: XDownstreamResource
            metadata:
              annotations:
                gotemplating.fn.crossplane.io/composition-resource-name: nop-resource
              name: {{ .observed.composite.resource.metadata.name }}
            spec:
              forProvider:
-               configData: {{ .observed.composite.resource.spec.coolField }}
-               resourceTier: basic
+               configData: updated-{{ .observed.composite.resource.spec.coolField }}
+               resourceTier: premium
        kind: GoTemplate
        source: Inline
      step: generate-resources
    - functionRef:
        name: function-auto-ready
      step: automatically-detect-ready-composed-resources

---

Summary: 1 modified

Applying this composition creates a new CompositionRevision, which 2 composites would adopt.

=== Affected Composite Resources ===

  ⚠ NopClaim/test-claim-1 (namespace: test-namespace)
  ⚠ NopClaim/test-claim-2 (namespace: test-namespace)

Summary: 2 resources with changes

=== Impact Analysis ===

~~~ XDownstreamResource/test-claim-1-xr
  apiVersion: nop.example.org/v1alpha1
  kind: XDownstreamResource
  metadata:
    annotations:
      crossplane.io/composition-resource-name: nop-resource
    labels:
      crossplane.io/claim-name: test-claim-1
      crossplane.io/claim-namespace: test-namespace
      crossplane.io/composite: test-claim-1-xr
    name: test-claim-1-xr
  spec:
    forProvider:
-     configData: claim-value-1
-     resourceTier: basic
+     configData: updated-claim-value-1
+     resourceTier: premium

---
~~~ XDownstreamResource/test-claim-2-xr
  apiVersion: nop.example.org/v1alpha1
  kind: XDownstreamResource
  metadata:
    annotations:
      crossplane.io/composition-resource-name: nop-resource
    labels:
      crossplane.io/claim-name: test-claim-2
      crossplane.io/claim-namespace: test-namespace
      crossplane.io/composite: test-claim-2-xr
    name: test-claim-2-xr
  spec:
    forProvider:
-     configData: claim-value-2
-     resourceTier: basic
+     configData: updated-claim-value-2
+     resourceTier: premium

---

Summary: 2 modified`,
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			NoColor:          true,
		},
		// Without -n every namespace is listed, so discovery finds each claim and its cluster-scoped
		// backing XR. The claim renders through that XR, so the XR is dropped: each change is reported
		// once, under the claim.
		"CompositionChangeImpactsClaimsAllNamespaces": {
			Reason: "A claim's backing XR discovered alongside it is not reported a second time",
			SetupFiles: []string{
				"testdata/comp/resources/claim-xrd.yaml",
				"testdata/comp/resources/claim-composition.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
				"testdata/comp/resources/test-namespace.yaml",
				"testdata/comp/resources/existing-claim-1.yaml",
				"testdata/comp/resources/existing-claim-2.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/comp/resources/existing-claim-1-xr.yaml",
					"testdata/comp/resources/existing-claim-downstream-1.yaml",
				),
				integration.Composes(
					"testdata/comp/resources/existing-claim-2-xr.yaml",
					"testdata/comp/resources/existing-claim-downstream-2.yaml",
				),
			},
			XrdAPIVersion:    integration.XRDV1,
			InputFiles:       []string{"testdata/comp/updated-claim-composition.yaml"},
			OutputFormat:     "json",
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			ExpectedStructured: simtest.Expect().
				WithComposition("nopclaims.diff.example.org").
				WithCompositionModified().
				WithAffectedResources(2, 2, 0, 0).
				WithXRImpact("NopClaim", "test-claim-1", "test-namespace", "changed").AndComp().
				WithXRImpact("NopClaim", "test-claim-2", "test-namespace", "changed"),
		},
		// Composition mode checks the composites it renders for overlaps as xr mode checks its inputs:
		// two XRs on one composition that render one fixed-name child differently would both control it.
		// Contention is checked before differing renderings, so it is the error the run fails with.
		"SharedChildDifferingRendersFail": {
			Reason: "Two XRs a composition change renders one child for, differently, fail the run",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/shared-child-composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
				"testdata/comp/resources/shared-child-xrs-differing.yaml",
			},
			InputFiles:       []string{"testdata/comp/updated-shared-child-composition.yaml"},
			Namespace:        "default",
			ExpectedError:    true,
			ExpectedExitCode: dp.ExitCodeToolError,
			ExpectedErrorContains: `resource "ns.nop.example.org/v1alpha1/XDownstreamResource/default/shared-child" would be ` +
				`controlled by more than one XR (XNopResource/shared-child-owner-a, XNopResource/shared-child-owner-b)`,
		},
		"SharedChildContendedEvenWhenIdentical": {
			Reason: "Two XRs a composition change renders one child for, identically, still contend for it",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/shared-child-composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
				"../../../internal/testutils/integration/testdata/diff/shared-child-xrs.yaml",
			},
			InputFiles:       []string{"testdata/comp/updated-shared-child-composition.yaml"},
			Namespace:        "default",
			ExpectedError:    true,
			ExpectedExitCode: dp.ExitCodeToolError,
			ExpectedErrorContains: `resource "ns.nop.example.org/v1alpha1/XDownstreamResource/default/shared-child" would be ` +
				`controlled by more than one XR (XNopResource/shared-child-owner-a, XNopResource/shared-child-owner-b)`,
		},
		"SSAFieldRemovalDetection": {
			Reason: "Validates that field removals are correctly detected when resources are created via Server-Side Apply with Crossplane's field manager (issue #121)",
			// Set up XRD, functions, and original composition with the removable field
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/comp/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
				"testdata/comp/resources/field-removal/composition-with-field.yaml",
			},
			// XR and downstream resource applied via SSA with Crossplane field manager
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				{
					OwnerFile: "testdata/comp/resources/field-removal/existing-xr.yaml",
					OwnedFiles: map[string]*integration.HierarchicalOwnershipRelation{
						"testdata/comp/resources/field-removal/existing-downstream.yaml": nil,
					},
				},
			},
			// Updated composition that removes the removableField
			InputFiles: []string{"testdata/comp/field-removal-updated-composition.yaml"},
			Namespace:  "default",
			ExpectedOutput: `
=== Composition Changes ===

~~~ Composition/xfieldremoval.diff.example.org
  apiVersion: apiextensions.crossplane.io/v1
  kind: Composition
  metadata:
    name: xfieldremoval.diff.example.org
  spec:
    compositeTypeRef:
      apiVersion: ns.diff.example.org/v1alpha1
      kind: XNopResource
    mode: Pipeline
    pipeline:
    - functionRef:
        name: function-go-templating
      input:
        apiVersion: template.fn.crossplane.io/v1beta1
        inline:
          template: |
            apiVersion: ns.nop.example.org/v1alpha1
            kind: XDownstreamResource
            metadata:
              name: {{ .observed.composite.resource.metadata.name }}
              namespace: {{ .observed.composite.resource.metadata.namespace }}
              annotations:
                gotemplating.fn.crossplane.io/composition-resource-name: nop-resource
            spec:
              forProvider:
                configData: {{ .observed.composite.resource.spec.coolField }}
-               removableField: will-be-removed
        kind: GoTemplate
        source: Inline
      step: generate-resources
    - functionRef:
        name: function-auto-ready
      step: automatically-detect-ready-composed-resources

---

Summary: 1 modified

Applying this composition creates a new CompositionRevision, which 1 composite would adopt.

=== Affected Composite Resources ===

  ⚠ XNopResource/field-removal-test (namespace: default)

Summary: 1 resource with changes

=== Impact Analysis ===

~~~ XDownstreamResource/field-removal-test
  apiVersion: ns.nop.example.org/v1alpha1
  kind: XDownstreamResource
  metadata:
    annotations:
      crossplane.io/composition-resource-name: nop-resource
    labels:
      crossplane.io/composite: field-removal-test
    name: field-removal-test
    namespace: default
  spec:
    forProvider:
      configData: test-value
-     removableField: will-be-removed

---

Summary: 1 modified`,
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			NoColor:          true,
		},
		"SHA256DigestFunctionReference": {
			Reason: "Validates composition diff works with functions referenced by SHA256 digest instead of tag",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/sha256-composition.yaml",
				"testdata/comp/resources/functions-sha256.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/comp/resources/existing-sha256-xr.yaml",
					"testdata/comp/resources/existing-sha256-downstream.yaml",
				),
			},
			InputFiles: []string{"testdata/comp/updated-sha256-composition.yaml"},
			Namespace:  "default",
			ExpectedOutput: `
=== Composition Changes ===

~~~ Composition/xnopresources-sha256.diff.example.org
  apiVersion: apiextensions.crossplane.io/v1
  kind: Composition
  metadata:
    name: xnopresources-sha256.diff.example.org
  spec:
    compositeTypeRef:
      apiVersion: ns.diff.example.org/v1alpha1
      kind: XNopResource
    mode: Pipeline
    pipeline:
    - functionRef:
        name: function-go-templating-sha256
      input:
        apiVersion: template.fn.crossplane.io/v1beta1
        inline:
          template: |
            apiVersion: ns.nop.example.org/v1alpha1
            kind: XDownstreamResource
            metadata:
              name: {{ .observed.composite.resource.metadata.name }}
              namespace: {{ .observed.composite.resource.metadata.namespace }}
              annotations:
                gotemplating.fn.crossplane.io/composition-resource-name: nop-resource
            spec:
              forProvider:
-               configData: {{ .observed.composite.resource.spec.coolField }}
-               resourceTier: basic
+               configData: updated-{{ .observed.composite.resource.spec.coolField }}
+               resourceTier: premium
        kind: GoTemplate
        source: Inline
      step: generate-resources
    - functionRef:
        name: function-auto-ready
      step: automatically-detect-ready-composed-resources

---

Summary: 1 modified

Applying this composition creates a new CompositionRevision, which 1 composite would adopt.

=== Affected Composite Resources ===

  ⚠ XNopResource/sha256-test-resource (namespace: default)

Summary: 1 resource with changes

=== Impact Analysis ===

~~~ XDownstreamResource/sha256-test-resource
  apiVersion: ns.nop.example.org/v1alpha1
  kind: XDownstreamResource
  metadata:
    annotations:
      crossplane.io/composition-resource-name: nop-resource
    labels:
      crossplane.io/composite: sha256-test-resource
    name: sha256-test-resource
    namespace: default
  spec:
    forProvider:
-     configData: sha256-value
-     resourceTier: basic
+     configData: updated-sha256-value
+     resourceTier: premium

---

Summary: 1 modified`,
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			NoColor:          true,
		},
		// Issue #485, comp's half. comp re-renders each discovered composite through DiffSingleResource,
		// the same recursion xr uses, so one fix covers both — this pins that for comp rather than
		// assuming it. comp's own error only counts the failures, so the assertion that matters is that
		// the depth message reaches the failing composite's impact entry: without it, a user would be
		// told something failed but not that the composition recurses into itself.
		"CyclicCompositionStopsAtMaxNestedDepth": {
			Reason:       "comp shares xr's recursion path (it enters via DiffSingleResource), so a composition cycle must stop at --max-nested-depth here too, and say so on the failing composite (#485)",
			OutputFormat: "json",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/cycle/xrds.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/cycle/compositions.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
				// Composed nothing: with a cyclic composition, Crossplane would nest XRs without end, so
				// the case models the XR before its first compose rather than an endless tree.
				"testdata/comp/resources/cycle/existing-cycle-xr.yaml",
			},
			InputFiles:            []string{"testdata/comp/updated-cycle-composition.yaml"},
			Namespace:             "default",
			ExpectedError:         true,
			ExpectedErrorContains: "impact analysis failed for 1 XR(s)",
			ExpectedExitCode:      dp.ExitCodeToolError,
			ExpectedStructured: simtest.Expect().
				WithComposition("xcycleas.cycle.example.org").
				WithAffectedResources(1, 0, 0, 1).
				WithXRImpact("XCycleA", "test-cycle", "default", "error").
				WithErrorContaining("maximum nesting depth exceeded"),
		},
		// The same failure, asserting the composite's error is recorded against it in the top-level errors[].
		"CyclicCompositionStopsAtMaxNestedDepthV2": {
			Reason:       "A failing composite's error is a structured errors[] entry recorded against it",
			OutputFormat: "json",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/cycle/xrds.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/cycle/compositions.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
				// Composed nothing, as in CyclicCompositionStopsAtMaxNestedDepth.
				"testdata/comp/resources/cycle/existing-cycle-xr.yaml",
			},
			InputFiles:            []string{"testdata/comp/updated-cycle-composition.yaml"},
			Namespace:             "default",
			ExpectedError:         true,
			ExpectedErrorContains: "impact analysis failed for 1 XR(s)",
			ExpectedExitCode:      dp.ExitCodeToolError,
			ExpectedStructured: simtest.Expect().
				WithErrorID("XCycleA/test-cycle").
				WithComposition("xcycleas.cycle.example.org").
				WithAffectedResources(1, 0, 0, 1).
				WithXRImpact("XCycleA", "test-cycle", "default", "error").
				WithErrorContaining("maximum nesting depth exceeded").
				WithErrorResourceID("XCycleA/test-cycle"),
		},
		"NestedXRUsesOwnComposition": {
			Reason: "Validates that nested XRs use their own composition from the cluster, not the parent's CLI composition",
			// Set up XRDs, compositions, and functions
			SetupFiles: []string{
				// Child XRD and composition (XNopResource uses xnopresources.diff.example.org)
				"../../../internal/testutils/integration/testdata/comp/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/original-composition.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
				// Parent XRD and composition
				"testdata/comp/resources/nested-xr/parent-xrd.yaml",
				"testdata/comp/resources/nested-xr/parent-composition.yaml",
			},
			// Use crossplaneManagedResources to set up proper ownership hierarchy
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				{
					// Parent XR owns both direct downstream and nested child XR
					OwnerFile: "testdata/comp/resources/nested-xr/existing-parent-xr.yaml",
					OwnedFiles: map[string]*integration.HierarchicalOwnershipRelation{
						// Direct downstream owned by parent XR
						"testdata/comp/resources/nested-xr/existing-parent-direct-downstream.yaml": nil,
						// Nested child XR owned by parent XR, which owns its own downstream
						"testdata/comp/resources/nested-xr/existing-nested-child-xr.yaml": {
							OwnedFiles: map[string]*integration.HierarchicalOwnershipRelation{
								// Child's downstream owned by nested child XR
								"testdata/comp/resources/nested-xr/existing-child-downstream.yaml": nil,
							},
						},
					},
				},
			},
			// Diff the updated parent composition
			InputFiles: []string{"testdata/comp/updated-parent-composition.yaml"},
			Namespace:  "default",
			// Expected: Only the parent's direct downstream shows changes (tier change).
			// The nested child's downstream should NOT change because:
			// 1. The nested XR (XNopResource) uses its own composition (xnopresources.diff.example.org)
			// 2. That child composition was NOT modified
			// Bug scenario: If nested XR incorrectly uses parent composition, we'd see wrong changes to child's downstream.
			// Expected output: Only the parent's direct downstream should show in the Impact Analysis.
			// The child's downstream (XDownstreamResource/test-parent-nested) should NOT appear here.
			// If it did, it would mean the nested XR incorrectly used the parent's CLI composition.
			ExpectedOutput: `=== Impact Analysis ===

~~~ XDownstreamResource/test-parent-direct
  apiVersion: ns.nop.example.org/v1alpha1
  kind: XDownstreamResource
  metadata:
    annotations:
      crossplane.io/composition-resource-name: direct-resource
    labels:
      crossplane.io/composite: test-parent
    name: test-parent-direct
    namespace: default
  spec:
    forProvider:
      configData: parent-parent-value
-     resourceTier: parent-tier
+     resourceTier: premium-tier

---

Summary: 1 modified`,
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			NoColor:          true,
		},
		"CrossNamespaceResourceCollision": {
			Reason: "Validates that resources with the same name in different namespaces are correctly distinguished",
			Skip:   true,
			SkipReason: "Blocked on https://github.com/crossplane-contrib/function-extra-resources/issues/106. " +
				"function-extra-resources (v0.2.0/v0.3.0) emits ResourceSelector{Namespace=\"\"} for " +
				"by-name references that omit `ref.namespace` — it only forwards user-yaml verbatim, " +
				"never inferring from the XR's namespace. Crossplane v2.3+'s render binary then does " +
				"InMemoryClient.Get(name, namespace=\"\"), a strict (GVK, ns, name) match that doesn't " +
				"find our resolved ConfigMap (stored at namespace=ns-{a,b}). By-label selectors aren't " +
				"affected — List(InNamespace(\"\")) lists across all namespaces — which is why our " +
				"other extra-resources tests still pass. Re-enable once a function release defaults " +
				"selector.Namespace to the XR's namespace.",
			Timeout: simharness.LongTimeout, // Test involves multiple namespaces and can be slow with Docker contention
			// This test catches a bug where the cache key didn't include namespace, causing
			// ConfigMaps with the same name in different namespaces to collide.
			// The XR in ns-a should get ConfigMap from ns-a (value: from-ns-a)
			// The XR in ns-b should get ConfigMap from ns-b (value: from-ns-b)
			// Bug: Both XRs would get the same ConfigMap (whichever was cached first)
			SetupFiles: []string{
				// Create namespaces first
				"testdata/comp/resources/namespace-collision/namespace-ns-a.yaml",
				"testdata/comp/resources/namespace-collision/namespace-ns-b.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
				"testdata/comp/resources/namespace-collision/collision-composition.yaml",
				"testdata/comp/resources/namespace-collision/collision-composition-revision.yaml",
				// ConfigMaps with same name in different namespaces
				"testdata/comp/resources/namespace-collision/configmap-ns-a.yaml",
				"testdata/comp/resources/namespace-collision/configmap-ns-b.yaml",
			},
			// XRs and their downstream resources in different namespaces
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				{
					OwnerFile: "testdata/comp/resources/namespace-collision/existing-xr-ns-a.yaml",
					OwnedFiles: map[string]*integration.HierarchicalOwnershipRelation{
						"testdata/comp/resources/namespace-collision/existing-downstream-ns-a.yaml": nil,
					},
				},
				{
					OwnerFile: "testdata/comp/resources/namespace-collision/existing-xr-ns-b.yaml",
					OwnedFiles: map[string]*integration.HierarchicalOwnershipRelation{
						"testdata/comp/resources/namespace-collision/existing-downstream-ns-b.yaml": nil,
					},
				},
			},
			InputFiles: []string{"testdata/comp/updated-collision-composition.yaml"},
			// Expected: Each XR's downstream should show the correct prefix applied to its namespace's ConfigMap value.
			// XR in ns-a: from-ns-a → prefix-from-ns-a
			// XR in ns-b: from-ns-b → prefix-from-ns-b
			// Bug scenario: If namespace collision occurs, ns-b would incorrectly show:
			//   from-ns-b → prefix-from-ns-a (wrong ConfigMap cached from ns-a)
			ExpectedOutput: `=== Impact Analysis ===

~~~ XDownstreamResource/xr-in-ns-a
  apiVersion: ns.nop.example.org/v1alpha1
  kind: XDownstreamResource
  metadata:
    annotations:
      crossplane.io/composition-resource-name: collision-resource
    labels:
      crossplane.io/composite: xr-in-ns-a
    name: xr-in-ns-a
    namespace: ns-a
  spec:
    forProvider:
-     configData: from-ns-a
+     configData: prefix-from-ns-a

---
~~~ XDownstreamResource/xr-in-ns-b
  apiVersion: ns.nop.example.org/v1alpha1
  kind: XDownstreamResource
  metadata:
    annotations:
      crossplane.io/composition-resource-name: collision-resource
    labels:
      crossplane.io/composite: xr-in-ns-b
    name: xr-in-ns-b
    namespace: ns-b
  spec:
    forProvider:
-     configData: from-ns-b
+     configData: prefix-from-ns-b

---

Summary: 2 modified`,
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			NoColor:          true,
		},
		// Paired comp sequencer-gating tests — mirror of the XR pair. Verifies that
		// --eventual-state on `comp` surfaces downstream resources hidden by
		// function-sequencer in the impact analysis.
		"CompSequencerGatesLaterStageWithoutEventualState": {
			Reason:       "`comp` without --eventual-state: sequencer-gated stage1 is absent from the impact analysis",
			Timeout:      simharness.LongTimeout,
			OutputFormat: "json",
			// eventualState deliberately false
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/sequencer-gating-composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			// The XR references sequencer-gating-composition by name, so comp discovers it. It has
			// composed stage0, which isn't Ready, so stage1 stays gated.
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes("testdata/comp/resources/existing-sequencer-gating-xr.yaml", "testdata/comp/resources/existing-sequencer-gating-stage0.yaml"),
			},
			InputFiles:       []string{"testdata/comp/updated-sequencer-gating-composition.yaml"},
			Namespace:        "default",
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			// Expect: impact shows stage0 modified (its configData changed); stage1 is absent from
			// downstream impact because sequencer gates it.
			ExpectedStructured: simtest.Expect().
				WithComposition("sequencer-gating-composition").
				WithCompositionModified().
				WithXRImpact("XNopResource", "sequencer-gating-test", "default", "changed").
				WithDownstreamSummary(0, 1, 0).
				WithDownstreamResource("modified", "XDownstreamResource", "0-stage0-resource", "default").
				WithFieldChange("spec.forProvider.configData", "existing-value", "updated-existing-value"),
			ExpectedError: false,
		},
		"CompSequencerGatedStageAppearsWithEventualState": {
			Reason:        "`comp` with --eventual-state: sequencer-gated stage1 surfaces in the impact analysis",
			Timeout:       simharness.LongTimeout, // Multiple simulation iterations need more time
			OutputFormat:  "json",
			EventualState: true,
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/sequencer-gating-composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			// The XR has composed stage0, which isn't Ready, so stage1 stays gated until eventual
			// state synthesizes stage0's Ready (#564).
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes("testdata/comp/resources/existing-sequencer-gating-xr.yaml", "testdata/comp/resources/existing-sequencer-gating-stage0.yaml"),
			},
			InputFiles:       []string{"testdata/comp/updated-sequencer-gating-composition.yaml"},
			Namespace:        "default",
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			// Expect: impact shows stage0 modified and stage1 added (eventual-state unlocks stage1).
			ExpectedStructured: simtest.Expect().
				WithComposition("sequencer-gating-composition").
				WithCompositionModified().
				WithXRImpact("XNopResource", "sequencer-gating-test", "default", "changed").
				WithDownstreamSummary(1, 1, 0).
				WithDownstreamResource("modified", "XDownstreamResource", "0-stage0-resource", "default").
				WithFieldChange("spec.forProvider.configData", "existing-value", "updated-existing-value").
				AndXR().
				WithDownstreamResource("added", "XDownstreamResource", "1-stage1-resource", "default").
				// dryRun absence through `comp`: a downstream addition that reached the apiserver
				// carries no dryRun object. Piggy-backed on an existing case rather than given its
				// own envtest instance, since it is a pure additional assertion.
				WithDryRunPerformed().
				WithField("spec.forProvider.configData", "updated-existing-value"),
			ExpectedError: false,
		},
		// Paired comp composition-driven gating tests — gating encoded in go-templating,
		// no function-sequencer. Mirrors the XR composition-driven pair.
		"CompConditionalGatingHidesLaterStageWithoutEventualState": {
			Reason:       "`comp` without --eventual-state: template-gated stage1 is absent from the impact analysis",
			Timeout:      simharness.LongTimeout,
			OutputFormat: "json",
			// eventualState deliberately false
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/conditional-gating-composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			// The XR has composed stage0, which isn't Ready, so stage1 stays gated.
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes("testdata/comp/resources/existing-conditional-gating-xr.yaml", "testdata/comp/resources/existing-conditional-gating-stage0.yaml"),
			},
			InputFiles:       []string{"testdata/comp/updated-conditional-gating-composition.yaml"},
			Namespace:        "default",
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			ExpectedStructured: simtest.Expect().
				WithComposition("conditional-gating-composition").
				WithCompositionModified().
				WithXRImpact("XNopResource", "conditional-gating-test", "default", "changed").
				WithDownstreamSummary(0, 1, 0).
				WithDownstreamResource("modified", "XDownstreamResource", "0-stage0-resource", "default").
				WithFieldChange("spec.forProvider.configData", "existing-value", "updated-existing-value"),
			ExpectedError: false,
		},
		"CompConditionalGatedStageAppearsWithEventualState": {
			Reason:        "`comp` with --eventual-state: template-gated stage1 surfaces in the impact analysis",
			Timeout:       simharness.LongTimeout, // Multiple simulation iterations need more time
			OutputFormat:  "json",
			EventualState: true,
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/conditional-gating-composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			// The XR has composed stage0, which isn't Ready, so stage1 stays gated until eventual
			// state synthesizes stage0's Ready (#564).
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes("testdata/comp/resources/existing-conditional-gating-xr.yaml", "testdata/comp/resources/existing-conditional-gating-stage0.yaml"),
			},
			InputFiles:       []string{"testdata/comp/updated-conditional-gating-composition.yaml"},
			Namespace:        "default",
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			ExpectedStructured: simtest.Expect().
				WithComposition("conditional-gating-composition").
				WithCompositionModified().
				WithXRImpact("XNopResource", "conditional-gating-test", "default", "changed").
				WithDownstreamSummary(1, 1, 0).
				WithDownstreamResource("modified", "XDownstreamResource", "0-stage0-resource", "default").
				WithFieldChange("spec.forProvider.configData", "existing-value", "updated-existing-value").
				AndXR().
				WithDownstreamResource("added", "XDownstreamResource", "1-stage1-resource", "default").
				WithField("spec.forProvider.configData", "updated-existing-value"),
			ExpectedError: false,
		},
		// dryRun presence through `comp`. dryRun lives on ChangeDetail, which `xr` reaches via
		// changes[] and `comp` via impactAnalysis[].downstreamChanges.changes[] — one shared wire
		// shape, but only an assertion through the comp renderer proves comp actually populates it.
		// The composed resource is deliberately absent from the setup so the impact is an ADDITION,
		// which is the only kind of change the dryRun field applies to: the XR has its composition
		// refs, but its first compose has not created its resource.
		"CompDownstreamAdditionReportsDryRunSkippedWhenDryRunOnExisting": {
			Reason:       "`comp` structured output carries dryRun.skipReason on a downstream addition when --dry-run-on=existing",
			OutputFormat: "json",
			DryRunOn:     "existing",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/comp/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/original-composition.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
				// existing-downstream-1.yaml is intentionally NOT applied: with no composed resource
				// in the cluster, the impact analysis reports an addition rather than a modification.
				"../../../internal/testutils/integration/testdata/comp/resources/existing-xr-1.yaml",
			},
			InputFiles:       []string{"../../../internal/testutils/integration/testdata/comp/updated-composition.yaml"},
			Namespace:        "default",
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			ExpectedStructured: simtest.Expect().
				WithComposition("xnopresources.diff.example.org").
				WithCompositionModified().
				WithXRImpact("XNopResource", "test-resource", "default", "changed").
				WithDownstreamSummary(1, 0, 0).
				WithDownstreamResource("added", "XDownstreamResource", "test-resource", "default").
				WithDryRunSkipped("disabled", "").
				WithField("spec.forProvider.configData", "updated-existing-value"),
			ExpectedError: false,
		},
		// The unverified-addition warning through `comp`: one run-wide summary in the top-level
		// warnings[], counting the downstream additions behind it, rather than one per composition.
		// A cluster-scoped XR is what lets the composition reach a namespace that does not exist.
		"CompDownstreamAdditionsInMissingNamespaceAreCountedInOneWarning": {
			Reason:       "`comp` summarises downstream additions it could not verify as one warning per GVK + namespace + cause, with a count",
			OutputFormat: "json",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/cluster-xrd.yaml",
				"testdata/comp/resources/unverified-cluster-composition.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
				"testdata/comp/resources/existing-unverified-cluster-xr.yaml",
			},
			InputFiles:       []string{"testdata/comp/updated-unverified-cluster-composition.yaml"},
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			ExpectedStderrContains: []string{
				`WARNING: skipped apiserver verification of added resources: their namespace does not exist yet, so their diffs omit server-side defaulting and admission ` +
					`(cause=namespaces "nonexistent-namespace" not found, count=2, gvk=/v1, Kind=ConfigMap, namespace=nonexistent-namespace)`,
			},
			ExpectedStructured: simtest.Expect().
				WithWarning("their namespace does not exist yet").
				WithWarningContext(map[string]string{
					"gvk":       "/v1, Kind=ConfigMap",
					"namespace": "nonexistent-namespace",
					"cause":     `namespaces "nonexistent-namespace" not found`,
					"count":     "2",
				}).
				And().
				WithComposition("xnopresources-unverified.diff.example.org").
				WithCompositionModified().
				WithXRImpact("XNopResource", "unverified-xr", "", "changed").
				WithDownstreamSummary(2, 0, 0).
				WithDownstreamResource("added", "ConfigMap", "unverified-xr-first", "nonexistent-namespace").
				WithDryRunSkipped("namespaceNotFound", "nonexistent-namespace").
				AndXR().
				WithDownstreamResource("added", "ConfigMap", "unverified-xr-second", "nonexistent-namespace").
				WithDryRunSkipped("namespaceNotFound", "nonexistent-namespace"),
			ExpectedError: false,
		},
		// --resource flag tests (issue #321)
		"ResourceFilterScopesAffectedXRs": {
			Reason: "--resource limits impact analysis to the named composites and ignores the rest",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/comp/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/original-composition.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes( // test-resource (default ns)
					"../../../internal/testutils/integration/testdata/comp/resources/existing-xr-1.yaml",
					"../../../internal/testutils/integration/testdata/comp/resources/existing-downstream-1.yaml",
				),
				integration.Composes( // another-resource (default ns)
					"../../../internal/testutils/integration/testdata/comp/resources/existing-xr-2.yaml",
					"../../../internal/testutils/integration/testdata/comp/resources/existing-downstream-2.yaml",
				),
			},
			InputFiles:       []string{"../../../internal/testutils/integration/testdata/comp/updated-composition.yaml"},
			Resources:        []string{"default/test-resource"},
			OutputFormat:     "json",
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			ExpectedStructured: simtest.Expect().
				WithComposition("xnopresources.diff.example.org").
				WithCompositionModified().
				WithAffectedResources(1, 1, 0, 0).
				WithXRImpact("XNopResource", "test-resource", "default", "changed"),
		},
		"LeastPrivilegeCredentialsDiffSucceeds": {
			Reason: "A user who can list only the composition's XR type, not Crossplane's own types, gets the admin's impact analysis",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/comp/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/original-composition.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"../../../internal/testutils/integration/testdata/comp/resources/existing-xr-1.yaml",
					"../../../internal/testutils/integration/testdata/comp/resources/existing-downstream-1.yaml",
				),
				integration.Composes(
					"../../../internal/testutils/integration/testdata/comp/resources/existing-xr-2.yaml",
					"../../../internal/testutils/integration/testdata/comp/resources/existing-downstream-2.yaml",
				),
			},
			InputFiles: []string{"../../../internal/testutils/integration/testdata/comp/updated-composition.yaml"},
			Namespace:  "default",
			// list on the XR type: without --resource, comp finds the composition's XRs by listing them.
			RbacRules:        simharness.LeastPrivilegeRules("get", "list", "create", "patch"),
			OutputFormat:     "json",
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			ExpectedStructured: simtest.Expect().
				WithComposition("xnopresources.diff.example.org").
				WithCompositionModified().
				WithAffectedResources(2, 2, 0, 0).
				WithXRImpact("XNopResource", "test-resource", "default", "changed").
				AndComp().
				WithXRImpact("XNopResource", "another-resource", "default", "changed"),
		},
		"NameScopedRunSucceedsWithoutCompositeList": {
			Reason: "--resource looks its composites up by name, so a user who cannot list the XR type gets the admin's impact analysis",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/comp/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/original-composition.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"../../../internal/testutils/integration/testdata/comp/resources/existing-xr-1.yaml",
					"../../../internal/testutils/integration/testdata/comp/resources/existing-downstream-1.yaml",
				),
				integration.Composes(
					"../../../internal/testutils/integration/testdata/comp/resources/existing-xr-2.yaml",
					"../../../internal/testutils/integration/testdata/comp/resources/existing-downstream-2.yaml",
				),
			},
			InputFiles:       []string{"../../../internal/testutils/integration/testdata/comp/updated-composition.yaml"},
			Resources:        []string{"default/test-resource"},
			RbacRules:        simharness.LeastPrivilegeRules("get", "create", "patch"),
			OutputFormat:     "json",
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			ExpectedStructured: simtest.Expect().
				WithComposition("xnopresources.diff.example.org").
				WithCompositionModified().
				WithAffectedResources(1, 1, 0, 0).
				WithXRImpact("XNopResource", "test-resource", "default", "changed"),
		},
		"ResourceFilterCommaSeparated": {
			Reason: "--resource accepts comma-separated values via kong's auto-parsing",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/comp/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/original-composition.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"../../../internal/testutils/integration/testdata/comp/resources/existing-xr-1.yaml",
					"../../../internal/testutils/integration/testdata/comp/resources/existing-downstream-1.yaml",
				),
				integration.Composes(
					"../../../internal/testutils/integration/testdata/comp/resources/existing-xr-2.yaml",
					"../../../internal/testutils/integration/testdata/comp/resources/existing-downstream-2.yaml",
				),
			},
			InputFiles:       []string{"../../../internal/testutils/integration/testdata/comp/updated-composition.yaml"},
			ResourcesCSV:     "default/test-resource,default/another-resource",
			OutputFormat:     "json",
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			ExpectedStructured: simtest.Expect().
				WithComposition("xnopresources.diff.example.org").
				WithCompositionModified().
				WithAffectedResources(2, 2, 0, 0).
				WithXRImpact("XNopResource", "test-resource", "default", "changed").
				AndComp().
				WithXRImpact("XNopResource", "another-resource", "default", "changed"),
		},
		"ResourceFilterUnmatched_FailsBeforeRendering": {
			Reason: "--resource naming a non-existent composite fails fast with an error before any rendering",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/comp/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/original-composition.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"../../../internal/testutils/integration/testdata/comp/resources/existing-xr-1.yaml",
					"../../../internal/testutils/integration/testdata/comp/resources/existing-downstream-1.yaml",
				),
			},
			InputFiles:            []string{"../../../internal/testutils/integration/testdata/comp/updated-composition.yaml"},
			Resources:             []string{"default/does-not-exist"},
			ExpectedError:         true,
			ExpectedErrorContains: "does-not-exist",
			ExpectedExitCode:      dp.ExitCodeToolError,
		},
		"ResourceFilterRespectsManualPolicy_WithoutIncludeManual": {
			Reason: "--resource matching a Manual-policy composite surfaces it as filtered (reason manual_policy) when --include-manual is unset",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/comp/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/original-composition.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"../../../internal/testutils/integration/testdata/comp/resources/existing-xr-manual.yaml",
					"../../../internal/testutils/integration/testdata/comp/resources/existing-downstream-manual.yaml",
				),
			},
			InputFiles:       []string{"../../../internal/testutils/integration/testdata/comp/updated-composition.yaml"},
			Resources:        []string{"default/manual-resource"},
			OutputFormat:     "json",
			ExpectedExitCode: dp.ExitCodeDiffDetected, // composition itself changed
			ExpectedStructured: simtest.Expect().
				WithComposition("xnopresources.diff.example.org").
				WithCompositionModified().
				WithXRImpact("XNopResource", "manual-resource", "default", "filtered").
				WithFilterReason("manualPolicy"),
		},
		"ResourceFilterRespectsManualPolicy_WithIncludeManual": {
			Reason: "--include-manual evaluates the Manual-policy composite normally instead of marking it filtered",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/comp/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/original-composition.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"../../../internal/testutils/integration/testdata/comp/resources/existing-xr-manual.yaml",
					"../../../internal/testutils/integration/testdata/comp/resources/existing-downstream-manual.yaml",
				),
			},
			InputFiles:       []string{"../../../internal/testutils/integration/testdata/comp/updated-composition.yaml"},
			Resources:        []string{"default/manual-resource"},
			IncludeManual:    true,
			OutputFormat:     "json",
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			ExpectedStructured: simtest.Expect().
				WithComposition("xnopresources.diff.example.org").
				WithCompositionModified().
				WithXRImpact("XNopResource", "manual-resource", "default", "changed"),
		},
		// Issue #388 (Bug A): an Automatic-policy XR whose compositionRevisionSelector does not match
		// the edited composition's labels would not adopt the resulting revision, so it must be
		// surfaced as filtered (reason revision_selector_mismatch), NOT evaluated for changes.
		"ResourceFilterRespectsRevisionSelectorMismatch": {
			Reason: "--resource matching an Automatic XR whose compositionRevisionSelector does not match the composition's labels surfaces it as filtered (reason revision_selector_mismatch)",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/comp/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/original-composition.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
				"testdata/comp/resources/existing-xr-selector-mismatch.yaml",
			},
			InputFiles:       []string{"testdata/comp/updated-composition-labeled.yaml"},
			Resources:        []string{"default/selector-mismatch-resource"},
			OutputFormat:     "json",
			ExpectedExitCode: dp.ExitCodeDiffDetected, // composition itself changed
			ExpectedStructured: simtest.Expect().
				WithComposition("xnopresources.diff.example.org").
				WithCompositionModified().
				WithXRImpact("XNopResource", "selector-mismatch-resource", "default", "filtered").
				WithFilterReason("revisionSelectorMismatch"),
			// Note: that --include-manual does NOT rescue a selector-mismatched Automatic XR is
			// proven at the unit level (TestDefaultCompDiffProcessor_partitionXRsByUpdatePolicy /
			// IncludeManualTrue_StillDropsSelectorMismatchedAutomaticXRs). A second slow
			// docker-render integration case for that flag interaction would be redundant.
		},
		// Issue #388 (Bug A, positive): an Automatic XR whose compositionRevisionSelector DOES match
		// the edited composition's labels is kept and evaluated for downstream changes (not filtered).
		"ResourceFilterRespectsRevisionSelectorMatch": {
			Reason: "--resource matching an Automatic XR whose compositionRevisionSelector matches the composition's labels keeps it and reports downstream changes",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/comp/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/original-composition.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
				"testdata/comp/resources/existing-xr-selector-match.yaml",
			},
			InputFiles:       []string{"testdata/comp/updated-composition-labeled.yaml"},
			Resources:        []string{"default/selector-match-resource"},
			OutputFormat:     "json",
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			ExpectedStructured: simtest.Expect().
				WithComposition("xnopresources.diff.example.org").
				WithCompositionModified().
				WithXRImpact("XNopResource", "selector-match-resource", "default", "changed"),
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			simharness.RunIntegrationTest(t, simharness.CompositionTest, tt)
		})
	}
}
