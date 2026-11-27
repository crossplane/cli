package crossplane

import (
	"context"

	un "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The fields SelectComposition passes ByName, naming where the Composition name came from.
const (
	fieldEnforcedCompositionRef = "XRD enforcedCompositionRef"
	fieldCompositionRef         = "compositionRef"
	fieldDefaultCompositionRef  = "XRD defaultCompositionRef"
)

// CompositionLookup finds the Composition one of SelectComposition's steps picks, from the caller's
// own source of Compositions of the composite's type.
type CompositionLookup[C any] interface {
	// ByName returns the Composition called name. field names where the name came from: the XRD's
	// enforcedCompositionRef, the composite's compositionRef, or the XRD's defaultCompositionRef.
	ByName(ctx context.Context, name, field string) (C, error)
	// ByLabels returns the only Composition carrying every label sel selects, as LabelsMatch does.
	ByLabels(ctx context.Context, sel map[string]string) (C, error)
	// ByType returns the only Composition.
	ByType(ctx context.Context) (C, error)
	// Malformed returns the error for the composite's field, at subpath below it, being of the wrong
	// type; err is the read's error.
	Malformed(err error, field string, subpath ...string) error
}

// SelectionRefs are the Compositions an XRD names for its composites.
type SelectionRefs struct {
	// Enforced is spec.enforcedCompositionRef.name, if set.
	Enforced string
	// Default is spec.defaultCompositionRef.name, if set.
	Default string
}

// XRDSelectionRefs returns the Compositions xrd names for its composites. v1 and v2 XRDs both keep
// them under spec.
func XRDSelectionRefs(xrd *un.Unstructured) SelectionRefs {
	enforced, _, _ := un.NestedString(xrd.Object, "spec", "enforcedCompositionRef", "name")
	def, _, _ := un.NestedString(xrd.Object, "spec", "defaultCompositionRef", "name")

	return SelectionRefs{Enforced: enforced, Default: def}
}

// SelectComposition returns the Composition Crossplane would select for res, in Crossplane's order:
// the XRD's enforcedCompositionRef, res's compositionRef, its compositionSelector's matchLabels (if it
// selects any), the XRD's defaultCompositionRef, then the only Composition of the type. A
// compositionSelector that is present but selects no labels skips the default, as Crossplane's does:
// Crossplane then picks any Composition of the type at random, which can't be previewed, so the only
// Composition of the type is used and several fail through l.ByType. The first step that applies
// decides; its lookup's error is returned as is. res's fields are read at the paths xrdAPIVersion gives
// (see NestedCrossplaneValue), and one of the wrong type fails through l.Malformed before any later step
// is tried.
func SelectComposition[C any](ctx context.Context, res *un.Unstructured, xrdAPIVersion string, refs SelectionRefs, l CompositionLookup[C]) (C, error) {
	var zero C

	if refs.Enforced != "" {
		return l.ByName(ctx, refs.Enforced, fieldEnforcedCompositionRef)
	}

	ref, err := XRCompositionRef(res.Object, xrdAPIVersion)
	if err != nil {
		return zero, l.Malformed(err, "compositionRef", "name")
	}

	if ref != "" {
		return l.ByName(ctx, ref, fieldCompositionRef)
	}

	sel, _, err := NestedCrossplaneValue(res.Object, xrdAPIVersion, "an object", un.NestedStringMap, "compositionSelector", "matchLabels")
	if err != nil {
		return zero, l.Malformed(err, "compositionSelector", "matchLabels")
	}

	if len(sel) > 0 {
		return l.ByLabels(ctx, sel)
	}

	// Crossplane applies the default only to a composite with no selector at all.
	if s, found, _ := NestedCrossplaneValue(res.Object, xrdAPIVersion, "an object", un.NestedFieldNoCopy, "compositionSelector"); found && s != nil {
		return l.ByType(ctx)
	}

	if refs.Default != "" {
		return l.ByName(ctx, refs.Default, fieldDefaultCompositionRef)
	}

	return l.ByType(ctx)
}
