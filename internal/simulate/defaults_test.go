package simulate

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
	kt "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/crossplane/crossplane/apis/v2/pkg"
	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"

	"github.com/crossplane/cli/v2/internal/diff/client/core"
	k8 "github.com/crossplane/cli/v2/internal/diff/client/kubernetes"
	"github.com/crossplane/cli/v2/internal/diff/diffprocessor"
	tu "github.com/crossplane/cli/v2/internal/diff/testutils"
)

// newCRDs returns the diff engine's SchemaClient over a fake dynamic client
// holding objs, as the one CRD cache a run shares, and that dynamic client.
func newCRDs(t *testing.T, objs ...runtime.Object) (k8.SchemaClient, *dynfake.FakeDynamicClient) {
	t.Helper()
	dc := dynfake.NewSimpleDynamicClient(runtime.NewScheme(), objs...)
	tc := tu.NewMockTypeConverter().WithDefaultGetResourceNameForGVK().Build()
	return k8.NewSchemaClient(&core.Clients{Dynamic: dc}, tc, tu.TestLogger(t, false)), dc
}

// noCRDs returns a CRD cache for a cluster that holds no CRDs.
func noCRDs(t *testing.T) CRDs {
	t.Helper()
	crds, _ := newCRDs(t)
	return crds
}

// countCRDGets counts the CRD GETs dc serves from here on.
func countCRDGets(dc *dynfake.FakeDynamicClient) *int {
	n := 0
	dc.PrependReactor("get", "customresourcedefinitions", func(kt.Action) (bool, runtime.Object, error) {
		n++
		return false, nil, nil
	})
	return &n
}

// failCRDGets makes dc's CRD GETs fail with err while *failing is true.
func failCRDGets(dc *dynfake.FakeDynamicClient, failing *bool, err error) {
	dc.PrependReactor("get", "customresourcedefinitions", func(kt.Action) (bool, runtime.Object, error) {
		if *failing {
			return true, nil, err
		}
		return false, nil, nil
	})
}

// vpcCRD is the VPC CRD, defaulting spec.deletionPolicy and, when revision is
// set, controlled by that ProviderRevision.
func vpcCRD(revision string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata":   map[string]any{"name": "vpcs.ec2.aws.upbound.io"},
		"spec": map[string]any{
			"group": "ec2.aws.upbound.io",
			"names": map[string]any{"kind": "VPC", "listKind": "VPCList", "plural": "vpcs", "singular": "vpc"},
			"scope": "Cluster",
			"versions": []any{map[string]any{
				"name": "v1beta1", "served": true, "storage": true,
				"schema": map[string]any{"openAPIV3Schema": map[string]any{
					"type": "object",
					"properties": map[string]any{"spec": map[string]any{
						"type":       "object",
						"properties": map[string]any{"deletionPolicy": map[string]any{"type": "string", "default": "Delete"}},
					}},
				}},
			}},
		},
	}}
	if revision != "" {
		_ = unstructured.SetNestedSlice(u.Object, []any{map[string]any{
			"apiVersion": "pkg.crossplane.io/v1",
			"kind":       "ProviderRevision",
			"name":       revision,
			"uid":        "00000000-0000-0000-0000-000000000000",
			"controller": true,
		}}, "metadata", "ownerReferences")
	}
	return u
}

func TestApplyCRDDefaultsCaching(t *testing.T) {
	ctx := context.Background()
	rm := meta.NewDefaultRESTMapper([]schema.GroupVersion{vpcGVK.GroupVersion()})
	rm.Add(vpcGVK, meta.RESTScopeRoot)

	policy := func(u *unstructured.Unstructured) string {
		s, _, _ := unstructured.NestedString(u.Object, "spec", "deletionPolicy")
		return s
	}

	t.Run("TransientErrorNotCached", func(t *testing.T) {
		crds, dc := newCRDs(t, vpcCRD(""))
		failing := true
		failCRDGets(dc, &failing, errors.New("throttled"))
		if got := policy(applyCRDDefaults(ctx, rm, crds, vpc("a", "10.0.0.0/16"))); got != "" {
			t.Errorf("applyCRDDefaults(...) during the failure: deletionPolicy = %q, want undefaulted", got)
		}
		failing = false
		if got := policy(applyCRDDefaults(ctx, rm, crds, vpc("a", "10.0.0.0/16"))); got != "Delete" {
			t.Errorf("applyCRDDefaults(...) after recovery: deletionPolicy = %q, want Delete", got)
		}
	})

	t.Run("NotFoundCached", func(t *testing.T) {
		crds, dc := newCRDs(t)
		_ = applyCRDDefaults(ctx, rm, crds, vpc("a", "10.0.0.0/16"))
		if err := dc.Tracker().Add(vpcCRD("")); err != nil {
			t.Fatalf("Add(CRD): %v", err)
		}
		if got := policy(applyCRDDefaults(ctx, rm, crds, vpc("a", "10.0.0.0/16"))); got != "" {
			t.Errorf("applyCRDDefaults(...) after the CRD appeared: deletionPolicy = %q, want the cached NotFound (undefaulted)", got)
		}
	})
}

// TestOneCRDFetchPerRun pins the per-run sharing: simulate's three CRD
// lookups (provider discovery, ProviderManaged and the Planner's fallback
// defaults) and the diff engine's validator and defaulter all read one
// SchemaClient, so a run fetches each CRD at most once.
func TestOneCRDFetchPerRun(t *testing.T) {
	ctx := context.Background()
	s := runtime.NewScheme()
	s.AddKnownTypeWithName(vpcGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(vpcGVK.GroupVersion().WithKind("VPCList"), &unstructured.UnstructuredList{})
	if err := pkg.AddToScheme(s); err != nil {
		t.Fatalf("cannot add pkg scheme: %v", err)
	}
	rm := meta.NewDefaultRESTMapper([]schema.GroupVersion{vpcGVK.GroupVersion()})
	rm.Add(vpcGVK, meta.RESTScopeRoot)

	prv := &pkgv1.ProviderRevision{}
	prv.SetName("provider-aws-ec2-abc123")
	prv.Status.ResolvedPackage = "xpkg.upbound.io/upbound/provider-aws-ec2:v2.9.0"
	// A CRD read through the kube client, not the shared cache, counts too.
	kubeCRDGets := 0
	kube := fake.NewClientBuilder().WithScheme(s).WithObjects(prv).
		WithInterceptorFuncs(interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if obj.GetObjectKind().GroupVersionKind().Kind == "CustomResourceDefinition" {
				kubeCRDGets++
			}
			return c.Get(ctx, key, obj, opts...)
		}}).Build()

	crds, dc := newCRDs(t, vpcCRD(prv.GetName()))
	gets := countCRDGets(dc)
	rs := []*unstructured.Unstructured{vpc("a", "10.0.0.0/16"), vpc("b", "10.1.0.0/16")}

	if imgs := RelevantProviderImages(ctx, kube, rm, crds, rs, false, NewImageCache(), io.Discard); len(imgs) != 1 {
		t.Errorf("RelevantProviderImages(...): want 1 image, got %v", imgs)
	}
	managed := ProviderManaged(ctx, rm, crds)
	planner := NewPlanner(kube, rm, crds, "default", &fakeRouter{}, time.Second)
	for _, r := range rs {
		if !managed(r) {
			t.Errorf("ProviderManaged(...)(%s): want true", r.GetName())
		}
		if rp := planner.Plan(ctx, r); !rp.Approximate || rp.Error != "" {
			t.Errorf("Plan(%s): want an approximate fallback, got %+v", r.GetName(), rp)
		}
	}

	v := diffprocessor.NewSchemaValidator(crds, tu.NewMockResourceClient().Build(), tu.NewMockDefinitionClient().Build(), tu.TestLogger(t, false))
	if err := v.EnsureComposedResourceCRDs(ctx, rs); err != nil {
		t.Errorf("EnsureComposedResourceCRDs(...): %v", err)
	}
	d := diffprocessor.NewDefaulter(crds, tu.NewMockDefinitionClient().WithXRDForXRNotFound().Build(), diffprocessor.LenientDefaulting)
	for _, r := range rs {
		if _, _, err := d.Default(ctx, r); err != nil {
			t.Errorf("Default(%s): %v", r.GetName(), err)
		}
	}

	if n := *gets + kubeCRDGets; n != 1 {
		t.Errorf("want 1 CRD GET for one kind across simulate and the engine, got %d", n)
	}
}

// TestTransientCRDErrorRetried pins the failure rule: a lookup that fails in a
// way a retry may fix (Forbidden, a 500) is not cached, so the next lookup
// asks again, and RelevantProviderImages still warns about the kind once.
func TestTransientCRDErrorRetried(t *testing.T) {
	ctx := context.Background()
	s := runtime.NewScheme()
	if err := pkg.AddToScheme(s); err != nil {
		t.Fatalf("cannot add pkg scheme: %v", err)
	}
	rm := meta.NewDefaultRESTMapper([]schema.GroupVersion{vpcGVK.GroupVersion()})
	rm.Add(vpcGVK, meta.RESTScopeRoot)

	prv := &pkgv1.ProviderRevision{}
	prv.SetName("provider-aws-ec2-abc123")
	prv.Status.ResolvedPackage = "xpkg.upbound.io/upbound/provider-aws-ec2:v2.9.0"
	kube := fake.NewClientBuilder().WithScheme(s).WithObjects(prv).Build()

	errs := map[string]error{
		"Forbidden":     apierrors.NewForbidden(schema.GroupResource{Resource: "customresourcedefinitions"}, "vpcs.ec2.aws.upbound.io", errors.New("denied")),
		"InternalError": apierrors.NewInternalError(errors.New("boom")),
	}
	for name, err := range errs {
		t.Run(name, func(t *testing.T) {
			crds, dc := newCRDs(t, vpcCRD(prv.GetName()))
			failing := true
			failCRDGets(dc, &failing, err)
			gets := countCRDGets(dc)

			var warned bytes.Buffer
			cache := NewImageCache()
			for range 2 {
				if imgs := RelevantProviderImages(ctx, kube, rm, crds, []*unstructured.Unstructured{vpc("a", "10.0.0.0/16")}, false, cache, &warned); len(imgs) != 0 {
					t.Errorf("RelevantProviderImages(failing): want no images, got %v", imgs)
				}
			}
			if n := strings.Count(warned.String(), "Warning:"); n != 1 {
				t.Errorf("RelevantProviderImages(failing): want 1 warning, got %d: %q", n, warned.String())
			}
			managed := ProviderManaged(ctx, rm, crds)
			if managed(vpc("a", "10.0.0.0/16")) {
				t.Errorf("ProviderManaged(...)(failing): want false")
			}
			if *gets != 3 {
				t.Errorf("want each failed lookup retried, got %d CRD GETs for 3 lookups", *gets)
			}

			failing = false
			if imgs := RelevantProviderImages(ctx, kube, rm, crds, []*unstructured.Unstructured{vpc("a", "10.0.0.0/16")}, false, cache, &warned); len(imgs) != 1 {
				t.Errorf("RelevantProviderImages(recovered): want the provider image, got %v", imgs)
			}
			if !managed(vpc("a", "10.0.0.0/16")) {
				t.Errorf("ProviderManaged(...)(recovered): want true")
			}
			if n := strings.Count(warned.String(), "Warning:"); n != 1 {
				t.Errorf("RelevantProviderImages(recovered): want no further warning, got %d: %q", n, warned.String())
			}
		})
	}
}
