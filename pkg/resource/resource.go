/*
Copyright 2023 The Crossplane Authors.

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

// Package resource contains the definition of the Resource used by all trace
// printers, and the client used to get a Resource and its children.
package resource

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/fieldpath"

	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
)

// Resource struct represents a kubernetes resource.
type Resource struct {
	Unstructured unstructured.Unstructured `json:"object"`
	Error        error                     `json:"error,omitempty"`

	// Relation describes why this resource is in the tree when it's not
	// actually part of it, e.g. an operation watching the parent resource.
	Relation string `json:"relation,omitempty"`

	Children []*Resource `json:"children,omitempty"`
}

// ResourceList struct represents a list of kubernetes resources.
// revive:disable-next-line:exported For consistency with Resource.
type ResourceList struct {
	Items []*Resource `json:"items"`
	Error error       `json:"error,omitempty"`
}

// FromObject returns a Resource wrapping the given typed object, converted
// to unstructured form under the given GVK, with the given relation. A failed
// conversion is carried on the Resource's Error, so it still renders as a
// node in the tree.
func FromObject(o client.Object, gvk schema.GroupVersionKind, relation string) *Resource {
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(o)
	if err != nil {
		u := unstructured.Unstructured{}
		u.SetGroupVersionKind(gvk)
		u.SetName(o.GetName())

		return &Resource{Unstructured: u, Error: err, Relation: relation}
	}

	u := unstructured.Unstructured{Object: obj}
	u.SetGroupVersionKind(gvk)

	return &Resource{Unstructured: u, Relation: relation}
}

// GetCondition of this resource.
func (r *Resource) GetCondition(ct xpv2.ConditionType) xpv2.Condition {
	conditioned := xpv2.ConditionedStatus{}
	// The path is directly `status` because conditions are inline.
	if err := fieldpath.Pave(r.Unstructured.Object).GetValueInto("status", &conditioned); err != nil {
		return xpv2.Condition{}
	}
	// We didn't use xpv1.CondidionedStatus.GetCondition because that's defaulting the
	// status to unknown if the condition is not found at all.
	for _, c := range conditioned.Conditions {
		if c.Type == ct {
			return c
		}
	}

	return xpv2.Condition{}
}
