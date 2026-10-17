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

// Package plan contains the shared per-resource plan model for simulate
// commands: the typed result, the client for provider diff servers, the
// client-side approximate fallback, and the output renderers.
package plan

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Action is what applying a resource would do.
type Action string

// The actions a plan can report. Replace is never something a Crossplane
// provider does to apply a change; it reports that the change could only be
// applied by destroying and recreating the external resource. Delete is
// reserved for composition-driven removals (project simulate); a diff server
// never returns it.
const (
	ActionNoOp    Action = "no-op"
	ActionCreate  Action = "create"
	ActionUpdate  Action = "update"
	ActionReplace Action = "replace"
	ActionDelete  Action = "delete"
)

// Origin says where a planned change comes from.
type Origin string

// A change originates either from the desired state the user wrote, or from
// the provider (a default, or a late-initialized value).
const (
	OriginDesired  Origin = "desired"
	OriginProvider Origin = "provider"
)

// Value is one side of a field change. Exactly one of Value, Unknown,
// Sensitive, or Unresolved describes it. A nil *Value means the field does
// not exist on that side at all.
type Value struct {
	// Value is the concrete value, when it is known and printable.
	Value any `json:"value,omitempty"`
	// Unknown means the value is known only after apply (computed).
	Unknown bool `json:"unknown,omitempty"`
	// Sensitive means the value exists but is redacted.
	Sensitive bool `json:"sensitive,omitempty"`
	// Unresolved means the value comes from a reference or Secret the diff
	// server could not resolve.
	Unresolved bool `json:"unresolved,omitempty"`
}

// FieldChange is one changed field in a plan, in CRD terms.
type FieldChange struct {
	// Path is the CRD field path, e.g. spec.forProvider.engineVersion.
	Path string `json:"path"`
	// Actual is the field's current value; nil when the field does not exist
	// yet.
	Actual *Value `json:"actual,omitempty"`
	// Planned is the field's value after apply; nil when the change removes
	// the field.
	Planned *Value `json:"planned,omitempty"`
	// RequiresReplace means this change can only be applied by destroying and
	// recreating the external resource.
	RequiresReplace bool `json:"requiresReplace,omitempty"`
	// Origin says whether the change comes from the desired state or from the
	// provider. Empty when unknown (e.g. the approximate fallback).
	Origin Origin `json:"origin,omitempty"`
}

// ResourcePlan is the plan for a single resource: what applying it would do.
type ResourcePlan struct {
	APIVersion string        `json:"apiVersion"`
	Kind       string        `json:"kind"`
	Name       string        `json:"name"`
	Namespace  string        `json:"namespace,omitempty"`
	Action     Action        `json:"action,omitempty"`
	Changes    []FieldChange `json:"changes,omitempty"`
	// ReplacePaths lists the field paths that force replacement.
	ReplacePaths []string `json:"replacePaths,omitempty"`
	// Approximate means this plan was computed client-side, without a provider
	// diff server: no replacement detection, no computed values.
	Approximate bool `json:"approximate,omitempty"`
	// FallbackReason says why this plan is approximate.
	FallbackReason string `json:"fallbackReason,omitempty"`
	// Error is set when no plan could be computed for this resource at all.
	Error string `json:"error,omitempty"`

	// Desired is the resource the plan is for. It is carried for rendering
	// (the plan output shows the user's own manifest, annotated) and is not
	// part of the serialized result.
	Desired *unstructured.Unstructured `json:"-"`
}

// GroupVersionKind returns the plan's resource type.
func (p *ResourcePlan) GroupVersionKind() schema.GroupVersionKind {
	return schema.FromAPIVersionAndKind(p.APIVersion, p.Kind)
}

// NewResourcePlan returns a ResourcePlan identifying the given desired
// resource, with no action decided yet.
func NewResourcePlan(desired *unstructured.Unstructured) *ResourcePlan {
	return &ResourcePlan{
		APIVersion: desired.GetAPIVersion(),
		Kind:       desired.GetKind(),
		Name:       desired.GetName(),
		Namespace:  desired.GetNamespace(),
		Desired:    desired,
	}
}
