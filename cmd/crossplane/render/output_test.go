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

package render

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/crossplane/crossplane-runtime/v2/pkg/fieldpath"

	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
)

// realTime is an arbitrary non-fixed timestamp, distinct from conditionTime(),
// used to verify that ReplaceConditionTimestamps actually rewrites it.
func realTime() metav1.Time {
	return metav1.NewTime(time.Date(2023, time.June, 15, 12, 30, 0, 0, time.UTC))
}

// conditionsOf reads status.conditions back out of an unstructured object. It
// returns nil if the object has no status or no conditions.
func conditionsOf(t *testing.T, u *unstructured.Unstructured) []xpv2.Condition {
	t.Helper()

	var cs xpv2.ConditionedStatus
	if err := fieldpath.Pave(u.Object).GetValueInto("status", &cs); err != nil {
		return nil
	}

	return cs.Conditions
}

func TestReplaceConditionTimestamps(t *testing.T) {
	type args struct {
		o runtime.Object
	}
	type want struct {
		// conditions is the expected status.conditions after the call. It is
		// only asserted when checkConditions is true.
		conditions      []xpv2.Condition
		checkConditions bool
		err             error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"NoStatus": {
			reason: "An object with no status should be a no-op and return nil.",
			args: args{
				o: &unstructured.Unstructured{
					Object: map[string]any{
						"apiVersion": "example.org/v1",
						"kind":       "Thing",
						"metadata":   map[string]any{"name": "test"},
						"spec":       map[string]any{"coolField": "cool"},
					},
				},
			},
			want: want{
				conditions:      nil,
				checkConditions: true,
				err:             nil,
			},
		},
		"StatusNoConditions": {
			reason: "An object with a status but no conditions should be a no-op and return nil.",
			args: args{
				o: &unstructured.Unstructured{Object: MustLoadJSON(`{
					"apiVersion": "example.org/v1",
					"kind": "Thing",
					"metadata": {"name": "test"},
					"status": {"phase": "Ready"}
				}`)},
			},
			want: want{
				conditions:      nil,
				checkConditions: true,
				err:             nil,
			},
		},
		"SingleCondition": {
			reason: "A single condition's timestamp should be replaced, preserving its other fields.",
			args: args{
				o: &unstructured.Unstructured{
					Object: map[string]any{
						"apiVersion": "example.org/v1",
						"kind":       "Thing",
						"metadata":   map[string]any{"name": "test"},
						"status": map[string]any{
							"conditions": []xpv2.Condition{{
								Type:               "Ready",
								Status:             corev1.ConditionTrue,
								Reason:             "Available",
								Message:            "all good",
								LastTransitionTime: realTime(),
							}},
						},
					},
				},
			},
			want: want{
				conditions: []xpv2.Condition{{
					Type:               "Ready",
					Status:             corev1.ConditionTrue,
					Reason:             "Available",
					Message:            "all good",
					LastTransitionTime: conditionTime(),
				}},
				checkConditions: true,
				err:             nil,
			},
		},
		"MultipleConditions": {
			reason: "Every condition's timestamp should be replaced, preserving order and other fields.",
			args: args{
				o: &unstructured.Unstructured{
					Object: map[string]any{
						"apiVersion": "example.org/v1",
						"kind":       "Thing",
						"metadata":   map[string]any{"name": "test"},
						"status": map[string]any{
							"conditions": []xpv2.Condition{
								{
									Type:               "Ready",
									Status:             corev1.ConditionTrue,
									Reason:             "Available",
									LastTransitionTime: realTime(),
								},
								{
									Type:               "Synced",
									Status:             corev1.ConditionFalse,
									Reason:             "ReconcileError",
									Message:            "boom",
									LastTransitionTime: metav1.NewTime(time.Date(2025, time.March, 9, 8, 0, 0, 0, time.UTC)),
								},
							},
						},
					},
				},
			},
			want: want{
				conditions: []xpv2.Condition{
					{
						Type:               "Ready",
						Status:             corev1.ConditionTrue,
						Reason:             "Available",
						LastTransitionTime: conditionTime(),
					},
					{
						Type:               "Synced",
						Status:             corev1.ConditionFalse,
						Reason:             "ReconcileError",
						Message:            "boom",
						LastTransitionTime: conditionTime(),
					},
				},
				checkConditions: true,
				err:             nil,
			},
		},
		"AlreadyFixedTimestamp": {
			reason: "A condition already at the fixed timestamp should be unchanged (idempotent).",
			args: args{
				o: &unstructured.Unstructured{
					Object: map[string]any{
						"apiVersion": "example.org/v1",
						"kind":       "Thing",
						"metadata":   map[string]any{"name": "test"},
						"status": map[string]any{
							"conditions": []xpv2.Condition{{
								Type:               "Ready",
								Status:             corev1.ConditionTrue,
								Reason:             "Available",
								LastTransitionTime: conditionTime(),
							}},
						},
					},
				},
			},
			want: want{
				conditions: []xpv2.Condition{{
					Type:               "Ready",
					Status:             corev1.ConditionTrue,
					Reason:             "Available",
					LastTransitionTime: conditionTime(),
				}},
				checkConditions: true,
				err:             nil,
			},
		},
		"TypedObjectConversion": {
			// A non-*unstructured.Unstructured runtime.Object exercises the
			// ToUnstructured conversion branch. The function converts into a
			// local copy and does not write back to the argument, so only the
			// returned error is observable here.
			reason: "A typed (non-unstructured) object should be converted without error.",
			args: args{
				o: &corev1.ConfigMap{
					TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
					ObjectMeta: metav1.ObjectMeta{Name: "test"},
					Data:       map[string]string{"key": "value"},
				},
			},
			want: want{
				checkConditions: false,
				err:             nil,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := ReplaceConditionTimestamps(tc.args.o)

			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("%s\nReplaceConditionTimestamps(): -want err, +got err:\n%s", tc.reason, diff)
			}

			if !tc.want.checkConditions {
				return
			}

			u, ok := tc.args.o.(*unstructured.Unstructured)
			if !ok {
				t.Fatalf("%s\ncheckConditions requires an *unstructured.Unstructured argument", tc.reason)
			}

			if diff := cmp.Diff(tc.want.conditions, conditionsOf(t, u)); diff != "" {
				t.Errorf("%s\nReplaceConditionTimestamps(): -want conditions, +got conditions:\n%s", tc.reason, diff)
			}
		})
	}
}
