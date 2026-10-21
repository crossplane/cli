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
	"testing"
	"time"

	diffv1alpha1 "github.com/crossplane/upjet/v2/proto/diff/v1alpha1"
	"github.com/google/go-cmp/cmp"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/crossplane/crossplane/apis/v2/pkg"
	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"

	"github.com/crossplane/cli/v2/internal/plan"
)

var vpcGVK = schema.GroupVersionKind{Group: "ec2.aws.upbound.io", Version: "v1beta1", Kind: "VPC"}

func fieldValue(v string) *diffv1alpha1.FieldValue {
	return &diffv1alpha1.FieldValue{Kind: &diffv1alpha1.FieldValue_Value{Value: structpb.NewStringValue(v)}}
}

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

func TestPlanner(t *testing.T) {
	ctx := context.Background()

	s := runtime.NewScheme()
	s.AddKnownTypeWithName(vpcGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(vpcGVK.GroupVersion().WithKind("VPCList"), &unstructured.UnstructuredList{})

	rm := meta.NewDefaultRESTMapper([]schema.GroupVersion{vpcGVK.GroupVersion()})
	rm.Add(vpcGVK, meta.RESTScopeRoot)

	t.Run("RoutedUpdate", func(t *testing.T) {
		live := vpc("my-vpc", "10.0.0.0/16")
		kube := fake.NewClientBuilder().WithScheme(s).WithObjects(live).Build()
		pc := &fakePlanClient{rsp: &diffv1alpha1.PlanResponse{
			Action: diffv1alpha1.Action_ACTION_UPDATE,
			Changes: []*diffv1alpha1.FieldChange{{
				Field:   "spec.forProvider.cidrBlock",
				Actual:  fieldValue("10.0.0.0/16"),
				Planned: fieldValue("10.1.0.0/16"),
			}},
		}}

		got := NewPlanner(kube, rm, "default", &fakeRouter{client: pc}, time.Second).Plan(ctx, vpc("my-vpc", "10.1.0.0/16"))

		if got.Action != plan.ActionUpdate || got.Approximate || got.Error != "" {
			t.Errorf("Plan(...): want a provider-computed update, got %+v", got)
		}
		if pc.got.GetActualResource() == nil {
			t.Errorf("Plan(...): want the live resource sent as actual state")
		}
	})

	t.Run("NoLiveCounterpartPlansCreate", func(t *testing.T) {
		kube := fake.NewClientBuilder().WithScheme(s).Build()
		pc := &fakePlanClient{rsp: &diffv1alpha1.PlanResponse{Action: diffv1alpha1.Action_ACTION_CREATE}}

		got := NewPlanner(kube, rm, "default", &fakeRouter{client: pc}, time.Second).Plan(ctx, vpc("new-vpc", "10.0.0.0/16"))

		if got.Action != plan.ActionCreate || got.Error != "" {
			t.Errorf("Plan(...): want a create, got %+v", got)
		}
		if pc.got.GetActualResource() != nil {
			t.Errorf("Plan(...): want no actual state for a missing live resource")
		}
	})

	t.Run("NoRouteFallsBack", func(t *testing.T) {
		live := vpc("my-vpc", "10.0.0.0/16")
		kube := fake.NewClientBuilder().WithScheme(s).WithObjects(live).Build()

		got := NewPlanner(kube, rm, "default", &fakeRouter{}, time.Second).Plan(ctx, vpc("my-vpc", "10.1.0.0/16"))

		if !got.Approximate || got.Action != plan.ActionUpdate {
			t.Errorf("Plan(...): want an approximate update fallback, got %+v", got)
		}
	})

	t.Run("NotSupportedFallsBack", func(t *testing.T) {
		live := vpc("my-vpc", "10.0.0.0/16")
		kube := fake.NewClientBuilder().WithScheme(s).WithObjects(live).Build()
		pc := &fakePlanClient{err: notSupportedErr(t)}

		got := NewPlanner(kube, rm, "default", &fakeRouter{client: pc}, time.Second).Plan(ctx, vpc("my-vpc", "10.1.0.0/16"))

		if !got.Approximate || got.Action != plan.ActionUpdate {
			t.Errorf("Plan(...): want an approximate fallback on DIFF_COMPUTATION_NOT_SUPPORTED, got %+v", got)
		}
	})

	t.Run("FallbackAppliesCRDDefaults", func(t *testing.T) {
		// The live resource carries fields the API server defaulted on
		// admission (deletionPolicy, managementPolicies) that the manifest
		// omits. Applying the manifest would default them right back, so the
		// fallback must not plan them as removals.
		crdGVK := schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"}

		defScheme := runtime.NewScheme()
		defScheme.AddKnownTypeWithName(vpcGVK, &unstructured.Unstructured{})
		defScheme.AddKnownTypeWithName(vpcGVK.GroupVersion().WithKind("VPCList"), &unstructured.UnstructuredList{})
		defScheme.AddKnownTypeWithName(crdGVK, &unstructured.Unstructured{})
		defScheme.AddKnownTypeWithName(crdGVK.GroupVersion().WithKind("CustomResourceDefinitionList"), &unstructured.UnstructuredList{})

		crd := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "apiextensions.k8s.io/v1",
			"kind":       "CustomResourceDefinition",
			"metadata":   map[string]any{"name": "vpcs.ec2.aws.upbound.io"},
			"spec": map[string]any{
				"group": "ec2.aws.upbound.io",
				"names": map[string]any{"kind": "VPC", "listKind": "VPCList", "plural": "vpcs", "singular": "vpc"},
				"scope": "Cluster",
				"versions": []any{map[string]any{
					"name":   "v1beta1",
					"served": true, "storage": true,
					"schema": map[string]any{"openAPIV3Schema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"spec": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"deletionPolicy":     map[string]any{"type": "string", "default": "Delete"},
									"managementPolicies": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "default": []any{"*"}},
									"forProvider":        map[string]any{"type": "object", "properties": map[string]any{"cidrBlock": map[string]any{"type": "string"}}},
								},
							},
						},
					}},
				}},
			},
		}}

		live := vpc("my-vpc", "10.0.0.0/16")
		_ = unstructured.SetNestedField(live.Object, "Delete", "spec", "deletionPolicy")
		_ = unstructured.SetNestedStringSlice(live.Object, []string{"*"}, "spec", "managementPolicies")

		crdGets := 0
		kube := fake.NewClientBuilder().WithScheme(defScheme).WithObjects(live, crd).
			WithInterceptorFuncs(interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if obj.GetObjectKind().GroupVersionKind().Kind == "CustomResourceDefinition" {
					crdGets++
				}
				return c.Get(ctx, key, obj, opts...)
			}}).Build()

		planner := NewPlanner(kube, rm, "default", &fakeRouter{}, time.Second)
		got := planner.Plan(ctx, vpc("my-vpc", "10.1.0.0/16"))

		if !got.Approximate || got.Action != plan.ActionUpdate {
			t.Fatalf("Plan(...): want an approximate update fallback, got %+v", got)
		}
		want := []plan.FieldChange{{
			Path:    "spec.forProvider.cidrBlock",
			Actual:  &plan.Value{Value: "10.0.0.0/16"},
			Planned: &plan.Value{Value: "10.1.0.0/16"},
		}}
		if diff := cmp.Diff(want, got.Changes); diff != "" {
			t.Errorf("Plan(...): want only the real change, no removals of defaulted fields, -want, +got:\n%s", diff)
		}

		// An identical manifest plans as a no-op, and the CRD is served from
		// the cache rather than fetched again.
		if again := planner.Plan(ctx, vpc("my-vpc", "10.0.0.0/16")); again.Action != plan.ActionNoOp {
			t.Errorf("Plan(...): want a no-op for a manifest omitting only defaulted fields, got %+v", again)
		}
		if crdGets != 1 {
			t.Errorf("Plan(...): want 1 CRD fetch for 2 resources of the same kind, got %d", crdGets)
		}
	})

	t.Run("ServerErrorBecomesResourceError", func(t *testing.T) {
		kube := fake.NewClientBuilder().WithScheme(s).Build()
		pc := &fakePlanClient{err: status.Error(codes.Internal, "boom")}

		got := NewPlanner(kube, rm, "default", &fakeRouter{client: pc}, time.Second).Plan(ctx, vpc("my-vpc", "10.1.0.0/16"))

		if got.Error == "" {
			t.Errorf("Plan(...): want a per-resource error, got %+v", got)
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

		got := NewPlanner(kube, seedRM, "default", &fakeRouter{client: pc2}, time.Second).Plan(ctx, vpc("my-vpc", "10.0.0.0/16"))

		if got.Error != "" {
			t.Fatalf("Plan(...): unexpected error: %s", got.Error)
		}
		if n := len(pc2.got.GetKubernetesObjectStore()); n != 1 {
			t.Errorf("Plan(...): want the ProviderConfig in the object store, got %d objects", n)
		}
		if k := pc2.got.GetKubernetesObjectStore()[0].GetFields()["kind"].GetStringValue(); k != "ProviderConfig" {
			t.Errorf("Plan(...): want a ProviderConfig in the object store, got %q", k)
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
		got, err := RelevantProviderImages(ctx, kube, rm, []*unstructured.Unstructured{vpc("a", "10.0.0.0/16"), vpc("b", "10.1.0.0/16"), unserved}, false)
		if err != nil {
			t.Fatalf("RelevantProviderImages(...): unexpected error: %v", err)
		}
		want := []string{"xpkg.upbound.io/upbound/provider-aws-ec2:v2.9.0"}
		if len(got) != 1 || got[0] != want[0] {
			t.Errorf("RelevantProviderImages(...): want %v, got %v", want, got)
		}

		// ProviderManaged follows the same chain: a VPC's CRD is controlled
		// by a ProviderRevision; an unserved kind has no CRD.
		managed := ProviderManaged(ctx, kube, rm)
		if !managed(vpc("a", "10.0.0.0/16")) {
			t.Errorf("ProviderManaged(...)(VPC): want true for a provider-installed kind")
		}
		if managed(unserved) {
			t.Errorf("ProviderManaged(...)(Key): want false for a kind with no CRD")
		}
	})

	t.Run("UnservedKindPlansCreate", func(t *testing.T) {
		// A kind the cluster does not serve: no RESTMapping, no live fetch.
		kube := fake.NewClientBuilder().WithScheme(s).Build()
		emptyRM := meta.NewDefaultRESTMapper(nil)
		pc := &fakePlanClient{rsp: &diffv1alpha1.PlanResponse{Action: diffv1alpha1.Action_ACTION_CREATE}}

		got := NewPlanner(kube, emptyRM, "default", &fakeRouter{client: pc}, time.Second).Plan(ctx, vpc("my-vpc", "10.0.0.0/16"))

		if got.Action != plan.ActionCreate || got.Error != "" {
			t.Errorf("Plan(...): want a create for an unserved kind, got %+v", got)
		}
	})
}
