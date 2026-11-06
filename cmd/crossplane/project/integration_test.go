//go:build integration

package project

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/client-go/rest"

	dp "github.com/crossplane/cli/v2/internal/diff/diffprocessor"
	tu "github.com/crossplane/cli/v2/internal/diff/testutils"
	"github.com/crossplane/cli/v2/internal/simulate/simtest"
	"github.com/crossplane/cli/v2/internal/testutils/integration"
)

// longTimeout is the --timeout for cases with multiple iterations, such as
// eventual state.
const longTimeout = 180 * time.Second

// DiffTestType represents the type of diff test to run.
type DiffTestType string

const (
	XRDiffTest          DiffTestType = "diff"
	CompositionDiffTest DiffTestType = "comp"
)

// IntegrationTestCase represents a common test case structure for both XR and composition diff tests.
type IntegrationTestCase struct {
	reason           string // Description of what this test validates
	setupFiles       []string
	deleteAfterSetup []string // Files whose resources are deleted after setup; with a finalizer this leaves them Terminating
	// crossplaneManagedResources are the XRs that exist in the cluster, with what they compose. Every
	// composed resource is server-side applied under Crossplane's composed field manager and given a
	// controller ownerReference to its XR's real UID, and each XR lists its composed resources in its
	// resourceRefs, as Crossplane leaves them. Use integration.Composes() for an XR without nested XRs. Put a
	// composed resource in setupFiles instead only when it deliberately has no controlling XR.
	crossplaneManagedResources []integration.HierarchicalOwnershipRelation
	// fieldManagerApplies are server-side applied, in order, after setupFiles and
	// crossplaneManagedResources, each under its own
	// field manager. Use it when a test depends on which manager owns a field: a plain Create
	// records only the test client as owner, and the apiserver drops managedFields supplied on Create.
	fieldManagerApplies []integration.FieldManagerApply
	// unestablishedXRDs are XRD manifests applied after setupFiles but, unlike an XRD in setupFiles, given
	// no generated CRDs: an XRD Crossplane has not established yet, so its XR and claim types are not served.
	unestablishedXRDs []string
	// rbacRules, when set, makes the case run the command twice: as the cluster admin, then as a user
	// bound to a ClusterRole granting only these rules (plus the discovery and self-review access the
	// apiserver's bootstrap RBAC grants every authenticated user). The two runs must print identical
	// output, and the usual assertions apply to the second.
	rbacRules              []rbacv1.PolicyRule
	inputFiles             []string // Input files to diff (XR YAML files or Composition YAML files)
	expectedOutput         string
	expectedError          bool
	expectedErrorContains  string
	expectedStderrContains []string // substrings that must appear on stderr (warnings, error lines)
	expectedExitCode       int      // Expected exit code (0=success, 1=tool error, 2=schema validation, 3=diff detected)
	noColor                bool
	namespace              string                    // For composition tests (optional)
	xrdAPIVersion          integration.XRDAPIVersion // For XR tests (optional)
	ignorePaths            []string                  // Paths to ignore in diffs
	eventualState          bool                      // Enable eventual state simulation for XR or composition tests (optional)
	timeout                time.Duration             // Custom timeout for this test (0 = use default)
	resources              []string                  // For composition tests: --resource values; each entry passed as one --resource flag
	resourcesCSV           string                    // For composition tests: alternative single --resource=a,b style invocation
	includeManual          bool                      // For composition tests: pass --include-manual flag
	analyzeUnchanged       bool                      // For composition tests: pass the deprecated --analyze-unchanged flag
	analyzeOn              string                    // For composition tests: pass --analyze-on=<value> (empty = rely on the default)
	// dryRunOn passes --dry-run-on=<value> (empty = rely on the default, which is "all").
	// The flag lives on CommonCmdFields, so it applies to both `xr` and `comp`.
	dryRunOn   string
	skip       bool
	skipReason string
	// JSON output support: set outputFormat to "json" to use structured assertions.
	outputFormat string // "json" or "" (default=visual diff)
	// expectedStructured is asserted against the JSON output in either mode. It is typed as the
	// Expectation interface, which every builder level implements, so a chain can end where it ends.
	expectedStructured simtest.Expectation
}

// runIntegrationTest runs a single integration test case for both XR and composition diff commands.
func runIntegrationTest(t *testing.T, testType DiffTestType, tt IntegrationTestCase) {
	t.Helper()

	t.Parallel() // Enable parallel test execution

	// Resolve a local crossplane binary if one is present. When non-empty,
	// it's threaded into the kong arg slice below as
	// --crossplane-render-binary=<path> so each parallel subtest has its own
	// copy with no shared process state. When empty, the diff command falls
	// through to the docker render engine — slower but works wherever Docker
	// does (including the Earthfile's go-test target's WITH DOCKER block).
	crossplaneBin := localCrossplaneBinary(t)

	// Skip test if requested
	if tt.skip {
		t.Skip(tt.skipReason)
		return
	}

	// Use custom timeout if specified, otherwise use default
	testTimeout := defaultTimeout
	if tt.timeout > 0 {
		testTimeout = tt.timeout
	}

	// Set up the test files
	testFiles := make([]string, 0, len(tt.inputFiles))

	// Handle any additional input files
	// Note: NewCompositeLoader handles both individual files and directories,
	// so we can pass paths directly without special handling
	testFiles = append(testFiles, tt.inputFiles...)

	// Capture stdout and stderr into separate buffers. The diff command
	// writes structured output (JSON / YAML / text diff) to stdout and
	// human-readable error lines to stderr; mixing them defeats JSON
	// parsing for tests that drive the structured assertion path.
	var stdout, stderr bytes.Buffer

	// Create command line args that match your pre-populated struct
	args := []string{
		fmt.Sprintf("--timeout=%s", testTimeout.String()),
	}

	// Only thread --crossplane-render-binary when a local binary exists; an
	// empty value would still parse but waste cycles, and a missing flag
	// lets the diff command pick the docker render engine cleanly.
	if crossplaneBin != "" {
		args = append(args, fmt.Sprintf("--crossplane-render-binary=%s", crossplaneBin))
	}

	// Add namespace if specified (for composition tests only)
	if tt.namespace != "" && testType == CompositionDiffTest {
		args = append(args, fmt.Sprintf("--namespace=%s", tt.namespace))
	}

	// Add no-color flag if true
	if tt.noColor {
		args = append(args, "--no-color")
	}

	// Add JSON output format if specified
	if tt.outputFormat == "json" {
		args = append(args, "--output=json")
	}

	// Add ignore-paths if specified
	if len(tt.ignorePaths) > 0 {
		for _, path := range tt.ignorePaths {
			args = append(args, fmt.Sprintf("--ignore-paths=%s", path))
		}
	}

	// Add eventual-state flag if specified (supported by both `xr` and `comp`).
	if tt.eventualState {
		args = append(args, "--eventual-state")
	}

	// --dry-run-on lives on CommonCmdFields, so unlike --analyze-on it is threaded for both
	// command types. An empty value relies on the flag's own default.
	if tt.dryRunOn != "" {
		args = append(args, "--dry-run-on="+tt.dryRunOn)
	}

	// Add --resource flags (composition tests only)
	if testType == CompositionDiffTest {
		for _, r := range tt.resources {
			args = append(args, fmt.Sprintf("--resource=%s", r))
		}

		if tt.resourcesCSV != "" {
			args = append(args, fmt.Sprintf("--resource=%s", tt.resourcesCSV))
		}

		if tt.includeManual {
			args = append(args, "--include-manual")
		}

		if tt.analyzeUnchanged {
			args = append(args, "--analyze-unchanged")
		}

		if tt.analyzeOn != "" {
			args = append(args, "--analyze-on="+tt.analyzeOn)
		}
	}

	// Add files as positional arguments
	args = append(args, testFiles...)

	// The render engine runs functions as Docker containers. Without a Docker daemon no rendering
	// case can run; cases that would not render anyway cannot be told apart upfront, so they skip.
	if !dockerAvailable() {
		t.Skip("env: no Docker daemon for the function render engine")
		return
	}

	// Start fails, rather than skips, a run without envtest's binaries or Crossplane's CRDs.
	cluster := integration.Start(t, integration.Setup{
		CRDDirs:                    []string{"testdata/" + string(testType) + "/crds"},
		SetupFiles:                 tt.setupFiles,
		UnestablishedXRDs:          tt.unestablishedXRDs,
		CrossplaneManagedResources: tt.crossplaneManagedResources,
		XRDAPIVersion:              tt.xrdAPIVersion,
		FieldManagerApplies:        tt.fieldManagerApplies,
		DeleteAfterSetup:           tt.deleteAfterSetup,
		RBACRules:                  tt.rbacRules,
	})

	// runOnce executes the command against the running cluster, leaving its output in stdout /
	// stderr. Everything the command layer touches is rebuilt per call, so a second run starts
	// with cold client caches just as a second invocation of the CLI would.
	runOnce := func(runCfg *rest.Config) (int, error) {
		stdout.Reset()
		stderr.Reset()

		return runSimulate(t, runCfg, testType, args, &stdout, &stderr)
	}

	exitCode, err := runOnce(cluster.Config)

	if cluster.UserConfig != nil {
		admin, adminErr := stdout.String(), err

		exitCode, err = runOnce(cluster.UserConfig)

		if restricted := stdout.String(); restricted != admin {
			t.Errorf("output with only the case's RBAC rules differs from the admin's; the run needs access the rules don't grant.\nadmin (error: %v):\n%s\nrestricted (error: %v, stderr below):\n%s\n%s",
				adminErr, admin, err, restricted, stderr.String())
		}
	}

	// Check exit code matches expected
	if exitCode != tt.expectedExitCode {
		t.Errorf("expected exit code %d, got %d", tt.expectedExitCode, exitCode)
	}

	// Assert stderr expectations before any of the early returns below, so they apply in every
	// output mode (human-readable, JSON, and expected-error cases alike).
	for _, want := range tt.expectedStderrContains {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("expected stderr to contain %q, got:\n%s", want, stderr.String())
		}
	}

	if tt.expectedError && err == nil {
		t.Fatal("expected error but got none")
	}

	if !tt.expectedError && err != nil {
		t.Fatalf("expected no error but got: %v", err)
	}

	// Check for specific error message if expected.
	//
	// Three modes:
	//   1. expectedError + expectedErrorContains: substring-check the error.
	//   2. expectedError + structured assertion: any error is fine here;
	//      the structured assertion below validates errors[].
	//   3. unexpected error: already caught by the t.Fatalf above.
	if err != nil && tt.expectedError {
		hasStructuredAssertion := tt.expectedStructured != nil

		switch {
		case tt.expectedErrorContains != "":
			if strings.Contains(err.Error(), tt.expectedErrorContains) {
				t.Logf("Got expected error containing: %s", tt.expectedErrorContains)
			} else {
				t.Errorf("Expected error containing %q, got: %v", tt.expectedErrorContains, err)
			}
		case hasStructuredAssertion:
			t.Logf("Got expected error; structured assertion will validate errors[]")
		default:
			t.Errorf("expectedError set without expectedErrorContains or structured assertion; got: %v", err)
		}
	}

	// Skip stdout check for expected-error cases that use the legacy
	// substring assertion. Cases that pin a structured assertion fall
	// through so the JSON path is exercised against errors[] / changes.
	if tt.expectedError && tt.expectedErrorContains != "" &&
		tt.expectedStructured == nil {
		return
	}

	// Check the output
	outputStr := stdout.String()

	// Handle JSON output format with structured assertions
	if tt.outputFormat == "json" && tt.expectedStructured != nil {
		simtest.AssertStructured(t, outputStr, tt.expectedStructured)
		return
	}

	// Using TrimSpace because the output might have trailing newlines
	if !strings.Contains(strings.TrimSpace(outputStr), strings.TrimSpace(tt.expectedOutput)) {
		// Strings aren't equal, *including* ansi.  but we can compare ignoring ansi to determine what output to
		// show for the failure.  if the difference is only in color codes, we'll show escaped ansi codes.
		out := outputStr

		expect := tt.expectedOutput
		if tu.CompareIgnoringAnsi(strings.TrimSpace(outputStr), strings.TrimSpace(tt.expectedOutput)) {
			out = strconv.QuoteToASCII(outputStr)
			expect = strconv.QuoteToASCII(tt.expectedOutput)
		}

		t.Fatalf("expected output to contain:\n%s\n\nbut got:\n%s", expect, out)
	}
}

// leastPrivilegeRules is the access a diff of an XNopResource composing XDownstreamResources needs, the
// types the testdata XRDs, compositions and CRDs use. It grants Crossplane's own types only get, except a
// list of CompositionRevisions, which are found by their composition's label. It grants list only where a
// lookup is scoped by a label. xrVerbs are the verbs on
// the XR type: get to read the existing XR, patch and create to dry-run it, and list only where the
// command finds composites by listing them.
func leastPrivilegeRules(xrVerbs ...string) []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		// Fetched by name as the input needs them: the XR's XRD and CRD, its Composition, the
		// pipeline's Functions, and any EnvironmentConfig a function requires by name.
		{APIGroups: []string{"apiextensions.crossplane.io"}, Resources: []string{"compositeresourcedefinitions", "compositions", "environmentconfigs"}, Verbs: []string{"get"}},
		{APIGroups: []string{"apiextensions.k8s.io"}, Resources: []string{"customresourcedefinitions"}, Verbs: []string{"get"}},
		{APIGroups: []string{"pkg.crossplane.io"}, Resources: []string{"functions"}, Verbs: []string{"get"}},
		// A composition's revisions are listed by its crossplane.io/composition-name label.
		{APIGroups: []string{"apiextensions.crossplane.io"}, Resources: []string{"compositionrevisions"}, Verbs: []string{"get", "list"}},
		// The XR type.
		{APIGroups: []string{"ns.diff.example.org"}, Resources: []string{"xnopresources"}, Verbs: xrVerbs},
		// Composed resources: read from the XR's resource tree, listed by their crossplane.io/composite
		// label to find a new XR's existing ones, and dry-run created or applied.
		{APIGroups: []string{"ns.nop.example.org"}, Resources: []string{"xdownstreamresources"}, Verbs: []string{"get", "list", "create", "patch"}},
	}
}

// TestCompDiffIntegration runs an integration test for the composition diff command.
func TestCompDiffIntegration(t *testing.T) {
	t.Parallel()

	tests := map[string]IntegrationTestCase{
		"CompositionChangeImpactsXRs": {
			reason: "Validates composition change impacts existing XRs",
			// Set up existing XRs that use the original composition
			setupFiles: []string{
				"testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/original-composition.yaml",
				"testdata/comp/resources/functions.yaml",
			},
			// Add existing XRs that use the composition
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/comp/resources/existing-xr-1.yaml",
					"testdata/comp/resources/existing-downstream-1.yaml",
				),
				integration.Composes(
					"testdata/comp/resources/existing-xr-2.yaml",
					"testdata/comp/resources/existing-downstream-2.yaml",
				),
			},
			// New composition files that will be diffed
			inputFiles: []string{"testdata/comp/updated-composition.yaml"},
			namespace:  "default",
			expectedOutput: `
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
			expectedError:    false,
			expectedExitCode: dp.ExitCodeDiffDetected,
			noColor:          true,
		},
		// Issue #452: an XR with a deletionTimestamp is on Crossplane's teardown path and will never
		// adopt the diffed composition's revision. Rendering it yields no composed resources (the
		// composite reconciler takes its deletion path), so including it produces a meaningless
		// impact analysis. It must be excluded and counted, leaving only the live XR evaluated.
		"CompositionDiffExcludesDeletingXRs": {
			reason: "A deleting XR is excluded from impact analysis and counted as filteredByDeletion; the live XR is still evaluated",
			setupFiles: []string{
				"testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/original-composition.yaml",
				"testdata/comp/resources/functions.yaml",
			},
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				{
					OwnerFile: "testdata/comp/resources/existing-xr-1.yaml",
					OwnedFiles: map[string]*integration.HierarchicalOwnershipRelation{
						"testdata/comp/resources/existing-downstream-1.yaml": nil,
					},
				},
				{
					OwnerFile: "testdata/comp/resources/existing-xr-deleting.yaml",
					OwnedFiles: map[string]*integration.HierarchicalOwnershipRelation{
						"testdata/comp/resources/existing-downstream-deleting.yaml": nil,
					},
				},
			},
			deleteAfterSetup: []string{"testdata/comp/resources/existing-xr-deleting.yaml"},
			inputFiles:       []string{"testdata/comp/updated-composition.yaml"},
			namespace:        "default",
			outputFormat:     "json",
			expectedExitCode: dp.ExitCodeDiffDetected, // the composition itself changed
			// Total counts both discovered XRs; only the live one is evaluated (1 with changes), and
			// the deleting one is reported via filteredByDeletion. In default-discovery mode filtered
			// XRs are counted but not surfaced as impact entries, so only test-resource appears.
			expectedStructured: simtest.Expect().
				WithComposition("xnopresources.diff.example.org").
				WithCompositionModified().
				WithAffectedResources(2, 1, 0, 0).
				WithFilteredByDeletion(1).
				WithXRImpact("XNopResource", "test-resource", "default", "changed"),
		},
		// Issue #452, --resource mode: filtered XRs are surfaced explicitly there, so a user who names
		// a deleting composite is told why it was skipped rather than getting silence.
		"ResourceFilterSurfacesDeletingXR": {
			reason: "--resource matching a deleting composite surfaces it as filtered with reason deleting",
			setupFiles: []string{
				"testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/original-composition.yaml",
				"testdata/comp/resources/functions.yaml",
			},
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				{
					OwnerFile: "testdata/comp/resources/existing-xr-deleting.yaml",
					OwnedFiles: map[string]*integration.HierarchicalOwnershipRelation{
						"testdata/comp/resources/existing-downstream-deleting.yaml": nil,
					},
				},
			},
			deleteAfterSetup: []string{"testdata/comp/resources/existing-xr-deleting.yaml"},
			inputFiles:       []string{"testdata/comp/updated-composition.yaml"},
			resources:        []string{"default/deleting-resource"},
			outputFormat:     "json",
			expectedExitCode: dp.ExitCodeDiffDetected, // the composition itself changed
			expectedStructured: simtest.Expect().
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
			reason: "comp against an XR whose resourceRefs name a deleted composed resource warns and reports it as an addition",
			setupFiles: []string{
				"testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/original-composition.yaml",
				"testdata/comp/resources/functions.yaml",
			},
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				{
					OwnerFile: "testdata/comp/resources/existing-xr-1.yaml",
					OwnedFiles: map[string]*integration.HierarchicalOwnershipRelation{
						"testdata/comp/resources/existing-downstream-1.yaml": nil,
					},
				},
			},
			deleteAfterSetup: []string{"testdata/comp/resources/existing-downstream-1.yaml"},
			inputFiles:       []string{"testdata/comp/updated-composition.yaml"},
			namespace:        "default",
			outputFormat:     "json",
			expectedExitCode: dp.ExitCodeDiffDetected,
			expectedStructured: simtest.Expect().
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
		// Regression test for #552:
		// the updated composition stops rendering resource2, which the XR controls, so it is removed;
		// the XR's resourceRefs also name a resource another XR controls and one with no controller,
		// which the composition doesn't render either, and neither of those is removed.
		"CompositionDropsResourceRemovingOnlyWhatTheXRControls": {
			reason: "A composition that stops rendering resources removes only those the XR controls",
			// What this case pins: test-resource's resourceRefs still name two resources it doesn't control.
			setupFiles: []string{
				"testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/removal-test-original-composition.yaml",
				"testdata/diff/resources/composition.yaml",
				"testdata/comp/resources/functions.yaml",
				// Deliberately not in crossplaneManagedResources: its controller reference was removed, so
				// no XR controls it, though test-resource's resourceRefs still name it.
				"testdata/diff/resources/removal-test-ns-downstream-without-controller.yaml",
			},
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/comp/resources/existing-xr-listing-uncontrolled-resources.yaml",
					"testdata/diff/resources/removal-test-ns-downstream-resource1.yaml", // still rendered: kept
					"testdata/diff/resources/removal-test-ns-downstream-resource2.yaml", // no longer rendered: removed
				),
				integration.Composes(
					"testdata/diff/resources/existing-other-xr.yaml",
					"testdata/diff/resources/existing-other-xr-downstream.yaml",
				),
			},
			inputFiles:       []string{"testdata/diff/resources/removal-test-composition.yaml"},
			namespace:        "default",
			outputFormat:     "json",
			expectedExitCode: dp.ExitCodeDiffDetected,
			expectedStructured: simtest.Expect().
				WithComposition("removal-test-composition").
				WithCompositionModified().
				WithAffectedResources(1, 1, 0, 0).
				WithXRImpact("XNopResource", "test-resource", "default", "changed").
				WithDownstreamSummary(0, 0, 1).
				WithDownstreamResource("removed", "XDownstreamResource", "resource-to-be-removed", "default"),
		},
		"CompositionDiffIgnorePaths": {
			reason: "Validates that ArgoCD annotations are ignored in composition diffs",
			setupFiles: []string{
				"testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/original-composition.yaml",
				"testdata/comp/resources/functions.yaml",
			},
			// Add existing XR with ArgoCD annotations
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/comp/resources/existing-xr-with-argocd.yaml",
					"testdata/comp/resources/existing-downstream-with-argocd.yaml",
				),
			},
			inputFiles: []string{"testdata/comp/composition-no-changes.yaml"},
			namespace:  "default",
			// The composition is unchanged, so impact analysis would be skipped by default (issue
			// #453). This case is about XR/downstream-level ignore paths, so it opts back in.
			analyzeUnchanged: true,
			ignorePaths: []string{
				"metadata.annotations[argocd.argoproj.io/tracking-id]",
				"metadata.labels[argocd.argoproj.io/instance]",
			},
			expectedOutput: `
=== Composition Changes ===

No changes detected in composition xnopresources.diff.example.org

=== Affected Composite Resources ===

  ✓ XNopResource/test-resource (namespace: default)

Summary: 1 resource unchanged

=== Impact Analysis ===

All composite resources are up-to-date. No downstream resource changes detected.

`,
			expectedError: false,
			noColor:       true,
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
			reason: "An unchanged composition skips impact analysis entirely and says so",
			setupFiles: []string{
				"testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/original-composition.yaml",
				"testdata/comp/resources/functions.yaml",
			},
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/comp/resources/existing-xr-1.yaml",
					"testdata/comp/resources/existing-downstream-1.yaml",
				),
			},
			inputFiles: []string{"testdata/comp/composition-no-changes.yaml"},
			namespace:  "default",
			expectedOutput: `
=== Composition Changes ===

No changes detected in composition xnopresources.diff.example.org

Impact analysis skipped: this composition is identical to the cluster's, so applying it creates no new CompositionRevision and no composite resource could change as a result. Pass --analyze-on=always to evaluate them anyway.

`,
			expectedError: false,
			// No composition change and no evaluated XRs, so nothing was detected.
			expectedExitCode: dp.ExitCodeSuccess,
			noColor:          true,
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
			reason: "A composition differing only in kubectl's last-applied-configuration still counts as changed, because applying it creates a new CompositionRevision",
			setupFiles: []string{
				"testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/original-composition-kubectl-applied.yaml",
				"testdata/comp/resources/functions.yaml",
			},
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/comp/resources/existing-xr-1-kubectl-applied.yaml",
					"testdata/comp/resources/existing-downstream-1.yaml",
				),
			},
			inputFiles:       []string{"testdata/comp/composition-no-changes.yaml"},
			namespace:        "default",
			outputFormat:     "json",
			expectedExitCode: dp.ExitCodeSuccess,
			expectedStderrContains: []string{
				// Issue #474: the composites are still evaluated, but they are rendered with their existing
				// compositionRevisionRef, because the name of the revision this would create cannot be
				// predicted for a client-side-applied composition. Saying so is the honest position: if this
				// composition's template read the revision name, the resulting change would go undetected.
				"Could not predict the name of the CompositionRevision",
			},
			expectedStructured: simtest.Expect().
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
			reason: "A composition whose live last-applied-configuration already describes the file is unchanged, because re-applying it writes nothing",
			setupFiles: []string{
				"testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/original-composition-kubectl-applied-current.yaml",
				"testdata/comp/resources/functions.yaml",
			},
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/comp/resources/existing-xr-1-kubectl-applied-current.yaml",
					"testdata/comp/resources/existing-downstream-1.yaml",
				),
			},
			inputFiles:       []string{"testdata/comp/composition-no-changes.yaml"},
			namespace:        "default",
			outputFormat:     "json",
			expectedExitCode: dp.ExitCodeSuccess,
			expectedStructured: simtest.Expect().
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
			reason: "A metadata-only composition edit whose template reads the CompositionRevision name changes rendered output, and is detected rather than assumed away",
			setupFiles: []string{
				"testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/revision-templating-composition.yaml",
				"testdata/comp/resources/revision-templating-composition-revision.yaml",
				"testdata/comp/resources/functions.yaml",
			},
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/comp/resources/existing-xr-revision-ref.yaml",
					"testdata/comp/resources/existing-downstream-revision-ref.yaml",
				),
			},
			inputFiles:       []string{"testdata/comp/revision-templating-updated-composition.yaml"},
			namespace:        "default",
			outputFormat:     "json",
			expectedExitCode: dp.ExitCodeDiffDetected,
			expectedStructured: simtest.Expect().
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
			reason: "Under --include-manual, the predicted revision name reaches an Automatic composite but not a Manual one pinned to an older revision",
			setupFiles: []string{
				"testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/revision-templating-composition.yaml",
				"testdata/comp/resources/revision-templating-composition-revision.yaml",
				"testdata/comp/resources/revision-templating-composition-older-revision.yaml",
				"testdata/comp/resources/functions.yaml",
			},
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/comp/resources/existing-xr-revision-ref.yaml",
					"testdata/comp/resources/existing-downstream-revision-ref.yaml",
				),
				integration.Composes(
					"testdata/comp/resources/existing-xr-pinned-revision-ref.yaml",
					"testdata/comp/resources/existing-downstream-pinned-revision-ref.yaml",
				),
			},
			inputFiles:       []string{"testdata/comp/revision-templating-updated-composition.yaml"},
			namespace:        "default",
			includeManual:    true,
			outputFormat:     "json",
			expectedExitCode: dp.ExitCodeDiffDetected,
			expectedStructured: simtest.Expect().
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
			reason: "A metadata-only composition edit is rendered for an Automatic claim with the revision its backing XR would re-point to",
			setupFiles: []string{
				"testdata/comp/resources/claim-xrd.yaml",
				"testdata/comp/resources/claim-revision-templating-composition.yaml",
				"testdata/comp/resources/functions.yaml",
				"testdata/comp/resources/test-namespace.yaml",
				"testdata/comp/resources/existing-claim-revision-ref.yaml",
			},
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/comp/resources/existing-claim-revision-ref-xr.yaml",
					"testdata/comp/resources/existing-claim-revision-ref-downstream.yaml",
				),
			},
			xrdAPIVersion:    integration.XRDV1, // the XRD is v1, so Crossplane writes the XR's resourceRefs at spec.resourceRefs
			inputFiles:       []string{"testdata/comp/claim-revision-templating-updated-composition.yaml"},
			namespace:        "test-namespace",
			outputFormat:     "json",
			expectedExitCode: dp.ExitCodeDiffDetected,
			expectedStructured: simtest.Expect().
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
			reason: "An Automatic claim with nothing seeded renders with its backing XR's compositionRevisionRef, so a template reading the revision name shows no spurious change",
			setupFiles: []string{
				"testdata/comp/resources/claim-xrd.yaml",
				"testdata/comp/resources/claim-revision-templating-composition-kubectl-applied.yaml",
				"testdata/comp/resources/functions.yaml",
				"testdata/comp/resources/test-namespace.yaml",
				"testdata/comp/resources/existing-claim-revision-ref-kubectl-applied.yaml",
			},
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/comp/resources/existing-claim-revision-ref-kubectl-applied-xr.yaml",
					"testdata/comp/resources/existing-claim-revision-ref-kubectl-applied-downstream.yaml",
				),
			},
			xrdAPIVersion:          integration.XRDV1, // the XRD is v1, so Crossplane writes the XR's resourceRefs at spec.resourceRefs
			inputFiles:             []string{"testdata/comp/resources/claim-revision-templating-composition.yaml"},
			namespace:              "test-namespace",
			outputFormat:           "json",
			expectedExitCode:       dp.ExitCodeSuccess,
			expectedStderrContains: []string{"Could not predict the name of the CompositionRevision"},
			expectedStructured: simtest.Expect().
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
			reason: "--analyze-on=spec-change leaves a metadata-only change unevaluated, while still reporting that it creates a CompositionRevision",
			setupFiles: []string{
				"testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/original-composition-kubectl-applied.yaml",
				"testdata/comp/resources/functions.yaml",
			},
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/comp/resources/existing-xr-1-kubectl-applied.yaml",
					"testdata/comp/resources/existing-downstream-1.yaml",
				),
			},
			inputFiles:       []string{"testdata/comp/composition-no-changes.yaml"},
			namespace:        "default",
			analyzeOn:        "spec-change",
			outputFormat:     "json",
			expectedExitCode: dp.ExitCodeSuccess,
			expectedStructured: simtest.Expect().
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
			reason: "--analyze-unchanged evaluates affected XRs even though the composition is unchanged, surfacing deltas not caused by it",
			setupFiles: []string{
				"testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/original-composition.yaml",
				"testdata/comp/resources/functions.yaml",
			},
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/comp/resources/existing-xr-1.yaml",
					"testdata/comp/resources/existing-downstream-drifted.yaml",
				),
			},
			inputFiles:       []string{"testdata/comp/composition-no-changes.yaml"},
			namespace:        "default",
			analyzeUnchanged: true,
			outputFormat:     "json",
			expectedExitCode: dp.ExitCodeDiffDetected,
			expectedStructured: simtest.Expect().
				WithComposition("xnopresources.diff.example.org").
				WithAffectedResources(1, 1, 0, 0).
				WithXRImpact("XNopResource", "test-resource", "default", "changed").
				WithDownstreamSummary(0, 1, 0).
				WithDownstreamResource("modified", "XDownstreamResource", "test-resource", "default").
				WithFieldChange("spec.forProvider.configData", "drifted-value", "existing-value"),
		},
		"CompositionDiffCustomNamespace": {
			reason: "Validates composition diff with custom namespace",
			// Set up existing XRs in both custom and default namespaces to validate filtering
			setupFiles: []string{
				"testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/original-composition.yaml",
				"testdata/comp/resources/functions.yaml",
				// Create the custom namespace first
				"testdata/comp/resources/custom-namespace.yaml",
			},
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				// Add existing XRs in custom namespace (should be included in output)
				integration.Composes(
					"testdata/comp/resources/existing-custom-ns-xr.yaml",
					"testdata/comp/resources/existing-custom-ns-downstream.yaml",
				),
				// Add existing XRs in default namespace (should be filtered out)
				integration.Composes(
					"testdata/comp/resources/existing-xr-1.yaml",
					"testdata/comp/resources/existing-downstream-1.yaml",
				),
			},
			// New composition files that will be diffed
			inputFiles: []string{"testdata/comp/updated-composition.yaml"},
			namespace:  "custom-namespace",
			// Expected output should only show custom-namespace resources, NOT default namespace resources
			// This validates that namespace filtering works correctly
			expectedOutput: `
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
			expectedError:    false,
			expectedExitCode: dp.ExitCodeDiffDetected,
			noColor:          true,
		},
		// Two compositions that BOTH genuinely change, each with its own affected XR. This is the
		// aggregation case: one CompositionDiff entry per input, each carrying its own impact analysis,
		// with hasDiffs ORed across them. MultipleCompositionDiffImpact below covers the mixed
		// changed/unchanged variant; before this case existed, nothing covered two compositions that
		// both report impacts — the second composition there has no affected XR at all.
		"MultipleChangedCompositionsBothReportImpact": {
			reason: "Two changed compositions each report their own affected XR's downstream changes",
			setupFiles: []string{
				"testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/original-composition.yaml",
				"testdata/comp/resources/original-composition-2.yaml",
				"testdata/comp/resources/functions.yaml",
			},
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				// XR using composition 1
				integration.Composes(
					"testdata/comp/resources/existing-xr-1.yaml",
					"testdata/comp/resources/existing-downstream-1.yaml",
				),
				// XR using composition 2
				integration.Composes(
					"testdata/comp/resources/existing-xr-v2.yaml",
					"testdata/comp/resources/existing-downstream-v2.yaml",
				),
			},
			inputFiles: []string{
				"testdata/comp/updated-composition.yaml",
				"testdata/comp/updated-composition-2-changed.yaml",
			},
			namespace:        "default",
			outputFormat:     "json",
			expectedExitCode: dp.ExitCodeDiffDetected,
			expectedStructured: simtest.Expect().
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
			reason: "Validates multiple compositions in one invocation: the changed one reports impact on existing XRs, the unchanged one is skipped",
			// Set up existing XRs that use both compositions
			setupFiles: []string{
				"testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/original-composition.yaml",
				"testdata/comp/resources/original-composition-2.yaml",
				"testdata/comp/resources/functions.yaml",
			},
			// Add existing XRs that use the compositions
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/comp/resources/existing-xr-1.yaml",
					"testdata/comp/resources/existing-downstream-1.yaml",
				),
				integration.Composes(
					"testdata/comp/resources/existing-xr-2.yaml",
					"testdata/comp/resources/existing-downstream-2.yaml",
				),
			},
			// Multiple composition files that will be diffed
			inputFiles: []string{
				"testdata/comp/updated-composition.yaml",
				"testdata/comp/updated-composition-2.yaml",
			},
			namespace: "default",
			expectedOutput: `
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
			expectedError:    false,
			expectedExitCode: dp.ExitCodeDiffDetected,
			noColor:          true,
		},
		"CompositionDiffFiltersManualXRs": {
			reason: "Validates composition diff filters Manual policy XRs by default",
			// Set up existing XRs - one with Automatic policy and one with Manual policy
			setupFiles: []string{
				"testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/original-composition.yaml",
				"testdata/comp/resources/functions.yaml",
			},
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				// Add existing XR with Automatic policy (should be included)
				integration.Composes(
					"testdata/comp/resources/existing-xr-1.yaml",
					"testdata/comp/resources/existing-downstream-1.yaml",
				),
				// Add existing XR with Manual policy (should be filtered out by default)
				integration.Composes(
					"testdata/comp/resources/existing-xr-manual.yaml",
					"testdata/comp/resources/existing-downstream-manual.yaml",
				),
			},
			// Updated composition
			inputFiles: []string{"testdata/comp/updated-composition.yaml"},
			namespace:  "default",
			// Expected output should only show test-resource (Automatic), NOT manual-resource (Manual)
			expectedOutput: `
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
			expectedError:    false,
			expectedExitCode: dp.ExitCodeDiffDetected,
			noColor:          true,
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
			// The XR composes the
			// resource as Crossplane leaves it, named in its resourceRefs at v1beta1, so this also checks
			// that it is not reported as removed (#552).
			reason: "Validates composition upgrade that changes resource API version shows as update not remove/add",
			setupFiles: []string{
				"testdata/comp/resources/xrd.yaml",
				// NOTE: xapimigrate CRD is auto-loaded from testdata/comp/crds/
				// We apply no XRD for XApiMigrateResource because it is a regular managed resource
				// (not a composite), and an XRD would make it composite, causing infinite recursion.
				"testdata/comp/resources/api-version-original-composition.yaml",
				"testdata/comp/resources/functions.yaml",
			},
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				{
					OwnerFile: "testdata/comp/resources/existing-api-version-xr.yaml",
					OwnedFiles: map[string]*integration.HierarchicalOwnershipRelation{
						"testdata/comp/resources/existing-api-version-downstream-v1beta1.yaml": nil,
					},
				},
			},
			inputFiles: []string{"testdata/comp/api-version-updated-composition.yaml"},
			namespace:  "default",
			expectedOutput: `
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
			expectedError:    false,
			expectedExitCode: dp.ExitCodeDiffDetected,
			noColor:          true,
		},
		"NetNewCompositionNoImpact": {
			reason: "Validates net-new composition with no downstream impact",
			// Set up cluster with existing resources but no composition that matches the new one
			setupFiles: []string{
				"testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/original-composition.yaml",
				"testdata/comp/resources/functions.yaml",
			},
			// Add existing XRs that use different compositions (won't be affected)
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/comp/resources/existing-xr-1.yaml",
					"testdata/comp/resources/existing-downstream-1.yaml",
				),
			},
			// Net-new composition file that doesn't exist in cluster and targets different XR type
			inputFiles: []string{"testdata/comp/net-new-composition.yaml"},
			namespace:  "default",
			expectedOutput: `
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
			expectedError:    false,
			expectedExitCode: dp.ExitCodeDiffDetected,
			noColor:          true,
		},
		"CompositionChangeWithUnchangedDownstreamResources": {
			reason: "Validates status indicators for unchanged downstream resources",
			// Set up existing XRs that use the original composition
			setupFiles: []string{
				"testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/status-indicator-composition.yaml",
				"testdata/comp/resources/functions.yaml",
			},
			// Add existing XRs that use the composition
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
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
			inputFiles: []string{"testdata/comp/status-indicator-updated-composition.yaml"},
			namespace:  "default",
			expectedOutput: `
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
			expectedError:    false,
			expectedExitCode: dp.ExitCodeDiffDetected,
			noColor:          true,
		},
		"CompositionChangeWithMixedStatusAndColors": {
			reason: "Validates status indicators with colorization for mixed changed/unchanged XRs",
			setupFiles: []string{
				"testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/mixed-status-composition.yaml",
				"testdata/comp/resources/functions.yaml",
			},
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
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
			inputFiles: []string{"testdata/comp/mixed-status-updated-composition.yaml"},
			namespace:  "default",
			expectedOutput: strings.Join([]string{
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
			expectedError:    false,
			expectedExitCode: dp.ExitCodeDiffDetected,
			noColor:          false,
		},
		"CompositionChangeImpactsClaims": {
			reason: "Validates composition change impacts existing Claims (issue #120)",
			// Set up existing Claims and their XRs that use the original composition
			setupFiles: []string{
				// XRD, composition, and functions
				"testdata/comp/resources/claim-xrd.yaml",
				"testdata/comp/resources/claim-composition.yaml",
				"testdata/comp/resources/functions.yaml",
				// Test namespace
				"testdata/comp/resources/test-namespace.yaml",
				// Existing Claims and their corresponding XRs
				"testdata/comp/resources/existing-claim-1.yaml",
				"testdata/comp/resources/existing-claim-2.yaml",
			},
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/comp/resources/existing-claim-1-xr.yaml",
					"testdata/comp/resources/existing-claim-downstream-1.yaml",
				),
				integration.Composes(
					"testdata/comp/resources/existing-claim-2-xr.yaml",
					"testdata/comp/resources/existing-claim-downstream-2.yaml",
				),
			},
			xrdAPIVersion: integration.XRDV1, // the XRD is v1, so Crossplane writes the XR's resourceRefs at spec.resourceRefs
			// Updated composition that will be diffed
			inputFiles: []string{"testdata/comp/updated-claim-composition.yaml"},
			namespace:  "test-namespace",
			expectedOutput: `
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
			expectedError:    false,
			expectedExitCode: dp.ExitCodeDiffDetected,
			noColor:          true,
		},
		// Without -n every namespace is listed, so discovery finds each claim and its cluster-scoped
		// backing XR. The claim renders through that XR, so the XR is dropped: each change is reported
		// once, under the claim.
		"CompositionChangeImpactsClaimsAllNamespaces": {
			reason: "A claim's backing XR discovered alongside it is not reported a second time",
			setupFiles: []string{
				"testdata/comp/resources/claim-xrd.yaml",
				"testdata/comp/resources/claim-composition.yaml",
				"testdata/comp/resources/functions.yaml",
				"testdata/comp/resources/test-namespace.yaml",
				"testdata/comp/resources/existing-claim-1.yaml",
				"testdata/comp/resources/existing-claim-2.yaml",
			},
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/comp/resources/existing-claim-1-xr.yaml",
					"testdata/comp/resources/existing-claim-downstream-1.yaml",
				),
				integration.Composes(
					"testdata/comp/resources/existing-claim-2-xr.yaml",
					"testdata/comp/resources/existing-claim-downstream-2.yaml",
				),
			},
			xrdAPIVersion:    integration.XRDV1,
			inputFiles:       []string{"testdata/comp/updated-claim-composition.yaml"},
			outputFormat:     "json",
			expectedExitCode: dp.ExitCodeDiffDetected,
			expectedStructured: simtest.Expect().
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
			reason: "Two XRs a composition change renders one child for, differently, fail the run",
			setupFiles: []string{
				"testdata/diff/resources/xrd.yaml",
				"testdata/diff/resources/shared-child-composition.yaml",
				"testdata/diff/resources/functions.yaml",
				"testdata/comp/resources/shared-child-xrs-differing.yaml",
			},
			inputFiles:       []string{"testdata/comp/updated-shared-child-composition.yaml"},
			namespace:        "default",
			expectedError:    true,
			expectedExitCode: dp.ExitCodeToolError,
			expectedErrorContains: `resource "ns.nop.example.org/v1alpha1/XDownstreamResource/default/shared-child" would be ` +
				`controlled by more than one XR (XNopResource/shared-child-owner-a, XNopResource/shared-child-owner-b)`,
		},
		"SharedChildContendedEvenWhenIdentical": {
			reason: "Two XRs a composition change renders one child for, identically, still contend for it",
			setupFiles: []string{
				"testdata/diff/resources/xrd.yaml",
				"testdata/diff/resources/shared-child-composition.yaml",
				"testdata/diff/resources/functions.yaml",
				"testdata/diff/shared-child-xrs.yaml",
			},
			inputFiles:       []string{"testdata/comp/updated-shared-child-composition.yaml"},
			namespace:        "default",
			expectedError:    true,
			expectedExitCode: dp.ExitCodeToolError,
			expectedErrorContains: `resource "ns.nop.example.org/v1alpha1/XDownstreamResource/default/shared-child" would be ` +
				`controlled by more than one XR (XNopResource/shared-child-owner-a, XNopResource/shared-child-owner-b)`,
		},
		"SSAFieldRemovalDetection": {
			reason: "Validates that field removals are correctly detected when resources are created via Server-Side Apply with Crossplane's field manager (issue #121)",
			// Set up XRD, functions, and original composition with the removable field
			setupFiles: []string{
				"testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/functions.yaml",
				"testdata/comp/resources/field-removal/composition-with-field.yaml",
			},
			// XR and downstream resource applied via SSA with Crossplane field manager
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				{
					OwnerFile: "testdata/comp/resources/field-removal/existing-xr.yaml",
					OwnedFiles: map[string]*integration.HierarchicalOwnershipRelation{
						"testdata/comp/resources/field-removal/existing-downstream.yaml": nil,
					},
				},
			},
			// Updated composition that removes the removableField
			inputFiles: []string{"testdata/comp/field-removal-updated-composition.yaml"},
			namespace:  "default",
			expectedOutput: `
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
			expectedError:    false,
			expectedExitCode: dp.ExitCodeDiffDetected,
			noColor:          true,
		},
		"SHA256DigestFunctionReference": {
			reason: "Validates composition diff works with functions referenced by SHA256 digest instead of tag",
			setupFiles: []string{
				"testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/sha256-composition.yaml",
				"testdata/comp/resources/functions-sha256.yaml",
			},
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/comp/resources/existing-sha256-xr.yaml",
					"testdata/comp/resources/existing-sha256-downstream.yaml",
				),
			},
			inputFiles: []string{"testdata/comp/updated-sha256-composition.yaml"},
			namespace:  "default",
			expectedOutput: `
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
			expectedError:    false,
			expectedExitCode: dp.ExitCodeDiffDetected,
			noColor:          true,
		},
		// Issue #485, comp's half. comp re-renders each discovered composite through DiffSingleResource,
		// the same recursion xr uses, so one fix covers both — this pins that for comp rather than
		// assuming it. comp's own error only counts the failures, so the assertion that matters is that
		// the depth message reaches the failing composite's impact entry: without it, a user would be
		// told something failed but not that the composition recurses into itself.
		"CyclicCompositionStopsAtMaxNestedDepth": {
			reason:       "comp shares xr's recursion path (it enters via DiffSingleResource), so a composition cycle must stop at --max-nested-depth here too, and say so on the failing composite (#485)",
			outputFormat: "json",
			setupFiles: []string{
				"testdata/diff/resources/cycle/xrds.yaml",
				"testdata/diff/resources/cycle/compositions.yaml",
				"testdata/comp/resources/functions.yaml",
				// Composed nothing: with a cyclic composition, Crossplane would nest XRs without end, so
				// the case models the XR before its first compose rather than an endless tree.
				"testdata/comp/resources/cycle/existing-cycle-xr.yaml",
			},
			inputFiles:            []string{"testdata/comp/updated-cycle-composition.yaml"},
			namespace:             "default",
			expectedError:         true,
			expectedErrorContains: "impact analysis failed for 1 XR(s)",
			expectedExitCode:      dp.ExitCodeToolError,
			expectedStructured: simtest.Expect().
				WithComposition("xcycleas.cycle.example.org").
				WithAffectedResources(1, 0, 0, 1).
				WithXRImpact("XCycleA", "test-cycle", "default", "error").
				WithErrorContaining("maximum nesting depth exceeded"),
		},
		// The same failure, asserting the composite's error is recorded against it in the top-level errors[].
		"CyclicCompositionStopsAtMaxNestedDepthV2": {
			reason:       "A failing composite's error is a structured errors[] entry recorded against it",
			outputFormat: "json",
			setupFiles: []string{
				"testdata/diff/resources/cycle/xrds.yaml",
				"testdata/diff/resources/cycle/compositions.yaml",
				"testdata/comp/resources/functions.yaml",
				// Composed nothing, as in CyclicCompositionStopsAtMaxNestedDepth.
				"testdata/comp/resources/cycle/existing-cycle-xr.yaml",
			},
			inputFiles:            []string{"testdata/comp/updated-cycle-composition.yaml"},
			namespace:             "default",
			expectedError:         true,
			expectedErrorContains: "impact analysis failed for 1 XR(s)",
			expectedExitCode:      dp.ExitCodeToolError,
			expectedStructured: simtest.Expect().
				WithErrorID("XCycleA/test-cycle").
				WithComposition("xcycleas.cycle.example.org").
				WithAffectedResources(1, 0, 0, 1).
				WithXRImpact("XCycleA", "test-cycle", "default", "error").
				WithErrorContaining("maximum nesting depth exceeded").
				WithErrorResourceID("XCycleA/test-cycle"),
		},
		"NestedXRUsesOwnComposition": {
			reason: "Validates that nested XRs use their own composition from the cluster, not the parent's CLI composition",
			// Set up XRDs, compositions, and functions
			setupFiles: []string{
				// Child XRD and composition (XNopResource uses xnopresources.diff.example.org)
				"testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/original-composition.yaml",
				"testdata/comp/resources/functions.yaml",
				// Parent XRD and composition
				"testdata/comp/resources/nested-xr/parent-xrd.yaml",
				"testdata/comp/resources/nested-xr/parent-composition.yaml",
			},
			// Use crossplaneManagedResources to set up proper ownership hierarchy
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
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
			inputFiles: []string{"testdata/comp/updated-parent-composition.yaml"},
			namespace:  "default",
			// Expected: Only the parent's direct downstream shows changes (tier change).
			// The nested child's downstream should NOT change because:
			// 1. The nested XR (XNopResource) uses its own composition (xnopresources.diff.example.org)
			// 2. That child composition was NOT modified
			// Bug scenario: If nested XR incorrectly uses parent composition, we'd see wrong changes to child's downstream.
			// Expected output: Only the parent's direct downstream should show in the Impact Analysis.
			// The child's downstream (XDownstreamResource/test-parent-nested) should NOT appear here.
			// If it did, it would mean the nested XR incorrectly used the parent's CLI composition.
			expectedOutput: `=== Impact Analysis ===

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
			expectedError:    false,
			expectedExitCode: dp.ExitCodeDiffDetected,
			noColor:          true,
		},
		"CrossNamespaceResourceCollision": {
			reason: "Validates that resources with the same name in different namespaces are correctly distinguished",
			skip:   true,
			skipReason: "Blocked on https://github.com/crossplane-contrib/function-extra-resources/issues/106. " +
				"function-extra-resources (v0.2.0/v0.3.0) emits ResourceSelector{Namespace=\"\"} for " +
				"by-name references that omit `ref.namespace` — it only forwards user-yaml verbatim, " +
				"never inferring from the XR's namespace. Crossplane v2.3+'s render binary then does " +
				"InMemoryClient.Get(name, namespace=\"\"), a strict (GVK, ns, name) match that doesn't " +
				"find our resolved ConfigMap (stored at namespace=ns-{a,b}). By-label selectors aren't " +
				"affected — List(InNamespace(\"\")) lists across all namespaces — which is why our " +
				"other extra-resources tests still pass. Re-enable once a function release defaults " +
				"selector.Namespace to the XR's namespace.",
			timeout: longTimeout, // Test involves multiple namespaces and can be slow with Docker contention
			// This test catches a bug where the cache key didn't include namespace, causing
			// ConfigMaps with the same name in different namespaces to collide.
			// The XR in ns-a should get ConfigMap from ns-a (value: from-ns-a)
			// The XR in ns-b should get ConfigMap from ns-b (value: from-ns-b)
			// Bug: Both XRs would get the same ConfigMap (whichever was cached first)
			setupFiles: []string{
				// Create namespaces first
				"testdata/comp/resources/namespace-collision/namespace-ns-a.yaml",
				"testdata/comp/resources/namespace-collision/namespace-ns-b.yaml",
				"testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/functions.yaml",
				"testdata/comp/resources/namespace-collision/collision-composition.yaml",
				"testdata/comp/resources/namespace-collision/collision-composition-revision.yaml",
				// ConfigMaps with same name in different namespaces
				"testdata/comp/resources/namespace-collision/configmap-ns-a.yaml",
				"testdata/comp/resources/namespace-collision/configmap-ns-b.yaml",
			},
			// XRs and their downstream resources in different namespaces
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
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
			inputFiles: []string{"testdata/comp/updated-collision-composition.yaml"},
			// Expected: Each XR's downstream should show the correct prefix applied to its namespace's ConfigMap value.
			// XR in ns-a: from-ns-a → prefix-from-ns-a
			// XR in ns-b: from-ns-b → prefix-from-ns-b
			// Bug scenario: If namespace collision occurs, ns-b would incorrectly show:
			//   from-ns-b → prefix-from-ns-a (wrong ConfigMap cached from ns-a)
			expectedOutput: `=== Impact Analysis ===

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
			expectedError:    false,
			expectedExitCode: dp.ExitCodeDiffDetected,
			noColor:          true,
		},
		// Paired comp sequencer-gating tests — mirror of the XR pair. Verifies that
		// --eventual-state on `comp` surfaces downstream resources hidden by
		// function-sequencer in the impact analysis.
		"CompSequencerGatesLaterStageWithoutEventualState": {
			reason:       "`comp` without --eventual-state: sequencer-gated stage1 is absent from the impact analysis",
			timeout:      longTimeout,
			outputFormat: "json",
			// eventualState deliberately false
			setupFiles: []string{
				"testdata/diff/resources/xrd.yaml",
				"testdata/diff/resources/sequencer-gating-composition.yaml",
				"testdata/diff/resources/functions.yaml",
			},
			// The XR references sequencer-gating-composition by name, so comp discovers it. It has
			// composed stage0, which isn't Ready, so stage1 stays gated.
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes("testdata/comp/resources/existing-sequencer-gating-xr.yaml", "testdata/comp/resources/existing-sequencer-gating-stage0.yaml"),
			},
			inputFiles:       []string{"testdata/comp/updated-sequencer-gating-composition.yaml"},
			namespace:        "default",
			expectedExitCode: dp.ExitCodeDiffDetected,
			// Expect: impact shows stage0 modified (its configData changed); stage1 is absent from
			// downstream impact because sequencer gates it.
			expectedStructured: simtest.Expect().
				WithComposition("sequencer-gating-composition").
				WithCompositionModified().
				WithXRImpact("XNopResource", "sequencer-gating-test", "default", "changed").
				WithDownstreamSummary(0, 1, 0).
				WithDownstreamResource("modified", "XDownstreamResource", "0-stage0-resource", "default").
				WithFieldChange("spec.forProvider.configData", "existing-value", "updated-existing-value"),
			expectedError: false,
		},
		"CompSequencerGatedStageAppearsWithEventualState": {
			reason:        "`comp` with --eventual-state: sequencer-gated stage1 surfaces in the impact analysis",
			timeout:       longTimeout, // Multiple simulation iterations need more time
			outputFormat:  "json",
			eventualState: true,
			setupFiles: []string{
				"testdata/diff/resources/xrd.yaml",
				"testdata/diff/resources/sequencer-gating-composition.yaml",
				"testdata/diff/resources/functions.yaml",
			},
			// The XR has composed stage0, which isn't Ready, so stage1 stays gated until eventual
			// state synthesizes stage0's Ready (#564).
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes("testdata/comp/resources/existing-sequencer-gating-xr.yaml", "testdata/comp/resources/existing-sequencer-gating-stage0.yaml"),
			},
			inputFiles:       []string{"testdata/comp/updated-sequencer-gating-composition.yaml"},
			namespace:        "default",
			expectedExitCode: dp.ExitCodeDiffDetected,
			// Expect: impact shows stage0 modified and stage1 added (eventual-state unlocks stage1).
			expectedStructured: simtest.Expect().
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
			expectedError: false,
		},
		// Paired comp composition-driven gating tests — gating encoded in go-templating,
		// no function-sequencer. Mirrors the XR composition-driven pair.
		"CompConditionalGatingHidesLaterStageWithoutEventualState": {
			reason:       "`comp` without --eventual-state: template-gated stage1 is absent from the impact analysis",
			timeout:      longTimeout,
			outputFormat: "json",
			// eventualState deliberately false
			setupFiles: []string{
				"testdata/diff/resources/xrd.yaml",
				"testdata/diff/resources/conditional-gating-composition.yaml",
				"testdata/diff/resources/functions.yaml",
			},
			// The XR has composed stage0, which isn't Ready, so stage1 stays gated.
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes("testdata/comp/resources/existing-conditional-gating-xr.yaml", "testdata/comp/resources/existing-conditional-gating-stage0.yaml"),
			},
			inputFiles:       []string{"testdata/comp/updated-conditional-gating-composition.yaml"},
			namespace:        "default",
			expectedExitCode: dp.ExitCodeDiffDetected,
			expectedStructured: simtest.Expect().
				WithComposition("conditional-gating-composition").
				WithCompositionModified().
				WithXRImpact("XNopResource", "conditional-gating-test", "default", "changed").
				WithDownstreamSummary(0, 1, 0).
				WithDownstreamResource("modified", "XDownstreamResource", "0-stage0-resource", "default").
				WithFieldChange("spec.forProvider.configData", "existing-value", "updated-existing-value"),
			expectedError: false,
		},
		"CompConditionalGatedStageAppearsWithEventualState": {
			reason:        "`comp` with --eventual-state: template-gated stage1 surfaces in the impact analysis",
			timeout:       longTimeout, // Multiple simulation iterations need more time
			outputFormat:  "json",
			eventualState: true,
			setupFiles: []string{
				"testdata/diff/resources/xrd.yaml",
				"testdata/diff/resources/conditional-gating-composition.yaml",
				"testdata/diff/resources/functions.yaml",
			},
			// The XR has composed stage0, which isn't Ready, so stage1 stays gated until eventual
			// state synthesizes stage0's Ready (#564).
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes("testdata/comp/resources/existing-conditional-gating-xr.yaml", "testdata/comp/resources/existing-conditional-gating-stage0.yaml"),
			},
			inputFiles:       []string{"testdata/comp/updated-conditional-gating-composition.yaml"},
			namespace:        "default",
			expectedExitCode: dp.ExitCodeDiffDetected,
			expectedStructured: simtest.Expect().
				WithComposition("conditional-gating-composition").
				WithCompositionModified().
				WithXRImpact("XNopResource", "conditional-gating-test", "default", "changed").
				WithDownstreamSummary(1, 1, 0).
				WithDownstreamResource("modified", "XDownstreamResource", "0-stage0-resource", "default").
				WithFieldChange("spec.forProvider.configData", "existing-value", "updated-existing-value").
				AndXR().
				WithDownstreamResource("added", "XDownstreamResource", "1-stage1-resource", "default").
				WithField("spec.forProvider.configData", "updated-existing-value"),
			expectedError: false,
		},
		// dryRun presence through `comp`. dryRun lives on ChangeDetail, which `xr` reaches via
		// changes[] and `comp` via impactAnalysis[].downstreamChanges.changes[] — one shared wire
		// shape, but only an assertion through the comp renderer proves comp actually populates it.
		// The composed resource is deliberately absent from the setup so the impact is an ADDITION,
		// which is the only kind of change the dryRun field applies to: the XR has its composition
		// refs, but its first compose has not created its resource.
		"CompDownstreamAdditionReportsDryRunSkippedWhenDryRunOnExisting": {
			reason:       "`comp` structured output carries dryRun.skipReason on a downstream addition when --dry-run-on=existing",
			outputFormat: "json",
			dryRunOn:     "existing",
			setupFiles: []string{
				"testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/original-composition.yaml",
				"testdata/comp/resources/functions.yaml",
				// existing-downstream-1.yaml is intentionally NOT applied: with no composed resource
				// in the cluster, the impact analysis reports an addition rather than a modification.
				"testdata/comp/resources/existing-xr-1.yaml",
			},
			inputFiles:       []string{"testdata/comp/updated-composition.yaml"},
			namespace:        "default",
			expectedExitCode: dp.ExitCodeDiffDetected,
			expectedStructured: simtest.Expect().
				WithComposition("xnopresources.diff.example.org").
				WithCompositionModified().
				WithXRImpact("XNopResource", "test-resource", "default", "changed").
				WithDownstreamSummary(1, 0, 0).
				WithDownstreamResource("added", "XDownstreamResource", "test-resource", "default").
				WithDryRunSkipped("disabled", "").
				WithField("spec.forProvider.configData", "updated-existing-value"),
			expectedError: false,
		},
		// The unverified-addition warning through `comp`: one run-wide summary in the top-level
		// warnings[], counting the downstream additions behind it, rather than one per composition.
		// A cluster-scoped XR is what lets the composition reach a namespace that does not exist.
		"CompDownstreamAdditionsInMissingNamespaceAreCountedInOneWarning": {
			reason:       "`comp` summarises downstream additions it could not verify as one warning per GVK + namespace + cause, with a count",
			outputFormat: "json",
			setupFiles: []string{
				"testdata/diff/resources/cluster-xrd.yaml",
				"testdata/comp/resources/unverified-cluster-composition.yaml",
				"testdata/comp/resources/functions.yaml",
				"testdata/comp/resources/existing-unverified-cluster-xr.yaml",
			},
			inputFiles:       []string{"testdata/comp/updated-unverified-cluster-composition.yaml"},
			expectedExitCode: dp.ExitCodeDiffDetected,
			expectedStderrContains: []string{
				`WARNING: skipped apiserver verification of added resources: their namespace does not exist yet, so their diffs omit server-side defaulting and admission ` +
					`(cause=namespaces "nonexistent-namespace" not found, count=2, gvk=/v1, Kind=ConfigMap, namespace=nonexistent-namespace)`,
			},
			expectedStructured: simtest.Expect().
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
			expectedError: false,
		},
		// --resource flag tests (issue #321)
		"ResourceFilterScopesAffectedXRs": {
			reason: "--resource limits impact analysis to the named composites and ignores the rest",
			setupFiles: []string{
				"testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/original-composition.yaml",
				"testdata/comp/resources/functions.yaml",
			},
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes( // test-resource (default ns)
					"testdata/comp/resources/existing-xr-1.yaml",
					"testdata/comp/resources/existing-downstream-1.yaml",
				),
				integration.Composes( // another-resource (default ns)
					"testdata/comp/resources/existing-xr-2.yaml",
					"testdata/comp/resources/existing-downstream-2.yaml",
				),
			},
			inputFiles:       []string{"testdata/comp/updated-composition.yaml"},
			resources:        []string{"default/test-resource"},
			outputFormat:     "json",
			expectedExitCode: dp.ExitCodeDiffDetected,
			expectedStructured: simtest.Expect().
				WithComposition("xnopresources.diff.example.org").
				WithCompositionModified().
				WithAffectedResources(1, 1, 0, 0).
				WithXRImpact("XNopResource", "test-resource", "default", "changed"),
		},
		"LeastPrivilegeCredentialsDiffSucceeds": {
			reason: "A user who can list only the composition's XR type, not Crossplane's own types, gets the admin's impact analysis",
			setupFiles: []string{
				"testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/original-composition.yaml",
				"testdata/comp/resources/functions.yaml",
			},
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/comp/resources/existing-xr-1.yaml",
					"testdata/comp/resources/existing-downstream-1.yaml",
				),
				integration.Composes(
					"testdata/comp/resources/existing-xr-2.yaml",
					"testdata/comp/resources/existing-downstream-2.yaml",
				),
			},
			inputFiles: []string{"testdata/comp/updated-composition.yaml"},
			namespace:  "default",
			// list on the XR type: without --resource, comp finds the composition's XRs by listing them.
			rbacRules:        leastPrivilegeRules("get", "list", "create", "patch"),
			outputFormat:     "json",
			expectedExitCode: dp.ExitCodeDiffDetected,
			expectedStructured: simtest.Expect().
				WithComposition("xnopresources.diff.example.org").
				WithCompositionModified().
				WithAffectedResources(2, 2, 0, 0).
				WithXRImpact("XNopResource", "test-resource", "default", "changed").
				AndComp().
				WithXRImpact("XNopResource", "another-resource", "default", "changed"),
		},
		"NameScopedRunSucceedsWithoutCompositeList": {
			reason: "--resource looks its composites up by name, so a user who cannot list the XR type gets the admin's impact analysis",
			setupFiles: []string{
				"testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/original-composition.yaml",
				"testdata/comp/resources/functions.yaml",
			},
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/comp/resources/existing-xr-1.yaml",
					"testdata/comp/resources/existing-downstream-1.yaml",
				),
				integration.Composes(
					"testdata/comp/resources/existing-xr-2.yaml",
					"testdata/comp/resources/existing-downstream-2.yaml",
				),
			},
			inputFiles:       []string{"testdata/comp/updated-composition.yaml"},
			resources:        []string{"default/test-resource"},
			rbacRules:        leastPrivilegeRules("get", "create", "patch"),
			outputFormat:     "json",
			expectedExitCode: dp.ExitCodeDiffDetected,
			expectedStructured: simtest.Expect().
				WithComposition("xnopresources.diff.example.org").
				WithCompositionModified().
				WithAffectedResources(1, 1, 0, 0).
				WithXRImpact("XNopResource", "test-resource", "default", "changed"),
		},
		"ResourceFilterCommaSeparated": {
			reason: "--resource accepts comma-separated values via kong's auto-parsing",
			setupFiles: []string{
				"testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/original-composition.yaml",
				"testdata/comp/resources/functions.yaml",
			},
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/comp/resources/existing-xr-1.yaml",
					"testdata/comp/resources/existing-downstream-1.yaml",
				),
				integration.Composes(
					"testdata/comp/resources/existing-xr-2.yaml",
					"testdata/comp/resources/existing-downstream-2.yaml",
				),
			},
			inputFiles:       []string{"testdata/comp/updated-composition.yaml"},
			resourcesCSV:     "default/test-resource,default/another-resource",
			outputFormat:     "json",
			expectedExitCode: dp.ExitCodeDiffDetected,
			expectedStructured: simtest.Expect().
				WithComposition("xnopresources.diff.example.org").
				WithCompositionModified().
				WithAffectedResources(2, 2, 0, 0).
				WithXRImpact("XNopResource", "test-resource", "default", "changed").
				AndComp().
				WithXRImpact("XNopResource", "another-resource", "default", "changed"),
		},
		"ResourceFilterUnmatched_FailsBeforeRendering": {
			reason: "--resource naming a non-existent composite fails fast with an error before any rendering",
			setupFiles: []string{
				"testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/original-composition.yaml",
				"testdata/comp/resources/functions.yaml",
			},
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/comp/resources/existing-xr-1.yaml",
					"testdata/comp/resources/existing-downstream-1.yaml",
				),
			},
			inputFiles:            []string{"testdata/comp/updated-composition.yaml"},
			resources:             []string{"default/does-not-exist"},
			expectedError:         true,
			expectedErrorContains: "does-not-exist",
			expectedExitCode:      dp.ExitCodeToolError,
		},
		"ResourceFilterRespectsManualPolicy_WithoutIncludeManual": {
			reason: "--resource matching a Manual-policy composite surfaces it as filtered (reason manual_policy) when --include-manual is unset",
			setupFiles: []string{
				"testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/original-composition.yaml",
				"testdata/comp/resources/functions.yaml",
			},
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/comp/resources/existing-xr-manual.yaml",
					"testdata/comp/resources/existing-downstream-manual.yaml",
				),
			},
			inputFiles:       []string{"testdata/comp/updated-composition.yaml"},
			resources:        []string{"default/manual-resource"},
			outputFormat:     "json",
			expectedExitCode: dp.ExitCodeDiffDetected, // composition itself changed
			expectedStructured: simtest.Expect().
				WithComposition("xnopresources.diff.example.org").
				WithCompositionModified().
				WithXRImpact("XNopResource", "manual-resource", "default", "filtered").
				WithFilterReason("manualPolicy"),
		},
		"ResourceFilterRespectsManualPolicy_WithIncludeManual": {
			reason: "--include-manual evaluates the Manual-policy composite normally instead of marking it filtered",
			setupFiles: []string{
				"testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/original-composition.yaml",
				"testdata/comp/resources/functions.yaml",
			},
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"testdata/comp/resources/existing-xr-manual.yaml",
					"testdata/comp/resources/existing-downstream-manual.yaml",
				),
			},
			inputFiles:       []string{"testdata/comp/updated-composition.yaml"},
			resources:        []string{"default/manual-resource"},
			includeManual:    true,
			outputFormat:     "json",
			expectedExitCode: dp.ExitCodeDiffDetected,
			expectedStructured: simtest.Expect().
				WithComposition("xnopresources.diff.example.org").
				WithCompositionModified().
				WithXRImpact("XNopResource", "manual-resource", "default", "changed"),
		},
		// Issue #388 (Bug A): an Automatic-policy XR whose compositionRevisionSelector does not match
		// the edited composition's labels would not adopt the resulting revision, so it must be
		// surfaced as filtered (reason revision_selector_mismatch), NOT evaluated for changes.
		"ResourceFilterRespectsRevisionSelectorMismatch": {
			reason: "--resource matching an Automatic XR whose compositionRevisionSelector does not match the composition's labels surfaces it as filtered (reason revision_selector_mismatch)",
			setupFiles: []string{
				"testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/original-composition.yaml",
				"testdata/comp/resources/functions.yaml",
				"testdata/comp/resources/existing-xr-selector-mismatch.yaml",
			},
			inputFiles:       []string{"testdata/comp/updated-composition-labeled.yaml"},
			resources:        []string{"default/selector-mismatch-resource"},
			outputFormat:     "json",
			expectedExitCode: dp.ExitCodeDiffDetected, // composition itself changed
			expectedStructured: simtest.Expect().
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
			reason: "--resource matching an Automatic XR whose compositionRevisionSelector matches the composition's labels keeps it and reports downstream changes",
			setupFiles: []string{
				"testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/original-composition.yaml",
				"testdata/comp/resources/functions.yaml",
				"testdata/comp/resources/existing-xr-selector-match.yaml",
			},
			inputFiles:       []string{"testdata/comp/updated-composition-labeled.yaml"},
			resources:        []string{"default/selector-match-resource"},
			outputFormat:     "json",
			expectedExitCode: dp.ExitCodeDiffDetected,
			expectedStructured: simtest.Expect().
				WithComposition("xnopresources.diff.example.org").
				WithCompositionModified().
				WithXRImpact("XNopResource", "selector-match-resource", "default", "changed"),
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			runIntegrationTest(t, CompositionDiffTest, tt)
		})
	}
}
