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
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func mr(spec map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ec2.aws.upbound.io/v1beta1",
		"kind":       "VPC",
		"metadata":   map[string]any{"name": "my-vpc"},
		"spec":       spec,
	}}
}

func managedBy(manager string, op metav1.ManagedFieldsOperationType, fields string) metav1.ManagedFieldsEntry {
	return metav1.ManagedFieldsEntry{
		Manager:    manager,
		Operation:  op,
		FieldsType: "FieldsV1",
		FieldsV1:   &metav1.FieldsV1{Raw: []byte(fields)},
	}
}

func TestFallback(t *testing.T) {
	type args struct {
		desired       *unstructured.Unstructured
		live          *unstructured.Unstructured
		managedFields []metav1.ManagedFieldsEntry
	}
	type want struct {
		action  Action
		changes []FieldChange
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"Create": {
			reason: "No live counterpart plans as a create listing the desired spec.",
			args: args{
				desired: mr(map[string]any{"forProvider": map[string]any{
					"cidrBlock": "10.0.0.0/16",
					"region":    "us-west-1",
				}}),
			},
			want: want{
				action: ActionCreate,
				changes: []FieldChange{
					{Path: "spec.forProvider.cidrBlock", Planned: &Value{Value: "10.0.0.0/16"}},
					{Path: "spec.forProvider.region", Planned: &Value{Value: "us-west-1"}},
				},
			},
		},
		"NoOp": {
			reason: "An identical spec plans as a no-op, even when numeric types differ between YAML and the API server.",
			args: args{
				desired: mr(map[string]any{"forProvider": map[string]any{"retention": int64(7)}}),
				live:    mr(map[string]any{"forProvider": map[string]any{"retention": float64(7)}}),
			},
			want: want{action: ActionNoOp},
		},
		"Update": {
			reason: "A changed nested field plans as an update with both sides.",
			args: args{
				desired: mr(map[string]any{"forProvider": map[string]any{"cidrBlock": "10.1.0.0/16"}}),
				live:    mr(map[string]any{"forProvider": map[string]any{"cidrBlock": "10.0.0.0/16"}}),
			},
			want: want{
				action: ActionUpdate,
				changes: []FieldChange{{
					Path:    "spec.forProvider.cidrBlock",
					Actual:  &Value{Value: "10.0.0.0/16"},
					Planned: &Value{Value: "10.1.0.0/16"},
				}},
			},
		},
		"AddAndRemove": {
			reason: "Fields only on one side plan as additions and removals.",
			args: args{
				desired: mr(map[string]any{"forProvider": map[string]any{"added": true}}),
				live:    mr(map[string]any{"forProvider": map[string]any{"removed": "gone"}}),
			},
			want: want{
				action: ActionUpdate,
				changes: []FieldChange{
					{Path: "spec.forProvider.added", Planned: &Value{Value: true}},
					{Path: "spec.forProvider.removed", Actual: &Value{Value: "gone"}},
				},
			},
		},
		"ListElementChange": {
			reason: "Same-length lists diff element-wise with indexed paths.",
			args: args{
				desired: mr(map[string]any{"forProvider": map[string]any{"zones": []any{"a", "c"}}}),
				live:    mr(map[string]any{"forProvider": map[string]any{"zones": []any{"a", "b"}}}),
			},
			want: want{
				action: ActionUpdate,
				changes: []FieldChange{{
					Path:    "spec.forProvider.zones[1]",
					Actual:  &Value{Value: "b"},
					Planned: &Value{Value: "c"},
				}},
			},
		},
		"DeepNestedListOfMaps": {
			reason: "Changes inside maps nested in lists diff leaf-wise with indexed paths; untouched siblings stay silent.",
			args: args{
				desired: mr(map[string]any{"forProvider": map[string]any{
					"parentIdRef": map[string]any{"name": "nebius-eu-north", "namespace": "modelplane-system"},
					"template": map[string]any{
						"filesystems": []any{map[string]any{
							"attachMode":         "READ_ONLY",
							"existingFilesystem": map[string]any{"idSelector": map[string]any{"matchControllerRef": true}},
							"mountTag":           "modelplane-cache",
						}},
					},
				}}),
				live: mr(map[string]any{"forProvider": map[string]any{
					"parentIdRef": map[string]any{"name": "nebius-eu-north", "namespace": "modelplane-system"},
					"template": map[string]any{
						"filesystems": []any{map[string]any{
							"attachMode":         "READ_WRITE",
							"existingFilesystem": map[string]any{"idSelector": map[string]any{"matchControllerRef": true}},
							"mountTag":           "modelplane-cache",
						}},
					},
				}}),
			},
			want: want{
				action: ActionUpdate,
				changes: []FieldChange{{
					Path:    "spec.forProvider.template.filesystems[0].attachMode",
					Actual:  &Value{Value: "READ_WRITE"},
					Planned: &Value{Value: "READ_ONLY"},
				}},
			},
		},
		"LateInitializedFieldsAreNotRemovals": {
			reason: "Live-only fields owned by the provider (late-initialized) or by nobody (API server defaults) survive an apply and must not plan as removals.",
			args: args{
				desired: mr(map[string]any{"forProvider": map[string]any{"cidrBlock": "10.0.0.0/16"}}),
				live: mr(map[string]any{
					"forProvider":    map[string]any{"cidrBlock": "10.0.0.0/16", "instanceTenancy": "default"},
					"deletionPolicy": "Delete",
				}),
				managedFields: []metav1.ManagedFieldsEntry{
					managedBy("composed", metav1.ManagedFieldsOperationApply, `{"f:spec":{"f:forProvider":{"f:cidrBlock":{}}}}`),
					managedBy("provider", metav1.ManagedFieldsOperationUpdate, `{"f:spec":{"f:forProvider":{"f:instanceTenancy":{}}}}`),
				},
			},
			want: want{action: ActionNoOp},
		},
		"CoOwnedFieldsAreNotRemovals": {
			reason: "A field the applier and another manager both own survives the apply - Kubernetes retains it while any owner remains - so omitting it is not a removal.",
			args: args{
				desired: mr(map[string]any{"forProvider": map[string]any{"cidrBlock": "10.0.0.0/16"}}),
				live: mr(map[string]any{"forProvider": map[string]any{
					"cidrBlock":       "10.0.0.0/16",
					"instanceTenancy": "default",
				}}),
				managedFields: []metav1.ManagedFieldsEntry{
					managedBy("composed", metav1.ManagedFieldsOperationApply, `{"f:spec":{"f:forProvider":{"f:cidrBlock":{},"f:instanceTenancy":{}}}}`),
					managedBy("provider", metav1.ManagedFieldsOperationUpdate, `{"f:spec":{"f:forProvider":{"f:instanceTenancy":{}}}}`),
				},
			},
			want: want{action: ActionNoOp},
		},
		"ApplierOwnedFieldsAreRemovals": {
			reason: "A live-only field the applier owns really is removed when the manifest omits it.",
			args: args{
				desired: mr(map[string]any{"forProvider": map[string]any{"cidrBlock": "10.0.0.0/16"}}),
				live: mr(map[string]any{"forProvider": map[string]any{
					"cidrBlock":          "10.0.0.0/16",
					"enableDnsHostnames": true,
				}}),
				managedFields: []metav1.ManagedFieldsEntry{
					managedBy("composed", metav1.ManagedFieldsOperationApply, `{"f:spec":{"f:forProvider":{"f:cidrBlock":{},"f:enableDnsHostnames":{}}}}`),
				},
			},
			want: want{
				action:  ActionUpdate,
				changes: []FieldChange{{Path: "spec.forProvider.enableDnsHostnames", Actual: &Value{Value: true}}},
			},
		},
		"ClientSideApplyCountsAsApplier": {
			reason: "kubectl's client-side apply records operation Update, but its fields are declarative intent and their removal is real.",
			args: args{
				desired: mr(map[string]any{"forProvider": map[string]any{}}),
				live:    mr(map[string]any{"forProvider": map[string]any{"cidrBlock": "10.0.0.0/16"}}),
				managedFields: []metav1.ManagedFieldsEntry{
					managedBy("kubectl-client-side-apply", metav1.ManagedFieldsOperationUpdate, `{"f:spec":{"f:forProvider":{"f:cidrBlock":{}}}}`),
				},
			},
			want: want{
				action:  ActionUpdate,
				changes: []FieldChange{{Path: "spec.forProvider.cidrBlock", Actual: &Value{Value: "10.0.0.0/16"}}},
			},
		},
		"AtomicListOwnershipCoversElements": {
			reason: "FieldsV1 owns atomic lists as a whole, so element removals below an applier-owned list are real, and ones below a provider-owned list are not.",
			args: args{
				desired: mr(map[string]any{"forProvider": map[string]any{}}),
				live: mr(map[string]any{
					"forProvider":        map[string]any{"securityGroupIds": []any{"sg-1"}},
					"managementPolicies": []any{"Observe"},
				}),
				managedFields: []metav1.ManagedFieldsEntry{
					managedBy("composed", metav1.ManagedFieldsOperationApply, `{"f:spec":{"f:managementPolicies":{}}}`),
					managedBy("provider", metav1.ManagedFieldsOperationUpdate, `{"f:spec":{"f:forProvider":{"f:securityGroupIds":{}}}}`),
				},
			},
			want: want{
				action:  ActionUpdate,
				changes: []FieldChange{{Path: "spec.managementPolicies[0]", Actual: &Value{Value: "Observe"}}},
			},
		},
		"ListLengthChange": {
			reason: "Lists of different lengths diff as one whole-list change.",
			args: args{
				desired: mr(map[string]any{"forProvider": map[string]any{"zones": []any{"a"}}}),
				live:    mr(map[string]any{"forProvider": map[string]any{"zones": []any{"a", "b"}}}),
			},
			want: want{
				action: ActionUpdate,
				changes: []FieldChange{{
					Path:    "spec.forProvider.zones",
					Actual:  &Value{Value: []any{"a", "b"}},
					Planned: &Value{Value: []any{"a"}},
				}},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if len(tc.args.managedFields) > 0 {
				tc.args.live.SetManagedFields(tc.args.managedFields)
			}

			got := Fallback(tc.args.desired, tc.args.live, "test")

			if !got.Approximate {
				t.Errorf("\n%s\nFallback(...): want Approximate, got exact", tc.reason)
			}
			if got.Action != tc.want.action {
				t.Errorf("\n%s\nFallback(...): want action %q, got %q", tc.reason, tc.want.action, got.Action)
			}
			if diff := cmp.Diff(tc.want.changes, got.Changes, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("\n%s\nFallback(...): -want changes, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestFieldChanges(t *testing.T) {
	cases := map[string]struct {
		actual, planned map[string]any
		want            []FieldChange
	}{
		"DeleteIsActualOnly": {
			actual: map[string]any{"metadata": map[string]any{"labels": map[string]any{"team": "a"}}, "spec": map[string]any{"size": 1}},
			want: []FieldChange{
				{Path: "metadata.labels.team", Actual: &Value{Value: "a"}},
				{Path: "spec.size", Actual: &Value{Value: 1}},
			},
		},
		"CreateIsPlannedOnly": {
			planned: map[string]any{"spec": map[string]any{"size": 1}},
			want:    []FieldChange{{Path: "spec.size", Planned: &Value{Value: 1}}},
		},
		"Update": {
			actual:  map[string]any{"spec": map[string]any{"size": 1}},
			planned: map[string]any{"spec": map[string]any{"size": 2}},
			want:    []FieldChange{{Path: "spec.size", Actual: &Value{Value: 1}, Planned: &Value{Value: 2}}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, FieldChanges(tc.actual, tc.planned)); diff != "" {
				t.Errorf("FieldChanges(...): -want, +got:\n%s", diff)
			}
		})
	}
}
