//go:build integration

package project

import (
	"context"
	"io"
	"os"
	"path/filepath"
	run "runtime"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/sergi/go-diff/diffmatchpatch"
	rbacv1 "k8s.io/api/rbac/v1"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	un "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	cgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	xpextv1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1"
	xpextv2 "github.com/crossplane/crossplane/apis/v2/apiextensions/v2"
	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"

	dclient "github.com/crossplane/cli/v2/internal/diff/client"
	dp "github.com/crossplane/cli/v2/internal/diff/diffprocessor"
	dt "github.com/crossplane/cli/v2/internal/diff/result"
	tu "github.com/crossplane/cli/v2/internal/diff/testutils"
)

const (
	defaultTimeout = 60 * time.Second
	longTimeout    = 180 * time.Second // For tests with multiple iterations (eventual state, concurrent rendering)
)

// DiffTestType represents the type of diff test to run.
type DiffTestType string

const (
	XRDiffTest          DiffTestType = "diff"
	CompositionDiffTest DiffTestType = "comp"
)

// fieldManagerApply is a manifest to server-side apply under a specific field manager.
type fieldManagerApply struct {
	file         string
	fieldManager string
}

// IntegrationTestCase represents a common test case structure for both XR and composition diff tests.
type IntegrationTestCase struct {
	setupFiles       []string
	deleteAfterSetup []string // Files whose resources are deleted after setup; with a finalizer this leaves them Terminating
	// crossplaneManagedResources are the XRs that exist in the cluster, with what they compose. Every
	// composed resource is server-side applied under Crossplane's composed field manager and given a
	// controller ownerReference to its XR's real UID, and each XR lists its composed resources in its
	// resourceRefs, as Crossplane leaves them. Use composes() for an XR without nested XRs. Put a
	// composed resource in setupFiles instead only when it deliberately has no controlling XR.
	crossplaneManagedResources []HierarchicalOwnershipRelation
	// fieldManagerApplies are server-side applied, in order, after setupFiles and
	// crossplaneManagedResources, each under its own
	// field manager. Use it when a test depends on which manager owns a field: a plain Create
	// records only the test client as owner, and the apiserver drops managedFields supplied on Create.
	fieldManagerApplies []fieldManagerApply
	// unestablishedXRDs are XRD manifests applied after setupFiles but, unlike an XRD in setupFiles, given
	// no generated CRDs: an XRD Crossplane has not established yet, so its XR and claim types are not served.
	unestablishedXRDs []string
	// rbacRules, when set, makes the case run the command twice: as the cluster admin, then as a user
	// bound to a ClusterRole granting only these rules (plus the discovery and self-review access the
	// apiserver's bootstrap RBAC grants every authenticated user). The two runs must print identical
	// output, and the usual assertions apply to the second.
	rbacRules     []rbacv1.PolicyRule
	xrdAPIVersion XrdAPIVersion // For XR tests (optional)
}

type XrdAPIVersion int

const (
	V2 XrdAPIVersion = iota // v2 comes first so that this is the default value
	V1
)

var versionNames = map[XrdAPIVersion]string{
	V1: "apiextensions.crossplane.io/v1",
	V2: "apiextensions.crossplane.io/v2",
}

func (s XrdAPIVersion) String() string {
	return versionNames[s]
}

// createTestScheme creates a runtime scheme with all required types registered.
// Each parallel test needs its own scheme because envtest modifies it during CRD installation.
func createTestScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()

	// Register Kubernetes types
	_ = cgoscheme.AddToScheme(scheme)

	// Register Crossplane types
	_ = xpextv1.AddToScheme(scheme)
	_ = xpextv2.AddToScheme(scheme)
	_ = pkgv1.AddToScheme(scheme)
	_ = extv1.AddToScheme(scheme)

	return scheme
}

// integrationCluster is a running test case's envtest cluster.
type integrationCluster struct {
	cfg     *rest.Config  // the cluster admin's
	userCfg *rest.Config  // a user granted only the case's rbacRules; nil when the case sets none
	client  client.Client // the admin's
	ctx     context.Context
}

// startIntegrationCluster starts a test case's envtest cluster and puts its setup state in place:
// tt's setupFiles, unestablishedXRDs, crossplaneManagedResources, fieldManagerApplies, deleteAfterSetup
// and rbacRules. The context is bounded by the case's timeout. The cluster is stopped and the context
// cancelled when the test ends.
func startIntegrationCluster(t *testing.T, testType DiffTestType, tt IntegrationTestCase, testTimeout time.Duration) integrationCluster {
	t.Helper()

	// Create a fresh scheme for each test to avoid concurrent map access.
	// Each parallel test needs its own scheme because envtest modifies it during CRD installation.
	scheme := createTestScheme()

	// Setup a brand new test environment for each test case
	_, thisFile, _, ok := run.Caller(0)
	if !ok {
		t.Fatal("failed to get caller information")
	}

	thisDir := filepath.Dir(thisFile)

	// Plain CRDs are read from these directories: Crossplane's own, at the crossplane version go.mod
	// pins (so they match the xcrd and other Crossplane code under test), and stand-ins for managed
	// resources. XR and claim CRDs are not: they are generated from the XRDs the test applies.
	crdPaths := []string{
		tu.PinnedCrossplaneCRDsDir(t),
		filepath.Join(thisDir, "testdata", string(testType), "crds"),
	}

	crds, err := envtestCRDs(crdPaths, tt.setupFiles)
	if err != nil {
		t.Fatalf("failed to assemble CRDs for the test environment: %v", err)
	}

	testEnv := &envtest.Environment{
		CRDs:   crds,
		Scheme: scheme,
		// Note: Leaving ControlPlane unset (nil) allows envtest to create its own control plane
		// with random ports, which enables parallel test execution without port conflicts.
		// Each test gets its own isolated API server and etcd instance on random available ports.
	}

	// Start the test environment
	cfg, err := testEnv.Start()
	if err != nil {
		t.Fatalf("failed to start test environment: %v", err)
	}

	// Ensure we clean up at the end of the test
	t.Cleanup(func() {
		err := testEnv.Stop()
		if err != nil {
			t.Logf("failed to stop test environment: %v", err)
		}
	})

	// Create a controller-runtime client for setup operations
	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
	t.Cleanup(cancel)

	// Envtest runs no Crossplane controllers, so the harness fills in what they would have written to the
	// setup fixtures, wherever a fixture leaves it out.
	defaults := newCrossplaneDefaults(k8sClient, crds)

	// Apply the setup resources
	if err := applyResourcesFromFiles(ctx, k8sClient, defaults, tt.setupFiles); err != nil {
		t.Fatalf("failed to setup resources: %v", err)
	}

	if err := applyResourcesFromFiles(ctx, k8sClient, defaults, tt.unestablishedXRDs); err != nil {
		t.Fatalf("failed to setup unestablished XRDs: %v", err)
	}

	// No definition or composition revision controller runs either: do what they would.
	if err := reconcileXRDCRDOwnership(ctx, k8sClient); err != nil {
		t.Fatalf("failed to make XRDs the controllers of their CRDs: %v", err)
	}

	if err := reconcileCompositionRevisions(ctx, k8sClient); err != nil {
		t.Fatalf("failed to reconcile CompositionRevisions: %v", err)
	}

	// Default to v2 API version for XR resources unless otherwise specified
	xrdAPIVersion := V2
	if tt.xrdAPIVersion != V2 {
		xrdAPIVersion = tt.xrdAPIVersion
	}

	// Apply Crossplane-managed resources (XRs, composed resources) using SSA with Crossplane field manager.
	// These resources simulate what Crossplane actually manages in production. They are in place before
	// fieldManagerApplies run, since those model another manager writing to objects that already exist.
	if len(tt.crossplaneManagedResources) > 0 {
		err := applyHierarchicalOwnership(ctx, tu.TestLogger(t, false), k8sClient, defaults, xrdAPIVersion, tt.crossplaneManagedResources)
		if err != nil {
			t.Fatalf("failed to setup Crossplane-managed resources: %v", err)
		}
	}

	for _, a := range tt.fieldManagerApplies {
		resources, err := readResourcesFromFile(a.file)
		if err != nil {
			t.Fatalf("failed to read %s: %v", a.file, err)
		}

		for _, r := range resources {
			if err := applyResourceWithSSA(ctx, k8sClient, r, a.fieldManager); err != nil {
				t.Fatalf("failed to setup field-managed resources: %v", err)
			}
		}
	}

	// Issue a delete for resources that should be observed mid-deletion. The manifests must
	// carry a finalizer so envtest (which runs no controllers) leaves the object in place with
	// metadata.deletionTimestamp set, reproducing a real "Terminating" resource.
	if err := deleteResourcesFromFiles(ctx, k8sClient, tt.deleteAfterSetup); err != nil {
		t.Fatalf("failed to delete resources: %v", err)
	}

	cluster := integrationCluster{cfg: cfg, client: k8sClient, ctx: ctx}

	if tt.rbacRules != nil {
		cluster.userCfg, err = addUserWithRules(ctx, testEnv, k8sClient, cfg, tt.rbacRules)
		if err != nil {
			t.Fatalf("failed to set up a user with the case's RBAC rules: %v", err)
		}
	}

	return cluster
}

// TestDiffSingleResourceConcurrent diffs several XRs from concurrent goroutines with one processor,
// against a real apiserver and real functions, and checks each gets the diffs and the run the
// warnings a sequential run of the same XRs gets. Run it with -race: it is what shows the processor,
// its caches and its render engine are safe for concurrent use (#579).
func TestDiffSingleResourceConcurrent(t *testing.T) {
	t.Parallel()

	tu.SetupKubeTestLogger(t)

	// Same environment guards the runner applies before cluster startup: this test needs the
	// same pieces (Docker to render, envtest binaries, the pinned Crossplane CRDs).
	if !dockerAvailable() {
		t.Skip("env: no Docker daemon for the function render engine")
		return
	}

	if !envtestBinariesAvailable() {
		t.Skip("env: no envtest binaries (KUBEBUILDER_ASSETS unset)")
		return
	}

	if os.Getenv("CROSSPLANE_CRDS_DIR") == "" {
		t.Skip("env: CROSSPLANE_CRDS_DIR unset (run via nix run .#test or set it to Crossplane's cluster/crds)")
		return
	}

	cluster := startIntegrationCluster(t, XRDiffTest, IntegrationTestCase{
		setupFiles: []string{
			"testdata/diff/resources/xrd.yaml",
			"testdata/diff/resources/composition.yaml",
			"testdata/diff/resources/functions.yaml",
		},
		crossplaneManagedResources: []HierarchicalOwnershipRelation{
			composes(
				"testdata/diff/resources/existing-xr.yaml",
				"testdata/diff/resources/existing-downstream-resource.yaml",
			),
		},
	}, defaultTimeout)
	cfg, ctx := cluster.cfg, cluster.ctx

	// One modified and one new XR, each diffed several times at once.
	var inputs []*un.Unstructured

	for _, f := range []string{"testdata/diff/modified-xr.yaml", "testdata/diff/new-xr-second.yaml"} {
		res, err := readResourcesFromFile(f)
		if err != nil {
			t.Fatalf("cannot read %s: %v", f, err)
		}

		inputs = append(inputs, res...)
	}

	inputs = slices.Concat(inputs, inputs, inputs)

	// diffAll diffs inputs with a new processor, concurrently or one at a time, and returns each
	// input's diffs (by index) and the warnings the run raised.
	diffAll := func(t *testing.T, concurrent bool) ([]map[string]lineDiffs, []dt.OutputWarning) {
		t.Helper()

		log := tu.TestLogger(t, false)

		clients, err := dclient.New(cfg, log)
		if err != nil {
			t.Fatalf("client.New: %v", err)
		}

		if err := clients.Initialize(ctx, log); err != nil {
			t.Fatalf("Clients.Initialize: %v", err)
		}

		advisories := dp.NewAdvisoryCollector(io.Discard)
		// The processor the xr command builds, with its flags' defaults.
		opts := []dp.ProcessorOption{
			dp.WithLogger(log),
			dp.WithAdvisories(advisories),
			dp.WithMaxNestedDepth(10),
			dp.WithMaxRenderIterations(20),
			dp.WithDryRunOn(dp.DryRunOnAll),
		}
		if bin := localCrossplaneBinary(t); bin != "" {
			opts = append(opts, dp.WithCrossplaneRenderBinary(bin))
		}

		proc := dp.NewDiffProcessor(clients.K8sClients, clients.XpClients, opts...)
		t.Cleanup(func() {
			if err := proc.Cleanup(context.WithoutCancel(ctx)); err != nil {
				t.Errorf("Cleanup: %v", err)
			}
		})

		if err := proc.Initialize(ctx); err != nil {
			t.Fatalf("Initialize: %v", err)
		}

		got := make([]map[string]lineDiffs, len(inputs))
		diff := func(i int) {
			diffs, _, err := proc.DiffSingleResource(ctx, inputs[i].DeepCopy(), clients.XpClients.Composition.FindMatchingComposition)
			if err != nil {
				t.Errorf("DiffSingleResource(%s): %v", inputs[i].GetName(), err)
				return
			}

			got[i] = make(map[string]lineDiffs, len(diffs))
			for k, d := range diffs {
				got[i][k] = lineDiffs{Type: d.DiffType, Lines: d.LineDiffs}
			}
		}

		var wg sync.WaitGroup

		for i := range inputs {
			if !concurrent {
				diff(i)
				continue
			}

			wg.Go(func() { diff(i) })
		}

		wg.Wait()

		return got, advisories.Advisories()
	}

	wantDiffs, wantWarnings := diffAll(t, false)
	gotDiffs, gotWarnings := diffAll(t, true)

	if diff := cmp.Diff(wantDiffs, gotDiffs); diff != "" {
		t.Errorf("concurrent diffs differ from sequential diffs (-sequential +concurrent):\n%s", diff)
	}

	if diff := cmp.Diff(wantWarnings, gotWarnings); diff != "" {
		t.Errorf("concurrent warnings differ from sequential warnings (-sequential +concurrent):\n%s", diff)
	}
}

// lineDiffs is what TestDiffSingleResourceConcurrent compares of a resource's diff.
type lineDiffs struct {
	Type  dt.DiffType
	Lines []diffmatchpatch.Diff
}
