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
	"testing"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"
)

func TestResolveApplier(t *testing.T) {
	apply := func(manager string) metav1.ManagedFieldsEntry {
		return metav1.ManagedFieldsEntry{Manager: manager, Operation: metav1.ManagedFieldsOperationApply}
	}
	update := func(manager string) metav1.ManagedFieldsEntry {
		return metav1.ManagedFieldsEntry{Manager: manager, Operation: metav1.ManagedFieldsOperationUpdate}
	}
	live := func(lastApplied bool, entries ...metav1.ManagedFieldsEntry) *unstructured.Unstructured {
		u := &unstructured.Unstructured{Object: map[string]any{}}
		u.SetManagedFields(entries)
		if lastApplied {
			u.SetAnnotations(map[string]string{LastAppliedConfigAnnotation: "{}"})
		}
		return u
	}

	type args struct {
		live  *unstructured.Unstructured
		flags ApplierFlags
	}
	cases := map[string]struct {
		reason string
		args   args
		want   Applier
	}{
		"NoLiveObject": {
			reason: "A resource that does not exist yet has no applier to find.",
			args:   args{},
			want:   Applier{Mode: ApplyUndetermined},
		},
		"NoManagedFields": {
			reason: "An object with no managedFields has no candidates.",
			args:   args{live: live(false)},
			want:   Applier{Mode: ApplyUndetermined},
		},
		"OneServerSideApplier": {
			reason: "With no flags, the only candidate is the applier.",
			args:   args{live: live(false, apply("argocd-controller"), update("kube-controller-manager"))},
			want:   Applier{Mode: ApplyServerSide, FieldManager: "argocd-controller", Candidates: []string{"argocd-controller"}},
		},
		"CrossplaneManagersAreNotCandidates": {
			reason: "Crossplane's own managers and simulate's never apply a user's manifest.",
			args: args{live: live(false,
				apply("argocd-controller"),
				apply("crossplane"),
				apply("crossplane-simulate"),
				apply("apiextensions.crossplane.io/composed/0f9a"),
				apply("apiextensions.crossplane.io/claim"),
			)},
			want: Applier{Mode: ApplyServerSide, FieldManager: "argocd-controller", Candidates: []string{"argocd-controller"}},
		},
		"SubresourceEntriesAreNotCandidates": {
			reason: "An Apply to a subresource, such as status, does not apply the manifest.",
			args: args{live: live(false,
				apply("argocd-controller"),
				metav1.ManagedFieldsEntry{Manager: "status-writer", Operation: metav1.ManagedFieldsOperationApply, Subresource: "status"},
			)},
			want: Applier{Mode: ApplyServerSide, FieldManager: "argocd-controller", Candidates: []string{"argocd-controller"}},
		},
		"OnlyUpdates": {
			reason: "Managers that only Update are no appliers, so there is no candidate.",
			args:   args{live: live(false, update("kubectl-edit"))},
			want:   Applier{Mode: ApplyUndetermined},
		},
		"OneClientSideApplier": {
			reason: "With no flags, a last-applied-configuration annotation alone makes client-side apply the applier, whatever its manager is called.",
			args:   args{live: live(true, update("argocd-controller"))},
			want:   Applier{Mode: ApplyClientSide, ClientSide: true},
		},
		"TwoServerSideAppliers": {
			reason: "With no flags, two candidates leave the applier undetermined.",
			args:   args{live: live(false, apply("platform-operator"), apply("argocd-controller"), apply("argocd-controller"))},
			want:   Applier{Mode: ApplyUndetermined, Candidates: []string{"argocd-controller", "platform-operator"}},
		},
		"ServerSideAndClientSideAppliers": {
			reason: "With no flags, a server-side candidate and a client-side one leave the applier undetermined.",
			args:   args{live: live(true, apply("argocd-controller"))},
			want:   Applier{Mode: ApplyUndetermined, Candidates: []string{"argocd-controller"}, ClientSide: true},
		},
		"ServerSideOneServerSideApplier": {
			reason: "--server-side alone takes the only server-side candidate, ignoring a client-side one.",
			args:   args{live: live(true, apply("argocd-controller")), flags: ApplierFlags{ServerSide: ptr.To(true)}},
			want:   Applier{Mode: ApplyServerSide, FieldManager: "argocd-controller", Candidates: []string{"argocd-controller"}, ClientSide: true},
		},
		"ServerSideTwoServerSideAppliers": {
			reason: "--server-side alone with two server-side candidates leaves the applier undetermined.",
			args:   args{live: live(false, apply("argocd-controller"), apply("platform-operator")), flags: ApplierFlags{ServerSide: ptr.To(true)}},
			want:   Applier{Mode: ApplyUndetermined, Candidates: []string{"argocd-controller", "platform-operator"}},
		},
		"ServerSideOnlyClientSideApplier": {
			reason: "--server-side alone with no server-side candidate leaves the applier undetermined.",
			args:   args{live: live(true, update("kubectl-client-side-apply")), flags: ApplierFlags{ServerSide: ptr.To(true)}},
			want:   Applier{Mode: ApplyUndetermined, ClientSide: true},
		},
		"ServerSideWithFieldManager": {
			reason: "--server-side --field-manager M is server-side apply as M, even when M has applied nothing yet.",
			args:   args{live: live(true, apply("argocd-controller")), flags: ApplierFlags{FieldManager: "flux", ServerSide: ptr.To(true)}},
			want:   Applier{Mode: ApplyServerSide, FieldManager: "flux", Candidates: []string{"argocd-controller"}, ClientSide: true},
		},
		"NotServerSide": {
			reason: "--server-side=false is client-side apply.",
			args:   args{live: live(false, apply("argocd-controller")), flags: ApplierFlags{ServerSide: ptr.To(false)}},
			want:   Applier{Mode: ApplyClientSide, Candidates: []string{"argocd-controller"}},
		},
		"NotServerSideWithFieldManager": {
			reason: "--server-side=false is client-side apply even with --field-manager naming a server-side applier.",
			args:   args{live: live(false, apply("argocd-controller")), flags: ApplierFlags{FieldManager: "argocd-controller", ServerSide: ptr.To(false)}},
			want:   Applier{Mode: ApplyClientSide, Candidates: []string{"argocd-controller"}},
		},
		"FieldManagerThatApplies": {
			reason: "--field-manager M alone is server-side apply as M when M has an Apply entry.",
			args:   args{live: live(true, apply("argocd-controller"), apply("platform-operator")), flags: ApplierFlags{FieldManager: "platform-operator"}},
			want:   Applier{Mode: ApplyServerSide, FieldManager: "platform-operator", Candidates: []string{"argocd-controller", "platform-operator"}, ClientSide: true},
		},
		"FieldManagerThatOnlyUpdates": {
			reason: "--field-manager M alone is client-side apply when M has no Apply entry.",
			args:   args{live: live(true, apply("platform-operator"), update("argocd-controller")), flags: ApplierFlags{FieldManager: "argocd-controller"}},
			want:   Applier{Mode: ApplyClientSide, Candidates: []string{"platform-operator"}, ClientSide: true},
		},
		"FieldManagerThatAppliesOnlyASubresource": {
			reason: "--field-manager M alone is client-side apply when M's only Apply entry is to a subresource.",
			args: args{
				live:  live(false, metav1.ManagedFieldsEntry{Manager: "argocd-controller", Operation: metav1.ManagedFieldsOperationApply, Subresource: "status"}),
				flags: ApplierFlags{FieldManager: "argocd-controller"},
			},
			want: Applier{Mode: ApplyClientSide},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := ResolveApplier(tc.args.live, tc.args.flags)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("%s\nResolveApplier(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}
