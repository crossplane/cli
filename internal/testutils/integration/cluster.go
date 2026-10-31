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

package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	cgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	xpextv1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1"
	xpextv2 "github.com/crossplane/crossplane/apis/v2/apiextensions/v2"
	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"
)

// XRDAPIVersion is the apiextensions.crossplane.io version of the XRDs a Setup's XRs are defined by. It decides
// where Start writes an XR's resourceRefs.
type XRDAPIVersion int

const (
	// XRDV2 XRs list their composed resources at spec.crossplane.resourceRefs. It is the zero value, so the
	// default.
	XRDV2 XRDAPIVersion = iota
	// XRDV1 (legacy) XRs list their composed resources at spec.resourceRefs.
	XRDV1
)

// FieldManagerApply is a manifest to server-side apply under a specific field manager.
type FieldManagerApply struct {
	File         string
	FieldManager string
}

// Setup is the state Start puts in a fresh cluster, in the order its fields are listed. Every path is a YAML file
// of one or more manifests, relative to the test's working directory (its package directory).
type Setup struct {
	// CRDDirs are directories of plain CRDs to install besides Crossplane's own, such as stand-ins for managed
	// resources. XR and claim CRDs don't go here: Start generates them from the XRDs in SetupFiles, as Crossplane
	// would.
	CRDDirs []string
	// SetupFiles are created in order, each filled in with what Crossplane would have written to it (see
	// Start).
	SetupFiles []string
	// UnestablishedXRDs are XRD manifests created after SetupFiles but, unlike an XRD in SetupFiles, given no
	// generated CRDs: an XRD Crossplane has not established yet, so its XR and claim types are not served.
	UnestablishedXRDs []string
	// CrossplaneManagedResources are the XRs that exist in the cluster, with what they compose. Every composed
	// resource is server-side applied under crossplaneFieldManager and given a controller ownerReference to its
	// XR's real UID, and each XR lists its composed resources in its resourceRefs, as Crossplane leaves them. Use
	// Composes for an XR without nested XRs. Put a composed resource in SetupFiles instead only when it
	// deliberately has no controlling XR.
	CrossplaneManagedResources []HierarchicalOwnershipRelation
	// XRDAPIVersion is the version of the XRDs that define CrossplaneManagedResources' XRs.
	XRDAPIVersion XRDAPIVersion
	// FieldManagerApplies are server-side applied, in order, each under its own field manager. Use it when a
	// test depends on which manager owns a field: a plain Create records only the test client as owner, and the
	// apiserver drops managedFields supplied on Create.
	FieldManagerApplies []FieldManagerApply
	// DeleteAfterSetup are files whose resources are deleted once everything else is set up. With a finalizer,
	// which no controller clears under envtest, this leaves them Terminating.
	DeleteAfterSetup []string
	// RBACRules, when set, makes Start also provision a user bound to a ClusterRole granting only these rules
	// (plus the discovery and self-review access the apiserver's bootstrap RBAC grants every authenticated
	// user), returned as Cluster.UserConfig.
	RBACRules []rbacv1.PolicyRule
}

// Cluster is a running envtest cluster that Start has set up.
type Cluster struct {
	// Config is the cluster admin's.
	Config *rest.Config
	// UserConfig is that of a user granted only the Setup's RBACRules; nil when it sets none.
	UserConfig *rest.Config
	// Kubeconfig is a kubeconfig file's contents for the cluster admin, for commands that load one.
	Kubeconfig []byte
	// Client is the cluster admin's.
	Client client.Client
	// Context is the test's (t.Context()), cancelled when the test ends; go test's -timeout bounds the run.
	Context context.Context //nolint:containedctx // Handed to the test with the cluster it bounds.
}

// Start starts an envtest cluster and puts s in place. It serves Crossplane's own CRDs, at the version go.mod
// pins (PinnedCrossplaneCRDsDir), s.CRDDirs' CRDs, and the CRDs CRDsForXRD generates from the XRDs in
// s.SetupFiles.
//
// No Crossplane controller runs under envtest, so Start does what they would have to the resources it sets up,
// wherever a manifest leaves it out: XRDs control their generated CRDs; each Composition has a current
// CompositionRevision, controlled by it; a revision without a name is named and hash-labelled from its
// Composition; an XR and everything it composes carry its composite and claim labels; and an XR that names a
// Composition but no revision references its current one. A value a manifest sets always wins, which is how a
// test sets up a state Crossplane wouldn't leave.
//
// Start fails the test when the environment lacks envtest's binaries (KUBEBUILDER_ASSETS) or Crossplane's CRDs
// (CROSSPLANE_CRDS_DIR), rather than skipping it, so a broken environment can't pass silently; `nix run
// .#integration-test` provides both. The cluster is stopped and the context
// cancelled when the test ends.
func Start(t *testing.T, s Setup) *Cluster {
	t.Helper()

	// envtest needs kube-apiserver and etcd binaries it cannot download itself. Only a run that opted in with
	// the integration tag gets here, so a missing binary is a broken environment, not a reason to skip.
	if !envtestBinariesAvailable() {
		t.Fatal("env: no envtest binaries (KUBEBUILDER_ASSETS unset or without kube-apiserver); run via nix run .#integration-test, or set KUBEBUILDER_ASSETS to a directory of envtest's binaries")
	}

	if os.Getenv(crossplaneCRDsDirEnv) == "" {
		t.Fatalf("env: %s unset; run via nix run .#integration-test, or set it to Crossplane's cluster/crds",
			crossplaneCRDsDirEnv)
	}

	crds, err := envtestCRDs(append([]string{PinnedCrossplaneCRDsDir(t)}, s.CRDDirs...), s.SetupFiles)
	if err != nil {
		t.Fatalf("failed to assemble CRDs for the test environment: %v", err)
	}

	testEnv := &envtest.Environment{
		CRDs: crds,
		// Each cluster needs its own scheme, because envtest modifies it while installing CRDs.
		Scheme: newScheme(),
		// Leaving ControlPlane unset lets envtest start its own apiserver and etcd on random ports, so tests
		// can run their clusters in parallel.
	}

	cfg, err := testEnv.Start()
	if err != nil {
		t.Fatalf("failed to start test environment: %v", err)
	}

	t.Cleanup(func() {
		if err := testEnv.Stop(); err != nil {
			t.Logf("failed to stop test environment: %v", err)
		}
	})

	k8sClient, err := client.New(cfg, client.Options{Scheme: testEnv.Scheme})
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx := t.Context()

	defaults := newCrossplaneDefaults(k8sClient, crds)

	if err := applyResourcesFromFiles(ctx, k8sClient, defaults, s.SetupFiles); err != nil {
		t.Fatalf("failed to setup resources: %v", err)
	}

	if err := applyResourcesFromFiles(ctx, k8sClient, defaults, s.UnestablishedXRDs); err != nil {
		t.Fatalf("failed to setup unestablished XRDs: %v", err)
	}

	if err := reconcileXRDCRDOwnership(ctx, k8sClient); err != nil {
		t.Fatalf("failed to make XRDs the controllers of their CRDs: %v", err)
	}

	if err := reconcileCompositionRevisions(ctx, k8sClient); err != nil {
		t.Fatalf("failed to reconcile CompositionRevisions: %v", err)
	}

	// The Crossplane-managed resources are in place before FieldManagerApplies run, since those model another
	// manager writing to objects that already exist.
	if len(s.CrossplaneManagedResources) > 0 {
		if err := applyHierarchicalOwnership(ctx, k8sClient, defaults, s.XRDAPIVersion, s.CrossplaneManagedResources); err != nil {
			t.Fatalf("failed to setup Crossplane-managed resources: %v", err)
		}
	}

	for _, a := range s.FieldManagerApplies {
		resources, err := ReadResourcesFromFile(a.File)
		if err != nil {
			t.Fatalf("failed to read %s: %v", a.File, err)
		}

		for _, r := range resources {
			if err := applyResourceWithSSA(ctx, k8sClient, r, a.FieldManager); err != nil {
				t.Fatalf("failed to setup field-managed resources: %v", err)
			}
		}
	}

	if err := deleteResourcesFromFiles(ctx, k8sClient, s.DeleteAfterSetup); err != nil {
		t.Fatalf("failed to delete resources: %v", err)
	}

	cluster := &Cluster{Config: cfg, Kubeconfig: testEnv.KubeConfig, Client: k8sClient, Context: ctx}

	if s.RBACRules != nil {
		cluster.UserConfig, err = addUserWithRules(ctx, testEnv, k8sClient, cfg, s.RBACRules)
		if err != nil {
			t.Fatalf("failed to set up a user with the setup's RBAC rules: %v", err)
		}
	}

	return cluster
}

// newScheme returns a scheme of the Kubernetes and Crossplane types a test cluster serves.
func newScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()

	_ = cgoscheme.AddToScheme(scheme)
	_ = xpextv1.AddToScheme(scheme)
	_ = xpextv2.AddToScheme(scheme)
	_ = pkgv1.AddToScheme(scheme)
	_ = extv1.AddToScheme(scheme)

	return scheme
}

// envtestBinariesAvailable reports whether envtest can find a kube-apiserver to start: in KUBEBUILDER_ASSETS, or
// at envtest's built-in default.
func envtestBinariesAvailable() bool {
	if dir := os.Getenv("KUBEBUILDER_ASSETS"); dir != "" {
		if _, err := os.Stat(filepath.Join(dir, "kube-apiserver")); err == nil { //nolint:gosec // Path set by the test runner.
			return true
		}
	}

	_, err := os.Stat("/usr/local/kubebuilder/bin/kube-apiserver")

	return err == nil
}
