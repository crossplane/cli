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
	// Composite is the composite resource (XR or claim) this resource belongs
	// to, set by project simulate. Plans with a Composite render grouped
	// under it.
	Composite *ResourceRef `json:"composite,omitempty"`
	// DryRun is set when the resource's desired state could not be verified
	// against the API server; nil means it was verified (or not applicable).
	DryRun *Verification `json:"dryRun,omitempty"`

	// Before is the live object the plan compares, nil for a create. After
	// is the desired object, nil for a delete. They let a consumer match the
	// plan to its input and render its own diff. For project simulate both
	// are the cleaned views the cluster diff compares, with ignored paths,
	// status and server-managed metadata removed, so they agree with Changes;
	// both are nil when the cluster diff found the resource unchanged. For a
	// provider-planned resource After is the manifest sent to the provider,
	// not the provider's view of the result: computed values and provider
	// defaults appear only in Changes. A Secret's data and stringData values
	// are redacted (see RedactSecret).
	Before *unstructured.Unstructured `json:"before,omitempty"`
	After  *unstructured.Unstructured `json:"after,omitempty"`
}

// ResourceRef identifies a resource.
type ResourceRef struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Namespace  string `json:"namespace,omitempty"`
}

// Verification records a server-side dry run that did not happen: its presence
// means the dry run was not performed.
type Verification struct {
	// SkipReason says why the dry run was not performed.
	SkipReason string `json:"skipReason,omitempty"`
	// Detail is the underlying cause as reported by the cluster.
	Detail string `json:"detail,omitempty"`
	// StorageVersion is the CRD's storage version, set when it differs from
	// the version the resource was requested at.
	StorageVersion string `json:"storageVersion,omitempty"`
}

// GroupVersionKind returns the plan's resource type.
func (p *ResourcePlan) GroupVersionKind() schema.GroupVersionKind {
	return schema.FromAPIVersionAndKind(p.APIVersion, p.Kind)
}

// ResourceRef returns the plan's resource identity.
func (p *ResourcePlan) ResourceRef() ResourceRef {
	return ResourceRef{APIVersion: p.APIVersion, Kind: p.Kind, Name: p.Name, Namespace: p.Namespace}
}

// cleanView returns the redacted copy of u that a plan carries as before or
// after: u without status, server-managed metadata, the last-applied
// configuration annotation or composed resource references, the fields the
// diff engine also strips. It deliberately skips the engine's synthetic-name
// rule: resource simulate's objects carry real names, so a generateName-shaped
// metadata.name is kept, and so is an already empty spec.crossplane. u is not
// modified; nil stays nil.
func cleanView(u *unstructured.Unstructured) *unstructured.Unstructured {
	if u == nil {
		return nil
	}
	out := u.DeepCopy()
	delete(out.Object, "status")
	for _, f := range []string{"resourceVersion", "uid", "generation", "creationTimestamp", "managedFields", "selfLink", "ownerReferences"} {
		unstructured.RemoveNestedField(out.Object, "metadata", f)
	}
	removeEmptied(out.Object, "metadata", "annotations", "kubectl.kubernetes.io/last-applied-configuration")
	unstructured.RemoveNestedField(out.Object, "spec", "resourceRefs")
	removeEmptied(out.Object, "spec", "crossplane", "resourceRefs")
	return RedactSecret(out)
}

// removeEmptied removes obj's parent.child.key, then parent.child if that
// left it empty.
func removeEmptied(obj map[string]any, parent, child, key string) {
	m, ok, _ := unstructured.NestedMap(obj, parent, child)
	if !ok {
		return
	}
	if _, ok := m[key]; !ok {
		return
	}
	unstructured.RemoveNestedField(obj, parent, child, key)
	if len(m) == 1 {
		unstructured.RemoveNestedField(obj, parent, child)
	}
}

// RedactSecret returns u with each data and stringData value replaced by the
// sensitive-value marker when u is a core Secret, as a copy; any other object
// is returned as is. Keys are kept, so a consumer still sees which keys exist.
func RedactSecret(u *unstructured.Unstructured) *unstructured.Unstructured {
	if u == nil {
		return nil
	}
	if gvk := u.GroupVersionKind(); gvk.Group != "" || gvk.Kind != "Secret" {
		return u
	}

	sensitive := FormatValue(&Value{Sensitive: true})
	out := u.DeepCopy()
	for _, field := range []string{"data", "stringData"} {
		v, ok := out.Object[field]
		if !ok {
			continue
		}
		m, ok := v.(map[string]any)
		if !ok {
			// Malformed: redact the whole field rather than leak it.
			out.Object[field] = sensitive
			continue
		}
		for k := range m {
			m[k] = sensitive
		}
	}

	return out
}

// NewResourcePlan returns a ResourcePlan identifying the given desired
// resource, with no action decided yet.
func NewResourcePlan(desired *unstructured.Unstructured) *ResourcePlan {
	return &ResourcePlan{
		APIVersion: desired.GetAPIVersion(),
		Kind:       desired.GetKind(),
		Name:       desired.GetName(),
		Namespace:  desired.GetNamespace(),
	}
}
