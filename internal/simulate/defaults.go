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

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	xrpkg "github.com/crossplane/cli/v2/pkg/xr"
)

// crdDefaults caches the cluster's CRDs by group and kind, so the approximate
// fallback can apply the API server's schema defaults to a desired manifest
// before diffing it against the live resource. The live resource was defaulted
// by the API server on admission, and applying the manifest would default it
// again, so fields a manifest omits but the CRD defaults (deletionPolicy,
// managementPolicies, ...) are not removals and must not plan as ones. Like
// gather, this is best effort: a kind without a fetchable CRD simply diffs
// undefaulted, and the miss is cached so a run over many resources of that
// kind probes the cluster once.
type crdDefaults struct {
	cache map[schema.GroupKind]*extv1.CustomResourceDefinition
}

func newCRDDefaults() *crdDefaults {
	return &crdDefaults{cache: map[schema.GroupKind]*extv1.CustomResourceDefinition{}}
}

// apply returns a copy of desired with its CRD's defaults applied, as the API
// server would admit it, or desired itself when the cluster does not serve the
// kind or its CRD yields no usable schema.
func (c *crdDefaults) apply(ctx context.Context, kubeClient client.Client, rmapper meta.RESTMapper, desired *unstructured.Unstructured) *unstructured.Unstructured {
	gvk := desired.GroupVersionKind()
	crd, ok := c.cache[gvk.GroupKind()]
	if !ok {
		var definite bool
		crd, definite = fetchCRD(ctx, kubeClient, rmapper, gvk)
		// A transient failure is not cached, so a later resource retries.
		if definite {
			c.cache[gvk.GroupKind()] = crd
		}
	}
	if crd == nil {
		return desired
	}

	defaulted := desired.DeepCopy()
	if err := xrpkg.ApplyCRDDefaults(defaulted.Object, desired.GetAPIVersion(), *crd); err != nil {
		return desired
	}
	return defaulted
}

// fetchCRD fetches the CRD serving gvk. nil means no defaulting is possible:
// the kind is not served or its CRD is unreadable. definite is false when the
// fetch failed in a way a retry may fix, so the answer must not be cached.
func fetchCRD(ctx context.Context, kubeClient client.Client, rmapper meta.RESTMapper, gvk schema.GroupVersionKind) (crd *extv1.CustomResourceDefinition, definite bool) {
	mapping, err := rmapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, meta.IsNoMatchError(err)
	}

	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"})
	if err := kubeClient.Get(ctx, client.ObjectKey{Name: mapping.Resource.Resource + "." + gvk.Group}, u); err != nil {
		return nil, apierrors.IsNotFound(err)
	}
	crd = &extv1.CustomResourceDefinition{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, crd); err != nil {
		return nil, true
	}
	return crd, true
}
