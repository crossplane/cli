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

// Package health classifies the health of a live cluster object from its
// Synced and Ready conditions and its paused annotation. It is a leaf package
// so that both the diff engine and the simulate result model can use it.
package health

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	xpmeta "github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/crossplane/cli/v2/pkg/resource"
)

// State is the health tier of a live object.
type State string

// The health tiers, in the order Of checks them.
const (
	// Paused means the object carries the crossplane.io/paused: "true"
	// annotation, so its conditions are stale. It overrides the conditions.
	Paused State = "paused"
	// Failing means the object's Synced condition is False.
	Failing State = "failing"
	// NotReady means the object's Ready condition is False, and it is not
	// failing.
	NotReady State = "notReady"
	// Healthy is anything else, including an object with no Synced or Ready
	// condition, or with either Unknown.
	Healthy State = "healthy"
)

// Condition is one condition of a live object, as the cluster reports it.
type Condition struct {
	Status  corev1.ConditionStatus `json:"status"`
	Reason  string                 `json:"reason,omitempty"`
	Message string                 `json:"message,omitempty"`
}

// Health is the health of one live object: its tier, and the Synced and Ready
// conditions it was classified from, each absent when the object has none.
type Health struct {
	State  State      `json:"state"`
	Synced *Condition `json:"synced,omitempty"`
	Ready  *Condition `json:"ready,omitempty"`
}

// Of classifies the live object u. It returns nil when there is no live
// object: a resource the plan creates has no health.
func Of(u *unstructured.Unstructured) *Health {
	if u == nil {
		return nil
	}

	r := &resource.Resource{Unstructured: *u}
	h := &Health{
		State:  Healthy,
		Synced: condition(r.GetCondition(xpv2.TypeSynced)),
		Ready:  condition(r.GetCondition(xpv2.TypeReady)),
	}

	switch {
	case xpmeta.IsPaused(u):
		h.State = Paused
	case h.Synced != nil && h.Synced.Status == corev1.ConditionFalse:
		h.State = Failing
	case h.Ready != nil && h.Ready.Status == corev1.ConditionFalse:
		h.State = NotReady
	}

	return h
}

// Unhealthy reports whether h is a live object in any tier but healthy. No
// live object (nil) is not unhealthy.
func (h *Health) Unhealthy() bool {
	return h != nil && h.State != Healthy
}

// condition returns c, or nil when the object has no such condition.
func condition(c xpv2.Condition) *Condition {
	if c.Type == "" {
		return nil
	}

	return &Condition{Status: c.Status, Reason: string(c.Reason), Message: c.Message}
}
