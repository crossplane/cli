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
	"slices"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// LastAppliedConfigAnnotation is where kubectl's client-side apply records the
// manifest it last applied, which is what its next apply removes fields from.
const LastAppliedConfigAnnotation = "kubectl.kubernetes.io/last-applied-configuration"

// ApplyMode is how a manifest is applied to a live object.
type ApplyMode int

// The apply modes. Each removes different fields the manifest drops.
const (
	// ApplyUndetermined means the live object does not tell who applies it.
	ApplyUndetermined ApplyMode = iota
	// ApplyServerSide is server-side apply: it removes the fields its field
	// manager owned and no longer sends, unless another manager owns them too.
	ApplyServerSide
	// ApplyClientSide is kubectl's client-side apply: it removes the fields in
	// LastAppliedConfigAnnotation that the manifest drops, whatever owns them.
	ApplyClientSide
)

// ApplierFlags are how the user says who applies a manifest, as kubectl
// apply's flags of the same names. Their zero value infers it.
type ApplierFlags struct {
	// FieldManager is --field-manager. Empty is unset.
	FieldManager string
	// ServerSide is --server-side. Nil is unset.
	ServerSide *bool
}

// Applier is who applies a manifest to a live object, which decides what
// fields the apply removes.
type Applier struct {
	Mode ApplyMode
	// FieldManager is the field manager of an ApplyServerSide applier.
	FieldManager string
	// Candidates are the server-side appliers the live object records, sorted.
	Candidates []string
	// ClientSide reports that the live object records a client-side apply.
	ClientSide bool
	// ImperativeWriters are the field managers that wrote the live object
	// with an Update rather than an apply, sorted. Only an object without
	// LastAppliedConfigAnnotation has them, since client-side apply records
	// Updates.
	ImperativeWriters []string
}

// HasCandidates reports whether the live object records any applier. With
// none, no apply removes anything, so an undetermined applier changes nothing.
func (a Applier) HasCandidates() bool {
	return len(a.Candidates) > 0 || a.ClientSide
}

// DescribeCandidates lists the appliers the live object records, for a
// warning that the applier cannot be told.
func (a Applier) DescribeCandidates() string {
	c := a.Candidates
	if a.ClientSide {
		c = append(slices.Clip(c), "client-side apply ("+LastAppliedConfigAnnotation+")")
	}
	return strings.Join(c, ", ")
}

// ResolveApplier works out who applies a manifest to live, from flags and
// live's managedFields. Its candidates are the field managers with an Apply
// entry, other than Crossplane's own and those of subresources, and a
// client-side applier when live has LastAppliedConfigAnnotation. That
// applier's manager name is not recorded as such (Argo CD's client-side sync
// records an Update as argocd-controller, say), and does not change what it
// removes.
//
// An imperative writer is a field manager, other than Crossplane's own and
// those of subresources, with an Update entry on an object without
// LastAppliedConfigAnnotation: kubectl create or edit, a controller, or an
// API client. A lone server-side applier on such an object may only have set
// a few of its fields, so an imperative writer stops the applier being
// inferred.
//
//   - No flags: the only candidate, if there is exactly one and no imperative
//     writer.
//   - --server-side alone: the only server-side candidate, if there is
//     exactly one and no imperative writer.
//   - --server-side --field-manager M: server-side apply as M.
//   - --server-side=false, with or without --field-manager: client-side apply.
//   - --field-manager M alone: server-side apply as M if M has an Apply entry,
//     otherwise client-side apply.
//
// Otherwise the applier is ApplyUndetermined.
func ResolveApplier(live *unstructured.Unstructured, flags ApplierFlags) Applier {
	a := Applier{Mode: ApplyUndetermined}
	if live == nil {
		return a
	}

	_, a.ClientSide = live.GetAnnotations()[LastAppliedConfigAnnotation]

	applies := false
	for _, e := range live.GetManagedFields() {
		if e.Subresource != "" {
			continue
		}
		switch e.Operation {
		case metav1.ManagedFieldsOperationApply:
			applies = applies || e.Manager == flags.FieldManager
			if !crossplaneManager(e.Manager) && !slices.Contains(a.Candidates, e.Manager) {
				a.Candidates = append(a.Candidates, e.Manager)
			}
		case metav1.ManagedFieldsOperationUpdate:
			if !a.ClientSide && !crossplaneManager(e.Manager) && !slices.Contains(a.ImperativeWriters, e.Manager) {
				a.ImperativeWriters = append(a.ImperativeWriters, e.Manager)
			}
		}
	}
	slices.Sort(a.Candidates)
	slices.Sort(a.ImperativeWriters)

	switch {
	case flags.ServerSide != nil && !*flags.ServerSide:
		a.Mode = ApplyClientSide
	case flags.FieldManager != "" && (flags.ServerSide != nil || applies):
		a.Mode, a.FieldManager = ApplyServerSide, flags.FieldManager
	case flags.FieldManager != "":
		a.Mode = ApplyClientSide
	case len(a.Candidates) == 1 && len(a.ImperativeWriters) == 0 && (flags.ServerSide != nil || !a.ClientSide):
		a.Mode, a.FieldManager = ApplyServerSide, a.Candidates[0]
	case len(a.Candidates) == 0 && a.ClientSide && flags.ServerSide == nil:
		a.Mode = ApplyClientSide
	}
	return a
}

// crossplaneManager reports whether manager is one of Crossplane's own, or
// simulate's, none of which applies a manifest a user wrote.
func crossplaneManager(manager string) bool {
	return manager == "crossplane" || manager == "crossplane-simulate" || strings.HasPrefix(manager, "apiextensions.crossplane.io/")
}
