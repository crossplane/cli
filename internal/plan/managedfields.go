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

// applierOwned returns the union of live's field sets owned by declarative
// appliers - server-side apply managers, plus kubectl's client-side apply -
// minus the fields other managers own too. Applying a manifest only removes
// fields its applier owns exclusively: a live-only field outside this set -
// late-initialized by the provider, defaulted by the API server and owned by
// nobody, or co-owned by another manager that keeps it alive - survives the
// apply and must not plan as a removal. nil means ownership is unknown (no
// managedFields, or ones that cannot be parsed) and removals should be taken
// at face value.
func applierOwned(live *unstructured.Unstructured) *fieldpath.Set {
	if live == nil {
		return nil
	}
	entries := live.GetManagedFields()
	if len(entries) == 0 {
		return nil
	}

	owned := fieldpath.NewSet()
	others := fieldpath.NewSet()
	for _, e := range entries {
		if e.Subresource != "" || e.FieldsV1 == nil {
			continue
		}
		s := fieldpath.NewSet()
		if err := s.FromJSON(bytes.NewReader(e.FieldsV1.Raw)); err != nil {
			return nil
		}
		if e.Operation == metav1.ManagedFieldsOperationApply || e.Manager == "kubectl-client-side-apply" {
			owned = owned.Union(s)
		} else {
			others = others.Union(s)
		}
	}
	return owned.Difference(others)
}

// ownedLeaf reports whether leaves - the leaf members of an applier-owned
// field set - contains fp or a prefix of it. An exact leaf is a plainly owned
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
