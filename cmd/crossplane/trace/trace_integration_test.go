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

package trace

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/kong"
	"github.com/google/go-cmp/cmp"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	"github.com/crossplane/cli/v2/internal/testutils/integration"
)

func TestMain(m *testing.M) {
	integration.ReapOrphanedEnvtest(func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) })
	os.Exit(m.Run())
}

// TestTraceIntegration traces a claim through its XR to the resources the XR composes, read from a real API
// server, and checks the tree the default printer draws.
func TestTraceIntegration(t *testing.T) {
	cluster := integration.Start(t, integration.Setup{
		CRDDirs:    []string{"testdata/integration/crds"},
		SetupFiles: []string{"testdata/integration/setup.yaml"},
		CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
			integration.Composes("testdata/integration/xr.yaml",
				"testdata/integration/composed-ready.yaml",
				"testdata/integration/composed-failing.yaml"),
		},
		XRDAPIVersion: integration.XRDV1,
	}, 2*time.Minute)

	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(kubeconfig, cluster.Kubeconfig, 0o600); err != nil {
		t.Fatalf("cannot write kubeconfig: %v", err)
	}

	// trace loads its kubeconfig the way kubectl does.
	t.Setenv("KUBECONFIG", kubeconfig)

	var c Cmd

	parser, err := kong.New(&c)
	if err != nil {
		t.Fatalf("kong.New(): %v", err)
	}

	kongCtx, err := parser.Parse([]string{"nopclaim", "test-claim", "--namespace=test-namespace"})
	if err != nil {
		t.Fatalf("Parse(): %v", err)
	}

	var stdout bytes.Buffer
	kongCtx.Stdout = &stdout

	if err := c.Run(kongCtx, logging.NewNopLogger()); err != nil {
		t.Fatalf("Run(): %v", err)
	}

	// Children sort by apiVersion, kind and name, so failing comes before ready. No controller runs under envtest,
	// so the claim and XR have no conditions.
	//nolint:dupword // Columns of the table repeat values.
	want := `
NAME                                      SYNCED   READY   STATUS
NopClaim/test-claim (test-namespace)      -        -
└─ XNop/test-claim-xr                     -        -
   ├─ NopResource/test-claim-xr-failing   False    -       ReconcileError: cannot observe external resource
   └─ NopResource/test-claim-xr-ready     True     True    Available
`

	if diff := cmp.Diff(trimLines(want), trimLines(stdout.String())); diff != "" {
		t.Errorf("trace output: -want, +got:\n%s", diff)
	}
}

// trimLines drops leading and trailing blank lines, and the padding tabwriter leaves at the end of a line.
func trimLines(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}

	return strings.Join(lines, "\n")
}
