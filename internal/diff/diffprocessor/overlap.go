package diffprocessor

import (
	"fmt"
	"maps"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	un "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	dt "github.com/crossplane/cli/v2/internal/diff/result"
)

// This file holds the overlap checks among a run's inputs and renders: identity of an object
// independent of API version, which input manages another, and two renders reaching one resource.
// An input with no name yet (generateName only) has no identity.

// management is how one input of a run manages another; see managedBy.
type management int

const (
	notManaged management = iota
	// claimedBy: the managed XR is bound to the manager, a claim, whose controller writes it.
	claimedBy
	// composedBy: the managed XR's controller reference names the manager, whose composition writes it.
	composedBy
)

// managedBy reports whether the XR xr is managed by manager, another input of the run, and how: bound
// to it as a claim (see boundToClaim), or controlled by it, per the controller reference on what is known
// of xr (see knownForms; its cluster copy carries the one Crossplane set). The reference is compared by
// group, kind and name, and by namespace for a namespaced manager, which can only own objects in its own
// namespace; never by UID, which a render synthesizes afresh. Each group is the input's diff group, or
// the zero group when nothing has been rendered.
func managedBy(xrGroup dt.XRDiffGroup, xr *un.Unstructured, managerGroup dt.XRDiffGroup, manager *un.Unstructured) management {
	if boundToClaim(xrGroup, xr, managerGroup, manager) {
		return claimedBy
	}

	id := identityOf(manager)
	if id == "" {
		return notManaged
	}

	for _, obj := range knownForms(xrGroup, xr) {
		ref := metav1.GetControllerOf(obj)
		if ref == nil {
			continue
		}

		if identity(ref.APIVersion, ref.Kind, manager.GetNamespace(), ref.Name) == id &&
			(manager.GetNamespace() == "" || manager.GetNamespace() == obj.GetNamespace()) {
			return composedBy
		}
	}

	return notManaged
}

// boundToClaim reports whether xr is the XR bound to claim. Either side of the binding is enough, read
// from the raw input and from the cluster copy its own diff carries: the claim's spec.resourceRef (which
// only claims have; XRs have resourceRefs) naming the XR, or the XR's spec.claimRef, or the
// claim-name/claim-namespace labels Crossplane stamps on it, naming the claim.
func boundToClaim(xrGroup dt.XRDiffGroup, xr *un.Unstructured, claimGroup dt.XRDiffGroup, claim *un.Unstructured) bool {
	// A claim with no name yet binds nothing. Without this, an XR with no claimRef — whose reference
	// identity is also "" — would match it.
	if identityOf(claim) == "" {
		return false
	}

	for _, obj := range knownForms(claimGroup, claim) {
		if refIdentity(obj, "spec", "resourceRef") == identityOf(xr) {
			return true
		}
	}

	for _, obj := range knownForms(xrGroup, xr) {
		if refIdentity(obj, "spec", "claimRef") == identityOf(claim) {
			return true
		}

		labels := obj.GetLabels()
		if name, ok := labels[LabelClaimName]; ok && name == claim.GetName() && labels[LabelClaimNamespace] == claim.GetNamespace() {
			return true
		}
	}

	return false
}

// knownForms returns what is known of an input's object: the input as given and, from its own diff —
// which the calculator stores even when equal — its cluster copy and rendered form.
func knownForms(g dt.XRDiffGroup, res *un.Unstructured) []*un.Unstructured {
	forms := []*un.Unstructured{res}

	if own := g.Diffs[dt.MakeDiffKeyFromResource(res)]; own != nil {
		for _, obj := range []*un.Unstructured{own.Current.Raw, own.Desired.Raw} {
			if obj != nil {
				forms = append(forms, obj)
			}
		}
	}

	return forms
}

// renderOverlapErrors reports the resources produced by more than one input's render that no single
// diff can truthfully represent, most-significant cause first per resource and ordered by identity.
//
// Resources are matched on their version-independent identity (group, kind, namespace, name), not on the
// diff key: served versions are views of one stored object, so one object rendered at two API versions
// produces two keys yet is still contested. Renderings at different versions are never identical, so such
// an overlap lands on contention or disagreement.
//
// A diff key carries no owning-input component (see dt.MakeDiffKey), so any view that merges the groups
// into one map — the deprecated flat changes[] — keeps only one entry per key. Whether that loses
// anything is decided by who would control the resource, read from the rendered (or, for a removal,
// current) object's controller reference and compared by group, kind and name — never UID, which a
// render synthesizes afresh for an XR that does not exist yet:
//
//   - Different controllers: contention. Crossplane's server-side apply refuses a second controller
//     reference, so the first XR to create the object keeps it and every other fails to reconcile it.
//     No diff predicts that, however alike the renderings.
//   - One controller or none, identical renderings (diff type and Clean views, which cleanupForDiff has
//     stripped of ownerReferences and uid): one change reached twice. Merged, not reported.
//   - One controller or none, differing renderings: no single diff is right for both. Defensive — the
//     ordinary causes are input errors, caught by the earlier stages.
//
// A key whose every entry is DiffTypeEqual is not reported: equal diffs appear in no rendered view.
func renderOverlapErrors(groups []dt.XRDiffGroup) []error {
	byID := make(map[string][]overlapProducer) // identity -> its renderings, in input order

	for i, g := range groups {
		keys := make([]string, 0, len(g.Diffs))
		for key := range g.Diffs {
			keys = append(keys, key)
		}

		sort.Strings(keys) // one input may render an identity under two keys; order them stably

		for _, key := range keys {
			id := diffKeyIdentity(key)
			if id == "" {
				id = key // not a parseable key; fall back to comparing it exactly
			}

			byID[id] = append(byID[id], overlapProducer{group: i, key: key})
		}
	}

	ids := make([]string, 0, len(byID))
	for id, ps := range byID {
		if len(ps) > 1 {
			ids = append(ids, id)
		}
	}

	sort.Strings(ids)

	var errs []error

	for _, id := range ids {
		if err := renderOverlapError(groups, byID[id]); err != nil {
			errs = append(errs, err)
		}
	}

	return errs
}

// withUnchanged returns groups with each one's unchanged renderings, indexed like groups (see
// dtypes.RenderedResources.Unchanged), added to a copy of its diffs. It is what the overlap check is
// given: those renderings are left out of the report, but two XRs rendering one resource contend for
// it whether or not either rendering changes it. An errored group (nil diffs) gets none.
func withUnchanged(groups []dt.XRDiffGroup, unchanged []map[string]*dt.ResourceDiff) []dt.XRDiffGroup {
	out := make([]dt.XRDiffGroup, 0, len(groups))

	for i, g := range groups {
		if g.Diffs != nil && i < len(unchanged) && len(unchanged[i]) > 0 {
			diffs := maps.Clone(unchanged[i])
			maps.Copy(diffs, g.Diffs)
			g.Diffs = diffs
		}

		out = append(out, g)
	}

	return out
}

// overlapProducer is one rendering of an overlapping resource: the input group that produced it and the
// diff key it produced it under. One object reached at two API versions has two keys.
type overlapProducer struct {
	group int
	key   string
}

func renderOverlapError(groups []dt.XRDiffGroup, producers []overlapProducer) error {
	inputs := make([]string, 0, len(producers))

	var (
		controllers    []string
		allEqual       = true
		identical      = true
		seenController = make(map[string]bool)
		seenGroup      = make(map[int]bool)
		keys           []string
		seenKey        = make(map[string]bool)
		first          = groups[producers[0].group].Diffs[producers[0].key]
	)

	for _, p := range producers {
		g, d := groups[p.group], groups[p.group].Diffs[p.key]

		if !seenKey[p.key] {
			seenKey[p.key] = true
			keys = append(keys, p.key)
		}

		// The same input may reach the object again at another version. Its rendering still counts, but it
		// is neither a second input nor a second placeholder.
		if !seenGroup[p.group] {
			seenGroup[p.group] = true
			inputs = append(inputs, fmt.Sprintf("%s/%s", g.XR.Kind, g.XR.Name))
		}

		// A nil entry is not expected; treat it as observable and as matching nothing, so a malformed
		// group errors loudly rather than being merged away.
		if d == nil || d.DiffType != dt.DiffTypeEqual {
			allEqual = false
		}

		if !SameRendering(first, d) {
			identical = false
		}

		if id, display := controllerOf(d); id != "" && !seenController[id] {
			seenController[id] = true
			controllers = append(controllers, display)
		}
	}

	sort.Strings(controllers)

	key := fmt.Sprintf("%q", keys[0])
	if len(keys) > 1 {
		others := make([]string, 0, len(keys)-1)
		for _, k := range keys[1:] {
			others = append(others, fmt.Sprintf("%q", k))
		}

		key += " (also rendered as " + strings.Join(others, ", ") + ")"
	}

	switch {
	case allEqual:
		return nil
	case len(controllers) > 1:
		return errors.Errorf("cannot combine diffs: resource %s would be controlled by more than one XR (%s); "+
			"Crossplane gives it to whichever of them creates it first, and the other fails to reconcile it, "+
			"so no diff predicts applying these inputs together — diff them separately", key, strings.Join(controllers, ", "))
	case identical:
		return nil
	default:
		return errors.Errorf("cannot combine diffs: inputs %s both produce resource %s, but differently, so no "+
			"single diff is correct for both — diff them separately", strings.Join(inputs, ", "), key)
	}
}

// controllerOf returns the identity of the XR that would control the diffed resource, as a comparison
// key (group/kind/name) and a display form (Kind/Name), or empty strings if nothing controls it. It
// reads the rendered object, falling back to the current one for a removal. The UID is deliberately
// ignored; see renderOverlapErrors.
func controllerOf(d *dt.ResourceDiff) (string, string) {
	if d == nil {
		return "", ""
	}

	obj := d.Desired.Raw
	if obj == nil {
		obj = d.Current.Raw
	}

	if obj == nil {
		return "", ""
	}

	ref := metav1.GetControllerOf(obj)
	if ref == nil {
		return "", ""
	}

	return identity(ref.APIVersion, ref.Kind, "", ref.Name), fmt.Sprintf("%s/%s", ref.Kind, ref.Name)
}

// SameRendering reports whether two diffs describe the same change: the same diff type, and the same
// cleaned desired and current objects. A nil diff matches nothing.
func SameRendering(a, b *dt.ResourceDiff) bool {
	if a == nil || b == nil {
		return false
	}

	return a.DiffType == b.DiffType &&
		sameObject(a.Desired.Clean, b.Desired.Clean) &&
		sameObject(a.Current.Clean, b.Current.Clean)
}

func sameObject(a, b *un.Unstructured) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}

	return equality.Semantic.DeepEqual(a.Object, b.Object)
}

// identity is an object's version-independent identity; "" for an object with no name yet.
func identity(apiVersion, kind, namespace, name string) string {
	if name == "" || kind == "" {
		return ""
	}

	group := ""
	if gv, err := schema.ParseGroupVersion(apiVersion); err == nil {
		group = gv.Group
	}

	return group + "/" + kind + "/" + namespace + "/" + name
}

func identityOf(res *un.Unstructured) string {
	return identity(res.GetAPIVersion(), res.GetKind(), res.GetNamespace(), res.GetName())
}

// diffKeyIdentity is identity for a key built by dt.MakeDiffKey ("<apiVersion>/<kind>/<namespace>/<name>",
// where apiVersion may itself contain a slash).
func diffKeyIdentity(key string) string {
	parts := strings.Split(key, "/")
	n := len(parts)

	if n < 4 {
		return ""
	}

	return identity(strings.Join(parts[:n-3], "/"), parts[n-3], parts[n-2], parts[n-1])
}

// refIdentity is the identity of the object reference (apiVersion, kind, name, and optionally namespace)
// at path, or "" if there is none.
func refIdentity(obj *un.Unstructured, path ...string) string {
	ref, found, err := un.NestedStringMap(obj.Object, path...)
	if err != nil || !found {
		return ""
	}

	return identity(ref["apiVersion"], ref["kind"], ref["namespace"], ref["name"])
}
