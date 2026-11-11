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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	un "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"

	"github.com/crossplane/cli/v2/internal/testutils/integration"
)

func TestMain(m *testing.M) {
	integration.ReapOrphanedEnvtest(func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) })
	os.Exit(m.Run())
}

// TestTraceIntegration traces resources read from a real API server: a claim through its XR to the resources the
// XR composes, a cluster-scoped XR no claim binds, and a Configuration with its revisions and dependencies. Each
// case is a trace command line and what it prints, or the error it returns.
func TestTraceIntegration(t *testing.T) {
	cluster := integration.Start(t, integration.Setup{
		CRDDirs:    []string{"testdata/integration/crds"},
		SetupFiles: []string{"testdata/integration/setup.yaml", "testdata/integration/packages.yaml"},
		CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
			integration.Composes("testdata/integration/xr.yaml",
				"testdata/integration/composed-ready.yaml",
				"testdata/integration/composed-failing.yaml"),
			integration.Composes("testdata/integration/xr-standalone.yaml",
				"testdata/integration/composed-standalone.yaml"),
		},
		XRDAPIVersion: integration.XRDV1,
	})

	// Revisions sort by creationTimestamp, which has one-second resolution, so the current revision is created a
	// second after the inactive one to make their order stable.
	time.Sleep(time.Second)
	createFromFile(t, cluster, "testdata/integration/packages-current-revision.yaml")
	setPackageStatus(t, cluster, "testdata/integration/packages.yaml", "testdata/integration/packages-current-revision.yaml")

	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(kubeconfig, cluster.Kubeconfig, 0o600); err != nil {
		t.Fatalf("cannot write kubeconfig: %v", err)
	}

	// trace loads its kubeconfig the way kubectl does.
	t.Setenv("KUBECONFIG", kubeconfig)

	// Children sort by apiVersion, kind and name, so failing comes before ready. No controller runs under envtest,
	// so the claim and XR have no conditions.
	//nolint:dupword // Columns of the table repeat values.
	claimTree := `
NAME                                      SYNCED   READY   STATUS
NopClaim/test-claim (test-namespace)      -        -
└─ XNop/test-claim-xr                     -        -
   ├─ NopResource/test-claim-xr-failing   False    -       ReconcileError: cannot observe external resource
   └─ NopResource/test-claim-xr-ready     True     True    Available
`

	claimNodes := &node{Kind: "NopClaim", Name: "test-claim", Children: []*node{
		{Kind: "XNop", Name: "test-claim-xr", Children: []*node{
			{Kind: "NopResource", Name: "test-claim-xr-failing"},
			{Kind: "NopResource", Name: "test-claim-xr-ready"},
		}},
	}}

	type want struct {
		// stdout is the whole output, compared with trimLines.
		stdout string
		// tree is the kind, name and children of each resource in json or yaml output.
		tree *node
		// err is a substring of the error Run returns.
		err string
	}

	cases := map[string]struct {
		reason string
		args   []string
		want   want
	}{
		"Default": {
			reason: "The default printer should draw the claim, its XR and the XR's composed resources as a tree.",
			args:   []string{"nopclaim", "test-claim", "--namespace=test-namespace"},
			want:   want{stdout: claimTree},
		},
		"ConcurrencyOne": {
			reason: "Loading the tree one resource at a time should draw the same tree.",
			args:   []string{"nopclaim", "test-claim", "--namespace=test-namespace", "--concurrency=1"},
			want:   want{stdout: claimTree},
		},
		"TypeVersionGroupSlashName": {
			reason: "A TYPE.VERSION.GROUP/NAME resource should trace the same claim.",
			args:   []string{"nopclaims.v1alpha1.trace.example.org/test-claim", "-n", "test-namespace"},
			want:   want{stdout: claimTree},
		},
		"KindSlashName": {
			reason: "A kind, rather than a plural, with the name after a slash should trace the same claim.",
			args:   []string{"NopClaim/test-claim", "-n", "test-namespace"},
			want:   want{stdout: claimTree},
		},
		"PluralSeparateName": {
			reason: "A plural resource with a separate name argument should trace the same claim.",
			args:   []string{"nopclaims", "test-claim", "-n", "test-namespace"},
			want:   want{stdout: claimTree},
		},
		"Wide": {
			reason: "The wide printer should add each composed resource's composition resource name.",
			args:   []string{"nopclaim", "test-claim", "--namespace=test-namespace", "-o", "wide"},
			//nolint:dupword // Columns of the table repeat values.
			want: want{stdout: `
NAME                                      RESOURCE   SYNCED   READY   STATUS
NopClaim/test-claim (test-namespace)                 -        -
└─ XNop/test-claim-xr                                -        -
   ├─ NopResource/test-claim-xr-failing   failing    False    -       ReconcileError: cannot observe external resource
   └─ NopResource/test-claim-xr-ready     ready      True     True    Available
`},
		},
		"JSON": {
			reason: "The json printer should print the same tree.",
			args:   []string{"nopclaim", "test-claim", "--namespace=test-namespace", "-o", "json"},
			want:   want{tree: claimNodes},
		},
		"YAML": {
			reason: "The yaml printer should print the same tree.",
			args:   []string{"nopclaim", "test-claim", "--namespace=test-namespace", "-o", "yaml"},
			want:   want{tree: claimNodes},
		},
		"Dot": {
			reason: "The dot printer should draw a node per resource, and an edge from each to its parent.",
			args:   []string{"nopclaim", "test-claim", "--namespace=test-namespace", "-o", "dot"},
			want:   want{stdout: dotClaimTree},
		},
		"ShowConnectionSecrets": {
			reason: "With --show-connection-secrets the claim's and the XR's connection secrets should be children.",
			args:   []string{"nopclaim", "test-claim", "--namespace=test-namespace", "--show-connection-secrets"},
			//nolint:dupword // Columns of the table repeat values.
			want: want{stdout: `
NAME                                               SYNCED   READY   STATUS
NopClaim/test-claim (test-namespace)               -        -
├─ XNop/test-claim-xr                              -        -
│  ├─ NopResource/test-claim-xr-failing            False    -       ReconcileError: cannot observe external resource
│  ├─ NopResource/test-claim-xr-ready              True     True    Available
│  └─ Secret/test-claim-xr-conn (test-namespace)   -        -
└─ Secret/test-claim-conn (test-namespace)         -        -
`},
		},
		"ClusterScopedXR": {
			reason: "An XR no claim binds should be traced with no namespace.",
			args:   []string{"xnop", "standalone-xr"},
			//nolint:dupword // Columns of the table repeat values.
			want: want{stdout: `
NAME                                SYNCED   READY   STATUS
XNop/standalone-xr                  -        -
└─ NopResource/standalone-xr-only   True     True    Available
`},
		},
		"Nonexistent": {
			reason: "A resource that does not exist should be reported as such.",
			args:   []string{"nopclaim", "missing", "--namespace=test-namespace"},
			want: want{err: `cannot get requested resource: kind=NopClaim name=missing namespace=test-namespace ` +
				`(verify context, namespace, and that the resource exists): nopclaims.trace.example.org "missing" not found`},
		},
		"PackageDefaults": {
			reason: "A Configuration should show its active revision and each dependency once, by default.",
			args:   []string{"configuration", "platform"},
			//nolint:dupword // Columns of the table repeat values.
			want: want{stdout: `
NAME                                         VERSION   INSTALLED   HEALTHY   STATE    STATUS
Configuration/platform                       v1.0.0    True        True      -        HealthyPackageRevision
├─ ConfigurationRevision/platform-2222       v1.0.0    -           True      Active   HealthyPackageRevision
├─ Provider/provider-nop                     v0.5.0    True        False     -        UnhealthyPackageRevision: runtime is not ready
│  └─ ProviderRevision/provider-nop-aaaa     v0.5.0    -           True      Active   HealthyPackageRevision
└─ Function/function-dummy                   v0.3.0    True        True      -        HealthyPackageRevision
   └─ FunctionRevision/function-dummy-bbbb   v0.3.0    -           True      Active   HealthyPackageRevision
`},
		},
		"PackageAllRevisionsAllDependencies": {
			reason: "With all revisions and all dependencies, the inactive revision and the Provider's own dependency on the Function should show too.",
			args:   []string{"configuration", "platform", "--show-package-revisions=all", "--show-package-dependencies=all"},
			//nolint:dupword // Columns of the table repeat values.
			want: want{stdout: `
NAME                                            VERSION   INSTALLED   HEALTHY   STATE      STATUS
Configuration/platform                          v1.0.0    True        True      -          HealthyPackageRevision
├─ ConfigurationRevision/platform-1111          v0.9.0    -           True      Inactive   HealthyPackageRevision
├─ ConfigurationRevision/platform-2222          v1.0.0    -           True      Active     HealthyPackageRevision
├─ Provider/provider-nop                        v0.5.0    True        False     -          UnhealthyPackageRevision: runtime is not ready
│  ├─ ProviderRevision/provider-nop-aaaa        v0.5.0    -           True      Active     HealthyPackageRevision
│  └─ Function/function-dummy                   v0.3.0    True        True      -          HealthyPackageRevision
│     └─ FunctionRevision/function-dummy-bbbb   v0.3.0    -           True      Active     HealthyPackageRevision
└─ Function/function-dummy                      v0.3.0    True        True      -          HealthyPackageRevision
   └─ FunctionRevision/function-dummy-bbbb      v0.3.0    -           True      Active     HealthyPackageRevision
`},
		},
		"PackageNoRevisionsNoDependencies": {
			reason: "With no revisions and no dependencies, only the Configuration should show.",
			args:   []string{"configuration", "platform", "--show-package-revisions=none", "--show-package-dependencies=none"},
			//nolint:dupword // Columns of the table repeat values.
			want: want{stdout: `
NAME                     VERSION   INSTALLED   HEALTHY   STATE   STATUS
Configuration/platform   v1.0.0    True        True      -       HealthyPackageRevision
`},
		},
		"PackageRuntimeConfigs": {
			reason: "With --show-package-runtime-configs the Function should show the runtime config it references.",
			args:   []string{"function", "function-dummy", "--show-package-runtime-configs"},
			//nolint:dupword // Columns of the table repeat values.
			want: want{stdout: `
NAME                                      VERSION   INSTALLED   HEALTHY   STATE    STATUS
Function/function-dummy                   v0.3.0    True        True      -        HealthyPackageRevision
├─ DeploymentRuntimeConfig/default                  -           -         -
└─ FunctionRevision/function-dummy-bbbb   v0.3.0    -           True      Active   HealthyPackageRevision
`},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			stdout, err := runTrace(t, tc.args)

			if tc.want.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want.err) {
					t.Fatalf("\n%s\nRun(): want error containing %q, got %v", tc.reason, tc.want.err, err)
				}

				return
			}

			if err != nil {
				t.Fatalf("\n%s\nRun(): %v", tc.reason, err)
			}

			if tc.want.tree != nil {
				if diff := cmp.Diff(tc.want.tree, parseTree(t, tc.args, stdout)); diff != "" {
					t.Errorf("\n%s\ntrace tree: -want, +got:\n%s", tc.reason, diff)
				}

				return
			}

			if diff := cmp.Diff(trimLines(tc.want.stdout), trimLines(stdout)); diff != "" {
				t.Errorf("\n%s\ntrace output: -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

// dotClaimTree is the dot printer's graph of the claim: nodes are numbered breadth first.
const dotClaimTree = `graph  {
	
	n1[label="Name: NopClaim/test-claim\nApiVersion: trace.example.org/v1alpha1\nNamespace: test-namespace\nReady: \nSynced: \n",penwidth="2"];
	n2[label="Name: XNop/test-claim-xr\nApiVersion: trace.example.org/v1alpha1\nReady: \nSynced: \n",penwidth="2"];
	n3[label="Name: NopResource/test-claim-xr-failing\nApiVersion: nop.example.org/v1alpha1\nReady: \nSynced: False\n",penwidth="2"];
	n4[label="Name: NopResource/test-claim-xr-ready\nApiVersion: nop.example.org/v1alpha1\nReady: True\nSynced: True\n",penwidth="2"];
	n1--n2;
	n2--n3;
	n2--n4;
	
}
`

// runTrace parses args as the trace command's and runs it, returning what it prints.
func runTrace(t *testing.T, args []string) (string, error) {
	t.Helper()

	var c Cmd

	parser, err := kong.New(&c)
	if err != nil {
		t.Fatalf("kong.New(): %v", err)
	}

	kongCtx, err := parser.Parse(args)
	if err != nil {
		t.Fatalf("Parse(%v): %v", args, err)
	}

	var stdout bytes.Buffer
	kongCtx.Stdout = &stdout

	err = c.Run(kongCtx, logging.NewNopLogger())

	return stdout.String(), err
}

// node is a resource's kind and name in a trace tree, with its children's.
type node struct {
	Kind     string
	Name     string
	Children []*node
}

// parseTree reads the tree trace printed as json or yaml; sigs.k8s.io/yaml reads both.
func parseTree(t *testing.T, args []string, out string) *node {
	t.Helper()

	type printed struct {
		Object   map[string]any `json:"object"`
		Children []*printed     `json:"children"`
	}

	var p printed
	if err := yaml.Unmarshal([]byte(out), &p); err != nil {
		t.Fatalf("cannot unmarshal the output of trace %v: %v", args, err)
	}

	var toNode func(p *printed) *node
	toNode = func(p *printed) *node {
		o := &un.Unstructured{Object: p.Object}
		n := &node{Kind: o.GetKind(), Name: o.GetName()}

		for _, c := range p.Children {
			n.Children = append(n.Children, toNode(c))
		}

		return n
	}

	return toNode(&p)
}

// createFromFile creates the resources in path.
func createFromFile(t *testing.T, cluster *integration.Cluster, path string) {
	t.Helper()

	objs, err := integration.ReadResourcesFromFile(path)
	if err != nil {
		t.Fatal(err)
	}

	for _, o := range objs {
		if err := cluster.Client.Create(cluster.Context, o); err != nil {
			t.Fatalf("cannot create %s %s: %v", o.GetKind(), o.GetName(), err)
		}
	}
}

// setPackageStatus writes the status each package and package revision in paths declares, which the API server
// drops on create, and gives each revision a controller reference to the package its parent package label names,
// as the package manager would.
func setPackageStatus(t *testing.T, cluster *integration.Cluster, paths ...string) {
	t.Helper()

	for _, path := range paths {
		objs, err := integration.ReadResourcesFromFile(path)
		if err != nil {
			t.Fatal(err)
		}

		for _, want := range objs {
			status, ok := want.Object["status"]
			if !ok {
				continue
			}

			got := &un.Unstructured{}
			got.SetGroupVersionKind(want.GroupVersionKind())

			if err := cluster.Client.Get(cluster.Context, client.ObjectKeyFromObject(want), got); err != nil {
				t.Fatalf("cannot get %s %s: %v", want.GetKind(), want.GetName(), err)
			}

			if parent := want.GetLabels()[pkgv1.LabelParentPackage]; parent != "" {
				owner := &un.Unstructured{}
				owner.SetAPIVersion(want.GetAPIVersion())
				owner.SetKind(strings.TrimSuffix(want.GetKind(), "Revision"))

				if err := cluster.Client.Get(cluster.Context, client.ObjectKey{Name: parent}, owner); err != nil {
					t.Fatalf("cannot get the package of %s %s: %v", want.GetKind(), want.GetName(), err)
				}

				got.SetOwnerReferences([]metav1.OwnerReference{*metav1.NewControllerRef(owner, owner.GroupVersionKind())})

				if err := cluster.Client.Update(cluster.Context, got); err != nil {
					t.Fatalf("cannot set the owner of %s %s: %v", want.GetKind(), want.GetName(), err)
				}
			}

			got.Object["status"] = status
			if err := cluster.Client.Status().Update(cluster.Context, got); err != nil {
				t.Fatalf("cannot set the status of %s %s: %v", want.GetKind(), want.GetName(), err)
			}
		}
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
