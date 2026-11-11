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

package xr_test

import (
	"strings"
	"testing"

	dp "github.com/crossplane/cli/v2/internal/diff/diffprocessor"
	tu "github.com/crossplane/cli/v2/internal/diff/testutils"
	"github.com/crossplane/cli/v2/internal/simulate/simtest"
	"github.com/crossplane/cli/v2/internal/testutils/integration"
	"github.com/crossplane/cli/v2/internal/testutils/integration/simharness"
)

// TestDiffIntegration runs an integration test for the diff command.
//
//nolint:tparallel // simharness.RunIntegrationTest calls t.Parallel in each subtest.
func TestDiffIntegration(t *testing.T) {
	t.Parallel()

	tests := map[string]simharness.IntegrationTestCase{
		"NewResourceDiff": {
			Reason:     "Shows color diff for new resources",
			InputFiles: []string{"../../../internal/testutils/integration/testdata/diff/new-xr.yaml"},
			SetupFiles: []string{
				// TODO: For v2, we need to upgrade the XRD apiversion to v2 + put the xr in a namespace.
				// this might be better served as a separate test(s).
				// since we aren't actually creating resources (there's no crossplane actually running in unit tests),
				// we don't need to worry about upgrading providers or anything.
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			ExpectedOutput: strings.Join([]string{
				`+++ XDownstreamResource/test-resource
`, tu.Green(`+ apiVersion: ns.nop.example.org/v1alpha1
+ kind: XDownstreamResource
+ metadata:
+   annotations:
+     crossplane.io/composition-resource-name: nop-resource
+   labels:
+     crossplane.io/composite: test-resource
+   name: test-resource
+   namespace: default
+ spec:
+   forProvider:
+     configData: new-value
`), `
---
+++ XNopResource/test-resource
`, tu.Green(`+ apiVersion: ns.diff.example.org/v1alpha1
+ kind: XNopResource
+ metadata:
+   name: test-resource
+   namespace: default
+ spec:
+   coolField: new-value
`), `
---
`,
			}, ""),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		"AutomaticNamespacePropagation": {
			Reason:       "Validates automatic namespace propagation for namespaced managed resources",
			OutputFormat: "json",
			InputFiles:   []string{"../../../internal/testutils/integration/testdata/diff/new-xr.yaml"},
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"testdata/diff/resources/composition-no-namespace-patch.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			ExpectedStructured: simtest.Expect().
				WithSummary(2, 0, 0).
				WithAddedResource("XDownstreamResource", "test-resource", "default").
				WithField("spec.forProvider.configData", "new-value").
				And().
				WithAddedResource("XNopResource", "test-resource", "default").
				WithField("spec.coolField", "new-value"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		// A built-in Kubernetes type has no CRD, so this exercises the paths
		// that would otherwise read scope and schema from one. Before scope
		// came from discovery this failed outright, with "cannot determine
		// scope for resource ConfigMap/... CRD not found".
		"RendersBuiltInResourceWithoutCRD": {
			Reason:       "A composition rendering a built-in Kubernetes resource (no CRD) produces a diff for it",
			OutputFormat: "json",
			InputFiles:   []string{"../../../internal/testutils/integration/testdata/diff/new-xr.yaml"},
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"testdata/diff/resources/composition-with-configmap.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			ExpectedStructured: simtest.Expect().
				WithSummary(3, 0, 0).
				WithAddedResource("ConfigMap", "test-resource-config", "default").
				WithField("data.configData", "new-value").
				And().
				WithAddedResource("XDownstreamResource", "test-resource", "default").
				WithField("spec.forProvider.configData", "new-value").
				And().
				WithAddedResource("XNopResource", "test-resource", "default").
				WithField("spec.coolField", "new-value"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		"UnservedGroupIsReportedAsAnUnknownTypeNotAMissingNamespace": {
			// From the PR #502 review. A resource whose group/version the cluster does not serve must be
			// reported as an unknown type; blaming its namespace would send the user after the wrong root
			// cause entirely, since the namespace here exists and is irrelevant.
			//
			// Worth being precise about what this covers, because it is NOT the sentinel branch in
			// dryRunCreateAddition. For a *.k8s.io group, two earlier guards let the resource through —
			// IsCRDRequired skips discovery for that suffix, and FetchCurrentObject swallows NotFound as
			// "this is new" — but removeNamespacesFromClusterScopedResources then fails first, because
			// scope determination cannot resolve the GVK either. So this pins the outer behaviour (an
			// accurate, type-naming error rather than a namespace one) without depending on which layer
			// produces it.
			//
			// The sentinel branch is what covers the case this CANNOT reach: scope determination falls
			// back to a CRD lookup when discovery fails, whereas GVKToGVR is discovery-only, so a CRD
			// whose version is not `served` passes the scope check and fails inside DryRunCreate. That
			// route is unit-covered in diff_calculator_test.go, since arranging a served:false CRD whose
			// scope still resolves is far more setup than the classification warrants.
			Reason:       "An addition whose group/version the cluster does not serve is reported as an unknown type, not as a missing namespace",
			OutputFormat: "json",
			InputFiles:   []string{"../../../internal/testutils/integration/testdata/diff/new-xr.yaml"},
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"testdata/diff/resources/composition-with-unserved-k8s-group.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			ExpectedError:         true,
			ExpectedErrorContains: "totallynotserved.k8s.io/v1",
			ExpectedExitCode:      dp.ExitCodeToolError,
		},
		"AdditionInMissingNamespaceDegradesRatherThanFailing": {
			// Regression test for the e2e failure the first cut of this feature caused
			// (TestDiffConcurrentDirectory diffs 21 XRs into a namespace that is never created).
			//
			// NamespaceLifecycle admission refuses a dry-run create into a namespace that does not exist.
			// That must degrade, not fail the run: a Namespace and its contents are routinely applied
			// together, so the namespace's absence at diff time says nothing about whether the apply will
			// succeed — which is what separates it from a quota or webhook refusal, where the verdict
			// describes the object and will still hold.
			//
			// The exit code is the real assertion. Before the fix this was exit 1 with no diff at all.
			Reason:       "An addition whose namespace does not exist yet still produces a diff, marked as unverified",
			OutputFormat: "json",
			InputFiles:   []string{"testdata/diff/new-xr-missing-namespace.yaml"},
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			ExpectedStructured: simtest.Expect().
				WithSummary(2, 0, 0).
				WithAddedResource("XNopResource", "test-resource", "nonexistent-namespace").
				WithField("spec.coolField", "new-value").
				WithDryRunSkipped("namespaceNotFound", "nonexistent-namespace").
				And().
				WithAddedResource("XDownstreamResource", "test-resource", "nonexistent-namespace").
				WithDryRunSkipped("namespaceNotFound", "nonexistent-namespace").
				And().
				// One warning per GVK, and each cause exactly the apiserver's message. Through the REAL
				// ApplyClient, whose error is wrapped with "failed to dry-run create resource <Kind>/<name>":
				// if that wrapper leaked into the cause, every resource would be a distinct warning. The unit
				// test mirrors the wrapping by hand; this pins it against the client that actually produces it.
				WithWarning("their namespace does not exist yet").
				WithWarningContext(map[string]string{
					"gvk":       "ns.diff.example.org/v1alpha1, Kind=XNopResource",
					"namespace": "nonexistent-namespace",
					"cause":     `namespaces "nonexistent-namespace" not found`,
					"count":     "1",
				}).
				And().
				WithWarning("their namespace does not exist yet").
				WithWarningContext(map[string]string{
					"gvk":       "ns.nop.example.org/v1alpha1, Kind=XDownstreamResource",
					"namespace": "nonexistent-namespace",
					"cause":     `namespaces "nonexistent-namespace" not found`,
					"count":     "1",
				}),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		// Human-readable output shows no dryRun at all, so stderr is the only place a person learns that
		// additions went unverified, and how many. Two inputs into one missing namespace share each GVK's
		// warning rather than raising one per resource.
		"UnverifiedAdditionsAcrossInputsAreCountedInOneWarning": {
			Reason: "In text mode, additions that could not be verified are summarised on stderr once per GVK + namespace + cause, with a count of the resources behind it",
			InputFiles: []string{
				"testdata/diff/new-xr-missing-namespace.yaml",
				"testdata/diff/new-xr-missing-namespace-second.yaml",
			},
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			NoColor: true,
			ExpectedStderrContains: []string{
				`WARNING: skipped apiserver verification of added resources: their namespace does not exist yet, so their diffs omit server-side defaulting and admission ` +
					`(cause=namespaces "nonexistent-namespace" not found, count=2, gvk=ns.diff.example.org/v1alpha1, Kind=XNopResource, namespace=nonexistent-namespace)`,
				`WARNING: skipped apiserver verification of added resources: their namespace does not exist yet, so their diffs omit server-side defaulting and admission ` +
					`(cause=namespaces "nonexistent-namespace" not found, count=2, gvk=ns.nop.example.org/v1alpha1, Kind=XDownstreamResource, namespace=nonexistent-namespace)`,
			},
			ExpectedOutput:   "+++ XDownstreamResource/second-resource",
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		"BuiltInResourceAdditionPicksUpApiserverDefaults": {
			// The choice of a built-in type keeps this test non-vacuous whatever crossplane-diff defaults
			// locally. The lenient Defaulter (defaulter.go) predicts CRD-derived `default:` values for an
			// addition that gets no apiserver result, and passes anything without a CRD through unchanged.
			// Deployment has no CRD, so IsCRDRequired is false, and the fields asserted below can ONLY have
			// come from round-tripping the addition through the apiserver.
			//
			// Every asserted default is string-valued on purpose: assertChangeFields compares with
			// reflect.DeepEqual against JSON-decoded values, so a numeric default (revisionHistoryLimit: 10)
			// would arrive as float64 and an int literal here would fail for the wrong reason.
			Reason:       "An added built-in resource is dry-run created against the apiserver, so apiserver-side defaults appear in its addition diff",
			OutputFormat: "json",
			InputFiles:   []string{"../../../internal/testutils/integration/testdata/diff/new-xr.yaml"},
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"testdata/diff/resources/composition-with-builtin-defaults.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			ExpectedStructured: simtest.Expect().
				WithSummary(2, 0, 0).
				WithAddedResource("Deployment", "test-resource-deploy", "default").
				// Absence of dryRun is the machine-readable claim that this desired state was
				// verified against the apiserver. Asserted alongside the defaults so that a
				// regression which stopped dry-running additions fails on the contract as well as
				// on whichever fields happen to depend on server-side defaulting.
				WithDryRunPerformed().
				WithField("spec.strategy.type", "RollingUpdate").
				WithField("spec.template.spec.restartPolicy", "Always").
				WithField("spec.template.spec.dnsPolicy", "ClusterFirst").
				And().
				WithAddedResource("XNopResource", "test-resource", "default").
				WithDryRunPerformed().
				WithField("spec.coolField", "new-value"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		// The inverse of the case above, and the pair is what makes either meaningful: on its own,
		// "the defaults appear" could be satisfied by something other than the apiserver round-trip,
		// and "the defaults are absent" could be satisfied by the feature being broken. Together they
		// pin the flag as the thing that decides.
		//
		// This also pins that `existing` reproduces the behaviour from before additions were dry-run
		// (#334), which is why the fields are asserted absent rather than merely unasserted.
		"BuiltInResourceAdditionSkipsApiserverWhenDryRunOnExisting": {
			Reason:       "With --dry-run-on=existing an added built-in resource is not sent to the apiserver, so its diff carries no server-side defaults and reports skipReason disabled",
			OutputFormat: "json",
			DryRunOn:     "existing",
			InputFiles:   []string{"../../../internal/testutils/integration/testdata/diff/new-xr.yaml"},
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"testdata/diff/resources/composition-with-builtin-defaults.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			ExpectedStructured: simtest.Expect().
				WithSummary(2, 0, 0).
				WithAddedResource("Deployment", "test-resource-deploy", "default").
				// No detail substring: "disabled" is the one skip reason the user asked for, so
				// there is no cluster explanation to propagate.
				WithDryRunSkipped("disabled", "").
				WithFieldAbsent("spec.strategy.type").
				WithFieldAbsent("spec.template.spec.restartPolicy").
				WithFieldAbsent("spec.template.spec.dnsPolicy").
				// The rendered fields must still be there — `existing` degrades fidelity, it does
				// not degrade the diff.
				WithField("spec.template.spec.containers[0].image", "nginx:1.27").
				And().
				WithAddedResource("XNopResource", "test-resource", "default").
				WithDryRunSkipped("disabled", "").
				WithField("spec.coolField", "new-value"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		// A status authored by the composition pipeline must survive the apiserver round-trip.
		// XStatusResource declares the status subresource, so the apiserver strips status from the
		// dry-run create and returns an object without one; the value below can therefore only come
		// from the calculator re-attaching what was rendered. Every other composed kind in this
		// testdata tree omits the subresource, which would have made this assertion vacuous.
		//
		// SKIPPED, and the reason is a finding rather than an environment problem: the re-attach
		// works, but nothing renders it. See skipReason.
		"AddedResourceRetainsCompositionAuthoredStatus": {
			Skip: true,
			SkipReason: "The re-attached status cannot be observed in any output, so this assertion " +
				"cannot pass and could not be made to fail by breaking the production code it targets. " +
				"cleanupForDiff (compare/compare.go) deletes metadata.status unconditionally from " +
				"both sides of every diff, in the same block that deletes ownerReferences — the strip whose " +
				"identical consequence for ownerReferences this feature's own spec documented (§1a) and then " +
				"missed for status. ResourceViews.Clean is the only view any renderer reads" +
				"; ResourceViews.Desired.Raw, which does carry the " +
				"re-attached status, has no reader at all. Verified empirically: the apiserver's dry-run " +
				"create response for XStatusResource carries no status, the object handed to cleanupForDiff " +
				"carries status.phase=authored-by-composition, and the object cleanupForDiff returns does " +
				"not. Deciding whether status belongs in diff output is a rendering-design question well " +
				"outside a test task, so this is left failing-by-skip with its instrument intact rather " +
				"than quietly reworded into an assertion that passes.",
			Reason:       "A status written by the composition pipeline survives the dry-run create of an added resource",
			OutputFormat: "json",
			InputFiles:   []string{"../../../internal/testutils/integration/testdata/diff/new-xr.yaml"},
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"testdata/diff/resources/composition-with-status.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			ExpectedStructured: simtest.Expect().
				WithSummary(2, 0, 0).
				WithAddedResource("XStatusResource", "test-resource", "default").
				WithDryRunPerformed().
				WithField("status.phase", "authored-by-composition").
				WithField("spec.forProvider.configData", "new-value").
				And().
				WithAddedResource("XNopResource", "test-resource", "default").
				WithField("spec.coolField", "new-value"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		// The apiserver runs names.Generator in rest.BeforeCreate, ahead of the dry-run
		// short-circuit at the storage layer, so it mints a real random name even for a request it
		// never persists. Unless that identity is restored, a generateName addition's diff differs on
		// every invocation — which only a repeated run can detect, hence assertDeterministicAcrossRuns.
		"GenerateNameAdditionIsDeterministicAcrossRuns": {
			Reason:                        "An addition using generateName renders identically across two runs, i.e. no server-generated name leaks into the diff",
			OutputFormat:                  "json",
			DryRunOn:                      "all",
			AssertDeterministicAcrossRuns: true,
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"testdata/diff/resources/generated-name-composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			InputFiles: []string{"testdata/diff/generated-name-xr.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(2, 0, 0).
				// Both the XR and its composed resource use generateName, so both exercise the
				// identity restore. The pattern is the renderer's display form for a
				// not-yet-named object; a leaked server name would be a random suffix instead.
				WithAddedResource("XDownstreamResource", "", "default").
				WithDryRunPerformed().
				WithNamePattern(`^test-resource-\(generated\)$`).
				WithField("spec.forProvider.configData", "new-value").
				And().
				WithAddedResource("XNopResource", "", "default").
				WithDryRunPerformed().
				WithNamePattern(`^generated-xr-\(generated\)$`).
				WithField("spec.coolField", "new-value"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		// Cluster rejection of an ADDITION. A ValidatingAdmissionPolicy is the instrument rather than a CEL
		// x-kubernetes-validations rule on the CRD, because crossplane-diff evaluates CEL rules
		// locally before the diff calculator runs: a CEL rule would reject the resource without the
		// apiserver ever being consulted, so the test would pass on code that never dry-ran anything.
		"AddedResourceRejectedByAdmissionPolicyIsAValidationFailure": {
			Reason:       "An apiserver rejection of an added resource is exit 2 with a typed admission validation failure, not a bare tool error",
			OutputFormat: "json",
			InputFiles:   []string{"../../../internal/testutils/integration/testdata/diff/new-xr.yaml"},
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
				"testdata/diff/resources/rejecting-admission-policy.yaml",
			},
			AwaitAdmissionDenialFor: "testdata/diff/resources/admission-policy-canary.yaml",
			ExpectedError:           true,
			ExpectedExitCode:        dp.ExitCodeSchemaValidation,
			ExpectedStructured: simtest.Expect().
				// errors[].resourceID names the input the user supplied, so the rejected composed
				// resource has to be identifiable from the message and the validationFailures entry.
				WithError("XNopResource/test-resource").
				WithMessageContaining("the cluster rejected XDownstreamResource/test-resource").
				WithValidationFailure("ns.nop.example.org/v1alpha1", "XDownstreamResource", "test-resource", "default").
				WithStatus("invalid").
				// The "admission" type is what distinguishes a cluster rejection from a local schema
				// rejection; field is empty because an admission refusal is not localized to one field.
				WithFieldError("admission", "").
				WithMessageContaining("this cluster refuses that configData value"),
		},
		// Cluster rejection of a MODIFICATION — the deliberate behaviour change. The identical cluster fact used to
		// surface as a plain tool error (exit 1) for an existing resource while an addition's rejection
		// would have been exit 2, i.e. the same rejection reported in two different tiers depending
		// only on whether the resource happened to exist. This pins the reclassification.
		"ModifiedResourceRejectedByAdmissionPolicyIsAValidationFailure": {
			Reason:       "An apiserver rejection of an EXISTING resource's dry-run apply is reported in the same tier as a rejected addition",
			OutputFormat: "json",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
				"testdata/diff/resources/rejecting-admission-policy.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"../../../internal/testutils/integration/testdata/diff/resources/existing-xr.yaml",
					"../../../internal/testutils/integration/testdata/diff/resources/existing-downstream-resource.yaml",
				),
			},
			AwaitAdmissionDenialFor: "testdata/diff/resources/admission-policy-canary.yaml",
			InputFiles:              []string{"../../../internal/testutils/integration/testdata/diff/modified-xr.yaml"},
			ExpectedError:           true,
			ExpectedExitCode:        dp.ExitCodeSchemaValidation,
			ExpectedStructured: simtest.Expect().
				WithError("XNopResource/test-resource").
				WithMessageContaining("the cluster rejected XDownstreamResource/test-resource").
				WithValidationFailure("ns.nop.example.org/v1alpha1", "XDownstreamResource", "test-resource", "default").
				WithStatus("invalid").
				WithFieldError("admission", "").
				WithMessageContaining("this cluster refuses that configData value"),
		},
		"MultipleXRsGroupedByInputXR": {
			Reason:       "Two input XRs in one invocation are grouped per input XR in the xrs[] structured view",
			OutputFormat: "json",
			InputFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/new-xr.yaml",
				"testdata/diff/new-xr-second.yaml",
			},
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			ExpectedStructured: simtest.Expect().
				// Aggregate flat view still reports the merged total (2 adds per XR).
				WithSummary(4, 0, 0).
				// Grouped view: one XR(...) sub-expression per input XR, each with
				// its own changed tree.
				WithXRs(
					simtest.XR("XNopResource", "test-resource", "default").
						Status("changed").
						Summary(2, 0, 0).
						Change("added", "XNopResource", "test-resource", "default").
						WithField("spec.coolField", "new-value").
						Change("added", "XDownstreamResource", "test-resource", "default").
						WithField("spec.forProvider.configData", "new-value"),
					simtest.XR("XNopResource", "second-resource", "default").
						Status("changed").
						Summary(2, 0, 0).
						Change("added", "XNopResource", "second-resource", "default").
						WithField("spec.coolField", "second-value").
						Change("added", "XDownstreamResource", "second-resource", "default").
						WithField("spec.forProvider.configData", "second-value"),
				).
				Build(),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		"MultipleXRsGroupedByInputXRV2": {
			Reason:       "The composites[] entries group each input's changes",
			OutputFormat: "json",
			InputFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/new-xr.yaml",
				"testdata/diff/new-xr-second.yaml",
			},
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			ExpectedStructured: simtest.Expect().
				// The top-level summary counts distinct resources.
				WithSummary(4, 0, 0).
				WithXRs(
					simtest.XR("XNopResource", "test-resource", "default").
						Status("changed").
						Summary(2, 0, 0).
						Change("added", "XNopResource", "test-resource", "default").
						WithField("spec.coolField", "new-value").
						Change("added", "XDownstreamResource", "test-resource", "default").
						WithField("spec.forProvider.configData", "new-value"),
					simtest.XR("XNopResource", "second-resource", "default").
						Status("changed").
						Summary(2, 0, 0).
						Change("added", "XNopResource", "second-resource", "default").
						WithField("spec.coolField", "second-value").
						Change("added", "XDownstreamResource", "second-resource", "default").
						WithField("spec.forProvider.configData", "second-value"),
				),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		"ModifiedResourceDiff": {
			Reason: "Shows color diff for modified resources",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			// put an existing XR in the cluster to diff against
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"../../../internal/testutils/integration/testdata/diff/resources/existing-xr.yaml",
					"../../../internal/testutils/integration/testdata/diff/resources/existing-downstream-resource.yaml",
				),
			},
			InputFiles: []string{"../../../internal/testutils/integration/testdata/diff/modified-xr.yaml"},
			ExpectedOutput: `
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
` + tu.Red("-     configData: existing-value") + `
` + tu.Green("+     configData: modified-value") + `

---
~~~ XNopResource/test-resource
  apiVersion: ns.diff.example.org/v1alpha1
  kind: XNopResource
  metadata:
    labels:
      crossplane.io/composite: test-resource
    name: test-resource
    namespace: default
  spec:
` + tu.Red("-   coolField: existing-value") + `
` + tu.Green("+   coolField: modified-value") + `
    crossplane:
      compositionRef:
        name: xnopresources.diff.example.org
      compositionRevisionRef:
        name: xnopresources.diff.example.org-b616215
      compositionUpdatePolicy: Automatic

---

Summary: 2 modified`,
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		"IgnorePathsArgoCD": {
			Reason: "Ignores ArgoCD annotations and labels when --ignore-paths is specified",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			// put an existing resource with different ArgoCD annotations
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/diff/resources/existing-xr-with-argocd.yaml",
					"testdata/diff/resources/existing-downstream-resource-with-argocd.yaml",
				),
			},
			InputFiles: []string{"testdata/diff/xr-with-argocd-annotations.yaml"},
			IgnorePaths: []string{
				"metadata.annotations[argocd.argoproj.io/tracking-id]",
				"metadata.labels[argocd.argoproj.io/instance]",
			},
			OutputFormat:       "json",
			ExpectedStructured: simtest.Expect().WithSummary(0, 0, 0),
			ExpectedError:      false,
		},
		"IgnorePathsMixedChanges": {
			// Regression test: when a resource has BOTH ignored path changes
			// AND non-ignored spec changes, the summary must count the
			// resource as modified once and the emitted diff bodies must not
			// leak the ignored paths. The renderer-level unit test
			// TestStructuredDiffRenderer_RespectsIgnorePaths asserts the
			// absence of ignored fields in diff.old / diff.new; this test
			// pins the summary counts and the visible non-ignored changes
			// through the full CLI stack.
			Reason: "Ignores ArgoCD annotations/labels while still reporting a non-ignored spec change",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/diff/resources/existing-xr-with-argocd.yaml",
					"testdata/diff/resources/existing-downstream-resource-with-argocd.yaml",
				),
			},
			InputFiles: []string{"testdata/diff/xr-with-argocd-mixed-changes.yaml"},
			IgnorePaths: []string{
				"metadata.annotations[argocd.argoproj.io/tracking-id]",
				"metadata.labels[argocd.argoproj.io/instance]",
			},
			OutputFormat: "json",
			ExpectedStructured: simtest.Expect().
				WithSummary(0, 2, 0).
				WithModifiedResource("XDownstreamResource", "test-resource", "default").
				WithFieldChange("spec.forProvider.configData", "new-value", "modified-value").
				And().
				WithModifiedResource("XNopResource", "test-resource", "default").
				WithFieldChange("spec.coolField", "new-value", "modified-value"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		"ModifiedXRCreatesDownstream": {
			Reason:       "Shows diff when modified XR creates new downstream resource",
			OutputFormat: "json",
			// The XR has its composition refs but no composed resource yet: Crossplane selects the
			// composition and revision before it composes, so this is an XR whose first compose has not
			// created its resource (a function error, say). That is what makes the resource an addition.
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/existing-xr.yaml",
			},
			InputFiles: []string{"../../../internal/testutils/integration/testdata/diff/modified-xr.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(1, 1, 0).
				WithAddedResource("XDownstreamResource", "test-resource", "default").
				WithField("spec.forProvider.configData", "modified-value").
				And().
				WithModifiedResource("XNopResource", "test-resource", "default").
				WithFieldChange("spec.coolField", "existing-value", "modified-value"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		// Issue #452, `xr` side, delivered through the warning channel added for #459. Unlike `comp`,
		// which excludes deleting XRs from impact analysis, `xr` diffs exactly the resource the user
		// named — so it still emits the diff and raises an advisory. This asserts BOTH halves of the
		// dual emission: the stderr line for humans and the warnings[] entry for machines.
		"DeletingXRWarnsButStillDiffs": {
			Reason:       "xr against an XR whose cluster copy is being deleted still emits the diff, plus a warning on stderr and in warnings[]",
			OutputFormat: "json",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			// A Terminating XR still has the resource it composed until garbage collection removes it.
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes("testdata/diff/resources/existing-xr-deleting.yaml", "../../../internal/testutils/integration/testdata/diff/resources/existing-downstream-resource.yaml"),
			},
			DeleteAfterSetup: []string{"testdata/diff/resources/existing-xr-deleting.yaml"},
			InputFiles:       []string{"../../../internal/testutils/integration/testdata/diff/modified-xr.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(0, 2, 0).
				WithModifiedResource("XDownstreamResource", "test-resource", "default").
				WithFieldChange("spec.forProvider.configData", "existing-value", "modified-value").
				And().
				WithModifiedResource("XNopResource", "test-resource", "default").
				WithFieldChange("spec.coolField", "existing-value", "modified-value").
				And().
				WithWarning("being deleted in the cluster"),
			ExpectedStderrContains: []string{
				"WARNING: The resource being diffed is being deleted in the cluster",
				"resource=XNopResource/test-resource",
			},
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		// Issue #495: the XR still references a composed resource that was deleted out of band. The
		// resource tree records that child as NotFound, which is tolerated: the resource really is
		// gone, so it is neither observed nor a removal — but the user is told. The other per-node
		// errors (Forbidden, transport) are fatal; envtest grants the harness everything, so those are
		// covered by the FetchObservedResources unit tests instead.
		"ComposedResourceDeletedOutOfBandWarns": {
			Reason:       "xr against an XR whose resourceRefs name a deleted composed resource omits it from observed state and warns",
			OutputFormat: "json",
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				{
					OwnerFile: "testdata/diff/resources/existing-removal-test-xr.yaml",
					OwnedFiles: map[string]*integration.HierarchicalOwnershipRelation{
						"../../../internal/testutils/integration/testdata/diff/resources/removal-test-ns-downstream-resource1.yaml": nil,
						"../../../internal/testutils/integration/testdata/diff/resources/removal-test-ns-downstream-resource2.yaml": nil,
					},
				},
			},
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/removal-test-composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			DeleteAfterSetup: []string{"../../../internal/testutils/integration/testdata/diff/resources/removal-test-ns-downstream-resource2.yaml"},
			InputFiles:       []string{"../../../internal/testutils/integration/testdata/diff/unmodified-xr.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(0, 0, 0).
				WithWarning("was not found in the cluster").
				WithWarningContext(map[string]string{
					"resource":  "XDownstreamResource/resource-to-be-removed",
					"namespace": "default",
				}),
			ExpectedStderrContains: []string{
				"WARNING: A composed resource referenced by its XR was not found in the cluster",
				"resource=XDownstreamResource/resource-to-be-removed",
			},
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeSuccess,
		},
		"EnvironmentConfigIncorporation": {
			Reason:       "Validates EnvironmentConfig (v1beta1) incorporation in diff",
			OutputFormat: "json",
			SetupFiles: []string{
				"testdata/diff/resources/xdownstreamenvresource-xrd.yaml",
				"testdata/diff/resources/env-xrd.yaml",
				"testdata/diff/resources/env-composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
				"testdata/diff/resources/environment-config-v1beta1.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/diff/resources/existing-env-xr.yaml",
					"testdata/diff/resources/existing-env-downstream-resource.yaml",
				),
			},
			XrdAPIVersion: integration.XRDV1, // the XRD is v1, so Crossplane writes the XR's resourceRefs at spec.resourceRefs
			InputFiles:    []string{"testdata/diff/modified-env-xr.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(0, 2, 0).
				WithModifiedResource("XDownstreamEnvResource", "test-env-resource", "").
				WithFieldChange("spec.forProvider.configData", "existing-config-value", "modified-config-value").
				And().
				WithModifiedResource("XEnvResource", "test-env-resource", "").
				WithFieldChange("spec.configKey", "existing-config-value", "modified-config-value"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		"ExternalResourceDependencies": {
			Reason:       "Validates diff with external resource dependencies via fn-external-resources",
			OutputFormat: "json",
			// this test does a weird thing where it changes the XR but all the downstream changes come from external
			// resources, including a field path from the XR itself.
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
				"testdata/diff/resources/external-resource-configmap.yaml",
				"testdata/diff/resources/external-res-fn-composition.yaml",
				"testdata/diff/resources/external-named-clusterrole.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/diff/resources/existing-xr-with-external-dep.yaml",
					"testdata/diff/resources/existing-downstream-with-external-dep.yaml",
				),
			},
			InputFiles: []string{"testdata/diff/modified-xr-with-external-dep.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(0, 2, 0).
				WithModifiedResource("XDownstreamResource", "test-resource", "default").
				WithFieldChange("spec.forProvider.configData", "existing-value", "testing-external-resource-data").
				WithFieldChange("spec.forProvider.roleName", "old-role-name", "external-named-clusterrole").
				And().
				WithModifiedResource("XNopResource", "test-resource", "default").
				WithFieldChange("spec.coolField", "existing-value", "modified-with-external-dep").
				WithFieldChange("spec.environment", "staging", "testing"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		// Same-namespace case: the matchLabels ExtraResources selector omits the
		// namespace, and the matched ConfigMap lives in "default" — the SAME
		// namespace as the XR. This passed even before the #376 fix, because the
		// old resolveNamespace defaulted an empty selector namespace to the XR
		// namespace, which happened to be where the ConfigMap was. See
		// "TemplatedExtraResourcesCrossNamespace" for the cross-namespace case
		// that requires the all-namespaces lookup.
		"TemplatedExtraResourcesSameNamespace": {
			Reason:       "Validates diff with templated ExtraResources (matchLabels, no namespace) resolving a resource in the XR's own namespace",
			OutputFormat: "json",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
				"testdata/diff/resources/external-resource-configmap.yaml",
				"testdata/diff/resources/external-res-gotpl-composition.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/diff/resources/existing-xr-with-external-dep-templated.yaml",
					"testdata/diff/resources/existing-downstream-with-external-dep.yaml",
				),
			},
			InputFiles: []string{"testdata/diff/modified-xr-with-external-dep.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(0, 2, 0).
				WithModifiedResource("XDownstreamResource", "test-resource", "default").
				WithFieldChange("spec.forProvider.configData", "existing-value", "modified-with-external-dep").
				WithFieldChange("spec.forProvider.roleName", "old-role-name", "templated-external-resource-testing").
				And().
				WithModifiedResource("XNopResource", "test-resource", "default").
				WithFieldChange("spec.coolField", "existing-value", "modified-with-external-dep").
				WithFieldChange("spec.environment", "staging", "testing"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		// Reproduction for crossplane-contrib/crossplane-diff#376: a matchLabels
		// ExtraResources selector that omits the namespace is a cluster-wide
		// (all-namespaces) lookup in Crossplane (internal/xfn/required_resources.go
		// lists with client.InNamespace(rs.GetNamespace()); empty = all
		// namespaces). Here the matched ConfigMap lives in "other-namespace" while
		// the XR is in "default". Pre-fix, resolveNamespace defaulted the empty
		// selector namespace to the XR namespace ("default"), so the dynamic
		// client listed only "default", found nothing, and the go-templating
		// requirement came back empty — the downstream XDownstreamResource would
		// not render at all (or a real template reading the value would fatal with
		// ValueError). Post-fix the lookup spans all namespaces, finds the
		// ConfigMap, and the downstream renders with roleName derived from it.
		"TemplatedExtraResourcesCrossNamespace": {
			Reason:       "matchLabels ExtraResources (no namespace) must resolve a resource in a DIFFERENT namespace than the XR via an all-namespaces lookup (issue #376)",
			OutputFormat: "json",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
				"testdata/diff/resources/external-resource-configmap-other-namespace.yaml",
				"testdata/diff/resources/external-res-gotpl-composition.yaml",
			},
			InputFiles: []string{"testdata/diff/new-xr-with-cross-ns-external-dep.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(2, 0, 0).
				WithAddedResource("XDownstreamResource", "test-resource", "default").
				WithField("spec.forProvider.configData", "new-value").
				// roleName is templated from the matched ConfigMap's name, proving
				// the cross-namespace resource was resolved.
				WithField("spec.forProvider.roleName", "templated-external-resource-crossns").
				And().
				WithAddedResource("XNopResource", "test-resource", "default").
				WithField("spec.coolField", "new-value").
				WithField("spec.environment", "crossns"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		"TemplatedExtraResourcesMatchNameNotFound": {
			// Regression test for crossplane-contrib/crossplane-diff#355: a
			// go-templating ExtraResources block using matchName for a
			// resource that does not exist in the cluster must NOT abort the
			// diff. Pre-fix, the iterative render loop received an error
			// from requirements_provider.processNameSelector and surfaced it
			// as a per-XR failure. Post-fix, the unmet requirement is silently
			// skipped (mirroring upstream xfn.required_resources.go) and the
			// composition renders its downstream resource normally.
			Reason:       "matchName requirement for a missing resource must not abort the diff (issue #355)",
			OutputFormat: "json",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
				"testdata/diff/resources/external-res-matchname-gotpl-composition.yaml",
			},
			InputFiles: []string{"../../../internal/testutils/integration/testdata/diff/new-xr.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(2, 0, 0).
				WithAddedResource("XDownstreamResource", "test-resource", "default").
				WithField("spec.forProvider.configData", "new-value").
				WithField("spec.forProvider.roleName", "static-role").
				And().
				WithAddedResource("XNopResource", "test-resource", "default").
				WithField("spec.coolField", "new-value"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		"MultiStepFatalAfterExtraResources": {
			// Reproduces the PR #295 bug #1 scenario: a multi-step pipeline where
			// step 1 successfully emits ExtraResources requirements and composed
			// resources, and a LATER step fatals. Per crossplane/cmd/crank/render/
			// render.go, a fatal in step N still returns the accumulated
			// requirements from step <N. So the render output has:
			//   renderErr != nil AND len(output.Requirements) > 0
			//
			// On iteration 2 (after requirements are cached) newReqCount == 0,
			// and the OLD condition
			//   renderErr != nil && newReqCount == 0 && len(output.Requirements) == 0
			// is false — the error falls through to checkStability which declares
			// the pipeline "stable" on an Outputs with nil CompositeResource,
			// causing a SIGSEGV in CalculateNonRemovalDiffs.
			//
			// Expected behavior (with PR #295's render-loop fix): a clean fatal
			// error bubbles up instead of a segfault. The diff is NOT produced
			// because the pipeline legitimately failed.
			Reason:       "Multi-step pipeline with step-1 requirements + later-step fatal should surface the error cleanly, not SIGSEGV",
			OutputFormat: "json",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
				"testdata/diff/resources/external-resource-configmap.yaml",
				"testdata/diff/resources/multistep-fatal-after-extra-res-composition.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/diff/resources/existing-xr-with-external-dep-multistep-fatal.yaml",
					"testdata/diff/resources/existing-downstream-with-external-dep.yaml",
				),
			},
			InputFiles:            []string{"testdata/diff/modified-xr-with-external-dep.yaml"},
			ExpectedError:         true,
			ExpectedErrorContains: `"always-fatal" returned a fatal result`,
			ExpectedExitCode:      dp.ExitCodeToolError,
		},
		"UnguardedTemplatedExtraResources": {
			// Regression guard: a user template that accesses .extraResources
			// directly (e.g. `index .extraResources "configmaps"`) without a
			// `{{- with .extraResources }}` guard fails fatally on the first
			// function invocation. function-go-templating's template data has no
			// "extraResources" entry on that first call — crossplane's
			// FetchingFunctionRunner only populates req.ExtraResources AFTER a
			// function returns a non-fatal rsp.Requirements, and the Fatal from
			// this template short-circuits before that ever happens.
			//
			// Verified empirically (2026-04-29) against a kind cluster with
			// Crossplane + function-go-templating v0.11.0: a live controller
			// produces the identical error, and the XR stays Synced=False with
			// ReconcileError. So crossplane-diff must surface the same fatal
			// error — not mask it with a diff — to match production behavior.
			//
			// This test also guards against a hypothetical "fixed" prefetch that
			// pre-populated .extraResources for the template: that would make
			// crossplane-diff report a successful diff for a composition that
			// actually fails in-cluster, which is a false positive. If this test
			// ever starts passing with an unexpected exit code or producing a
			// diff, something has diverged from prod behavior.
			Reason:       "Unguarded .extraResources access must surface a fatal error matching what a live Crossplane controller produces (verified empirically against kind cluster on 2026-04-29)",
			OutputFormat: "json",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
				"testdata/diff/resources/external-resource-configmap.yaml",
				"testdata/diff/resources/unguarded-external-res-gotpl-composition.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/diff/resources/existing-xr-with-external-dep-unguarded.yaml",
					"testdata/diff/resources/existing-downstream-with-external-dep.yaml",
				),
			},
			InputFiles:            []string{"testdata/diff/modified-xr-with-external-dep.yaml"},
			ExpectedError:         true,
			ExpectedErrorContains: `<index .extraResources "configmaps">: error calling index: index of untyped nil`,
			ExpectedExitCode:      dp.ExitCodeToolError,
		},
		"CrossNamespaceResourceDependencies": {
			Reason:       "Validates cross-namespace resource dependencies via fn-external-resources",
			OutputFormat: "json",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
				"testdata/diff/resources/cross-namespace-configmap.yaml",
				"testdata/diff/resources/cross-namespace-fn-composition.yaml",
				"testdata/diff/resources/external-named-clusterrole.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/diff/resources/existing-cross-ns-xr.yaml",
					"testdata/diff/resources/existing-cross-ns-downstream.yaml",
				),
			},
			InputFiles: []string{"testdata/diff/modified-cross-ns-xr.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(0, 2, 0).
				WithModifiedResource("XDownstreamResource", "test-cross-ns-resource", "default").
				WithFieldChange("spec.forProvider.configData", "existing-cross-ns-data-existing-named-data-old-cross-ns-role", "cross-namespace-data-another-cross-namespace-data-external-named-clusterrole").
				And().
				WithModifiedResource("XNopResource", "test-cross-ns-resource", "default").
				WithFieldChange("spec.coolField", "existing-cross-ns-value", "modified-cross-ns-value").
				WithFieldChange("spec.environment", "staging", "production"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		"ResourceRemovalHierarchyV1ClusterScoped": {
			Reason:        "Validates resource removal detection with hierarchy using v1 style resourceRefs and cluster scoped downstreams",
			XrdAPIVersion: integration.XRDV1,
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				{
					OwnerFile: "testdata/diff/resources/existing-legacy-xr.yaml",
					OwnedFiles: map[string]*integration.HierarchicalOwnershipRelation{
						"testdata/diff/resources/removal-test-legacycluster-downstream-resource1.yaml": nil, // Will be kept
						"testdata/diff/resources/removal-test-legacy-nested-xr.yaml": {
							// A nested XR, no longer rendered: removed, with what it composes
							OwnedFiles: map[string]*integration.HierarchicalOwnershipRelation{
								"testdata/diff/resources/removal-test-legacy-nested-xr-managed.yaml": nil, // Child will also be removed
							},
						},
					},
				},
			},
			SetupFiles: []string{
				"testdata/diff/resources/legacy-xrd.yaml",
				"testdata/diff/resources/removal-test-legacy-composition.yaml",
				"testdata/diff/resources/removal-test-legacy-child-xrd.yaml",
				"testdata/diff/resources/removal-test-legacy-child-composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			InputFiles: []string{"testdata/diff/modified-legacy-xr.yaml"},
			ExpectedOutput: `
~~~ XDownstreamResource/resource-to-be-kept
  apiVersion: legacycluster.nop.example.org/v1alpha1
  kind: XDownstreamResource
  metadata:
    annotations:
      crossplane.io/composition-resource-name: resource1
    labels:
      crossplane.io/composite: test-resource
    name: resource-to-be-kept
  spec:
    forProvider:
-     configData: existing-value
+     configData: modified-value

---
--- XDownstreamResource/resource-to-be-removed-managed
- apiVersion: legacycluster.nop.example.org/v1alpha1
- kind: XDownstreamResource
- metadata:
-   annotations:
-     crossplane.io/composition-resource-name: managed-resource
-   labels:
-     crossplane.io/composite: test-resource
-   name: resource-to-be-removed-managed
- spec:
-   forProvider:
-     configData: child-value

---
--- XLegacyChildResource/resource-to-be-removed
- apiVersion: legacycluster.diff.example.org/v1alpha1
- kind: XLegacyChildResource
- metadata:
-   annotations:
-     crossplane.io/composition-resource-name: resource2
-   labels:
-     crossplane.io/composite: test-resource
-   name: resource-to-be-removed
- spec:
-   childField: child-value
-   compositionRef:
-     name: xlegacychildresources.legacycluster.diff.example.org
-   compositionRevisionRef:
-     name: xlegacychildresources.legacycluster.diff.example.org-bf2c887
-   compositionUpdatePolicy: Automatic

---
~~~ XNopResource/test-resource
  apiVersion: legacycluster.diff.example.org/v1alpha1
  kind: XNopResource
  metadata:
    labels:
      crossplane.io/composite: test-resource
    name: test-resource
  spec:
    compositionRef:
      name: removal-test-composition
    compositionRevisionRef:
      name: removal-test-composition-4b6f9f1
    compositionUpdatePolicy: Automatic
-   coolField: existing-value
+   coolField: modified-value

---

Summary: 2 modified, 2 removed`,
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			NoColor:          true,
		},
		// Issue #259: function-sequencer hides resources from later stages, but those
		// resources should be shown as removed if they exist in the cluster.
		// https://github.com/crossplane-contrib/crossplane-diff/issues/259
		"FunctionSequencerPreservesExistingResources": {
			Reason: "Function-sequencer should NOT hide resources that already exist in the cluster (fix for issue #259)",
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				{
					OwnerFile: "testdata/diff/resources/existing-sequencer-xr.yaml",
					OwnedFiles: map[string]*integration.HierarchicalOwnershipRelation{
						// Stage 0 resource - exists in cluster
						"testdata/diff/resources/sequencer-stage0-downstream.yaml": nil,
						// Stage 1 resource - exists in cluster, should NOT be hidden by sequencer
						// because observed resources are passed correctly to the function pipeline.
						// Stage 0 has no Ready condition, so the sequencer would gate stage 1 if it
						// were not observed: this case depends on the XR's own composed resources
						// reaching render as observed state.
						"testdata/diff/resources/sequencer-stage1-downstream.yaml": nil,
					},
				},
			},
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"testdata/diff/resources/sequencer-composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			InputFiles:   []string{"testdata/diff/resources/sequencer-xr.yaml"},
			OutputFormat: "json",
			// Expected: With the fix, observed resources ARE passed to function-sequencer.
			// Function-sequencer sees both resources exist and does NOT remove them from desired state.
			// Since existing resources match rendered resources, there should be no changes.
			// XR diff outputs nothing when there are no changes (unlike comp diff which says "No changes detected").
			ExpectedStructured: simtest.Expect().WithSummary(0, 0, 0),
			ExpectedError:      false,
			ExpectedExitCode:   dp.ExitCodeSuccess,
		},
		// Issue #259: Validates that removal detection works even when the XR has no changes.
		// This is the core fix for the function-sequencer bug where existingXR was nil because
		// the XR diff wasn't stored when there were no changes.
		// https://github.com/crossplane-contrib/crossplane-diff/issues/259
		"ResourceRemovalWithUnmodifiedXR": {
			Reason:       "Validates resource removal detection works when XR has no changes (uses existingXRFromCluster fallback)",
			OutputFormat: "json",
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				{
					OwnerFile: "testdata/diff/resources/existing-removal-test-xr.yaml",
					OwnedFiles: map[string]*integration.HierarchicalOwnershipRelation{
						"../../../internal/testutils/integration/testdata/diff/resources/removal-test-ns-downstream-resource1.yaml": nil, // Will be kept
						"testdata/diff/resources/removal-test-nested-xr.yaml": {
							// A nested XR, no longer rendered: removed, with what it composes
							OwnedFiles: map[string]*integration.HierarchicalOwnershipRelation{
								"testdata/diff/resources/removal-test-nested-xr-managed.yaml": nil, // Child will also be removed
							},
						},
					},
				},
			},
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/removal-test-composition.yaml",
				"testdata/diff/resources/nested/child-xrd.yaml",
				"testdata/diff/resources/nested/child-composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			InputFiles: []string{"../../../internal/testutils/integration/testdata/diff/unmodified-xr.yaml"}, // XR has NO changes
			// Expected: the nested XR resource2 and what it composes are removed, but XR and resource1 have no changes
			ExpectedStructured: simtest.Expect().
				WithSummary(0, 0, 2).
				WithRemovedResource("XChildResource", "resource-to-be-removed", "default").
				// dryRun must be absent on a removal. A removal has no desired state to preview, so it can
				// never carry a dryRun object, and one appearing would wrongly mark it unverified; WithDryRunPerformed asserts exactly that absence. The method
				// name reads oddly here — nothing was dry run — but the claim it makes ("no dryRun
				// object is present") is the right one, and having a second spelling for the same
				// assertion would be worse.
				WithDryRunPerformed().
				WithField("spec.childField", "child-value").
				And().
				WithRemovedResource("XDownstreamResource", "resource-to-be-removed-managed", "default").
				WithDryRunPerformed().
				WithField("spec.forProvider.configData", "child-value"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		// #552: removal mirrors Crossplane's garbage collector, which deletes a composed resource only if
		// the composite controls it. The XR's resourceRefs name four resources, and its render desires only
		// the first: one it controls and renders, one it controls and no longer renders, one another XR
		// controls, and one with no controller reference at all. Only the second is removed.
		"ResourceRemovalSkipsResourcesTheXRDoesNotControl": {
			Reason: "A composed resource the XR's resourceRefs name but the XR does not control is not reported removed",
			// What this case pins: test-resource's resourceRefs still name two resources it doesn't control.
			OutputFormat: "json",
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/diff/resources/existing-xr-listing-uncontrolled-resources.yaml",
					"../../../internal/testutils/integration/testdata/diff/resources/removal-test-ns-downstream-resource1.yaml", // still rendered: kept
					"../../../internal/testutils/integration/testdata/diff/resources/removal-test-ns-downstream-resource2.yaml", // no longer rendered: removed
				),
				integration.Composes(
					"../../../internal/testutils/integration/testdata/diff/resources/existing-other-xr.yaml",
					"../../../internal/testutils/integration/testdata/diff/resources/existing-other-xr-downstream.yaml",
				),
			},
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/removal-test-composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
				// Deliberately not in crossplaneManagedResources: its controller reference was removed, so
				// no XR controls it, though test-resource's resourceRefs still name it.
				"../../../internal/testutils/integration/testdata/diff/resources/removal-test-ns-downstream-without-controller.yaml",
			},
			InputFiles: []string{"../../../internal/testutils/integration/testdata/diff/unmodified-xr.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(0, 0, 1).
				WithRemovedResource("XDownstreamResource", "resource-to-be-removed", "default").
				WithField("spec.forProvider.configData", "existing-value"),
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		"ResourceRemovalHierarchyV2Namespaced": {
			Reason:       "Validates resource removal detection with hierarchy using v2 style resourceRefs and namespaced downstreams",
			OutputFormat: "json",
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				{
					OwnerFile: "testdata/diff/resources/existing-removal-test-xr.yaml",
					OwnedFiles: map[string]*integration.HierarchicalOwnershipRelation{
						"../../../internal/testutils/integration/testdata/diff/resources/removal-test-ns-downstream-resource1.yaml": nil, // Will be kept
						"testdata/diff/resources/removal-test-nested-xr.yaml": {
							// A nested XR, no longer rendered: removed, with what it composes
							OwnedFiles: map[string]*integration.HierarchicalOwnershipRelation{
								"testdata/diff/resources/removal-test-nested-xr-managed.yaml": nil, // Child will also be removed
							},
						},
					},
				},
			},
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/removal-test-composition.yaml",
				"testdata/diff/resources/nested/child-xrd.yaml",
				"testdata/diff/resources/nested/child-composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			InputFiles: []string{"../../../internal/testutils/integration/testdata/diff/modified-xr.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(0, 2, 2).
				WithModifiedResource("XDownstreamResource", "resource-to-be-kept", "default").
				WithFieldChange("spec.forProvider.configData", "existing-value", "modified-value").
				And().
				WithRemovedResource("XChildResource", "resource-to-be-removed", "default").
				WithField("spec.childField", "child-value").
				And().
				WithRemovedResource("XDownstreamResource", "resource-to-be-removed-managed", "default").
				WithField("spec.forProvider.configData", "child-value").
				And().
				WithModifiedResource("XNopResource", "test-resource", "default").
				WithFieldChange("spec.coolField", "existing-value", "modified-value"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		"ResourceRemovalHierarchyV2ClusterScoped": {
			Reason:       "Validates resource removal detection with hierarchy using v2 style resourceRefs and cluster scoped downstreams",
			OutputFormat: "json",
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				{
					OwnerFile: "testdata/diff/resources/existing-cluster-xr.yaml",
					OwnedFiles: map[string]*integration.HierarchicalOwnershipRelation{
						"testdata/diff/resources/removal-test-cluster-downstream-resource1.yaml": nil, // Will be kept
						"testdata/diff/resources/removal-test-nested-xr.yaml": {
							// A nested XR, no longer rendered: removed, with what it composes
							OwnedFiles: map[string]*integration.HierarchicalOwnershipRelation{
								"testdata/diff/resources/removal-test-nested-xr-managed.yaml": nil, // Child will also be removed
							},
						},
					},
				},
			},
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/cluster-xrd.yaml",
				"testdata/diff/resources/removal-test-cluster-composition.yaml",
				"testdata/diff/resources/nested/child-xrd.yaml",
				"testdata/diff/resources/nested/child-composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			InputFiles: []string{"testdata/diff/modified-cluster-xr.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(0, 2, 2).
				WithModifiedResource("XDownstreamResource", "resource-to-be-kept", "").
				WithFieldChange("spec.forProvider.configData", "existing-value", "modified-value").
				And().
				WithRemovedResource("XChildResource", "resource-to-be-removed", "default").
				WithField("spec.childField", "child-value").
				And().
				WithRemovedResource("XDownstreamResource", "resource-to-be-removed-managed", "default").
				WithField("spec.forProvider.configData", "child-value").
				And().
				WithModifiedResource("XNopResource", "test-resource", "").
				WithFieldChange("spec.coolField", "existing-value", "modified-value"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		"ResourceWithGenerateName": {
			Reason:       "Validates handling of resources with generateName",
			OutputFormat: "json",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
				"testdata/diff/resources/generated-name-composition.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				{
					// Set up the XR as the owner of the generated composed resource
					OwnerFile: "testdata/diff/resources/existing-generate-name-xr.yaml",
					OwnedFiles: map[string]*integration.HierarchicalOwnershipRelation{
						// This file has a generated name and is owned by the XR
						"testdata/diff/resources/existing-downstream-with-generated-name.yaml": nil,
					},
				},
			},
			// Use a composition that uses generateName for composed resources
			InputFiles: []string{"../../../internal/testutils/integration/testdata/diff/new-xr.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(0, 2, 0).
				WithModifiedResource("XDownstreamResource", "test-resource-abc123", "default").
				WithFieldChange("spec.forProvider.configData", "existing-value", "new-value").
				And().
				WithModifiedResource("XNopResource", "test-resource", "default").
				WithFieldChange("spec.coolField", "existing-value", "new-value"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		// The generateName counterpart of NewXRWithExistingComposedResource. A new XR renders its composed
		// resource with only a generateName, and nothing is observed for it, so no name identifies the
		// existing resource: only its crossplane.io/composite label and composition-resource-name
		// annotation can. ResourceWithGenerateName cannot reach that lookup, because there the XR exists and
		// render names the resource after the observed one.
		"NewXRAdoptsOrphanedGenerateNamedComposedResource": {
			Reason:       "A new XR's generateName composed resource is matched to an existing orphan by its composite label",
			OutputFormat: "json",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
				"testdata/diff/resources/generated-name-composition.yaml",
				// Deliberately not in crossplaneManagedResources: an orphan has no controller
				// ownerReference (the garbage collector strips it when the XR is deleted with
				// --cascade=orphan) and no XR lists it, but it keeps its labels and annotations.
				"testdata/diff/resources/existing-downstream-with-generated-name.yaml",
			},
			InputFiles: []string{"../../../internal/testutils/integration/testdata/diff/new-xr.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(1, 1, 0).
				WithAddedResource("XNopResource", "test-resource", "default").
				And().
				WithModifiedResource("XDownstreamResource", "test-resource-abc123", "default").
				WithFieldChange("spec.forProvider.configData", "existing-value", "new-value"),
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		// A generateName-only input next to an ordinary one is an ordinary invocation. The unnamed input
		// has no identity, so input validation must not mistake it for the named XR's claim: an XR with
		// no claimRef has an empty reference identity too, and the two must not match.
		"GenerateNameInputAlongsideANamedInput": {
			Reason:       "A generateName-only input beside a named one is valid; both are diffed",
			OutputFormat: "json",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			InputFiles:       []string{"../../../internal/testutils/integration/testdata/diff/new-xr.yaml", "testdata/diff/generated-name-xr.yaml"},
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			ExpectedStructured: simtest.Expect().
				WithSummary(4, 0, 0).
				WithXRs(
					simtest.XR("XNopResource", "test-resource", "default").Status("changed").Summary(2, 0, 0),
					simtest.XR("XNopResource", "generated-xr-(generated)", "default").Status("changed").Summary(2, 0, 0),
				),
		},
		"NewXRWithGenerateName": {
			Reason:       "Shows diff for new XR with generateName",
			OutputFormat: "json",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
				// We don't add any existing XR, as we're testing creation of a new one
			},
			InputFiles: []string{"testdata/diff/generated-name-xr.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(2, 0, 0).
				WithAddedResource("XDownstreamResource", "", "default").
				WithNamePattern(`generated-xr-\(generated\)`).
				WithField("spec.forProvider.configData", "new-value").
				And().
				WithAddedResource("XNopResource", "", "default").
				WithNamePattern(`generated-xr-\(generated\)`).
				WithField("spec.coolField", "new-value").
				And().
				// Issue #477: the grouped view must attribute these changes to a
				// named XR. metadata.name is empty on the input, so the identity
				// carries the effective (synthesized) name — the same one the
				// changes above are keyed by.
				WithXRs(
					simtest.XR("XNopResource", "generated-xr-(generated)", "default").
						Status("changed").
						Summary(2, 0, 0),
				),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		// Issue #476. The same file passed twice — a fat-fingered command line, or CI enumerating one
		// file twice — has one clear intent. It is diffed once, with a warning, and the run succeeds.
		"IdenticalDuplicateInputIsDeduplicated": {
			Reason:       "An input identical to an earlier one is diffed once, with a warning, not rejected",
			OutputFormat: "json",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			InputFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/new-xr.yaml",
				"../../../internal/testutils/integration/testdata/diff/new-xr.yaml",
			},
			ExpectedExitCode:       dp.ExitCodeDiffDetected,
			ExpectedStderrContains: []string{"Ignoring a duplicate input: it is identical to an earlier one"},
			// The warning channel's contract is both halves: stderr above, and warnings[] here.
			ExpectedStructured: simtest.Expect().
				WithSummary(2, 0, 0).
				WithXRs(simtest.XR("XNopResource", "test-resource", "default").Status("changed").Summary(2, 0, 0)).
				WithWarning("Ignoring a duplicate input: it is identical to an earlier one").
				WithWarningContext(map[string]string{"resource": "XNopResource/test-resource", "input": "2", "duplicateOf": "1"}),
		},
		// The same object twice with different content fails loudly: applying both would leave whichever
		// is applied last, and the order inputs are given in is not a statement of intent.
		"SameObjectTwiceWithDifferentContentFails": {
			Reason: "One object given twice with different content is an input error on both inputs",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			InputFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/new-xr.yaml",
				"../../../internal/testutils/integration/testdata/diff/modified-xr.yaml",
			},
			ExpectedError:         true,
			ExpectedErrorContains: "input 2 defines XNopResource/test-resource differently from input 1",
			ExpectedExitCode:      dp.ExitCodeToolError,
		},
		// A child XR passed alongside the parent whose composition renders it: the parent's composition
		// writes that child, so the child input would be a second writer. Rejected on the child; the
		// parent's own diff is unaffected.
		"InputComposedByAnotherInputIsRejected": {
			Reason: "An XR supplied alongside the input whose composition composes it is an input error",
			SetupFiles: []string{
				"testdata/diff/resources/nested/parent-xrd.yaml",
				"testdata/diff/resources/nested/child-xrd.yaml",
				"testdata/diff/resources/nested/parent-composition.yaml",
				"testdata/diff/resources/nested/child-composition.yaml",
				"testdata/diff/resources/xdownstreamenvresource-xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			InputFiles: []string{
				"testdata/diff/new-nested-xr.yaml",
				"testdata/diff/new-nested-child-xr.yaml",
			},
			ExpectedError: true,
			ExpectedErrorContains: "XChildResource/test-parent-child is composed by input XParentResource/test-parent, " +
				"whose composition writes it",
			ExpectedExitCode: dp.ExitCodeToolError,
		},
		// Issue #476. Two distinct XRs whose composition gives one child a fixed name render the same
		// object — here with byte-identical content. Crossplane gives it to whichever XR creates it first
		// and the other fails to reconcile it, so the run must fail however alike the renderings are.
		// This depends on real renders carrying the controller reference; unit tests build that by hand.
		"TwoXRsComposingOneObjectContend": {
			Reason: "Two XRs that would both control one object fail, even when their renderings are identical",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/shared-child-composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			InputFiles:       []string{"../../../internal/testutils/integration/testdata/diff/shared-child-xrs.yaml"},
			ExpectedError:    true,
			ExpectedExitCode: dp.ExitCodeToolError,
			ExpectedErrorContains: `resource "ns.nop.example.org/v1alpha1/XDownstreamResource/default/shared-child" would be ` +
				`controlled by more than one XR (XNopResource/shared-child-owner-a, XNopResource/shared-child-owner-b)`,
		},
		// Reproduces the PR #294 scenario: a net-new XR is diffed while the
		// composed resource it would manage already exists in the cluster
		// (e.g. after the prior XR was deleted with --cascade=orphan, or while
		// importing existing resources under Crossplane management). The render
		// pipeline emits ownerReferences with an empty UID for new XRs;
		// without the fallback in CalculateNonRemovalDiffs, UpdateOwnerRefs
		// runs as a no-op and the real apiserver rejects the dry-run apply
		// with "metadata.ownerReferences.uid: must not be empty".
		"NewXRWithExistingComposedResource": {
			Reason:       "Shows diff for new XR whose composed resource already exists in the cluster (PR #294)",
			OutputFormat: "json",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
				// Pre-existing composed resource; no backing XR in the cluster. Deliberately not in
				// crossplaneManagedResources: an orphaned resource has no controller ownerReference (the
				// garbage collector strips it when the XR is deleted with --cascade=orphan), and adopting
				// exactly such a resource is what this case tests.
				"../../../internal/testutils/integration/testdata/diff/resources/existing-downstream-resource.yaml",
			},
			InputFiles: []string{"../../../internal/testutils/integration/testdata/diff/new-xr.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(1, 1, 0).
				WithAddedResource("XNopResource", "test-resource", "default").
				WithField("spec.coolField", "new-value").
				And().
				WithModifiedResource("XDownstreamResource", "test-resource", "default").
				WithFieldChange("spec.forProvider.configData", "existing-value", "new-value"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		"MultipleXRs": {
			Reason:       "Validates diff for multiple XRs",
			OutputFormat: "json",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			// Add an existing XR and downstream resource to test modification
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"../../../internal/testutils/integration/testdata/diff/resources/existing-xr.yaml",
					"../../../internal/testutils/integration/testdata/diff/resources/existing-downstream-resource.yaml",
				),
			},
			InputFiles: []string{
				"testdata/diff/first-xr.yaml",
				"../../../internal/testutils/integration/testdata/diff/modified-xr.yaml",
			},
			ExpectedStructured: simtest.Expect().
				WithSummary(2, 2, 0).
				WithAddedResource("XDownstreamResource", "first-resource", "default").
				WithField("spec.forProvider.configData", "first-value").
				And().
				WithModifiedResource("XDownstreamResource", "test-resource", "default").
				WithFieldChange("spec.forProvider.configData", "existing-value", "modified-value").
				And().
				WithAddedResource("XNopResource", "first-resource", "default").
				WithField("spec.coolField", "first-value").
				And().
				WithModifiedResource("XNopResource", "test-resource", "default").
				WithFieldChange("spec.coolField", "existing-value", "modified-value"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		"LeastPrivilegeCredentialsDiffSucceeds": {
			Reason: "A user who can only get Crossplane's types, not list them, gets the admin's diff of a new and a modified XR",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"../../../internal/testutils/integration/testdata/diff/resources/existing-xr.yaml",
					"../../../internal/testutils/integration/testdata/diff/resources/existing-downstream-resource.yaml",
				),
			},
			// The new XR names its composition: one that names none is matched against every Composition,
			// which takes a list of them.
			InputFiles:   []string{"testdata/diff/first-xr-with-composition-ref.yaml", "../../../internal/testutils/integration/testdata/diff/modified-xr.yaml"},
			RbacRules:    simharness.LeastPrivilegeRules("get", "create", "patch"),
			OutputFormat: "json",
			ExpectedStructured: simtest.Expect().
				WithSummary(2, 2, 0).
				WithAddedResource("XDownstreamResource", "first-resource", "default").
				WithField("spec.forProvider.configData", "first-value").
				And().
				WithModifiedResource("XDownstreamResource", "test-resource", "default").
				WithFieldChange("spec.forProvider.configData", "existing-value", "modified-value").
				And().
				WithAddedResource("XNopResource", "first-resource", "default").
				WithField("spec.coolField", "first-value").
				And().
				WithModifiedResource("XNopResource", "test-resource", "default").
				WithFieldChange("spec.coolField", "existing-value", "modified-value"),
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		"UnrelatedXRDWithoutCRDDoesNotFailRun": {
			Reason: "An XRD unrelated to the input whose CRD does not exist yet does not affect the diff",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			UnestablishedXRDs: []string{"testdata/diff/resources/unestablished-xrd.yaml"},
			InputFiles:        []string{"../../../internal/testutils/integration/testdata/diff/new-xr.yaml"},
			OutputFormat:      "json",
			// The same diff NewResourceDiff shows, which has no such XRD.
			ExpectedStructured: simtest.Expect().
				WithSummary(2, 0, 0).
				WithAddedResource("XDownstreamResource", "test-resource", "default").
				WithField("spec.forProvider.configData", "new-value").
				And().
				WithAddedResource("XNopResource", "test-resource", "default").
				WithField("spec.coolField", "new-value"),
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		"MissingFunctionFailsWithSameError": {
			Reason: "A pipeline naming a Function the cluster does not have fails, naming the function and its step",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"testdata/diff/resources/composition-with-missing-function.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			InputFiles:            []string{"../../../internal/testutils/integration/testdata/diff/new-xr.yaml"},
			ExpectedError:         true,
			ExpectedErrorContains: `cannot get functions from pipeline: function "function-not-installed" referenced in pipeline step "generate-resources" not found`,
			ExpectedExitCode:      dp.ExitCodeToolError,
		},
		"SelectCompositionByDirectReference": {
			Reason:       "Validates composition selection by direct reference",
			OutputFormat: "json",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
				// Add multiple compositions for the same XR type
				"testdata/diff/resources/default-composition.yaml",
				"testdata/diff/resources/production-composition.yaml",
				"testdata/diff/resources/staging-composition.yaml",
			},
			InputFiles: []string{
				"testdata/diff/xr-with-composition-ref.yaml",
			},
			ExpectedStructured: simtest.Expect().
				WithSummary(2, 0, 0).
				WithAddedResource("XDownstreamResource", "test-resource", "default").
				WithField("spec.forProvider.configData", "test-value").
				WithField("spec.forProvider.resourceTier", "production").
				And().
				WithAddedResource("XNopResource", "test-resource", "default").
				WithField("spec.coolField", "test-value"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		"SelectCompositionByLabelSelector": {
			Reason:       "Validates composition selection by label selector",
			OutputFormat: "json",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
				// Add multiple compositions for the same XR type
				"testdata/diff/resources/default-composition.yaml",
				"testdata/diff/resources/production-composition.yaml",
				"testdata/diff/resources/staging-composition.yaml",
			},
			InputFiles: []string{
				"testdata/diff/xr-with-composition-selector.yaml",
			},
			ExpectedStructured: simtest.Expect().
				WithSummary(2, 0, 0).
				WithAddedResource("XDownstreamResource", "test-resource", "default").
				WithField("spec.forProvider.configData", "test-value").
				WithField("spec.forProvider.resourceTier", "staging").
				And().
				WithAddedResource("XNopResource", "test-resource", "default").
				WithField("spec.coolField", "test-value"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		"AmbiguousCompositionSelection": {
			Reason: "Validates error on ambiguous composition selection",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
				// Add multiple compositions for the same XR type
				"testdata/diff/resources/default-composition.yaml",
				"testdata/diff/resources/production-composition.yaml",
				"testdata/diff/resources/staging-composition.yaml",
			},
			InputFiles: []string{
				"testdata/diff/xr-with-ambiguous-selector.yaml",
			},
			ExpectedError:         true,
			ExpectedExitCode:      dp.ExitCodeToolError,
			ExpectedErrorContains: "ambiguous composition selection: multiple compositions match",
			NoColor:               true,
		},
		"SchemaValidationError": {
			Reason: "Validates exit code 2 for schema validation errors and structured per-resource failure detail in JSON output",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			InputFiles: []string{
				"testdata/diff/invalid-schema-xr.yaml",
			},
			OutputFormat:     "json",
			ExpectedError:    true,
			ExpectedExitCode: dp.ExitCodeSchemaValidation,
			// Structured assertion: lock down the typed
			// validationFailures payload for both the failing XR and
			// its composed resource. Field paths and error types are
			// ours to assert; the embedded k8s message wording is
			// left unasserted (apimachinery upgrades can shift its
			// phrasing without changing our output contract).
			ExpectedStructured: simtest.Expect().
				WithError("XNopResource/invalid-schema-xr").
				WithValidationFailure("ns.diff.example.org/v1alpha1", "XNopResource", "invalid-schema-xr", "default").
				WithStatus("invalid").
				WithFieldError("schema", "spec.coolField").
				AndFieldError().
				AndValidationFailure().
				WithValidationFailure("ns.nop.example.org/v1alpha1", "XDownstreamResource", "invalid-schema-xr", "default").
				WithStatus("invalid").
				WithFieldError("schema", "spec.forProvider.configData").
				AndFieldError().
				AndValidationFailure().
				AndError(),
			NoColor: true,
		},
		"NewClaimShowsDiff": {
			Reason:       "Shows diff for new claim",
			OutputFormat: "json",
			SetupFiles: []string{
				"testdata/diff/resources/existing-namespace.yaml",
				// Add the necessary CRDs and compositions for claim diffing
				"testdata/diff/resources/claim-xrd.yaml",
				"testdata/diff/resources/claim-composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			InputFiles: []string{"testdata/diff/new-claim.yaml"},
			// The claim is defaulted with its own CRD (generated from claim-xrd.yaml), which shares the XRD's
			// user default spec.tier with the XR's CRD but, unlike it, does not default compositionUpdatePolicy.
			ExpectedStructured: simtest.Expect().
				WithSummary(2, 0, 0).
				WithAddedResource("NopClaim", "test-claim", "existing-namespace").
				WithField("spec.coolField", "new-value").
				WithField("spec.tier", "standard").
				WithFieldAbsent("spec.compositionUpdatePolicy").
				And().
				WithAddedResource("XDownstreamResource", "test-claim", "").
				WithField("spec.forProvider.configData", "new-value"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		"NewClaimWithClaimRefComposition": {
			Reason:       "Shows diff for new claim when composition uses spec.claimRef - claimRef is synthesized for new claims",
			OutputFormat: "json",
			SetupFiles: []string{
				"testdata/diff/resources/existing-namespace.yaml",
				"testdata/diff/resources/claim-xrd.yaml",
				"testdata/diff/resources/claim-composition-with-claimref.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			InputFiles: []string{"testdata/diff/new-claim-with-claimref-composition.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(2, 0, 0).
				WithAddedResource("NopClaim", "test-claim", "existing-namespace").
				WithField("spec.coolField", "new-value").
				And().
				WithAddedResource("XDownstreamResource", "test-claim", "").
				WithField("spec.forProvider.configData", "existing-namespace/test-claim"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		"ModifiedClaimShowsDiff": {
			Reason:       "Shows diff for modified claim",
			OutputFormat: "json",
			SetupFiles: []string{
				"testdata/diff/resources/existing-namespace.yaml",
				// Add necessary CRDs and composition
				"testdata/diff/resources/claim-xrd.yaml",
				"testdata/diff/resources/claim-composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
				// Add existing resources for comparison
				"testdata/diff/resources/existing-claim.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/diff/resources/existing-claim-xr.yaml",
					"testdata/diff/resources/existing-claim-downstream-resource.yaml",
				),
			},
			XrdAPIVersion: integration.XRDV1, // the XRD is v1, so Crossplane writes the XR's resourceRefs at spec.resourceRefs
			InputFiles:    []string{"testdata/diff/modified-claim.yaml"},
			ExpectedStructured: simtest.Expect().
				// The composed resource is labelled with the backing XR the claim's resourceRef names, so it
				// already belongs to this claim: no ownership advisory (#534).
				WithNoWarnings().
				WithSummary(0, 2, 0).
				WithModifiedResource("NopClaim", "test-claim", "existing-namespace").
				WithFieldChange("spec.coolField", "existing-value", "modified-value").
				And().
				WithModifiedResource("XDownstreamResource", "test-claim-82crv", "").
				WithFieldChange("spec.forProvider.configData", "existing-value", "modified-value"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		// #503, for a claim. A claim is diffed as the claim itself, so its dry-run payload must carry
		// none of the defaults its CRD declares for fields the manifest omits.
		"XRDDefaultedFieldOnClaimOwnedByAnotherManagerIsNotReportedChanged": {
			Reason:       "An XRD-defaulted field the claim manifest omits keeps another manager's non-default value in the diff",
			OutputFormat: "json",
			SetupFiles: []string{
				"testdata/diff/resources/existing-namespace.yaml",
				"testdata/diff/resources/claim-xrd.yaml",
				"testdata/diff/resources/claim-composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
				"testdata/diff/resources/existing-claim.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/diff/resources/existing-claim-xr.yaml",
					"testdata/diff/resources/existing-claim-downstream-resource.yaml",
				),
			},
			XrdAPIVersion: integration.XRDV1, // the XRD is v1, so Crossplane writes the XR's resourceRefs at spec.resourceRefs
			FieldManagerApplies: []integration.FieldManagerApply{
				{File: "testdata/diff/resources/existing-claim-defaulted-fields-other-manager.yaml", FieldManager: "platform-operator"},
			},
			InputFiles: []string{"testdata/diff/modified-claim-omitting-defaulted-fields.yaml"},
			ExpectedStructured: simtest.Expect().
				// As in ModifiedClaimShowsDiff: the composed resource belongs to the claim's backing XR (#534).
				WithNoWarnings().
				WithSummary(0, 2, 0).
				WithModifiedResource("NopClaim", "test-claim", "existing-namespace").
				WithFieldChange("spec.coolField", "existing-value", "modified-value").
				WithFieldChange("spec.compositeDeletePolicy", "Foreground", "Foreground").
				WithFieldChange("spec.compositionUpdatePolicy", "Manual", "Manual").
				And().
				WithModifiedResource("XDownstreamResource", "test-claim-82crv", "").
				WithFieldChange("spec.forProvider.configData", "existing-value", "modified-value"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		// A claim is defaulted with its own CRD, never its XR's; only the XR that render consumes gets
		// the XR CRD's defaults. The claimnested CRDs mirror what Crossplane generates: both carry the
		// XRD's user default spec.tier, only the claim's defaults spec.compositeDeletePolicy, and only
		// the XR's defaults spec.compositionUpdatePolicy. The composition writes the render input's
		// tier and policy into the composed resource, so each case pins all three views at once: the
		// claim's payload, its predicted or apiserver-defaulted form, and the render input.
		"NewClaimIsDefaultedWithItsOwnCRDWhenDryRunCreated": {
			Reason:       "A new claim shows its own CRD's defaults, from the apiserver, and render sees its XR's",
			OutputFormat: "json",
			SetupFiles: []string{
				"testdata/diff/resources/claim-nested/parent-definition.yaml",
				"testdata/diff/resources/claim-nested/parent-defaults-composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			InputFiles: []string{"testdata/diff/new-parent-claim-omitting-defaulted-fields.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(2, 0, 0).
				WithAddedResource("ParentNopClaim", "new-parent-claim", "default").
				WithDryRunPerformed().
				WithField("spec.tier", "standard").
				WithField("spec.compositeDeletePolicy", "Background").
				WithFieldAbsent("spec.compositionUpdatePolicy").
				And().
				WithAddedResource("XDownstreamResource", "new-parent-claim-defaults", "").
				WithField("spec.forProvider.configData", "standard/Automatic"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		"NewClaimIsPredictedWithItsOwnCRDsDefaultsWithoutDryRun": {
			Reason:       "With --dry-run-on=existing a new claim is predicted with its own CRD's defaults, not its XR's",
			OutputFormat: "json",
			DryRunOn:     "existing",
			SetupFiles: []string{
				"testdata/diff/resources/claim-nested/parent-definition.yaml",
				"testdata/diff/resources/claim-nested/parent-defaults-composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			InputFiles: []string{"testdata/diff/new-parent-claim-omitting-defaulted-fields.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(2, 0, 0).
				WithAddedResource("ParentNopClaim", "new-parent-claim", "default").
				WithDryRunSkipped("disabled", "").
				WithField("spec.tier", "standard").
				WithField("spec.compositeDeletePolicy", "Background").
				WithFieldAbsent("spec.compositionUpdatePolicy").
				And().
				WithAddedResource("XDownstreamResource", "new-parent-claim-defaults", "").
				WithDryRunSkipped("disabled", "").
				WithField("spec.forProvider.configData", "standard/Automatic"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		"XRDDefaultsAppliedBeforeRendering": {
			Reason:       "Validates that XRD defaults are applied to XR before rendering",
			OutputFormat: "json",
			InputFiles:   []string{"testdata/diff/xr-with-missing-defaults.yaml"},
			SetupFiles: []string{
				"testdata/diff/resources/xrd-with-defaults.yaml",
				"testdata/diff/resources/composition-with-defaults.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			ExpectedStructured: simtest.Expect().
				WithSummary(1, 0, 0).
				WithAddedResource("XTestDefaultResource", "test-resource-with-defaults", "default").
				WithField("spec.region", "us-east-1").
				WithField("spec.size", "large"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		// The sibling of XRDDefaultsAppliedBeforeRendering, whose composition renders nothing, so it checks
		// only the XR's dry run. Here a composition reads the composite's defaulted fields into a composed
		// resource: a user default (spec.region) for a v2 and a legacy XR, and for the legacy XR also the
		// machinery default spec.compositionUpdatePolicy. Rendering an undefaulted XR writes "<no value>".
		"XRDDefaultsReachTheRender": {
			Reason:       "Validates that a composition observes an XR's CRD defaults, for both a v2 and a legacy XR",
			OutputFormat: "json",
			SetupFiles: []string{
				"testdata/diff/resources/rendered-defaults/definitions.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			InputFiles: []string{"testdata/diff/new-rendered-defaults-xrs.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(4, 0, 0).
				WithAddedResource("XRegionalDatabase", "modern-database", "default").
				WithField("spec.region", "us-east-1").
				And().
				WithAddedResource("XLegacyRegionalDatabase", "legacy-database", "").
				WithField("spec.region", "eu-west-1").
				And().
				WithAddedResource("XDownstreamResource", "modern-database", "default").
				WithField("spec.forProvider.configData", "us-east-1/large").
				And().
				WithAddedResource("XDownstreamResource", "legacy-database", "").
				WithField("spec.forProvider.configData", "eu-west-1/small/Automatic"),
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		// #503: a composed resource's dry-run apply payload must be exactly what the composition
		// rendered. If it carried a locally-defaulted spec.forProvider.size: small, the apply would
		// claim the field away from the manager that set it to large, predicting a change real
		// Crossplane (which never sends the field) would not make. The composition renders
		// everything else identically, so any modification reported for the composed resource could
		// only have come from the defaulted field.
		"CRDDefaultedFieldOwnedByAnotherManagerIsNotReportedChanged": {
			Reason:       "A CRD-defaulted field the composition does not render keeps another manager's non-default value in the diff",
			OutputFormat: "json",
			InputFiles:   []string{"../../../internal/testutils/integration/testdata/diff/new-xr.yaml"},
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"testdata/diff/resources/composition-with-crd-defaulted-downstream.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			// The composed resource deliberately has no controlling XR, as after its XR was deleted with
			// --cascade=orphan: the XR is new, and what this case pins is the composed resource's field
			// managers, which an orphan keeps. Giving it an XR would make the XR modified rather than new.
			FieldManagerApplies: []integration.FieldManagerApply{
				{
					File:         "testdata/diff/resources/existing-defaulted-downstream-composed.yaml",
					FieldManager: "apiextensions.crossplane.io/composed/2b9d3c4e-0000-4000-8000-000000000503",
				},
				{
					File:         "testdata/diff/resources/existing-defaulted-downstream-other-manager.yaml",
					FieldManager: "platform-operator",
				},
			},
			ExpectedStructured: simtest.Expect().
				WithSummary(1, 0, 0).
				WithAddedResource("XNopResource", "test-resource", "default"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		// #503, for the XR itself. The XR is defaulted before rendering, and must be: the composition
		// has to see the spec Crossplane would. But the XR's own dry-run apply payload must not carry
		// those defaults, or it claims spec.region from the manager that set it to eu-west-1 and reports
		// a change applying the manifest would not make. Nothing is composed, so the XR is the only
		// thing that could be reported changed.
		"XRDDefaultedFieldOwnedByAnotherManagerIsNotReportedChanged": {
			Reason:       "An XRD-defaulted field the XR manifest omits keeps another manager's non-default value in the diff",
			OutputFormat: "json",
			InputFiles:   []string{"testdata/diff/xr-with-missing-defaults.yaml"},
			SetupFiles: []string{
				"testdata/diff/resources/xrd-with-defaults.yaml",
				"testdata/diff/resources/composition-with-defaults.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			FieldManagerApplies: []integration.FieldManagerApply{
				{File: "testdata/diff/xr-with-missing-defaults.yaml", FieldManager: "kubectl"},
				{File: "testdata/diff/resources/existing-xr-with-defaults-reconciled.yaml", FieldManager: integration.CompositeReconcilerFieldManager},
				{File: "testdata/diff/resources/existing-xr-with-defaults-other-manager.yaml", FieldManager: "platform-operator"},
			},
			ExpectedStructured: simtest.Expect().WithSummary(0, 0, 0),
			ExpectedError:      false,
			ExpectedExitCode:   dp.ExitCodeSuccess,
		},
		// With no apiserver result for an addition, its defaults have to be predicted locally. These
		// pin that keeping local defaults out of dry-run payloads did not also take them out of the
		// additions that are never sent: an XRD default on the XR, and a CRD default on a composed
		// resource.
		"XRDDefaultsShownForAddedXRWithoutDryRun": {
			Reason:       "With --dry-run-on=existing an added XR still shows the defaults its XRD declares",
			OutputFormat: "json",
			DryRunOn:     "existing",
			InputFiles:   []string{"testdata/diff/xr-with-missing-defaults.yaml"},
			SetupFiles: []string{
				"testdata/diff/resources/xrd-with-defaults.yaml",
				"testdata/diff/resources/composition-with-defaults.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			ExpectedStructured: simtest.Expect().
				WithSummary(1, 0, 0).
				WithAddedResource("XTestDefaultResource", "test-resource-with-defaults", "default").
				WithDryRunSkipped("disabled", "").
				WithField("spec.region", "us-east-1").
				WithField("spec.size", "large").
				WithField("spec.tags.environment", "development"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		"CRDDefaultsShownForAddedComposedResourceWithoutDryRun": {
			Reason:       "With --dry-run-on=existing an added composed resource still shows the defaults its CRD declares",
			OutputFormat: "json",
			DryRunOn:     "existing",
			InputFiles:   []string{"../../../internal/testutils/integration/testdata/diff/new-xr.yaml"},
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"testdata/diff/resources/composition-with-crd-defaulted-downstream.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			ExpectedStructured: simtest.Expect().
				WithSummary(2, 0, 0).
				WithAddedResource("XDefaultedDownstreamResource", "test-resource-defaulted", "default").
				WithDryRunSkipped("disabled", "").
				WithField("spec.forProvider.size", "small").
				WithField("spec.forProvider.configData", "new-value").
				And().
				WithAddedResource("XNopResource", "test-resource", "default"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		// The local prediction defaults an addition against the version it was requested at. Its CRD
		// stores another version, whose defaults (and conversion) the cluster may also apply, so the
		// prediction cannot be trusted to match and the advisory says so — even under
		// --dry-run-on=existing, whose unverified additions are otherwise not warned about. Defaulting
		// itself is unchanged: size is the requested version's default, not the storage version's.
		// The XR's own XRD has one version, so it raises no warning, which the count pins.
		"AdditionAtANonStorageVersionWarnsThatItsDiffOmitsTheStorageVersion": {
			Reason:       "With --dry-run-on=existing an addition requested at a version its CRD does not store warns that the cluster stores it at another version",
			OutputFormat: "json",
			DryRunOn:     "existing",
			InputFiles:   []string{"../../../internal/testutils/integration/testdata/diff/new-xr.yaml"},
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"testdata/diff/resources/composition-with-non-storage-version-downstream.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			ExpectedStructured: simtest.Expect().
				WithSummary(2, 0, 0).
				WithAddedResource("XVersionedDownstreamResource", "test-resource-versioned", "default").
				WithDryRunSkipped("disabled", "").
				WithDryRunStorageVersion("v1").
				WithField("spec.forProvider.size", "small").
				And().
				WithAddedResource("XNopResource", "test-resource", "default").
				WithDryRunSkipped("disabled", "").
				And().
				WithWarning("they are stored at a version other than the one requested").
				WithWarningContext(map[string]string{
					"gvk":       "ns.nop.example.org/v1alpha1, Kind=XVersionedDownstreamResource",
					"namespace": "default",
					"versions":  "requested v1alpha1, stored v1",
					"count":     "1",
				}),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		"AdditionAtANonStorageVersionWarnsOnStderr": {
			Reason:     "In text mode the storage-version note reaches stderr, the only place a person sees it",
			DryRunOn:   "existing",
			InputFiles: []string{"../../../internal/testutils/integration/testdata/diff/new-xr.yaml"},
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"testdata/diff/resources/composition-with-non-storage-version-downstream.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			NoColor: true,
			ExpectedStderrContains: []string{
				`WARNING: did not verify added resources against the apiserver (--dry-run-on=existing); they are stored at a version other than the one requested, ` +
					`whose defaults and conversion the cluster may apply but their diffs do not ` +
					`(count=1, gvk=ns.nop.example.org/v1alpha1, Kind=XVersionedDownstreamResource, namespace=default, versions=requested v1alpha1, stored v1)`,
			},
			ExpectedOutput:   "+++ XVersionedDownstreamResource/test-resource-versioned",
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		"XRDDefaultsNoOverride": {
			Reason:       "Validates that XRD defaults do not override user-specified values",
			OutputFormat: "json",
			InputFiles:   []string{"testdata/diff/xr-with-overridden-defaults.yaml"},
			SetupFiles: []string{
				"testdata/diff/resources/xrd-with-defaults.yaml",
				"testdata/diff/resources/composition-with-defaults.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			ExpectedStructured: simtest.Expect().
				WithSummary(1, 0, 0).
				WithAddedResource("XTestDefaultResource", "test-resource-with-overrides", "default").
				WithField("spec.region", "us-west-2").
				WithField("spec.size", "xlarge"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		"NewNestedXRCreatesChildren": {
			Reason:       "Validates that new nested XR creates child XR and downstream resources",
			OutputFormat: "json",
			SetupFiles: []string{
				// XRDs for parent and child
				"testdata/diff/resources/nested/parent-xrd.yaml",
				"testdata/diff/resources/nested/child-xrd.yaml",
				// Compositions for parent and child
				"testdata/diff/resources/nested/parent-composition.yaml",
				"testdata/diff/resources/nested/child-composition.yaml",
				// XRD for downstream managed resource
				"testdata/diff/resources/xdownstreamenvresource-xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			InputFiles: []string{"testdata/diff/new-nested-xr.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(3, 0, 0).
				WithAddedResource("XChildResource", "test-parent-child", "default").
				WithField("spec.childField", "parent-value").
				And().
				WithAddedResource("XDownstreamResource", "test-parent-child-managed", "default").
				WithField("spec.forProvider.configData", "parent-value").
				And().
				WithAddedResource("XParentResource", "test-parent", "default").
				WithField("spec.parentField", "parent-value"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		// Issue #485. The unit test for the depth guard uses a render stub that caps its own recursion,
		// so it cannot show what a real cycle does across real renders: real templates, real names that
		// grow at every level (kept under the generated CRDs' 63-character limit by a short suffix), real
		// schema validation and dry-run at each level. This drives the whole path. It fails at exactly one level past the
		// default --max-nested-depth of 10, attributed to the XR the user named.
		"CyclicCompositionStopsAtMaxNestedDepth": {
			Reason:       "A composition cycle (XCycleA -> XCycleB -> XCycleA ...) must stop at --max-nested-depth with a clear error, not recurse until the stack overflows (#485)",
			OutputFormat: "json",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/cycle/xrds.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/cycle/compositions.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			InputFiles:            []string{"testdata/diff/new-cycle-xr.yaml"},
			ExpectedError:         true,
			ExpectedErrorContains: "maximum nesting depth exceeded: XCycleB/test-cycle-c-c-c-c-c-c-c-c-c-c-c (nested depth 11) is nested 11 levels deep, but --max-nested-depth is 10",
			ExpectedExitCode:      dp.ExitCodeToolError,
			ExpectedStructured: simtest.Expect().
				WithError("XCycleA/test-cycle").
				WithMessageContaining("maximum nesting depth exceeded"),
		},
		"ModifiedNestedXRPropagatesChanges": {
			Reason:       "Validates that modified nested XR propagates changes through child XR to downstream resources",
			OutputFormat: "json",
			SetupFiles: []string{
				// XRDs for parent and child
				"testdata/diff/resources/nested/parent-xrd.yaml",
				"testdata/diff/resources/nested/child-xrd.yaml",
				// Compositions for parent and child
				"testdata/diff/resources/nested/parent-composition.yaml",
				"testdata/diff/resources/nested/child-composition.yaml",
				// XRD for downstream managed resource
				"testdata/diff/resources/xdownstreamenvresource-xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			// Existing resources: the parent XR composes the child XR, which composes the managed resource
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				{
					OwnerFile: "testdata/diff/resources/nested/existing-parent-xr.yaml",
					OwnedFiles: map[string]*integration.HierarchicalOwnershipRelation{
						"testdata/diff/resources/nested/existing-child-xr.yaml": {
							OwnedFiles: map[string]*integration.HierarchicalOwnershipRelation{
								"testdata/diff/resources/nested/existing-managed-resource.yaml": nil,
							},
						},
					},
				},
			},
			InputFiles: []string{"testdata/diff/modified-nested-xr.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(0, 3, 0).
				WithModifiedResource("XChildResource", "test-parent-child", "default").
				WithFieldChange("spec.childField", "existing-value", "modified-value").
				And().
				WithModifiedResource("XDownstreamResource", "test-parent-child-managed", "default").
				WithFieldChange("spec.forProvider.configData", "existing-value", "modified-value").
				And().
				WithModifiedResource("XParentResource", "test-parent", "default").
				WithFieldChange("spec.parentField", "existing-value", "modified-value"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		// #503, for a nested XR. It is defaulted before its own render, so that render input carries
		// spec.tier: standard although the parent's composition never sets it. Sending it would claim
		// spec.tier from the manager that set it to premium.
		"XRDDefaultedFieldOnNestedXROwnedByAnotherManagerIsNotReportedChanged": {
			Reason:       "An XRD-defaulted field a parent composition omits from a nested XR keeps another manager's non-default value in the diff",
			OutputFormat: "json",
			SetupFiles: []string{
				"testdata/diff/resources/nested/parent-xrd.yaml",
				"testdata/diff/resources/nested/child-xrd.yaml",
				"testdata/diff/resources/nested/parent-composition.yaml",
				"testdata/diff/resources/nested/child-composition.yaml",
				"testdata/diff/resources/xdownstreamenvresource-xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				{
					OwnerFile: "testdata/diff/resources/nested/existing-parent-xr.yaml",
					OwnedFiles: map[string]*integration.HierarchicalOwnershipRelation{
						"testdata/diff/resources/nested/existing-child-xr.yaml": {
							OwnedFiles: map[string]*integration.HierarchicalOwnershipRelation{
								"testdata/diff/resources/nested/existing-managed-resource.yaml": nil,
							},
						},
					},
				},
			},
			FieldManagerApplies: []integration.FieldManagerApply{
				{File: "testdata/diff/resources/nested/existing-child-xr-tier-other-manager.yaml", FieldManager: "platform-operator"},
			},
			InputFiles: []string{"testdata/diff/modified-nested-xr.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(0, 3, 0).
				WithModifiedResource("XChildResource", "test-parent-child", "default").
				WithFieldChange("spec.childField", "existing-value", "modified-value").
				WithFieldChange("spec.tier", "premium", "premium").
				And().
				WithModifiedResource("XDownstreamResource", "test-parent-child-managed", "default").
				WithFieldChange("spec.forProvider.configData", "existing-value", "modified-value").
				And().
				WithModifiedResource("XParentResource", "test-parent", "default").
				WithFieldChange("spec.parentField", "existing-value", "modified-value"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		// Composition Revision tests for v2 XRDs
		"V2ManualPolicyPinnedRevision": {
			Reason:       "Validates v2 XR with Manual update policy stays on pinned revision",
			OutputFormat: "json",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"testdata/diff/resources/composition-revision-v1.yaml",
				"testdata/diff/resources/composition-v2.yaml", // Current composition is v2
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/diff/resources/existing-xr-manual-v1.yaml",
					"testdata/diff/resources/existing-downstream-manual-v1.yaml",
				),
			},
			InputFiles: []string{"testdata/diff/modified-xr-manual-v1.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(0, 2, 0).
				WithModifiedResource("XDownstreamResource", "test-manual-v1", "default").
				WithFieldChange("spec.forProvider.configData", "v1-existing-value", "v1-modified-value").
				And().
				WithModifiedResource("XNopResource", "test-manual-v1", "default").
				WithFieldChange("spec.coolField", "existing-value", "modified-value"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		"V2AutomaticPolicyLatestRevision": {
			Reason:       "Validates v2 XR with Automatic update policy uses latest revision",
			OutputFormat: "json",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"testdata/diff/resources/composition-revision-v1.yaml",
				"testdata/diff/resources/composition-v2.yaml", // Current composition is v2
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				// The XR is still on v1, and its composed resource holds v1's data.
				integration.Composes(
					"testdata/diff/resources/existing-xr-automatic.yaml",
					"testdata/diff/resources/existing-downstream-automatic.yaml",
				),
			},
			InputFiles: []string{"testdata/diff/modified-xr-automatic.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(0, 2, 0).
				WithModifiedResource("XDownstreamResource", "test-automatic", "default").
				WithFieldChange("spec.forProvider.configData", "v1-existing-value", "v2-modified-value").
				And().
				WithModifiedResource("XNopResource", "test-automatic", "default").
				WithFieldChange("spec.coolField", "existing-value", "modified-value"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		"V2AutomaticPolicyRevisionSelectorPinsMatchingRevision": {
			// Issue #388 (Bug B): under Automatic policy WITH a compositionRevisionSelector, the XR
			// resolves to the latest revision MATCHING the selector, not the latest overall. Here the
			// selector pins channel=stable (revision 1) even though channel=preview (revision 2) is
			// newer — so the diff must render against the stable template (stable-*), not preview-*.
			Reason:       "Validates v2 Automatic XR with compositionRevisionSelector resolves to the selector-matching revision, not the newest overall",
			OutputFormat: "json",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"testdata/diff/resources/composition-revision-stable.yaml",  // revision 1, channel=stable
				"testdata/diff/resources/composition-revision-preview.yaml", // revision 2, channel=preview (newest)
				"testdata/diff/resources/composition-v2.yaml",               // current composition
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/diff/resources/existing-xr-selector-stable.yaml",
					"testdata/diff/resources/existing-downstream-selector-stable.yaml",
				),
			},
			InputFiles: []string{"testdata/diff/modified-xr-selector-stable.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(0, 2, 0).
				WithModifiedResource("XDownstreamResource", "test-selector", "default").
				// stable-* (not preview-*) proves the selector, not "latest overall", chose the revision.
				WithFieldChange("spec.forProvider.configData", "stable-existing-value", "stable-modified-value").
				And().
				WithModifiedResource("XNopResource", "test-selector", "default").
				WithFieldChange("spec.coolField", "existing-value", "modified-value"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		"V2AutomaticPolicyRevisionSelectorNoMatchErrors": {
			// The mirror of the above: a selector matching NO revision must fail the diff rather than
			// silently rendering against a non-matching revision (accuracy over guessing).
			Reason:       "Validates v2 Automatic XR whose compositionRevisionSelector matches no revision fails the diff",
			OutputFormat: "json",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"testdata/diff/resources/composition-revision-stable.yaml",
				"testdata/diff/resources/composition-revision-preview.yaml",
				"testdata/diff/resources/composition-v2.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			InputFiles:            []string{"testdata/diff/modified-xr-selector-nomatch.yaml"},
			ExpectedError:         true,
			ExpectedErrorContains: "match selector",
			ExpectedExitCode:      dp.ExitCodeToolError,
		},
		"V2ManualRevisionUpgradeDiff": {
			Reason:       "Validates v2 XR changing revision in Manual mode shows upgrade diff",
			OutputFormat: "json",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"testdata/diff/resources/composition-revision-v1.yaml",
				"testdata/diff/resources/composition-v2.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/diff/resources/existing-xr-manual-v1.yaml",
					"testdata/diff/resources/existing-downstream-manual-v1.yaml",
				),
			},
			InputFiles: []string{"testdata/diff/modified-xr-manual-upgrade-to-v2.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(0, 2, 0).
				WithModifiedResource("XDownstreamResource", "test-manual-v1", "default").
				WithFieldChange("spec.forProvider.configData", "v1-existing-value", "v2-modified-value").
				And().
				WithModifiedResource("XNopResource", "test-manual-v1", "default").
				WithFieldChange("spec.coolField", "existing-value", "modified-value"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		"CompositionRevisionUpgradesResourceAPIVersion": {
			// NOTE: This test validates that resources are correctly matched across API version changes,
			// avoiding delete/recreate. The composition template changes from v1beta1 to v1beta2, but
			// Kubernetes automatically converts resources between served API versions. When we query for
			// the v1beta2 resource, Kubernetes finds the v1beta1 resource and returns it auto-converted
			// to v1beta2. From Kubernetes' perspective, the resource exists as both versions simultaneously,
			// so there's no apiVersion field change to show in the diff. The important thing is that the
			// resource is matched (unchanged, not ---/+++), preventing delete/recreate operations.
			//
			// The XR composes the resource as Crossplane leaves it: controlled by the XR, and named in the
			// XR's resourceRefs at v1beta1, the version it was composed at. Crossplane's garbage collector
			// matches what an XR composed to what it renders by composition resource name, so it keeps the
			// resource; removal detection must too, rather than report the v1beta1 reference unmatched by
			// the v1beta2 render (#552).
			Reason:       "Validates XR upgrading composition revision that changes resource API version shows as update not remove/add",
			OutputFormat: "json",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				// NOTE: xapimigrate CRD is auto-loaded from testdata/diff/crds/
				// We apply no XRD for XApiMigrateResource because it is a regular managed resource
				// (not a composite), and an XRD would make it composite.
				"testdata/diff/resources/api-version-composition-revision-v1.yaml",
				"testdata/diff/resources/api-version-composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				{
					OwnerFile: "testdata/diff/resources/existing-api-version-xr-rev1.yaml",
					OwnedFiles: map[string]*integration.HierarchicalOwnershipRelation{
						"testdata/diff/resources/existing-api-version-downstream-v1beta1.yaml": nil,
					},
				},
			},
			InputFiles: []string{"testdata/diff/modified-api-version-xr-rev2.yaml"},
			// Key assertion: nothing is added or removed, proving API version migration works. The
			// XApiMigrateResource is stored identically at either version, so it renders unchanged; an
			// unmatched one would be a +++ addition plus a --- removal. Only the XR's revision ref changes.
			ExpectedStructured: simtest.Expect().
				WithSummary(0, 1, 0).
				WithModifiedResource("XNopResource", "test-api-version-xr", "default"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		"CompositionRevisionUpgradesResourceAPIVersionAndChangesIt": {
			// The same upgrade, with a change to the XR that the new revision renders into the resource too.
			// The resource is one object, modified once at the version it is rendered at. Matched by API
			// version, the v1beta1 reference was a removal contradicting that modification, and the xr
			// command refused to combine the two diffs of one object (#552).
			Reason:       "Validates an API version change plus a field change is one modification, not a removal and a modification",
			OutputFormat: "json",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"testdata/diff/resources/api-version-composition-revision-v1.yaml",
				"testdata/diff/resources/api-version-composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				{
					OwnerFile: "testdata/diff/resources/existing-api-version-xr-rev1.yaml",
					OwnedFiles: map[string]*integration.HierarchicalOwnershipRelation{
						"testdata/diff/resources/existing-api-version-downstream-v1beta1.yaml": nil,
					},
				},
			},
			InputFiles: []string{"testdata/diff/modified-api-version-xr-rev2-changed-field.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(0, 2, 0).
				WithModifiedResource("XApiMigrateResource", "test-api-version-xr-api-resource", "default").
				WithFieldChange("spec.forProvider.configData", "test-value", "changed-value").
				And().
				WithModifiedResource("XNopResource", "test-api-version-xr", "default").
				WithFieldChange("spec.coolField", "test-value", "changed-value"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		"V2SwitchManualToAutomatic": {
			Reason:       "Validates v2 XR switching from Manual to Automatic mode uses latest revision",
			OutputFormat: "json",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"testdata/diff/resources/composition-revision-v1.yaml",
				"testdata/diff/resources/composition-v2.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/diff/resources/existing-xr-manual-v1.yaml",
					"testdata/diff/resources/existing-downstream-manual-v1.yaml",
				),
			},
			InputFiles: []string{"testdata/diff/modified-xr-switch-to-automatic.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(0, 2, 0).
				WithModifiedResource("XDownstreamResource", "test-manual-v1", "default").
				WithFieldChange("spec.forProvider.configData", "v1-existing-value", "v2-modified-value").
				And().
				WithModifiedResource("XNopResource", "test-manual-v1", "default").
				WithFieldChange("spec.coolField", "existing-value", "modified-value"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		"V2NetNewManualNoRevRef": {
			Reason:       "Validates v2 net new XR with Manual policy but no revision ref uses latest revision",
			OutputFormat: "json",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"testdata/diff/resources/composition-revision-v1.yaml",
				"testdata/diff/resources/composition-v2.yaml", // Current composition is v2
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			InputFiles: []string{"testdata/diff/new-xr-manual-no-ref.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(2, 0, 0).
				WithAddedResource("XDownstreamResource", "test-manual-no-ref", "default").
				WithField("spec.forProvider.configData", "v2-new-value").
				And().
				WithAddedResource("XNopResource", "test-manual-no-ref", "default").
				WithField("spec.coolField", "new-value"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		// Composition Revision tests for v1 XRDs (Crossplane 1.20 compatibility)
		"V1ManualRevisionUpgradeDiff": {
			Reason:        "Validates v1 XR with Manual update policy changing revision shows upgrade diff",
			OutputFormat:  "json",
			XrdAPIVersion: integration.XRDV1,
			SetupFiles: []string{
				"testdata/diff/resources/legacy-xrd.yaml",
				"testdata/diff/resources/legacy-composition-revision-v1.yaml",
				"testdata/diff/resources/legacy-composition-v2.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/diff/resources/existing-legacy-xr-manual-v1.yaml",
					"testdata/diff/resources/existing-legacy-downstream-manual-v1.yaml",
				),
			},
			InputFiles: []string{"testdata/diff/modified-legacy-xr-manual-upgrade-to-v2.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(0, 2, 0).
				WithModifiedResource("XDownstreamResource", "test-legacy-manual-v1", "").
				WithFieldChange("spec.forProvider.configData", "v1-existing-value", "v2-modified-value").
				And().
				WithModifiedResource("XNopResource", "test-legacy-manual-v1", "").
				WithFieldChange("spec.coolField", "existing-value", "modified-value"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		// Issue #499: Crossplane's composite reconciler writes compositionRevisionRef, so a hand-written
		// manifest omits it — and an apply that omits a field leaves it where it is. The render must
		// therefore see the cluster copy's ref. Here the composition propagates the revision name into the
		// composed resource, so a render that dropped the ref reported configData changing from the real
		// revision name to empty for an input identical to what is deployed.
		"ExistingXROmittingRevisionRefRendersWithClusterRef": {
			Reason:       "An existing XR whose input omits compositionRevisionRef renders with the cluster's ref, so a template reading the revision name shows no spurious change",
			OutputFormat: "json",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/revision-templating-composition.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/revision-templating-composition-revision.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"../../../internal/testutils/integration/testdata/comp/resources/existing-xr-revision-ref.yaml",
					"../../../internal/testutils/integration/testdata/comp/resources/existing-downstream-revision-ref.yaml",
				),
			},
			InputFiles:         []string{"testdata/diff/existing-xr-revision-ref-omitted.yaml"},
			ExpectedStructured: simtest.Expect().WithSummary(0, 0, 0),
			ExpectedExitCode:   dp.ExitCodeSuccess,
		},
		// Issue #499, the consequence for which revision renders. The cluster's ref is inherited before the
		// composition is resolved, so a Manual composite whose input omits the ref stays on the revision it
		// is pinned to (-911318d, the v1 template), as it would in the cluster, instead of being rendered
		// against the latest one (-430f3f3, v2). Compare V2ManualPolicyPinnedRevision, the same change with
		// the ref spelled out.
		"V2ManualPolicyOmittedRevisionRefStaysPinned": {
			Reason:       "Validates v2 XR with Manual update policy whose input omits compositionRevisionRef stays on the cluster's pinned revision",
			OutputFormat: "json",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"testdata/diff/resources/composition-revision-v1.yaml",
				"testdata/diff/resources/composition-v2.yaml", // Current composition is v2
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/diff/resources/existing-xr-manual-v1.yaml",
					"testdata/diff/resources/existing-downstream-manual-v1.yaml",
				),
			},
			InputFiles: []string{"testdata/diff/modified-xr-manual-v1-omits-revision-ref.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(0, 2, 0).
				WithModifiedResource("XDownstreamResource", "test-manual-v1", "default").
				// v1-* (not v2-*) proves the pinned revision, not the latest, rendered.
				WithFieldChange("spec.forProvider.configData", "v1-existing-value", "v1-modified-value").
				And().
				WithModifiedResource("XNopResource", "test-manual-v1", "default").
				WithFieldChange("spec.coolField", "existing-value", "modified-value"),
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		// The v1-path (spec.compositionRevisionRef) twin of the above.
		"V1ManualPolicyOmittedRevisionRefStaysPinned": {
			Reason:        "Validates v1 XR with Manual update policy whose input omits compositionRevisionRef stays on the cluster's pinned revision",
			OutputFormat:  "json",
			XrdAPIVersion: integration.XRDV1,
			SetupFiles: []string{
				"testdata/diff/resources/legacy-xrd.yaml",
				"testdata/diff/resources/legacy-composition-revision-v1.yaml",
				"testdata/diff/resources/legacy-composition-v2.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/diff/resources/existing-legacy-xr-manual-v1.yaml",
					"testdata/diff/resources/existing-legacy-downstream-manual-v1.yaml",
				),
			},
			InputFiles: []string{"testdata/diff/modified-legacy-xr-manual-v1-omits-revision-ref.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(0, 2, 0).
				WithModifiedResource("XDownstreamResource", "test-legacy-manual-v1", "").
				WithFieldChange("spec.forProvider.configData", "v1-existing-value", "v1-modified-value").
				And().
				WithModifiedResource("XNopResource", "test-legacy-manual-v1", "").
				WithFieldChange("spec.coolField", "existing-value", "modified-value"),
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		// Issue #499 for a nested XR. It is rendered from its parent's output, which never carries
		// compositionRevisionRef, so it must inherit the cluster copy's, which ProcessNestedXRs hands it
		// rather than fetching it again. The child's composition propagates the revision name into the
		// managed resource; the parent's change reaches the child's childField but not that name, so the
		// managed resource must not be reported.
		"NestedXRInheritsClusterRevisionRef": {
			Reason:       "A nested XR renders with its cluster copy's compositionRevisionRef, so a template reading the revision name shows no spurious change",
			OutputFormat: "json",
			SetupFiles: []string{
				"testdata/diff/resources/nested/parent-xrd.yaml",
				"testdata/diff/resources/nested/child-xrd.yaml",
				"testdata/diff/resources/nested/parent-composition.yaml",
				"testdata/diff/resources/nested/child-revision-templating-composition.yaml",
				"testdata/diff/resources/xdownstreamenvresource-xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				{
					OwnerFile: "testdata/diff/resources/nested/existing-parent-xr.yaml",
					OwnedFiles: map[string]*integration.HierarchicalOwnershipRelation{
						"testdata/diff/resources/nested/existing-child-xr-revision-ref.yaml": {
							OwnedFiles: map[string]*integration.HierarchicalOwnershipRelation{
								"testdata/diff/resources/nested/existing-managed-resource-revision-ref.yaml": nil,
							},
						},
					},
				},
			},
			InputFiles: []string{"testdata/diff/modified-nested-xr.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(0, 2, 0).
				WithModifiedResource("XChildResource", "test-parent-child", "default").
				WithFieldChange("spec.childField", "existing-value", "modified-value").
				// The nested XR's own reconciler wrote the ref, not the parent's composed apply, so the
				// parent's render, which omits it, leaves it in place.
				WithFieldChange("spec.crossplane.compositionRevisionRef.name",
					"xchildresources.nested.example.org-96afc06", "xchildresources.nested.example.org-96afc06").
				And().
				WithModifiedResource("XParentResource", "test-parent", "default").
				WithFieldChange("spec.parentField", "existing-value", "modified-value"),
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		// Issue #498, as a user meets it: an Automatic claim's manifest omits the compositionRevisionRef the
		// claim syncer copied onto the cluster copy. Under Automatic the claim's ref is never propagated to
		// the backing XR anyway; the XR controller owns the XR's own, which is what render must see. Before,
		// the backing XR's ref was dropped, so the template read no revision name and configData was
		// reported changing to "v2:<no value>:existing-value" for an unchanged claim.
		"AutomaticClaimOmittingRevisionRefRendersWithBackingXRRef": {
			Reason:       "An Automatic claim renders with its backing XR's compositionRevisionRef, so a template reading the revision name shows no spurious change",
			OutputFormat: "json",
			SetupFiles: []string{
				"testdata/diff/resources/existing-namespace.yaml",
				"testdata/diff/resources/pinned-claim/definitions.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
				"testdata/diff/resources/pinned-claim/existing-automatic.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/diff/resources/pinned-claim/existing-automatic-xr.yaml",
					"testdata/diff/resources/pinned-claim/existing-automatic-downstream.yaml",
				),
			},
			XrdAPIVersion: integration.XRDV1, // the XRD is v1, so Crossplane writes the XR's resourceRefs at spec.resourceRefs
			InputFiles:    []string{"testdata/diff/unchanged-automatic-pinned-claim.yaml"},
			// No warnings either: the composed resource belongs to the claim's backing XR (#534).
			ExpectedStructured: simtest.Expect().WithNoWarnings().WithSummary(0, 0, 0),
			ExpectedExitCode:   dp.ExitCodeSuccess,
		},
		// A Manual claim's pin lives on its backing XR: the claim syncer copies the ref back to a claim only
		// under Automatic. Composition resolution and render must both see that pin. Before, the composition
		// was resolved from the claim, which names no revision, so the latest one (v2) was chosen, while the
		// render read the backing XR's ref (revision 1): configData became "v2:...-3d33597:...", a revision's template
		// paired with another revision's name.
		"ManualClaimRendersAgainstItsBackingXRsPinnedRevision": {
			Reason:       "A Manual claim whose backing XR is pinned to an older revision renders that revision, with that revision's name",
			OutputFormat: "json",
			SetupFiles: []string{
				"testdata/diff/resources/existing-namespace.yaml",
				"testdata/diff/resources/pinned-claim/definitions.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
				"testdata/diff/resources/pinned-claim/existing-manual.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/diff/resources/pinned-claim/existing-manual-xr.yaml",
					"testdata/diff/resources/pinned-claim/existing-manual-downstream.yaml",
				),
			},
			XrdAPIVersion: integration.XRDV1, // the XRD is v1, so Crossplane writes the XR's resourceRefs at spec.resourceRefs
			InputFiles:    []string{"testdata/diff/modified-manual-pinned-claim.yaml"},
			ExpectedStructured: simtest.Expect().
				// The composed resource is labelled with the backing XR's name, which the claim's resourceRef
				// names, so it already belongs to this claim: no ownership advisory (#534).
				WithNoWarnings().
				WithSummary(0, 2, 0).
				WithModifiedResource("Pinned", "manual-claim", "existing-namespace").
				WithFieldChange("spec.coolField", "existing-value", "modified-value").
				And().
				WithModifiedResource("XDownstreamResource", "manual-claim-x1y2z", "").
				WithFieldChange("spec.forProvider.configData",
					"v1:xpinneds.pinned.diff.example.org-3d33597:existing-value",
					"v1:xpinneds.pinned.diff.example.org-3d33597:modified-value"),
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		// Issue #536. Crossplane's composite reconciler resolves the revision, writes it to
		// compositionRevisionRef, and then composes, so an Automatic composite whose ref lags behind the
		// latest revision is composed from the latest one AND sees its name. Before, the latest revision
		// rendered while the template read the stale ref: configData became "v2:...-3d33597:...", a revision's
		// template paired with another revision's name.
		"AutomaticXRWithLaggingRevisionRefRendersLatestRevisionAndName": {
			Reason:       "An Automatic XR whose cluster ref names an older revision renders the latest revision, with that revision's name",
			OutputFormat: "json",
			SetupFiles: []string{
				"testdata/diff/resources/pinned-claim/definitions.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/diff/resources/pinned-claim/existing-lagging-automatic-xr.yaml",
					"testdata/diff/resources/pinned-claim/existing-lagging-automatic-downstream.yaml",
				),
			},
			XrdAPIVersion: integration.XRDV1, // the XRD is v1, so Crossplane writes the XR's resourceRefs at spec.resourceRefs
			InputFiles:    []string{"testdata/diff/unchanged-lagging-automatic-xr.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(0, 1, 0).
				WithModifiedResource("XDownstreamResource", "lagging-xr", "").
				WithFieldChange("spec.forProvider.configData",
					"v1:xpinneds.pinned.diff.example.org-3d33597:existing-value",
					"v2:xpinneds.pinned.diff.example.org-806289b:existing-value"),
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		// An XRD's defaultCompositionUpdatePolicy applies to a new composite before Crossplane resolves its
		// revision, so a new composite that names a revision and leaves the policy to an XRD defaulting to
		// Manual renders the revision it names. Before, the composition was resolved before defaulting, as
		// Automatic, so the latest revision (v2) rendered.
		"NewXRUnderManualDefaultingXRDRendersTheRevisionItNames": {
			Reason:       "A new XR whose XRD defaults its update policy to Manual is resolved as Manual",
			OutputFormat: "json",
			SetupFiles: []string{
				"testdata/diff/resources/manual-default/definitions.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			InputFiles: []string{"testdata/diff/new-manual-default-xr.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(2, 0, 0).
				WithAddedResource("XManualDefault", "new-manual-default", "default").
				And().
				WithAddedResource("XDownstreamResource", "new-manual-default", "default").
				WithField("spec.forProvider.configData", "v1-new-value"),
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		// Crossplane selects a composition only for a composite with no compositionRef, and never
		// re-selects. A composite whose input relies on a selector therefore keeps the compositionRef
		// Crossplane wrote, here composition A, even though the selector now matches two compositions.
		// Before, the selector was re-evaluated against the input, and the diff failed as ambiguous.
		"ExistingXRSelectingItsCompositionKeepsTheClusterCompositionRef": {
			Reason:       "An existing XR whose input selects its composition by label renders with the composition Crossplane already bound it to",
			OutputFormat: "json",
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
				"testdata/diff/resources/selector-bound/setup.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/diff/resources/selector-bound/existing-xr.yaml",
					"testdata/diff/resources/selector-bound/existing-downstream.yaml",
				),
			},
			InputFiles: []string{"testdata/diff/modified-xr-selector-bound.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(0, 2, 0).
				WithModifiedResource("XDownstreamResource", "selector-bound", "default").
				WithFieldChange("spec.forProvider.configData", "a-existing-value", "a-modified-value").
				And().
				WithModifiedResource("XNopResource", "selector-bound", "default").
				WithFieldChange("spec.coolField", "existing-value", "modified-value"),
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		// v2 XRD with v1-style composition paths (issue #206)
		"V2XRDWithV1StyleCompositionPaths": {
			Reason:       "Validates v2 XRD using v1-style spec.compositionRef paths are correctly recognized (issue #206)",
			OutputFormat: "json",
			SetupFiles: []string{
				"testdata/diff/resources/v2-xrd-with-v1-paths.yaml",
				"testdata/diff/resources/v2-xrd-with-v1-paths-composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/diff/resources/existing-v2xrd-v1paths-xr.yaml",
					"testdata/diff/resources/existing-v2xrd-v1paths-downstream.yaml",
				),
			},
			InputFiles: []string{"testdata/diff/resources/modified-v2xrd-v1paths-xr.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(0, 2, 0).
				WithModifiedResource("XDownstreamResource", "test-v2xrd-v1paths", "default").
				WithFieldChange("spec.forProvider.configData", "existing-value", "modified-value").
				And().
				WithModifiedResource("XNopResource", "test-v2xrd-v1paths", "default").
				WithFieldChange("spec.coolField", "existing-value", "modified-value"),
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		// A claim passed alongside its backing XR: Crossplane's claim controller writes that XR (a
		// server-side apply with forced ownership, stamping claimRef and the claim labels), so the XR
		// input would be a second writer. Rejected on the XR; the claim's diff still runs. The binding
		// is read from the claim's cluster copy (spec.resourceRef) and from the XR's claim labels.
		"ClaimPassedWithItsBackingXRIsRejected": {
			Reason: "A claim and the XR bound to it, passed together, is an input error on the XR",
			SetupFiles: []string{
				"testdata/diff/resources/existing-namespace.yaml",
				"testdata/diff/resources/claim-nested/parent-definition.yaml",
				"testdata/diff/resources/claim-nested/child-definition.yaml",
				"testdata/diff/resources/claim-nested/parent-composition.yaml",
				"testdata/diff/resources/claim-nested/child-composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
				"testdata/diff/resources/claim-nested/existing-claim.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				{
					OwnerFile: "testdata/diff/resources/claim-nested/existing-parent-xr.yaml",
					OwnedFiles: map[string]*integration.HierarchicalOwnershipRelation{
						"testdata/diff/resources/claim-nested/existing-child-xr.yaml": {
							OwnedFiles: map[string]*integration.HierarchicalOwnershipRelation{
								"testdata/diff/resources/claim-nested/existing-managed-resource.yaml": nil,
							},
						},
					},
				},
			},
			XrdAPIVersion: integration.XRDV1, // the XRDs are v1, so Crossplane writes the XRs' resourceRefs at spec.resourceRefs
			InputFiles: []string{
				"testdata/diff/modified-claim-nested.yaml",
				"testdata/diff/resources/claim-nested/existing-parent-xr.yaml",
			},
			ExpectedError: true,
			ExpectedErrorContains: "XNopParent/existing-parent-claim-82crv is the XR bound to claim " +
				"ParentNopClaim/existing-parent-claim, which Crossplane's claim controller writes",
			ExpectedExitCode: dp.ExitCodeToolError,
		},
		"ModifiedClaimWithNestedXRsShowsDiff": {
			Reason:       "Validates that modified Claims with nested XRs show proper diff (3 modified resources)",
			OutputFormat: "json",
			SetupFiles: []string{
				"testdata/diff/resources/existing-namespace.yaml",
				// XRDs for parent and child Claims (the harness generates their XR and Claim CRDs)
				"testdata/diff/resources/claim-nested/parent-definition.yaml",
				"testdata/diff/resources/claim-nested/child-definition.yaml",
				// Compositions for parent and child
				"testdata/diff/resources/claim-nested/parent-composition.yaml",
				"testdata/diff/resources/claim-nested/child-composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
				// Claim is set up separately (not via owner refs - it uses spec.resourceRef to link to backing XR)
				"testdata/diff/resources/claim-nested/existing-claim.yaml",
			},
			// Owner ref hierarchy matches Crossplane's actual ownership model:
			// - Backing XR is the root of the owner ref tree (no owner refs)
			// - Nested XR has owner ref to backing XR
			// - Managed resource has owner ref to nested XR
			// Note: Claim is NOT in this hierarchy - it links to backing XR via spec.resourceRef, not owner refs
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				{
					// Backing XR is the root of owner ref tree
					OwnerFile: "testdata/diff/resources/claim-nested/existing-parent-xr.yaml",
					OwnedFiles: map[string]*integration.HierarchicalOwnershipRelation{
						// Nested XR owned by backing XR
						"testdata/diff/resources/claim-nested/existing-child-xr.yaml": {
							// Managed resource owned by nested XR
							OwnedFiles: map[string]*integration.HierarchicalOwnershipRelation{
								"testdata/diff/resources/claim-nested/existing-managed-resource.yaml": nil,
							},
						},
					},
				},
			},
			InputFiles: []string{"testdata/diff/modified-claim-nested.yaml"},
			ExpectedStructured: simtest.Expect().
				WithSummary(0, 3, 0).
				WithModifiedResource("ClusterNopResource", "existing-parent-claim-82crv-nop", "").
				And().
				WithModifiedResource("ParentNopClaim", "existing-parent-claim", "default").
				WithFieldChange("spec.parentField", "existing-parent-value", "modified-parent-value").
				And().
				WithModifiedResource("XNopChild", "existing-parent-claim-82crv-child", "").
				WithFieldChange("spec.childField", "existing-parent-value", "modified-parent-value").
				// The nested XR's own reconciler wrote its composition refs, not the parent's composed
				// apply, so the parent's render, which omits them, leaves them in place.
				WithFieldChange("spec.compositionRef.name", "child-nop-claim-composition", "child-nop-claim-composition").
				WithFieldChange("spec.compositionRevisionRef.name", "child-nop-claim-composition-db22ad5", "child-nop-claim-composition-db22ad5"),
			XrdAPIVersion:    integration.XRDV1, // Use V1 style resourceRefs since XRDs have claims
			ExpectedError:    false,
			ExpectedExitCode: dp.ExitCodeDiffDetected,
		},
		"FunctionCredentialsAutoFetch": {
			Reason:       "Successfully renders XR when composition references credentials that exist in cluster",
			OutputFormat: "json",
			InputFiles:   []string{"testdata/diff/new-xr-with-creds.yaml"},
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"testdata/diff/resources/credentials/composition-with-creds.yaml",
				"testdata/diff/resources/credentials/function-credentials-secret.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			ExpectedStructured: simtest.Expect().
				WithSummary(2, 0, 0).
				WithAddedResource("XDownstreamResource", "test-resource-with-creds", "default").
				WithField("spec.forProvider.configData", "test-value-creds").
				And().
				WithAddedResource("XNopResource", "test-resource-with-creds", "default").
				WithField("spec.coolField", "test-value-creds"),
			ExpectedError: false,
		},
		"FunctionCredentialsFromCLI": {
			Reason:       "Successfully renders XR when credentials provided via --function-credentials flag",
			OutputFormat: "json",
			InputFiles:   []string{"testdata/diff/new-xr-with-creds.yaml"},
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"testdata/diff/resources/credentials/composition-with-creds.yaml",
				// Note: NOT setting up the secret in cluster - testing CLI override
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			// Credentials loaded from CLI flag file
			FunctionCredentials: "testdata/diff/resources/credentials/cli-credentials.yaml",
			ExpectedExitCode:    dp.ExitCodeDiffDetected,
			// WithNoWarnings is the point of this case for issue #481: the secret the composition
			// references is deliberately absent from the cluster and supplied by --function-credentials
			// instead, so the shortfall advisory must NOT fire. It used to, because the advisory was
			// raised inside the credential client, which runs before the CLI-supplied secrets are merged
			// in — telling a user who had just solved the problem to go and solve it.
			ExpectedStructured: simtest.Expect().
				WithSummary(2, 0, 0).
				WithNoWarnings().
				WithAddedResource("XDownstreamResource", "test-resource-with-creds", "default").
				WithField("spec.forProvider.configData", "test-value-creds").
				And().
				WithAddedResource("XNopResource", "test-resource-with-creds", "default").
				WithField("spec.coolField", "test-value-creds"),
			ExpectedError: false,
		},
		// Paired sequencer gating tests — the fixture sequencer-gating-composition.yaml
		// is deliberately correct (rules match composition-resource-names stage0-/stage1-
		// AND auto-ready runs BEFORE sequencer so DesiredComposed.Ready is populated
		// before sequencer's gating check). Together these two cases discriminate
		// --eventual-state from default behavior: only eventual-state surfaces stage1.
		"SequencerGatesLaterStageWithoutEventualState": {
			Reason:       "Without --eventual-state, function-sequencer hides stage1 until stage0 is ready",
			Timeout:      simharness.LongTimeout,
			OutputFormat: "json",
			// eventualState deliberately false
			InputFiles: []string{"testdata/diff/resources/sequencer-xr.yaml"},
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/sequencer-gating-composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			// Expect: XR + stage0 added; stage1 NOT present (gated).
			ExpectedStructured: simtest.Expect().
				WithSummary(2, 0, 0).
				WithAddedResource("XNopResource", "sequencer-test", "default").
				WithField("spec.coolField", "test-value").
				And().
				WithAddedResource("XDownstreamResource", "0-stage0-resource", "default").
				WithField("spec.forProvider.configData", "test-value"),
			ExpectedError: false,
		},
		"SequencerGatedStageAppearsWithEventualState": {
			Reason:        "With --eventual-state, sequencer-gated stage1 surfaces via synthesize-Ready iteration",
			Timeout:       simharness.LongTimeout, // Multiple simulation iterations need more time
			OutputFormat:  "json",
			EventualState: true,
			InputFiles:    []string{"testdata/diff/resources/sequencer-xr.yaml"},
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/sequencer-gating-composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			// Expect: XR + stage0 + stage1 all added (stage1 unlocked via eventual-state).
			ExpectedStructured: simtest.Expect().
				WithSummary(3, 0, 0).
				WithAddedResource("XNopResource", "sequencer-test", "default").
				WithField("spec.coolField", "test-value").
				And().
				WithAddedResource("XDownstreamResource", "0-stage0-resource", "default").
				WithField("spec.forProvider.configData", "test-value").
				And().
				WithAddedResource("XDownstreamResource", "1-stage1-resource", "default").
				WithField("spec.forProvider.configData", "test-value"),
			ExpectedError: false,
		},
		// Issue #564: an existing XR whose stage0 exists but is not Ready yet, the normal state of an
		// XR whose first stage is still provisioning. Nothing renders that is not already observed, so
		// eventual state used to stop at once and never synthesize stage0's Ready; but Crossplane
		// composes stage1 as soon as stage0 becomes Ready.
		"SequencerGatedStageAppearsForExistingXRWithNotReadyStage0WithEventualState": {
			Reason:        "With --eventual-state, stage1 surfaces when stage0 already exists but is not Ready",
			Timeout:       simharness.LongTimeout, // Multiple simulation iterations need more time
			OutputFormat:  "json",
			EventualState: true,
			InputFiles:    []string{"testdata/diff/resources/sequencer-xr.yaml"},
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/sequencer-gating-composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes("testdata/diff/resources/existing-sequencer-gating-xr.yaml", "testdata/diff/resources/existing-sequencer-gating-stage0.yaml"),
			},
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			// Expect: XR and stage0 modified, stage1 added (unlocked by stage0's synthesized Ready).
			ExpectedStructured: simtest.Expect().
				WithSummary(1, 2, 0).
				WithModifiedResource("XNopResource", "sequencer-test", "default").
				WithFieldChange("spec.coolField", "existing-value", "test-value").
				And().
				WithModifiedResource("XDownstreamResource", "0-stage0-resource", "default").
				WithFieldChange("spec.forProvider.configData", "existing-value", "test-value").
				And().
				WithAddedResource("XDownstreamResource", "1-stage1-resource", "default").
				WithField("spec.forProvider.configData", "test-value"),
			ExpectedError: false,
		},
		// The converged counterpart: both stages exist and are Ready, so synthesizing Ready changes
		// nothing and eventual state is the first render. Only the spec changes the input makes show.
		"EventualStateAddsNothingWhenEveryStageIsReady": {
			Reason:        "With --eventual-state, an XR whose composed resources are all Ready shows only its real changes",
			Timeout:       simharness.LongTimeout,
			OutputFormat:  "json",
			EventualState: true,
			InputFiles:    []string{"testdata/diff/resources/sequencer-xr.yaml"},
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/sequencer-gating-composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes("testdata/diff/resources/existing-sequencer-gating-xr.yaml",
					"testdata/diff/resources/existing-sequencer-gating-stage0-ready.yaml",
					"testdata/diff/resources/existing-sequencer-gating-stage1-ready.yaml"),
			},
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			ExpectedStructured: simtest.Expect().
				WithSummary(0, 3, 0).
				WithModifiedResource("XNopResource", "sequencer-test", "default").
				WithFieldChange("spec.coolField", "existing-value", "test-value").
				And().
				WithModifiedResource("XDownstreamResource", "0-stage0-resource", "default").
				WithFieldChange("spec.forProvider.configData", "existing-value", "test-value").
				And().
				WithModifiedResource("XDownstreamResource", "1-stage1-resource", "default").
				WithFieldChange("spec.forProvider.configData", "existing-value", "test-value"),
			ExpectedError: false,
		},
		// Paired composition-driven gating tests — the fixture conditional-gating-composition.yaml
		// encodes gating in go-templating itself (no function-sequencer): stage1 is only rendered
		// when observed stage0 has Ready=True. This isolates eventual-state simulation from
		// function-sequencer and exercises the synthesize-Ready path directly.
		"ConditionalGatingHidesLaterStageWithoutEventualState": {
			Reason:       "Without --eventual-state, template conditionally gates stage1 because observed stage0 is not Ready",
			Timeout:      simharness.LongTimeout,
			OutputFormat: "json",
			// eventualState deliberately false
			InputFiles: []string{"testdata/diff/resources/conditional-gating-xr.yaml"},
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/conditional-gating-composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			// Expect: XR + stage0 added; stage1 NOT present (gated by the template's Ready check).
			ExpectedStructured: simtest.Expect().
				WithSummary(2, 0, 0).
				WithAddedResource("XNopResource", "conditional-gating-test", "default").
				WithField("spec.coolField", "test-value").
				And().
				WithAddedResource("XDownstreamResource", "0-stage0-resource", "default").
				WithField("spec.forProvider.configData", "test-value"),
			ExpectedError: false,
		},
		"ConditionalGatedStageAppearsWithEventualState": {
			Reason:        "With --eventual-state, synthesize-Ready flips stage0 Ready=True in observed so the template renders stage1",
			Timeout:       simharness.LongTimeout, // Multiple simulation iterations need more time
			OutputFormat:  "json",
			EventualState: true,
			InputFiles:    []string{"testdata/diff/resources/conditional-gating-xr.yaml"},
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/conditional-gating-composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
			},
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			// Expect: XR + stage0 + stage1 all added.
			ExpectedStructured: simtest.Expect().
				WithSummary(3, 0, 0).
				WithAddedResource("XNopResource", "conditional-gating-test", "default").
				WithField("spec.coolField", "test-value").
				And().
				WithAddedResource("XDownstreamResource", "0-stage0-resource", "default").
				WithField("spec.forProvider.configData", "test-value").
				And().
				WithAddedResource("XDownstreamResource", "1-stage1-resource", "default").
				WithField("spec.forProvider.configData", "test-value"),
			ExpectedError: false,
		},
		"EventualStateWithSequencerAndEnvironmentConfigs": {
			Reason:  "Shows eventual state with function-sequencer AND function-environment-configs to verify requirements resolution during simulation",
			Timeout: simharness.LongTimeout, // Multiple simulation iterations with requirements resolution need more time
			// This test verifies that the eventual state simulation correctly resolves requirements
			// (like environment configs) during its iterative rendering, not just in the final render.
			// The composition uses:
			// 1. function-environment-configs to fetch env config (requires requirements iteration)
			// 2. function-go-templating to generate resources using env config data
			// 3. function-sequencer to stage resources
			// 4. function-auto-ready
			OutputFormat:  "json",
			EventualState: true,
			InputFiles:    []string{"testdata/diff/resources/sequencer-with-envconfig-xr.yaml"},
			SetupFiles: []string{
				"../../../internal/testutils/integration/testdata/diff/resources/xrd.yaml",
				"testdata/diff/resources/sequencer-with-envconfig-composition.yaml",
				"../../../internal/testutils/integration/testdata/diff/resources/functions.yaml",
				"testdata/diff/resources/environment-config-v1beta1.yaml",
			},
			ExpectedExitCode: dp.ExitCodeDiffDetected,
			ExpectedStructured: simtest.Expect().
				WithSummary(3, 0, 0). // 2 downstream resources + 1 XR
				WithAddedResource("XDownstreamResource", "0-stage0-resource", "default").
				WithField("spec.forProvider.configData", "test-value").
				WithField("spec.forProvider.resourceTier", "staging"). // From environment config
				And().
				WithAddedResource("XDownstreamResource", "1-stage1-resource", "default").
				WithField("spec.forProvider.configData", "test-value").
				WithField("spec.forProvider.resourceTier", "staging"). // From environment config
				And().
				WithAddedResource("XNopResource", "sequencer-envconfig-test", "default").
				WithField("spec.coolField", "test-value"),
			ExpectedError: false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			simharness.RunIntegrationTest(t, simharness.XRTest, tt)
		})
	}
}
