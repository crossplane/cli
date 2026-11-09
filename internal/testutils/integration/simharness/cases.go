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

package simharness

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/client-go/rest"

	tu "github.com/crossplane/cli/v2/internal/diff/testutils"
	"github.com/crossplane/cli/v2/internal/simulate/simtest"
	"github.com/crossplane/cli/v2/internal/testutils/integration"
)

// TestdataDir is the shared testdata, relative to a suite's package directory under cmd/crossplane.
const TestdataDir = "../../../internal/testutils/integration/testdata"

// TestType is the command a case runs against: its testdata subdirectory, and the simulate command its
// crossplane-diff args translate to.
type TestType string

// The test types: XRTest runs `xr simulate`, CompositionTest `composition simulate`.
const (
	XRTest          TestType = "diff"
	CompositionTest TestType = "comp"
)

// IntegrationTestCase represents a common test case structure for both XR and composition diff tests.
type IntegrationTestCase struct {
	Reason           string // Description of what this test validates
	SetupFiles       []string
	DeleteAfterSetup []string // Files whose resources are deleted after setup; with a finalizer this leaves them Terminating
	// crossplaneManagedResources are the XRs that exist in the cluster, with what they compose. Every
	// composed resource is server-side applied under Crossplane's composed field manager and given a
	// controller ownerReference to its XR's real UID, and each XR lists its composed resources in its
	// resourceRefs, as Crossplane leaves them. Use integration.Composes() for an XR without nested XRs. Put a
	// composed resource in setupFiles instead only when it deliberately has no controlling XR.
	CrossplaneManagedResources []integration.HierarchicalOwnershipRelation
	// fieldManagerApplies are server-side applied, in order, after setupFiles and
	// crossplaneManagedResources, each under its own
	// field manager. Use it when a test depends on which manager owns a field: a plain Create
	// records only the test client as owner, and the apiserver drops managedFields supplied on Create.
	FieldManagerApplies []integration.FieldManagerApply
	// unestablishedXRDs are XRD manifests applied after setupFiles but, unlike an XRD in setupFiles, given
	// no generated CRDs: an XRD Crossplane has not established yet, so its XR and claim types are not served.
	UnestablishedXRDs []string
	// rbacRules, when set, makes the case run the command twice: as the cluster admin, then as a user
	// bound to a ClusterRole granting only these rules (plus the discovery and self-review access the
	// apiserver's bootstrap RBAC grants every authenticated user). The two runs must print identical
	// output, and the usual assertions apply to the second.
	RbacRules              []rbacv1.PolicyRule
	InputFiles             []string // Input files to diff (XR YAML files or Composition YAML files)
	ExpectedOutput         string
	ExpectedError          bool
	ExpectedErrorContains  string
	ExpectedStderrContains []string // substrings that must appear on stderr (warnings, error lines)
	ExpectedExitCode       int      // Expected exit code (0=success, 1=tool error, 2=schema validation, 3=diff detected)
	NoColor                bool
	Namespace              string                    // For composition tests (optional)
	XrdAPIVersion          integration.XRDAPIVersion // For XR tests (optional)
	IgnorePaths            []string                  // Paths to ignore in diffs
	FunctionCredentials    string                    // Path to function credentials file (optional)
	EventualState          bool                      // Enable eventual state simulation for XR or composition tests (optional)
	Timeout                time.Duration             // Custom timeout for this test (0 = use default)
	Resources              []string                  // For composition tests: --resource values; each entry passed as one --resource flag
	ResourcesCSV           string                    // For composition tests: alternative single --resource=a,b style invocation
	IncludeManual          bool                      // For composition tests: pass --include-manual flag
	AnalyzeUnchanged       bool                      // For composition tests: pass the deprecated --analyze-unchanged flag
	AnalyzeOn              string                    // For composition tests: pass --analyze-on=<value> (empty = rely on the default)
	// dryRunOn passes --dry-run-on=<value> (empty = rely on the default, which is "all").
	// The flag lives on CommonCmdFields, so it applies to both `xr` and `comp`.
	DryRunOn string
	// awaitAdmissionDenialFor names a manifest whose resources a correctly-installed
	// ValidatingAdmissionPolicy must refuse. Before running the diff, the harness dry-run
	// creates each of them until the apiserver rejects it.
	//
	// This is not belt-and-braces: the apiserver loads admission policies through an informer,
	// so a diff issued immediately after the policy is created races the plugin and observes a
	// cluster that accepts everything. Measured on envtest 1.32, the policy took ~600ms and four
	// attempts to take effect — long enough that without this gate the test would pass or fail
	// depending on machine load, and a "pass" would prove nothing.
	AwaitAdmissionDenialFor string
	// assertDeterministicAcrossRuns runs the command twice against the same cluster and requires
	// byte-identical stdout, then applies the usual assertions to the second run. Needed for
	// server-generated identity: the apiserver runs names.Generator before the dry-run
	// short-circuit, so a generateName addition whose identity was not restored would emit a
	// different random name on each run — which a single-run test cannot see.
	AssertDeterministicAcrossRuns bool
	Skip                          bool
	SkipReason                    string
	// JSON output support: set outputFormat to "json" to use structured assertions.
	OutputFormat string // "json" or "" (default=visual diff)
	// expectedStructured is asserted against the JSON output in either mode. It is typed as the
	// Expectation interface, which every builder level implements, so a chain can end where it ends.
	ExpectedStructured simtest.Expectation
}

// RunIntegrationTest runs a single integration test case for both XR and composition diff commands.
//
//nolint:gocognit // One flow every case funnels through; splitting it would scatter the case semantics.
func RunIntegrationTest(t *testing.T, testType TestType, tt IntegrationTestCase) {
	t.Helper()

	t.Parallel() // Enable parallel test execution

	// Resolve a local crossplane binary if one is present. When non-empty,
	// it's threaded into the kong arg slice below as
	// --crossplane-render-binary=<path> so each parallel subtest has its own
	// copy with no shared process state. When empty, the diff command falls
	// through to the docker render engine — slower but works wherever Docker
	// does (including the Earthfile's go-test target's WITH DOCKER block).
	crossplaneBin := LocalCrossplaneBinary(t)

	// Skip test if requested
	if tt.Skip {
		t.Skip(tt.SkipReason)
		return
	}

	// Use custom timeout if specified, otherwise use default
	testTimeout := DefaultTimeout
	if tt.Timeout > 0 {
		testTimeout = tt.Timeout
	}

	// Set up the test files
	testFiles := make([]string, 0, len(tt.InputFiles))

	// Handle any additional input files
	// Note: NewCompositeLoader handles both individual files and directories,
	// so we can pass paths directly without special handling
	testFiles = append(testFiles, tt.InputFiles...)

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
	if tt.Namespace != "" && testType == CompositionTest {
		args = append(args, fmt.Sprintf("--namespace=%s", tt.Namespace))
	}

	// Add no-color flag if true
	if tt.NoColor {
		args = append(args, "--no-color")
	}

	// A structured expectation is only asserted for JSON output; without
	// it the case would silently pass on the empty text-output check.
	if tt.ExpectedStructured != nil && tt.OutputFormat != "json" {
		t.Fatalf("ExpectedStructured is set but OutputFormat is %q; set OutputFormat to \"json\" so the structured assertion runs", tt.OutputFormat)
	}

	// Add JSON output format if specified
	if tt.OutputFormat == "json" {
		args = append(args, "--output=json")
	}

	// Add ignore-paths if specified
	if len(tt.IgnorePaths) > 0 {
		for _, path := range tt.IgnorePaths {
			args = append(args, fmt.Sprintf("--ignore-paths=%s", path))
		}
	}

	// Add function-credentials if specified
	if tt.FunctionCredentials != "" {
		args = append(args, fmt.Sprintf("--function-credentials=%s", tt.FunctionCredentials))
	}

	// Add eventual-state flag if specified (supported by both `xr` and `comp`).
	if tt.EventualState {
		args = append(args, "--eventual-state")
	}

	// --dry-run-on lives on CommonCmdFields, so unlike --analyze-on it is threaded for both
	// command types. An empty value relies on the flag's own default.
	if tt.DryRunOn != "" {
		args = append(args, "--dry-run-on="+tt.DryRunOn)
	}

	// Add --resource flags (composition tests only)
	if testType == CompositionTest {
		for _, r := range tt.Resources {
			args = append(args, fmt.Sprintf("--resource=%s", r))
		}

		if tt.ResourcesCSV != "" {
			args = append(args, fmt.Sprintf("--resource=%s", tt.ResourcesCSV))
		}

		if tt.IncludeManual {
			args = append(args, "--include-manual")
		}

		if tt.AnalyzeUnchanged {
			args = append(args, "--analyze-unchanged")
		}

		if tt.AnalyzeOn != "" {
			args = append(args, "--analyze-on="+tt.AnalyzeOn)
		}
	}

	// Add files as positional arguments
	args = append(args, testFiles...)

	// The render engine runs functions as Docker containers. Without a Docker daemon no rendering
	// case can run; cases that would not render anyway cannot be told apart upfront, so they skip.
	if !DockerAvailable() {
		t.Skip("env: no Docker daemon for the function render engine")
		return
	}

	// Start fails, rather than skips, a run without envtest's binaries or Crossplane's CRDs.
	cluster := integration.Start(t, integration.Setup{
		CRDDirs:                    []string{TestdataDir + "/" + string(testType) + "/crds"},
		SetupFiles:                 tt.SetupFiles,
		UnestablishedXRDs:          tt.UnestablishedXRDs,
		CrossplaneManagedResources: tt.CrossplaneManagedResources,
		XRDAPIVersion:              tt.XrdAPIVersion,
		FieldManagerApplies:        tt.FieldManagerApplies,
		DeleteAfterSetup:           tt.DeleteAfterSetup,
		RBACRules:                  tt.RbacRules,
	})

	// Block until any ValidatingAdmissionPolicy in the setup is actually enforcing, so the diff
	// cannot race the apiserver's policy informer. See the field's doc comment.
	if err := integration.AwaitAdmissionDenial(cluster.Context, cluster.Client, tt.AwaitAdmissionDenialFor); err != nil {
		t.Fatalf("admission policy never took effect: %v", err)
	}

	// runOnce executes the command against the running cluster, leaving its output in stdout /
	// stderr. Everything the command layer touches is rebuilt per call, so a second run starts
	// with cold client caches just as a second invocation of the CLI would.
	runOnce := func(runCfg *rest.Config) (int, error) {
		stdout.Reset()
		stderr.Reset()

		return runSimulate(t, runCfg, testType, args, &stdout, &stderr)
	}

	exitCode, err := runOnce(cluster.Config)

	if tt.AssertDeterministicAcrossRuns {
		first := stdout.String()

		exitCode, err = runOnce(cluster.Config)

		if second := stdout.String(); second != first {
			t.Errorf("output differs between two runs against the same cluster; a value the apiserver generated has leaked into the diff.\nfirst run:\n%s\nsecond run:\n%s", first, second)
		}
	}

	if cluster.UserConfig != nil {
		admin, adminErr := stdout.String(), err

		exitCode, err = runOnce(cluster.UserConfig)

		if restricted := stdout.String(); restricted != admin {
			t.Errorf("output with only the case's RBAC rules differs from the admin's; the run needs access the rules don't grant.\nadmin (error: %v):\n%s\nrestricted (error: %v, stderr below):\n%s\n%s",
				adminErr, admin, err, restricted, stderr.String())
		}
	}

	// Check exit code matches expected
	if exitCode != tt.ExpectedExitCode {
		t.Errorf("expected exit code %d, got %d", tt.ExpectedExitCode, exitCode)
	}

	// Assert stderr expectations before any of the early returns below, so they apply in every
	// output mode (human-readable, JSON, and expected-error cases alike).
	for _, want := range tt.ExpectedStderrContains {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("expected stderr to contain %q, got:\n%s", want, stderr.String())
		}
	}

	if tt.ExpectedError && err == nil {
		t.Fatal("expected error but got none")
	}

	if !tt.ExpectedError && err != nil {
		t.Fatalf("expected no error but got: %v", err)
	}

	// Check for specific error message if expected.
	//
	// Three modes:
	//   1. expectedError + expectedErrorContains: substring-check the error.
	//   2. expectedError + structured assertion: any error is fine here;
	//      the structured assertion below validates errors[].
	//   3. unexpected error: already caught by the t.Fatalf above.
	if err != nil && tt.ExpectedError {
		hasStructuredAssertion := tt.ExpectedStructured != nil

		switch {
		case tt.ExpectedErrorContains != "":
			if strings.Contains(err.Error(), tt.ExpectedErrorContains) {
				t.Logf("Got expected error containing: %s", tt.ExpectedErrorContains)
			} else {
				t.Errorf("Expected error containing %q, got: %v", tt.ExpectedErrorContains, err)
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
	if tt.ExpectedError && tt.ExpectedErrorContains != "" &&
		tt.ExpectedStructured == nil {
		return
	}

	// Check the output
	outputStr := stdout.String()

	// Handle JSON output format with structured assertions
	if tt.OutputFormat == "json" && tt.ExpectedStructured != nil {
		simtest.AssertStructured(t, outputStr, tt.ExpectedStructured)
		return
	}

	// Using TrimSpace because the output might have trailing newlines
	if !strings.Contains(strings.TrimSpace(outputStr), strings.TrimSpace(tt.ExpectedOutput)) {
		// Strings aren't equal, *including* ansi.  but we can compare ignoring ansi to determine what output to
		// show for the failure.  if the difference is only in color codes, we'll show escaped ansi codes.
		out := outputStr

		expect := tt.ExpectedOutput
		if tu.CompareIgnoringAnsi(strings.TrimSpace(outputStr), strings.TrimSpace(tt.ExpectedOutput)) {
			out = strconv.QuoteToASCII(outputStr)
			expect = strconv.QuoteToASCII(tt.ExpectedOutput)
		}

		t.Fatalf("expected output to contain:\n%s\n\nbut got:\n%s", expect, out)
	}
}

// LeastPrivilegeRules is the access a diff of an XNopResource composing XDownstreamResources needs, the
// types the testdata XRDs, compositions and CRDs use. It grants Crossplane's own types only get, except a
// list of CompositionRevisions, which are found by their composition's label. It grants list only where a
// lookup is scoped by a label. xrVerbs are the verbs on
// the XR type: get to read the existing XR, patch and create to dry-run it, and list only where the
// command finds composites by listing them.
//
//nolint:goconst // Verbs read as RBAC rules, not as shared constants.
func LeastPrivilegeRules(xrVerbs ...string) []rbacv1.PolicyRule {
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
