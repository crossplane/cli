/*
Copyright 2025 The Crossplane Authors.

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

// Package ops enriches a tree of Resource with the operations
// (ops.crossplane.io) related to each resource: the WatchOperations watching
// it, the Operations they created in response to changes to it, and the
// Operations that applied changes to it.
package ops

import (
	"context"
	"sort"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	opsv1alpha1 "github.com/crossplane/crossplane/apis/v2/ops/v1alpha1"

	"github.com/crossplane/cli/v2/pkg/resource"
)

const (
	errListWatchOperations = "cannot list watch operations"
	errListCronOperations  = "cannot list cron operations"
	errListOperations      = "cannot list operations"
)

// Mode determines which operations related to a resource are shown.
type Mode string

const (
	// ModeNone shows no operations.
	ModeNone Mode = "none"
	// ModeTriggered shows the WatchOperations watching a resource and the
	// Operations they created in response to changes to it.
	ModeTriggered Mode = "triggered"
	// ModeAll additionally shows the Operations that applied changes to a
	// resource, grouped under the CronOperation or WatchOperation that
	// created them, if any.
	ModeAll Mode = "all"
)

// Relations set on the operations attached to a resource, to make clear they
// are not part of the resource tree, but only related to it.
const (
	// RelationWatching marks a WatchOperation watching the resource.
	RelationWatching = "watching"
	// RelationTriggered marks an Operation created in response to a change
	// to the resource.
	RelationTriggered = "triggered"
	// RelationApplied marks an Operation that applied changes to the
	// resource, or the CronOperation or WatchOperation that created one.
	RelationApplied = "applied"
)

// Enricher attaches the operations related to each resource in a tree of
// Resource as children of the resource they relate to.
type Enricher struct {
	client client.Client
	mode   Mode
}

// NewEnricher returns a new Enricher showing operations per the given Mode.
func NewEnricher(c client.Client, m Mode) *Enricher {
	return &Enricher{client: c, mode: m}
}

// Enrich attaches the operations related to each resource in the given trees
// as children of the resource they relate to. It's a no-op if the operations
// API is not available in the cluster.
func (e *Enricher) Enrich(ctx context.Context, roots ...*resource.Resource) error {
	if e.mode == ModeNone {
		return nil
	}

	s, err := e.load(ctx)
	if err != nil {
		return err
	}

	if s == nil {
		// The cluster doesn't support operations at all.
		return nil
	}

	for _, r := range roots {
		enrich(r, s, e.mode)
	}

	return nil
}

// snapshot holds all the operations in the cluster, to be matched against the
// resources in the tree.
type snapshot struct {
	watchOps []opsv1alpha1.WatchOperation
	cronOps  map[string]opsv1alpha1.CronOperation
	ops      []opsv1alpha1.Operation
}

// load returns a snapshot of all the operations in the cluster, or a nil
// snapshot without error if the cluster doesn't support the operations API.
func (e *Enricher) load(ctx context.Context) (*snapshot, error) {
	wl := &opsv1alpha1.WatchOperationList{}
	if err := e.client.List(ctx, wl); err != nil {
		if meta.IsNoMatchError(err) || runtime.IsNotRegisteredError(err) {
			return nil, nil
		}

		return nil, errors.Wrap(err, errListWatchOperations)
	}

	ol := &opsv1alpha1.OperationList{}
	if err := e.client.List(ctx, ol); err != nil {
		return nil, errors.Wrap(err, errListOperations)
	}

	s := &snapshot{watchOps: wl.Items, ops: ol.Items}

	// CronOperations never target a specific resource themselves, they are
	// only needed to group the Operations they created that applied one.
	if e.mode == ModeAll {
		cl := &opsv1alpha1.CronOperationList{}
		if err := e.client.List(ctx, cl); err != nil {
			return nil, errors.Wrap(err, errListCronOperations)
		}

		s.cronOps = make(map[string]opsv1alpha1.CronOperation, len(cl.Items))
		for _, co := range cl.Items {
			s.cronOps[co.GetName()] = co
		}
	}

	return s, nil
}

func (s *snapshot) watchOp(name string) *opsv1alpha1.WatchOperation {
	for i := range s.watchOps {
		if s.watchOps[i].GetName() == name {
			return &s.watchOps[i]
		}
	}

	return nil
}

// enrich walks the tree depth-first and appends the operations related to
// each resource as its children. Only the original tree is walked, the
// attached operations are not enriched themselves.
func enrich(r *resource.Resource, s *snapshot, mode Mode) {
	for _, c := range r.Children {
		enrich(c, s, mode)
	}

	r.Children = append(r.Children, relatedOperations(r, s, mode)...)
}

// relatedOperations returns the operations related to the given resource:
// WatchOperations watching it with the Operations they created for it as
// children and, in ModeAll, the Operations that applied changes to it grouped
// under the WatchOperation or CronOperation that created them, if any.
func relatedOperations(r *resource.Resource, s *snapshot, mode Mode) []*resource.Resource {
	a := &attacher{
		branches: map[string]*resource.Resource{},
		attached: map[string]bool{},
	}

	a.attachWatching(r, s)

	if mode == ModeAll {
		a.attachApplied(r, s)
	}

	for _, b := range a.related {
		sortByCreation(b.Children)
	}

	return a.related
}

// attacher accumulates the operations related to a resource.
type attacher struct {
	// related are the operations to attach to the resource.
	related []*resource.Resource
	// branches dedupes the WatchOperations and CronOperations in related.
	branches map[string]*resource.Resource
	// attached tracks the Operations already attached to a branch.
	attached map[string]bool
}

// branchFor returns the existing branch for the given WatchOperation or
// CronOperation, creating it with the given relation if needed.
func (a *attacher) branchFor(o client.Object, gvk schema.GroupVersionKind, relation string) *resource.Resource {
	key := gvk.Kind + "/" + o.GetName()
	if b, ok := a.branches[key]; ok {
		return b
	}

	b := asResource(o, gvk, relation)
	a.branches[key] = b
	a.related = append(a.related, b)

	return b
}

// attachWatching attaches the WatchOperations watching the given resource,
// with the Operations they created in response to changes to it as children.
func (a *attacher) attachWatching(r *resource.Resource, s *snapshot) {
	for i := range s.watchOps {
		wo := &s.watchOps[i]
		if !watchMatches(wo.Spec.Watch, &r.Unstructured) {
			continue
		}

		b := a.branchFor(wo, opsv1alpha1.WatchOperationGroupVersionKind, RelationWatching)

		for j := range s.ops {
			op := &s.ops[j]
			if op.GetLabels()[opsv1alpha1.LabelWatchOperationName] != wo.GetName() || !triggeredBy(op, &r.Unstructured) {
				continue
			}

			b.Children = append(b.Children, asResource(op, opsv1alpha1.OperationGroupVersionKind, RelationTriggered))
			a.attached[op.GetName()] = true
		}
	}
}

// attachApplied attaches the Operations that applied changes to the given
// resource, grouped under the WatchOperation or CronOperation that created
// them, if any.
func (a *attacher) attachApplied(r *resource.Resource, s *snapshot) {
	for i := range s.ops {
		op := &s.ops[i]
		if a.attached[op.GetName()] || !applied(op, &r.Unstructured) {
			continue
		}

		a.attached[op.GetName()] = true
		child := asResource(op, opsv1alpha1.OperationGroupVersionKind, RelationApplied)

		if b := a.creatorBranch(op, s); b != nil {
			appendChild(b, child)
			continue
		}

		a.related = append(a.related, child)
	}
}

// creatorBranch returns the branch for the WatchOperation or CronOperation
// that created the given Operation, or nil if it was created by neither.
func (a *attacher) creatorBranch(op *opsv1alpha1.Operation, s *snapshot) *resource.Resource {
	owner := metav1.GetControllerOf(op)
	if owner == nil || owner.APIVersion != opsv1alpha1.SchemeGroupVersion.String() {
		return nil
	}

	switch owner.Kind {
	case opsv1alpha1.WatchOperationKind:
		if wo := s.watchOp(owner.Name); wo != nil {
			return a.branchFor(wo, opsv1alpha1.WatchOperationGroupVersionKind, RelationApplied)
		}
	case opsv1alpha1.CronOperationKind:
		if co, ok := s.cronOps[owner.Name]; ok {
			return a.branchFor(&co, opsv1alpha1.CronOperationGroupVersionKind, RelationApplied)
		}
	}

	return nil
}

// appendChild appends the child to the branch, dropping the child's relation
// if it would just repeat the branch's one.
func appendChild(b, child *resource.Resource) {
	if child.Relation == b.Relation {
		child.Relation = ""
	}

	b.Children = append(b.Children, child)
}

// watchMatches returns true if the given watch spec selects the given
// resource.
func watchMatches(w opsv1alpha1.WatchSpec, u *unstructured.Unstructured) bool {
	if w.APIVersion != u.GetAPIVersion() || w.Kind != u.GetKind() {
		return false
	}

	if w.Namespace != "" && w.Namespace != u.GetNamespace() {
		return false
	}

	labels := u.GetLabels()
	for k, v := range w.MatchLabels {
		if labels[k] != v {
			return false
		}
	}

	return true
}

// triggeredBy returns true if the Operation is annotated as having been
// created in response to a change to the given resource.
func triggeredBy(op *opsv1alpha1.Operation, u *unstructured.Unstructured) bool {
	a := op.GetAnnotations()

	return a[opsv1alpha1.AnnotationWatchedResourceAPIVersion] == u.GetAPIVersion() &&
		a[opsv1alpha1.AnnotationWatchedResourceKind] == u.GetKind() &&
		a[opsv1alpha1.AnnotationWatchedResourceName] == u.GetName() &&
		a[opsv1alpha1.AnnotationWatchedResourceNamespace] == u.GetNamespace()
}

// applied returns true if the Operation reports having applied changes to the
// given resource.
func applied(op *opsv1alpha1.Operation, u *unstructured.Unstructured) bool {
	for _, ref := range op.Status.AppliedResourceRefs {
		if ref.APIVersion == u.GetAPIVersion() && ref.Kind == u.GetKind() &&
			ref.Name == u.GetName() && ptr.Deref(ref.Namespace, "") == u.GetNamespace() {
			return true
		}
	}

	return false
}

func asResource(o client.Object, gvk schema.GroupVersionKind, relation string) *resource.Resource {
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(o)
	if err != nil {
		u := unstructured.Unstructured{}
		u.SetGroupVersionKind(gvk)
		u.SetName(o.GetName())

		return &resource.Resource{Unstructured: u, Error: err, Relation: relation}
	}

	u := unstructured.Unstructured{Object: obj}
	u.SetGroupVersionKind(gvk)

	return &resource.Resource{Unstructured: u, Relation: relation}
}

func sortByCreation(rs []*resource.Resource) {
	sort.SliceStable(rs, func(i, j int) bool {
		ti, tj := rs[i].Unstructured.GetCreationTimestamp(), rs[j].Unstructured.GetCreationTimestamp()
		if ti.Equal(&tj) {
			return rs[i].Unstructured.GetName() < rs[j].Unstructured.GetName()
		}

		return ti.Before(&tj)
	})
}
