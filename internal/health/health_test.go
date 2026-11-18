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

package health

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestOf(t *testing.T) {
	cond := func(typ, status, reason, msg string) any {
		return map[string]any{"type": typ, "status": status, "reason": reason, "message": msg, "lastTransitionTime": "2026-01-01T00:00:00Z"}
	}
	obj := func(paused string, conds ...any) *unstructured.Unstructured {
		u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "example.org/v1", "kind": "Bucket", "metadata": map[string]any{"name": "b"}}}
		if paused != "" {
			u.SetAnnotations(map[string]string{"crossplane.io/paused": paused})
		}
		if conds != nil {
			u.Object["status"] = map[string]any{"conditions": conds}
		}
		return u
	}
	syncedFalse := cond("Synced", "False", "ReconcileError", "cannot update: denied")
	syncedTrue := cond("Synced", "True", "ReconcileSuccess", "")
	readyFalse := cond("Ready", "False", "Creating", "")
	readyTrue := cond("Ready", "True", "Available", "")

	cases := map[string]struct {
		reason string
		in     *unstructured.Unstructured
		want   *Health
	}{
		"NilObject": {
			reason: "No live object has no health.",
			want:   nil,
		},
		"NoConditions": {
			reason: "An object with no Synced or Ready condition, such as a ConfigMap, is healthy.",
			in:     obj(""),
			want:   &Health{State: Healthy},
		},
		"SyncedAndReady": {
			reason: "Synced=True and Ready=True is healthy, carrying both conditions.",
			in:     obj("", syncedTrue, readyTrue),
			want: &Health{
				State:  Healthy,
				Synced: &Condition{Status: corev1.ConditionTrue, Reason: "ReconcileSuccess"},
				Ready:  &Condition{Status: corev1.ConditionTrue, Reason: "Available"},
			},
		},
		"Unknown": {
			reason: "Unknown conditions are healthy.",
			in:     obj("", cond("Synced", "Unknown", "", ""), cond("Ready", "Unknown", "", "")),
			want: &Health{
				State:  Healthy,
				Synced: &Condition{Status: corev1.ConditionUnknown},
				Ready:  &Condition{Status: corev1.ConditionUnknown},
			},
		},
		"SyncedFalse": {
			reason: "Synced=False is failing, even when Ready=False too.",
			in:     obj("", syncedFalse, readyFalse),
			want: &Health{
				State:  Failing,
				Synced: &Condition{Status: corev1.ConditionFalse, Reason: "ReconcileError", Message: "cannot update: denied"},
				Ready:  &Condition{Status: corev1.ConditionFalse, Reason: "Creating"},
			},
		},
		"ReadyFalse": {
			reason: "Ready=False with Synced not False is not ready.",
			in:     obj("", syncedTrue, readyFalse),
			want: &Health{
				State:  NotReady,
				Synced: &Condition{Status: corev1.ConditionTrue, Reason: "ReconcileSuccess"},
				Ready:  &Condition{Status: corev1.ConditionFalse, Reason: "Creating"},
			},
		},
		"ReadyFalseOnly": {
			reason: "Ready=False with no Synced condition is not ready.",
			in:     obj("", readyFalse),
			want:   &Health{State: NotReady, Ready: &Condition{Status: corev1.ConditionFalse, Reason: "Creating"}},
		},
		"PausedOverridesFailing": {
			reason: "The paused annotation overrides Synced=False; the stale conditions are still reported.",
			in:     obj("true", syncedFalse),
			want: &Health{
				State:  Paused,
				Synced: &Condition{Status: corev1.ConditionFalse, Reason: "ReconcileError", Message: "cannot update: denied"},
			},
		},
		"PausedNotTrue": {
			reason: "Only the value \"true\" pauses an object.",
			in:     obj("false", syncedFalse),
			want: &Health{
				State:  Failing,
				Synced: &Condition{Status: corev1.ConditionFalse, Reason: "ReconcileError", Message: "cannot update: denied"},
			},
		},
		"MalformedStatus": {
			reason: "A status whose conditions cannot be read is treated as having none.",
			in:     &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{"conditions": "nope"}}},
			want:   &Health{State: Healthy},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := Of(tc.in)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("%s\nOf(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestUnhealthy(t *testing.T) {
	cases := map[string]struct {
		reason string
		in     *Health
		want   bool
	}{
		"Nil":      {reason: "No live object is not unhealthy.", in: nil, want: false},
		"Healthy":  {reason: "Healthy is not unhealthy.", in: &Health{State: Healthy}, want: false},
		"Failing":  {reason: "Failing is unhealthy.", in: &Health{State: Failing}, want: true},
		"NotReady": {reason: "Not ready is unhealthy.", in: &Health{State: NotReady}, want: true},
		"Paused":   {reason: "Paused is unhealthy.", in: &Health{State: Paused}, want: true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.in.Unhealthy(); got != tc.want {
				t.Errorf("%s\nUnhealthy(): want %t, got %t", tc.reason, tc.want, got)
			}
		})
	}
}
