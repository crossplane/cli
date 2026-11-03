//go:build integration

package project

import (
	"context"
	"io"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/sergi/go-diff/diffmatchpatch"
	un "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	dclient "github.com/crossplane/cli/v2/internal/diff/client"
	dp "github.com/crossplane/cli/v2/internal/diff/diffprocessor"
	dt "github.com/crossplane/cli/v2/internal/diff/result"
	tu "github.com/crossplane/cli/v2/internal/diff/testutils"
	"github.com/crossplane/cli/v2/internal/testutils/integration"
)

const (
	defaultTimeout = 60 * time.Second
	longTimeout    = 180 * time.Second // For tests with multiple iterations (eventual state, concurrent rendering)
)

// TestDiffSingleResourceConcurrent diffs several XRs from concurrent goroutines with one processor,
// against a real apiserver and real functions, and checks each gets the diffs and the run the
// warnings a sequential run of the same XRs gets. Run it with -race: it is what shows the processor,
// its caches and its render engine are safe for concurrent use (#579).
func TestDiffSingleResourceConcurrent(t *testing.T) {
	t.Parallel()

	tu.SetupKubeTestLogger(t)

	// Same environment guard the runner applies before cluster startup: this test also renders with
	// real functions.
	if !dockerAvailable() {
		t.Skip("env: no Docker daemon for the function render engine")
		return
	}

	cluster := integration.Start(t, integration.Setup{
		CRDDirs: []string{"testdata/diff/crds"},
		SetupFiles: []string{
			"testdata/diff/resources/xrd.yaml",
			"testdata/diff/resources/composition.yaml",
			"testdata/diff/resources/functions.yaml",
		},
		CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
			integration.Composes(
				"testdata/diff/resources/existing-xr.yaml",
				"testdata/diff/resources/existing-downstream-resource.yaml",
			),
		},
	})
	cfg := cluster.Config

	ctx, cancel := context.WithTimeout(cluster.Context, defaultTimeout)
	t.Cleanup(cancel)

	// One modified and one new XR, each diffed several times at once.
	var inputs []*un.Unstructured

	for _, f := range []string{"testdata/diff/modified-xr.yaml", "testdata/diff/new-xr-second.yaml"} {
		res, err := integration.ReadResourcesFromFile(f)
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
