package simulate

import (
	"context"
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestCRDDefaultsCaching(t *testing.T) {
	ctx := context.Background()
	crdGVK := schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"}
	sc := runtime.NewScheme()
	sc.AddKnownTypeWithName(crdGVK, &unstructured.Unstructured{})
	sc.AddKnownTypeWithName(crdGVK.GroupVersion().WithKind("CustomResourceDefinitionList"), &unstructured.UnstructuredList{})

	rm := meta.NewDefaultRESTMapper([]schema.GroupVersion{vpcGVK.GroupVersion()})
	rm.Add(vpcGVK, meta.RESTScopeRoot)

	newCRD := func() *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
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
	}
	policy := func(u *unstructured.Unstructured) string {
		s, _, _ := unstructured.NestedString(u.Object, "spec", "deletionPolicy")
		return s
	}

	t.Run("TransientErrorNotCached", func(t *testing.T) {
		failing := true
		kube := fake.NewClientBuilder().WithScheme(sc).WithObjects(newCRD()).
			WithInterceptorFuncs(interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if failing {
					return errors.New("throttled")
				}
				return c.Get(ctx, key, obj, opts...)
			}}).Build()
		c := newCRDDefaults()
		if got := policy(c.apply(ctx, kube, rm, vpc("a", "10.0.0.0/16"))); got != "" {
			t.Errorf("apply(...) during the failure: deletionPolicy = %q, want undefaulted", got)
		}
		failing = false
		if got := policy(c.apply(ctx, kube, rm, vpc("a", "10.0.0.0/16"))); got != "Delete" {
			t.Errorf("apply(...) after recovery: deletionPolicy = %q, want Delete", got)
		}
	})

	t.Run("NotFoundCached", func(t *testing.T) {
		kube := fake.NewClientBuilder().WithScheme(sc).Build()
		c := newCRDDefaults()
		_ = c.apply(ctx, kube, rm, vpc("a", "10.0.0.0/16"))
		if err := kube.Create(ctx, newCRD()); err != nil {
			t.Fatalf("Create(CRD): %v", err)
		}
		if got := policy(c.apply(ctx, kube, rm, vpc("a", "10.0.0.0/16"))); got != "" {
			t.Errorf("apply(...) after the CRD appeared: deletionPolicy = %q, want the cached NotFound (undefaulted)", got)
		}
	})
}
