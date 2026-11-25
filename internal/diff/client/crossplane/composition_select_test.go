package crossplane

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	un "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

// recordingLookup records the steps SelectComposition asks it for, and picks the name each step
// is given, "selected" for a selector, or "typed" for the type.
type recordingLookup struct {
	calls []string
}

func (r *recordingLookup) ByName(_ context.Context, name, field string) (string, error) {
	r.calls = append(r.calls, fmt.Sprintf("ByName %s %s", field, name))
	return name, nil
}

func (r *recordingLookup) ByLabels(_ context.Context, sel map[string]string) (string, error) {
	r.calls = append(r.calls, fmt.Sprintf("ByLabels %v", sel))
	return "selected", nil
}

func (r *recordingLookup) ByType(context.Context) (string, error) {
	r.calls = append(r.calls, "ByType")
	return "typed", nil
}

func (r *recordingLookup) Malformed(_ error, path ...string) error {
	r.calls = append(r.calls, "Malformed "+strings.Join(path, "."))
	return errors.New("malformed " + strings.Join(path, "."))
}

// TestSelectComposition covers the order SelectComposition applies Crossplane's selection steps
// in, for the XR of a v1 XRD (fields under spec) and of a v2 XRD (fields under spec.crossplane).
func TestSelectComposition(t *testing.T) {
	ref := func(name any) map[string]any { return map[string]any{"name": name} }
	selector := func(labels map[string]any) map[string]any { return map[string]any{"matchLabels": labels} }

	type args struct {
		refs SelectionRefs
		// fields are the XR's Composition fields, under spec for v1 and spec.crossplane for v2.
		fields map[string]any
	}

	type want struct {
		calls []string
		comp  string
		err   string
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"EnforcedOverCompositionRef": {
			reason: "The XRD's enforcedCompositionRef wins over the XR's compositionRef and selector.",
			args: args{
				refs:   SelectionRefs{Enforced: "e", Default: "d"},
				fields: map[string]any{"compositionRef": ref("r"), "compositionSelector": selector(map[string]any{"tier": "gold"})},
			},
			want: want{calls: []string{"ByName XRD enforcedCompositionRef e"}, comp: "e"},
		},
		"EnforcedIgnoresMalformedCompositionRef": {
			reason: "An enforced Composition means the XR's fields are never read, so a malformed one does not fail.",
			args:   args{refs: SelectionRefs{Enforced: "e"}, fields: map[string]any{"compositionRef": ref(3)}},
			want:   want{calls: []string{"ByName XRD enforcedCompositionRef e"}, comp: "e"},
		},
		"CompositionRefOverSelector": {
			reason: "The XR's compositionRef wins over its selector and the XRD's default.",
			args: args{
				refs:   SelectionRefs{Default: "d"},
				fields: map[string]any{"compositionRef": ref("r"), "compositionSelector": selector(map[string]any{"tier": "gold"})},
			},
			want: want{calls: []string{"ByName compositionRef r"}, comp: "r"},
		},
		"EmptyCompositionRefFallsThrough": {
			reason: "An empty compositionRef name names no Composition, so the selector is used.",
			args: args{
				fields: map[string]any{"compositionRef": ref(""), "compositionSelector": selector(map[string]any{"tier": "gold"})},
			},
			want: want{calls: []string{"ByLabels map[tier:gold]"}, comp: "selected"},
		},
		"SelectorOverDefault": {
			reason: "The XR's compositionSelector wins over the XRD's default.",
			args:   args{refs: SelectionRefs{Default: "d"}, fields: map[string]any{"compositionSelector": selector(map[string]any{"tier": "gold"})}},
			want:   want{calls: []string{"ByLabels map[tier:gold]"}, comp: "selected"},
		},
		"EmptySelectorFallsThrough": {
			reason: "A selector with no labels selects nothing, so the XRD's default is used.",
			args:   args{refs: SelectionRefs{Default: "d"}, fields: map[string]any{"compositionSelector": selector(map[string]any{})}},
			want:   want{calls: []string{"ByName XRD defaultCompositionRef d"}, comp: "d"},
		},
		"DefaultOverType": {
			reason: "With neither a compositionRef nor a selector, the XRD's default wins over the type.",
			args:   args{refs: SelectionRefs{Default: "d"}},
			want:   want{calls: []string{"ByName XRD defaultCompositionRef d"}, comp: "d"},
		},
		"TypeLast": {
			reason: "With nothing else to go on, the Composition of the XR's type is used.",
			want:   want{calls: []string{"ByType"}, comp: "typed"},
		},
		"MalformedCompositionRefFails": {
			reason: "A compositionRef name that is not a string fails before the selector, default or type is tried.",
			args: args{
				refs:   SelectionRefs{Default: "d"},
				fields: map[string]any{"compositionRef": ref(3), "compositionSelector": selector(map[string]any{"tier": "gold"})},
			},
			want: want{calls: []string{"Malformed compositionRef.name"}, err: "malformed compositionRef.name"},
		},
		"MalformedSelectorFails": {
			reason: "A selector label that is not a string fails before the default or type is tried.",
			args:   args{refs: SelectionRefs{Default: "d"}, fields: map[string]any{"compositionSelector": selector(map[string]any{"tier": 3})}},
			want:   want{calls: []string{"Malformed compositionSelector.matchLabels"}, err: "malformed compositionSelector.matchLabels"},
		},
	}

	for name, tc := range cases {
		for _, apiVersion := range []string{CrossplaneAPIExtGroupV1, CrossplaneAPIExtGroupV2} {
			t.Run(name+"/"+apiVersion, func(t *testing.T) {
				spec := map[string]any{}
				if tc.args.fields != nil {
					spec = tc.args.fields
					if apiVersion == CrossplaneAPIExtGroupV2 {
						spec = map[string]any{"crossplane": tc.args.fields}
					}
				}

				res := &un.Unstructured{Object: map[string]any{
					"apiVersion": "example.org/v1", "kind": "XR1",
					"metadata": map[string]any{"name": "my-xr"},
					"spec":     spec,
				}}

				l := &recordingLookup{}
				got, err := SelectComposition[string](t.Context(), res, apiVersion, tc.args.refs, l)

				gotErr := ""
				if err != nil {
					gotErr = err.Error()
				}

				if diff := cmp.Diff(tc.want.err, gotErr); diff != "" {
					t.Errorf("\n%s\nSelectComposition(...): -want err, +got err:\n%s", tc.reason, diff)
				}

				if diff := cmp.Diff(tc.want.comp, got); diff != "" {
					t.Errorf("\n%s\nSelectComposition(...): -want composition, +got composition:\n%s", tc.reason, diff)
				}

				if diff := cmp.Diff(tc.want.calls, l.calls); diff != "" {
					t.Errorf("\n%s\nSelectComposition(...): -want lookups, +got lookups:\n%s", tc.reason, diff)
				}
			})
		}
	}
}

// TestXRDSelectionRefs covers reading the enforced and default Composition from v1 and v2 XRDs,
// which keep both under spec.
func TestXRDSelectionRefs(t *testing.T) {
	xrd := func(apiVersion string, spec map[string]any) *un.Unstructured {
		return &un.Unstructured{Object: map[string]any{"apiVersion": apiVersion, "kind": "CompositeResourceDefinition", "spec": spec}}
	}
	both := map[string]any{
		"enforcedCompositionRef": map[string]any{"name": "e"},
		"defaultCompositionRef":  map[string]any{"name": "d"},
	}

	cases := map[string]struct {
		reason string
		xrd    *un.Unstructured
		want   SelectionRefs
	}{
		"V1": {
			reason: "A v1 XRD's enforced and default Composition are read.",
			xrd:    xrd(CrossplaneAPIExtGroupV1, both),
			want:   SelectionRefs{Enforced: "e", Default: "d"},
		},
		"V2": {
			reason: "A v2 XRD's enforced and default Composition are read.",
			xrd:    xrd(CrossplaneAPIExtGroupV2, both),
			want:   SelectionRefs{Enforced: "e", Default: "d"},
		},
		"Neither": {
			reason: "An XRD with neither gives empty refs.",
			xrd:    xrd(CrossplaneAPIExtGroupV2, map[string]any{}),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, XRDSelectionRefs(tc.xrd)); diff != "" {
				t.Errorf("\n%s\nXRDSelectionRefs(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}
