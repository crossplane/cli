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
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	xrpkg "github.com/crossplane/cli/v2/pkg/xr"
)

// CRDs gets the CRD serving a kind. The diff engine's k8s.SchemaClient
// satisfies it, so simulate and the engine share one per-run CRD cache: the
// SchemaClient caches a CRD it finds and a CRD found not to exist, and nothing
// else, so a failure a retry may fix (Forbidden, a 500, a timeout) is retried
// on the next lookup.
type CRDs interface {
	GetCRD(ctx context.Context, gvk schema.GroupVersionKind) (*extv1.CustomResourceDefinition, error)
}

// lookupCRD returns the CRD serving gvk, or nil when the cluster does not
// serve the kind or holds no CRD for it. Those answers are definite. An error
// is not: a retry may answer differently, so a caller must not cache it.
func lookupCRD(ctx context.Context, rmapper meta.RESTMapper, crds CRDs, gvk schema.GroupVersionKind) (*extv1.CustomResourceDefinition, error) {
	if _, err := rmapper.RESTMapping(gvk.GroupKind(), gvk.Version); err != nil {
		if meta.IsNoMatchError(err) {
			return nil, nil
		}
		return nil, errors.Wrapf(err, "cannot map %s", gvk)
	}
	crd, err := crds.GetCRD(ctx, gvk)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	return crd, err
}

// applyCRDDefaults returns a copy of desired with its CRD's defaults applied,
// as the API server would admit it, or desired itself when the cluster does
// not serve the kind, its CRD cannot be read or yields no usable schema. The
// approximate fallback diffs the result against the live resource, which the
// API server defaulted on admission, and applying the manifest would default
// it again, so fields a manifest omits but the CRD defaults (deletionPolicy,
// managementPolicies, ...) are not removals and must not plan as ones. Like
// gather, this is best effort: a kind without a readable CRD simply diffs
// undefaulted.
func applyCRDDefaults(ctx context.Context, rmapper meta.RESTMapper, crds CRDs, desired *unstructured.Unstructured) *unstructured.Unstructured {
	crd, err := lookupCRD(ctx, rmapper, crds, desired.GroupVersionKind())
	if err != nil || crd == nil {
		return desired
	}

	defaulted := desired.DeepCopy()
	if err := xrpkg.ApplyCRDDefaults(defaulted.Object, desired.GetAPIVersion(), *crd); err != nil {
		return desired
	}
	return defaulted
}
