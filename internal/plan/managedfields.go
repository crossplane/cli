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

package plan

import (
	"bytes"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/structured-merge-diff/v6/fieldpath"
)

// applierOwned returns the fields of live an apply by a removes when the
// manifest drops them: the fields a owns, minus the fields other managers own
// too. Applying a manifest only removes fields its applier owns exclusively: a
// live-only field outside this set - late-initialized by the provider,
// defaulted by the API server and owned by nobody, or co-owned by another
// manager that keeps it alive - survives the apply and must not plan as a
// removal.
//
// Server-side apply as a.FieldManager owns that manager's Apply entry. An
// ApplyUndetermined applier owns the union of every declarative applier's
// entries - server-side apply managers, plus kubectl's client-side apply - so
// it overstates removals when several appliers share an object. nil means
// ownership is unknown (managedFields that cannot be parsed, or none for an
// undetermined applier) and removals should be taken at face value. It does
// not model ApplyClientSide, which removes by LastAppliedConfigAnnotation
// rather than by ownership.
func applierOwned(live *unstructured.Unstructured, a Applier) *fieldpath.Set {
	if live == nil {
		return nil
	}
	entries := live.GetManagedFields()
	if len(entries) == 0 && a.Mode != ApplyServerSide {
		return nil
	}

	owned := fieldpath.NewSet()
	others := fieldpath.NewSet()
	for _, e := range entries {
		if e.Subresource != "" || e.FieldsV1 == nil {
			continue
		}
		s := fieldpath.NewSet()
		if err := s.FromJSON(bytes.NewReader(e.FieldsV1.GetRawBytes())); err != nil {
			return nil
		}
		if ownedBy(e, a) {
			owned = owned.Union(s)
		} else {
			others = others.Union(s)
		}
	}
	return owned.Difference(others)
}

// ownedBy reports whether entry e records fields applier a owns.
func ownedBy(e metav1.ManagedFieldsEntry, a Applier) bool {
	if a.Mode == ApplyServerSide {
		return e.Manager == a.FieldManager && e.Operation == metav1.ManagedFieldsOperationApply
	}
	return e.Operation == metav1.ManagedFieldsOperationApply || e.Manager == "kubectl-client-side-apply"
}

// ownedLeaf reports whether leaves - the leaf members of an applier-owned
// field set, or the paths clientSideRemovable returns - contains fp or a
// prefix of it. An exact leaf is a plainly owned
// field; a prefix leaf means the applier owns that whole subtree, the way
// FieldsV1 records atomic lists and maps.
func ownedLeaf(leaves *fieldpath.Set, fp fieldpath.Path) bool {
	for i := 1; i <= len(fp); i++ {
		if leaves.Has(fp[:i]) {
			return true
		}
	}
	return false
}
