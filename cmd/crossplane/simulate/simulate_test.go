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

package simulate

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	diffv1alpha1 "github.com/crossplane/upjet/v2/proto/diff/v1alpha1"
	"github.com/google/go-cmp/cmp"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/crossplane/crossplane/apis/v2/pkg"
	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"

	"github.com/crossplane/cli/v2/internal/diffserver"
	"github.com/crossplane/cli/v2/internal/plan"
)

var vpcGVK = schema.GroupVersionKind{Group: "ec2.aws.upbound.io", Version: "v1beta1", Kind: "VPC"}

func vpc(name, cidr string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ec2.aws.upbound.io/v1beta1",
		"kind":       "VPC",
		"metadata":   map[string]any{"name": name},
		"spec":       map[string]any{"forProvider": map[string]any{"cidrBlock": cidr}},
	}}
}

// fakeRouter routes every resource to the given client, or nowhere.
type fakeRouter struct {
	client diffv1alpha1.PlanServiceClient
}

func (f *fakeRouter) Route(schema.GroupKind) (diffv1alpha1.PlanServiceClient, bool) {
	if f.client == nil {
		return nil, false
	}
	return f.client, true
}

// fakePlanClient answers Plan in-process.
type fakePlanClient struct {
	rsp *diffv1alpha1.PlanResponse
	err error
	got *diffv1alpha1.PlanRequest
}

func (f *fakePlanClient) Plan(_ context.Context, in *diffv1alpha1.PlanRequest, _ ...grpc.CallOption) (*diffv1alpha1.PlanResponse, error) {
	f.got = in
	return f.rsp, f.err
}

func notSupportedErr(t *testing.T) error {
	t.Helper()
	st, err := status.New(codes.FailedPrecondition, "cannot compute diff").WithDetails(&errdetails.PreconditionFailure{
		Violations: []*errdetails.PreconditionFailure_Violation{{Type: "DIFF_COMPUTATION_NOT_SUPPORTED"}},
	})
	if err != nil {
		t.Fatalf("cannot build status: %v", err)
	}
	return st.Err()
}

func TestLoadProviderInputs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "providers.yaml")
	manifest := `---
apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: provider-aws-ec2
  annotations:
    simulate.crossplane.io/runtime-docker-publish-address: 0.0.0.0
    simulate.crossplane.io/runtime-docker-target: 192.168.64.2
spec:
  package: xpkg.upbound.io/upbound/provider-aws-ec2:v2.9.0
---
apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: provider-aws-s3
spec:
  package: xpkg.upbound.io/upbound/provider-aws-s3:v2.9.0
`
	if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
		t.Fatalf("cannot write manifest: %v", err)
	}

	got, err := loadProviderInputs(path)
	if err != nil {
		t.Fatalf("loadProviderInputs(...): unexpected error: %v", err)
	}
	want := []diffserver.Input{
		{
			Image:          "xpkg.upbound.io/upbound/provider-aws-ec2:v2.9.0",
			PublishAddress: netip.MustParseAddr("0.0.0.0"),
			Target:         "192.168.64.2",
		},
		{Image: "xpkg.upbound.io/upbound/provider-aws-s3:v2.9.0"},
	}
	if diff := cmp.Diff(want, got, cmp.Comparer(func(a, b netip.Addr) bool { return a == b })); diff != "" {
		t.Errorf("loadProviderInputs(...): -want, +got:\n%s", diff)
	}

	notAProvider := filepath.Join(dir, "other.yaml")
	if err := os.WriteFile(notAProvider, []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n"), 0o600); err != nil {
		t.Fatalf("cannot write manifest: %v", err)
	}
	if _, err := loadProviderInputs(notAProvider); err == nil {
		t.Errorf("loadProviderInputs(...): want an error for non-Provider manifests")
	}

	// Render's Stop cleanup policy is deliberately unsupported for diff
	// servers, so the annotation that would request it is rejected.
	stopCleanup := filepath.Join(dir, "stop.yaml")
	manifest = `---
apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: provider-aws-ec2
  annotations:
    simulate.crossplane.io/runtime-docker-cleanup: Stop
spec:
  package: xpkg.upbound.io/upbound/provider-aws-ec2:v2.9.0
`
	if err := os.WriteFile(stopCleanup, []byte(manifest), 0o600); err != nil {
		t.Fatalf("cannot write manifest: %v", err)
	}
	if _, err := loadProviderInputs(stopCleanup); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Errorf("loadProviderInputs(...): want an invalid-annotation error for the Stop cleanup policy, got %v", err)
	}
}

func TestSimulateOne(t *testing.T) {
	ctx := context.Background()

	s := runtime.NewScheme()
	s.AddKnownTypeWithName(vpcGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(vpcGVK.GroupVersion().WithKind("VPCList"), &unstructured.UnstructuredList{})

	rm := meta.NewDefaultRESTMapper([]schema.GroupVersion{vpcGVK.GroupVersion()})
	rm.Add(vpcGVK, meta.RESTScopeRoot)

	cmd := &Cmd{Timeout: time.Second}

	t.Run("RoutedUpdate", func(t *testing.T) {
		live := vpc("my-vpc", "10.0.0.0/16")
		kube := fake.NewClientBuilder().WithScheme(s).WithObjects(live).Build()
		pc := &fakePlanClient{rsp: &diffv1alpha1.PlanResponse{Action: diffv1alpha1.Action_ACTION_UPDATE}}

		got := cmd.simulateOne(ctx, kube, rm, "default", &fakeRouter{client: pc}, newObjectStoreCache(), vpc("my-vpc", "10.1.0.0/16"))

		if got.Action != plan.ActionUpdate || got.Approximate || got.Error != "" {
			t.Errorf("simulateOne(...): want a provider-computed update, got %+v", got)
		}
		if pc.got.GetActualResource() == nil {
			t.Errorf("simulateOne(...): want the live resource sent as actual state")
		}
	})

	t.Run("NoLiveCounterpartPlansCreate", func(t *testing.T) {
		kube := fake.NewClientBuilder().WithScheme(s).Build()
		pc := &fakePlanClient{rsp: &diffv1alpha1.PlanResponse{Action: diffv1alpha1.Action_ACTION_CREATE}}

		got := cmd.simulateOne(ctx, kube, rm, "default", &fakeRouter{client: pc}, newObjectStoreCache(), vpc("new-vpc", "10.0.0.0/16"))

		if got.Action != plan.ActionCreate || got.Error != "" {
			t.Errorf("simulateOne(...): want a create, got %+v", got)
		}
		if pc.got.GetActualResource() != nil {
			t.Errorf("simulateOne(...): want no actual state for a missing live resource")
		}
	})

	t.Run("NoRouteFallsBack", func(t *testing.T) {
		live := vpc("my-vpc", "10.0.0.0/16")
		kube := fake.NewClientBuilder().WithScheme(s).WithObjects(live).Build()

		got := cmd.simulateOne(ctx, kube, rm, "default", &fakeRouter{}, newObjectStoreCache(), vpc("my-vpc", "10.1.0.0/16"))

		if !got.Approximate || got.Action != plan.ActionUpdate {
			t.Errorf("simulateOne(...): want an approximate update fallback, got %+v", got)
		}
	})

	t.Run("NotSupportedFallsBack", func(t *testing.T) {
		live := vpc("my-vpc", "10.0.0.0/16")
		kube := fake.NewClientBuilder().WithScheme(s).WithObjects(live).Build()
		pc := &fakePlanClient{err: notSupportedErr(t)}

		got := cmd.simulateOne(ctx, kube, rm, "default", &fakeRouter{client: pc}, newObjectStoreCache(), vpc("my-vpc", "10.1.0.0/16"))

		if !got.Approximate || got.Action != plan.ActionUpdate {
			t.Errorf("simulateOne(...): want an approximate fallback on DIFF_COMPUTATION_NOT_SUPPORTED, got %+v", got)
		}
	})

	t.Run("ServerErrorBecomesResourceError", func(t *testing.T) {
		kube := fake.NewClientBuilder().WithScheme(s).Build()
		pc := &fakePlanClient{err: status.Error(codes.Internal, "boom")}

		got := cmd.simulateOne(ctx, kube, rm, "default", &fakeRouter{client: pc}, newObjectStoreCache(), vpc("my-vpc", "10.1.0.0/16"))

		if got.Error == "" {
			t.Errorf("simulateOne(...): want a per-resource error, got %+v", got)
		}
	})

	t.Run("SeedsProviderConfigIntoObjectStore", func(t *testing.T) {
		pcGVK := schema.GroupVersionKind{Group: "aws.upbound.io", Version: "v1beta1", Kind: "ProviderConfig"}
		secretGVK := schema.GroupVersionKind{Version: "v1", Kind: "Secret"}

		seedScheme := runtime.NewScheme()
		seedScheme.AddKnownTypeWithName(vpcGVK, &unstructured.Unstructured{})
		seedScheme.AddKnownTypeWithName(vpcGVK.GroupVersion().WithKind("VPCList"), &unstructured.UnstructuredList{})
		seedScheme.AddKnownTypeWithName(pcGVK, &unstructured.Unstructured{})
		seedScheme.AddKnownTypeWithName(pcGVK.GroupVersion().WithKind("ProviderConfigList"), &unstructured.UnstructuredList{})
		seedScheme.AddKnownTypeWithName(secretGVK, &unstructured.Unstructured{})
		seedScheme.AddKnownTypeWithName(secretGVK.GroupVersion().WithKind("SecretList"), &unstructured.UnstructuredList{})

		seedRM := meta.NewDefaultRESTMapper([]schema.GroupVersion{vpcGVK.GroupVersion(), pcGVK.GroupVersion()})
		seedRM.Add(vpcGVK, meta.RESTScopeRoot)
		seedRM.Add(pcGVK, meta.RESTScopeRoot)

		pc := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "aws.upbound.io/v1beta1",
			"kind":       "ProviderConfig",
			"metadata":   map[string]any{"name": "default"},
			"spec": map[string]any{"credentials": map[string]any{
				"source":    "Secret",
				"secretRef": map[string]any{"name": "aws-creds", "namespace": "crossplane-system", "key": "creds"},
			}},
		}}
		secret := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Secret",
			"metadata":   map[string]any{"name": "aws-creds", "namespace": "crossplane-system"},
		}}

		kube := fake.NewClientBuilder().WithScheme(seedScheme).WithObjects(pc, secret).Build()
		pc2 := &fakePlanClient{rsp: &diffv1alpha1.PlanResponse{Action: diffv1alpha1.Action_ACTION_CREATE}}

		got := cmd.simulateOne(ctx, kube, seedRM, "default", &fakeRouter{client: pc2}, newObjectStoreCache(), vpc("my-vpc", "10.0.0.0/16"))

		if got.Error != "" {
			t.Fatalf("simulateOne(...): unexpected error: %s", got.Error)
		}
		if n := len(pc2.got.GetKubernetesObjectStore()); n != 1 {
			t.Errorf("simulateOne(...): want the ProviderConfig in the object store, got %d objects", n)
		}
		if k := pc2.got.GetKubernetesObjectStore()[0].GetFields()["kind"].GetStringValue(); k != "ProviderConfig" {
			t.Errorf("simulateOne(...): want a ProviderConfig in the object store, got %q", k)
		}
	})

	t.Run("FindsProviderConfigInResourceGroup", func(t *testing.T) {
		// provider-kubernetes serves its ProviderConfig in the same group as
		// its resources, not one level up like upjet-based providers.
		objGVK := schema.GroupVersionKind{Group: "kubernetes.crossplane.io", Version: "v1alpha2", Kind: "Object"}
		pcGVK := schema.GroupVersionKind{Group: "kubernetes.crossplane.io", Version: "v1alpha1", Kind: "ProviderConfig"}

		scheme := runtime.NewScheme()
		scheme.AddKnownTypeWithName(objGVK, &unstructured.Unstructured{})
		scheme.AddKnownTypeWithName(objGVK.GroupVersion().WithKind("ObjectList"), &unstructured.UnstructuredList{})
		scheme.AddKnownTypeWithName(pcGVK, &unstructured.Unstructured{})
		scheme.AddKnownTypeWithName(pcGVK.GroupVersion().WithKind("ProviderConfigList"), &unstructured.UnstructuredList{})

		mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{objGVK.GroupVersion(), pcGVK.GroupVersion()})
		mapper.Add(objGVK, meta.RESTScopeRoot)
		mapper.Add(pcGVK, meta.RESTScopeRoot)

		pc := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "kubernetes.crossplane.io/v1alpha1",
			"kind":       "ProviderConfig",
			"metadata":   map[string]any{"name": "default"},
		}}
		desired := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "kubernetes.crossplane.io/v1alpha2",
			"kind":       "Object",
			"metadata":   map[string]any{"name": "my-object"},
		}}

		gets := 0
		kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pc).
			WithInterceptorFuncs(interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				gets++
				return c.Get(ctx, key, obj, opts...)
			}}).Build()

		stores := newObjectStoreCache()
		got := stores.gather(ctx, kube, mapper, "default", desired, nil)

		if len(got) != 1 || got[0].GetFields()["kind"].GetStringValue() != "ProviderConfig" {
			t.Errorf("gather(...): want the same-group ProviderConfig, got %v", got)
		}

		// A second resource referencing the same ProviderConfig is served from
		// the cache without another fetch.
		if again := stores.gather(ctx, kube, mapper, "default", desired, nil); len(again) != 1 {
			t.Errorf("gather(...): want the cached ProviderConfig, got %v", again)
		}
		if gets != 1 {
			t.Errorf("gather(...): want 1 fetch for 2 gathers of the same ProviderConfig, got %d", gets)
		}
	})

	t.Run("DiscoversOnlyRelevantProviders", func(t *testing.T) {
		crdGVK := schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"}

		discScheme := runtime.NewScheme()
		discScheme.AddKnownTypeWithName(crdGVK, &unstructured.Unstructured{})
		discScheme.AddKnownTypeWithName(crdGVK.GroupVersion().WithKind("CustomResourceDefinitionList"), &unstructured.UnstructuredList{})
		if err := pkg.AddToScheme(discScheme); err != nil {
			t.Fatalf("cannot add pkg scheme: %v", err)
		}

		// After an upgrade the inactive old revision still owns the CRD; only
		// the active revision is its controller, and discovery must follow
		// the controller reference.
		crd := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "apiextensions.k8s.io/v1",
			"kind":       "CustomResourceDefinition",
			"metadata": map[string]any{
				"name": "vpcs.ec2.aws.upbound.io",
				"ownerReferences": []any{
					map[string]any{
						"apiVersion": "pkg.crossplane.io/v1",
						"kind":       "ProviderRevision",
						"name":       "provider-aws-ec2-old999",
						"uid":        "00000000-0000-0000-0000-000000000001",
					},
					map[string]any{
						"apiVersion": "pkg.crossplane.io/v1",
						"kind":       "ProviderRevision",
						"name":       "provider-aws-ec2-abc123",
						"uid":        "00000000-0000-0000-0000-000000000000",
						"controller": true,
					},
				},
			},
		}}
		prv := &pkgv1.ProviderRevision{}
		prv.SetName("provider-aws-ec2-abc123")
		prv.Status.ResolvedPackage = "xpkg.upbound.io/upbound/provider-aws-ec2:v2.9.0"

		kube := fake.NewClientBuilder().WithScheme(discScheme).WithObjects(crd, prv).Build()

		// Two VPCs (same kind) plus a kind with no CRD on the cluster: exactly
		// one provider image, once.
		unserved := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "kms.aws.upbound.io/v1beta1",
			"kind":       "Key",
			"metadata":   map[string]any{"name": "k"},
		}}
		got, err := relevantProviderImages(ctx, kube, rm, []*unstructured.Unstructured{vpc("a", "10.0.0.0/16"), vpc("b", "10.1.0.0/16"), unserved}, false)
		if err != nil {
			t.Fatalf("relevantProviderImages(...): unexpected error: %v", err)
		}
		want := []string{"xpkg.upbound.io/upbound/provider-aws-ec2:v2.9.0"}
		if len(got) != 1 || got[0] != want[0] {
			t.Errorf("relevantProviderImages(...): want %v, got %v", want, got)
		}
	})

	t.Run("UnservedKindPlansCreate", func(t *testing.T) {
		// A kind the cluster does not serve: no RESTMapping, no live fetch.
		kube := fake.NewClientBuilder().WithScheme(s).Build()
		emptyRM := meta.NewDefaultRESTMapper(nil)
		pc := &fakePlanClient{rsp: &diffv1alpha1.PlanResponse{Action: diffv1alpha1.Action_ACTION_CREATE}}

		got := cmd.simulateOne(ctx, kube, emptyRM, "default", &fakeRouter{client: pc}, newObjectStoreCache(), vpc("my-vpc", "10.0.0.0/16"))

		if got.Action != plan.ActionCreate || got.Error != "" {
			t.Errorf("simulateOne(...): want a create for an unserved kind, got %+v", got)
		}
	})
}
