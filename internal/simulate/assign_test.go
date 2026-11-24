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

package simulate

import (
	cmpstd "cmp"
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	un "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/crossplane/cli/v2/internal/diff/testutils"
)

func testComp(name, kind string, labels map[string]string) *un.Unstructured {
	c := &un.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.crossplane.io/v1",
		"kind":       "Composition",
		"metadata":   map[string]any{"name": name},
		"spec": map[string]any{"compositeTypeRef": map[string]any{
			"apiVersion": "example.org/v1", "kind": kind,
		}},
	}}
	c.SetLabels(labels)
	return c
}

func testInput(kind, name string, spec map[string]any) *un.Unstructured {
	return &un.Unstructured{Object: map[string]any{
		"apiVersion": "example.org/v1", "kind": kind,
		"metadata": map[string]any{"name": name},
		"spec":     spec,
	}}
}

func TestAssignComposites(t *testing.T) {
	xr := schema.GroupVersionKind{Group: "example.org", Version: "v1", Kind: "XBucket"}
	// Claims of kind Bucket compose XBucket; XBucket inputs compose themselves.
	xrdFor := func(def, enforced string) func(*un.Unstructured) (xrdInfo, error) {
		return func(in *un.Unstructured) (xrdInfo, error) {
			if in.GetKind() == "Other" {
				return xrdInfo{}, errors.New("boom")
			}
			// Claims, and XRs of kind XLegacy (a v1 XRD), use legacy paths.
			return xrdInfo{CompositeGVK: xr, DefaultComposition: def, EnforcedComposition: enforced, Legacy: in.GetKind() != "XBucket"}, nil
		}
	}
	a := testComp("a", "XBucket", map[string]string{"tier": "gold", "team": "x"})
	b := testComp("b", "XBucket", map[string]string{"tier": "silver", "team": "x"})
	other := testComp("o", "XOther", map[string]string{"tier": "gold"})

	type args struct {
		comps    []*un.Unstructured
		input    *un.Unstructured
		defalt   string
		enforced string
	}
	type want struct {
		comp string
		err  string
	}
	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"V2CompositionRef": {
			reason: "A v2 XR's spec.crossplane.compositionRef picks the Composition.",
			args:   args{comps: []*un.Unstructured{a, b}, input: testInput("XBucket", "x", map[string]any{"crossplane": map[string]any{"compositionRef": map[string]any{"name": "b"}}})},
			want:   want{comp: "b"},
		},
		"V1ClaimCompositionRef": {
			reason: "A claim's spec.compositionRef picks the Composition.",
			args:   args{comps: []*un.Unstructured{a, b}, input: testInput("Bucket", "c", map[string]any{"compositionRef": map[string]any{"name": "a"}})},
			want:   want{comp: "a"},
		},
		"V2PrefersCrossplanePath": {
			reason: "A v2 XR's spec.crossplane.compositionRef wins over spec.compositionRef.",
			args:   args{comps: []*un.Unstructured{a, b}, input: testInput("XBucket", "x", map[string]any{"crossplane": map[string]any{"compositionRef": map[string]any{"name": "b"}}, "compositionRef": map[string]any{"name": "a"}})},
			want:   want{comp: "b"},
		},
		"V2FallsBackToSpecPath": {
			reason: "A v2 XR's spec.compositionRef is read when spec.crossplane has none, as the engine does.",
			args:   args{comps: []*un.Unstructured{a, b}, input: testInput("XBucket", "x", map[string]any{"compositionRef": map[string]any{"name": "b"}})},
			want:   want{comp: "b"},
		},
		"LegacyIgnoresCrossplanePath": {
			reason: "A v1 XRD's XR reads spec.compositionRef only; spec.crossplane is not its field.",
			args:   args{comps: []*un.Unstructured{a, b}, input: testInput("XLegacy", "x", map[string]any{"crossplane": map[string]any{"compositionRef": map[string]any{"name": "b"}}, "compositionRef": map[string]any{"name": "a"}})},
			want:   want{comp: "a"},
		},
		"ClaimIgnoresCrossplaneSelector": {
			reason: "A claim reads spec.compositionSelector only.",
			args:   args{comps: []*un.Unstructured{a, b}, input: testInput("Bucket", "c", map[string]any{"crossplane": map[string]any{"compositionSelector": map[string]any{"matchLabels": map[string]any{"tier": "gold"}}}, "compositionSelector": map[string]any{"matchLabels": map[string]any{"tier": "silver"}}})},
			want:   want{comp: "b"},
		},
		"CompositionRefNotInProject": {
			reason: "A compositionRef to a Composition the project doesn't build, or of another type, fails.",
			args:   args{comps: []*un.Unstructured{a, other}, input: testInput("XBucket", "x", map[string]any{"compositionRef": map[string]any{"name": "o"}})},
			want:   want{err: `--resource-file inputs include XBucket "x" (example.org/v1), whose compositionRef "o" is not a project Composition composing XBucket`},
		},
		"CompositionRefMalformed": {
			reason: "A compositionRef name of the wrong type fails rather than being ignored.",
			args:   args{comps: []*un.Unstructured{a}, input: testInput("XBucket", "x", map[string]any{"compositionRef": map[string]any{"name": 3}})},
			want:   want{err: `--resource-file inputs include XBucket "x" (example.org/v1), whose spec.compositionRef.name is malformed: .spec.compositionRef.name accessor error: 3 is of the type int, expected string`},
		},
		"V2Selector": {
			reason: "A v2 compositionSelector matches labels among type-matching Compositions.",
			args:   args{comps: []*un.Unstructured{other, a, b}, input: testInput("XBucket", "x", map[string]any{"crossplane": map[string]any{"compositionSelector": map[string]any{"matchLabels": map[string]any{"tier": "gold"}}}})},
			want:   want{comp: "a"},
		},
		"V1ClaimSelector": {
			reason: "A claim's spec.compositionSelector matches labels.",
			args:   args{comps: []*un.Unstructured{a, b}, input: testInput("Bucket", "c", map[string]any{"compositionSelector": map[string]any{"matchLabels": map[string]any{"tier": "silver"}}})},
			want:   want{comp: "b"},
		},
		"SelectorNoMatch": {
			reason: "A selector matching nothing fails, even if the XRD has a default.",
			args:   args{comps: []*un.Unstructured{a, b}, defalt: "a", input: testInput("XBucket", "x", map[string]any{"compositionSelector": map[string]any{"matchLabels": map[string]any{"tier": "bronze"}}})},
			want:   want{err: `--resource-file inputs include XBucket "x" (example.org/v1), whose compositionSelector matches no project Composition composing XBucket`},
		},
		"SelectorAmbiguous": {
			reason: "A selector matching several Compositions fails naming them.",
			args:   args{comps: []*un.Unstructured{b, a}, input: testInput("XBucket", "x", map[string]any{"compositionSelector": map[string]any{"matchLabels": map[string]any{"team": "x"}}})},
			want:   want{err: `--resource-file inputs include XBucket "x" (example.org/v1), which matches several project Compositions (a, b); set its compositionRef to pick one`},
		},
		"EmptySelectorFallsThrough": {
			reason: "An empty matchLabels selects nothing, so the XRD default picks.",
			args:   args{comps: []*un.Unstructured{a, b}, defalt: "b", input: testInput("XBucket", "x", map[string]any{"compositionSelector": map[string]any{"matchLabels": map[string]any{}}})},
			want:   want{comp: "b"},
		},
		"EnforcedCompositionRef": {
			reason: "The XRD's enforcedCompositionRef wins over the input's compositionRef, as in Crossplane.",
			args:   args{comps: []*un.Unstructured{a, b}, enforced: "b", input: testInput("XBucket", "x", map[string]any{"compositionRef": map[string]any{"name": "a"}})},
			want:   want{comp: "b"},
		},
		"EnforcedNotInProject": {
			reason: "An enforcedCompositionRef the project doesn't build fails.",
			args:   args{comps: []*un.Unstructured{a}, enforced: "z", input: testInput("XBucket", "x", nil)},
			want:   want{err: `--resource-file inputs include XBucket "x" (example.org/v1), whose XRD enforcedCompositionRef "z" is not a project Composition composing XBucket`},
		},
		"DefaultCompositionRef": {
			reason: "With no ref or selector, the XRD's defaultCompositionRef picks.",
			args:   args{comps: []*un.Unstructured{a, b}, defalt: "b", input: testInput("Bucket", "c", map[string]any{})},
			want:   want{comp: "b"},
		},
		"DefaultNotInProject": {
			reason: "A defaultCompositionRef the project doesn't build fails.",
			args:   args{comps: []*un.Unstructured{a}, defalt: "z", input: testInput("XBucket", "x", map[string]any{})},
			want:   want{err: `--resource-file inputs include XBucket "x" (example.org/v1), whose XRD defaultCompositionRef "z" is not a project Composition composing XBucket`},
		},
		"SoleTypeMatch": {
			reason: "Otherwise the only Composition of the composite type picks; a claim matches via its XRD's composite.",
			args:   args{comps: []*un.Unstructured{other, a}, input: testInput("Bucket", "c", nil)},
			want:   want{comp: "a"},
		},
		"NoTypeMatch": {
			reason: "No Composition of the type fails naming the input.",
			args:   args{comps: []*un.Unstructured{other}, input: testInput("XBucket", "x", nil)},
			want:   want{err: `--resource-file inputs include XBucket "x" (example.org/v1), but no project Composition composes its type example.org/v1, Kind=XBucket`},
		},
		"TypeAmbiguous": {
			reason: "Several Compositions of the type fail asking for compositionRef.",
			args:   args{comps: []*un.Unstructured{a, b}, input: testInput("XBucket", "x", nil)},
			want:   want{err: `--resource-file inputs include XBucket "x" (example.org/v1), which matches several project Compositions (a, b); set its compositionRef to pick one`},
		},
		"LookupError": {
			reason: "An XRD lookup error is returned.",
			args:   args{comps: []*un.Unstructured{a}, input: testInput("Other", "x", nil)},
			want:   want{err: "boom"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := assignComposites(tc.args.comps, []*un.Unstructured{tc.args.input}, xrdFor(tc.args.defalt, tc.args.enforced))
			gotErr := ""
			if err != nil {
				gotErr = err.Error()
			}
			if diff := cmp.Diff(tc.want.err, gotErr); diff != "" {
				t.Errorf("\n%s\nassignComposites(...): -want err, +got err:\n%s", tc.reason, diff)
			}
			var want map[string][]*un.Unstructured
			if tc.want.err == "" {
				want = map[string][]*un.Unstructured{tc.want.comp: {tc.args.input}}
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("\n%s\nassignComposites(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestAssignInputs(t *testing.T) {
	xrd := func(apiVersion, def string, referenceable bool) *un.Unstructured {
		return &un.Unstructured{Object: map[string]any{
			"apiVersion": apiVersion, "kind": "CompositeResourceDefinition",
			"metadata": map[string]any{"name": "xbuckets.example.org"},
			"spec": map[string]any{
				"group": "example.org", "scope": "Namespaced",
				"names":                 map[string]any{"kind": "XBucket"},
				"defaultCompositionRef": map[string]any{"name": def},
				"versions":              []any{map[string]any{"name": "v1", "referenceable": referenceable}},
			},
		}}
	}
	gvk := func(kind string) schema.GroupVersionKind {
		return schema.GroupVersionKind{Group: "example.org", Version: "v1", Kind: kind}
	}
	enforced := func(x *un.Unstructured, name string) *un.Unstructured {
		_ = un.SetNestedField(x.Object, name, "spec", "enforcedCompositionRef", "name")
		return x
	}
	generateNamed := func(prefix string) *un.Unstructured {
		x := testInput("XBucket", "", nil)
		x.SetGenerateName(prefix)
		return x
	}
	a := testComp("a", "XBucket", nil)
	b := testComp("b", "XBucket", nil)

	type args struct {
		xrd    *un.Unstructured
		ns     string
		inputs []*un.Unstructured
	}
	type want struct {
		assigned map[string][]string // Composition -> namespace/name
		err      string
	}
	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"ClaimUsesCompositeKindAndDefault": {
			reason: "A claim is matched to Compositions of its XRD's composite kind, the XRD default picking one, and gets the context namespace.",
			args:   args{xrd: xrd("apiextensions.crossplane.io/v1", "b", true), inputs: []*un.Unstructured{testInput("Bucket", "c", nil)}},
			want:   want{assigned: map[string][]string{"b": {"ctx-ns/c"}}},
		},
		"ClaimNoReferenceableVersion": {
			reason: "A claim whose XRD has no referenceable version fails, naming the input.",
			args:   args{xrd: xrd("apiextensions.crossplane.io/v1", "b", false), inputs: []*un.Unstructured{testInput("Bucket", "c", nil)}},
			want:   want{err: `--resource-file inputs include Bucket "c" (example.org/v1), whose XRD "xbuckets.example.org" has no referenceable version`},
		},
		"V1XRDReadsSpecPathOnly": {
			reason: "An XR of a v1 XRD ignores spec.crossplane.compositionRef, so the XRD default applies.",
			args:   args{xrd: xrd("apiextensions.crossplane.io/v1", "b", true), inputs: []*un.Unstructured{testInput("XBucket", "x", map[string]any{"crossplane": map[string]any{"compositionRef": map[string]any{"name": "a"}}})}},
			want:   want{assigned: map[string][]string{"b": {"ctx-ns/x"}}},
		},
		"V2XRDReadsCrossplanePath": {
			reason: "An XR of a v2 XRD reads spec.crossplane.compositionRef.",
			args:   args{xrd: xrd("apiextensions.crossplane.io/v2", "b", true), inputs: []*un.Unstructured{testInput("XBucket", "x", map[string]any{"crossplane": map[string]any{"compositionRef": map[string]any{"name": "a"}}})}},
			want:   want{assigned: map[string][]string{"a": {"ctx-ns/x"}}},
		},
		"NamespaceFlagDefaults": {
			reason: "--namespace, when set, is the namespace an input that sets none gets.",
			args:   args{xrd: xrd("apiextensions.crossplane.io/v2", "b", true), ns: "team-a", inputs: []*un.Unstructured{testInput("XBucket", "x", nil)}},
			want:   want{assigned: map[string][]string{"b": {"team-a/x"}}},
		},
		"DuplicateIdentity": {
			reason: "Two inputs of one group, kind, namespace and name fail rather than one silently winning.",
			args:   args{xrd: xrd("apiextensions.crossplane.io/v2", "b", true), inputs: []*un.Unstructured{testInput("XBucket", "x", nil), testInput("XBucket", "x", nil)}},
			want:   want{err: `--resource-file inputs include XBucket "x" (example.org/v1), which another --resource-file input also names; pass each composite once`},
		},
		"SameNameOtherNamespace": {
			reason: "Inputs that differ only in namespace are distinct.",
			args: args{xrd: xrd("apiextensions.crossplane.io/v2", "b", true), inputs: []*un.Unstructured{testInput("XBucket", "x", nil), func() *un.Unstructured {
				x := testInput("XBucket", "x", nil)
				x.SetNamespace("other")
				return x
			}()}},
			want: want{assigned: map[string][]string{"b": {"ctx-ns/x", "other/x"}}},
		},
		"GenerateNameOnlyDistinct": {
			reason: "Inputs with only a generateName name no composite, so two of one kind and namespace are both accepted.",
			args:   args{xrd: xrd("apiextensions.crossplane.io/v2", "b", true), inputs: []*un.Unstructured{generateNamed("x-"), generateNamed("x-")}},
			want:   want{assigned: map[string][]string{"b": {"ctx-ns/", "ctx-ns/"}}},
		},
		"V1XRDEnforcedComposition": {
			reason: "A v1 XRD's spec.enforcedCompositionRef is read, and wins over the input's compositionRef.",
			args:   args{xrd: enforced(xrd("apiextensions.crossplane.io/v1", "b", true), "b"), inputs: []*un.Unstructured{testInput("XBucket", "x", map[string]any{"compositionRef": map[string]any{"name": "a"}})}},
			want:   want{assigned: map[string][]string{"b": {"ctx-ns/x"}}},
		},
		"V2XRDEnforcedComposition": {
			reason: "A v2 XRD's spec.enforcedCompositionRef is read, and wins over the input's compositionRef.",
			args:   args{xrd: enforced(xrd("apiextensions.crossplane.io/v2", "b", true), "b"), inputs: []*un.Unstructured{testInput("XBucket", "x", map[string]any{"crossplane": map[string]any{"compositionRef": map[string]any{"name": "a"}}})}},
			want:   want{assigned: map[string][]string{"b": {"ctx-ns/x"}}},
		},
		"NoXRDFailsFast": {
			reason: "An input no cluster XRD defines fails the check, worded for --resource-file.",
			args:   args{xrd: xrd("apiextensions.crossplane.io/v2", "", true), inputs: []*un.Unstructured{testInput("Other", "o", nil)}},
			want:   want{err: `--resource-file inputs include Other "o" (example.org/v1), which no XRD in the cluster defines as a composite resource or claim (if it is one, check the kubeconfig context, its apiVersion and that its XRD is installed)`},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			lookup := func(kind string) func(context.Context, schema.GroupVersionKind) (*un.Unstructured, error) {
				return func(_ context.Context, g schema.GroupVersionKind) (*un.Unstructured, error) {
					if g == gvk(kind) {
						return tc.args.xrd, nil
					}
					return nil, errors.New("no XRD found")
				}
			}
			defs := &testutils.MockDefinitionClient{GetXRDForXRFn: lookup("XBucket"), GetXRDForClaimFn: lookup("Bucket")}
			into := map[string][]*un.Unstructured{}
			err := assignInputs(context.Background(), defs, cmpstd.Or(tc.args.ns, "ctx-ns"), []*un.Unstructured{a, b}, tc.args.inputs, into)
			gotErr := ""
			if err != nil {
				gotErr = err.Error()
			}
			if diff := cmp.Diff(tc.want.err, gotErr); diff != "" {
				t.Errorf("\n%s\nassignInputs(...): -want err, +got err:\n%s", tc.reason, diff)
			}
			var got map[string][]string
			for comp, xrs := range into {
				for _, x := range xrs {
					if got == nil {
						got = map[string][]string{}
					}
					got[comp] = append(got[comp], x.GetNamespace()+"/"+x.GetName())
				}
			}
			if diff := cmp.Diff(tc.want.assigned, got); diff != "" {
				t.Errorf("\n%s\nassignInputs(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}
