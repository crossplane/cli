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
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	un "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	xpextv1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1"

	"github.com/crossplane/cli/v2/internal/diff/testutils"
)

var errBoom = errors.New("boom")

// cmpInputError compares InputErrors by everything but their message.
var cmpInputError = cmp.Options{cmp.AllowUnexported(InputError{}), cmpopts.IgnoreFields(InputError{}, "msg")}

// rejected is the InputError for the --resource-file input of kind and name.
func rejected(kind, name string, why rejectKind) *InputError {
	return &InputError{Flag: FlagResourceFile, GVK: schema.GroupVersionKind{Group: "example.org", Version: "v1", Kind: kind}, Name: name, kind: why}
}

// splitRejected returns err as an InputError, or else err itself.
func splitRejected(err error) (*InputError, error) {
	if ie, ok := errors.AsType[*InputError](err); ok {
		return ie, nil
	}
	return nil, err
}

func TestAssignComposites(t *testing.T) {
	xr := schema.GroupVersionKind{Group: "example.org", Version: "v1", Kind: "XBucket"}
	// Claims of kind Bucket compose XBucket; XBucket inputs compose themselves.
	xrdFor := func(def, enforced string) func(*un.Unstructured) (xrdInfo, error) {
		return func(in *un.Unstructured) (xrdInfo, error) {
			if in.GetKind() == "Other" {
				return xrdInfo{}, errBoom
			}
			// Claims, and XRs of kind XLegacy (a v1 XRD), use legacy paths.
			return xrdInfo{CompositeGVK: xr, DefaultComposition: def, EnforcedComposition: enforced, Legacy: in.GetKind() != "XBucket"}, nil
		}
	}
	a := testutils.NewComposition("a").WithCompositeTypeRef("example.org/v1", "XBucket").WithLabels(map[string]string{"tier": "gold", "team": "x"}).BuildAsUnstructured()
	b := testutils.NewComposition("b").WithCompositeTypeRef("example.org/v1", "XBucket").WithLabels(map[string]string{"tier": "silver", "team": "x"}).BuildAsUnstructured()
	untiered := testutils.NewComposition("u").WithCompositeTypeRef("example.org/v1", "XBucket").WithLabels(map[string]string{"team": "y"}).BuildAsUnstructured()
	emptyTier := testutils.NewComposition("e").WithCompositeTypeRef("example.org/v1", "XBucket").WithLabels(map[string]string{"tier": ""}).BuildAsUnstructured()
	other := testutils.NewComposition("o").WithCompositeTypeRef("example.org/v1", "XOther").WithLabels(map[string]string{"tier": "gold"}).BuildAsUnstructured()

	type args struct {
		comps    []*un.Unstructured
		input    *un.Unstructured
		defalt   string
		enforced string
	}
	type want struct {
		comp     string
		rejected *InputError
		err      error
	}
	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"V2CompositionRef": {
			reason: "A v2 XR's spec.crossplane.compositionRef picks the Composition.",
			args:   args{comps: []*un.Unstructured{a, b}, input: testutils.NewResource("example.org/v1", "XBucket", "x").WithSpec(map[string]any{"crossplane": map[string]any{"compositionRef": map[string]any{"name": "b"}}}).Build()},
			want:   want{comp: "b"},
		},
		"V1ClaimCompositionRef": {
			reason: "A claim's spec.compositionRef picks the Composition.",
			args:   args{comps: []*un.Unstructured{a, b}, input: testutils.NewResource("example.org/v1", "Bucket", "c").WithSpec(map[string]any{"compositionRef": map[string]any{"name": "a"}}).Build()},
			want:   want{comp: "a"},
		},
		"V2PrefersCrossplanePath": {
			reason: "A v2 XR's spec.crossplane.compositionRef wins over spec.compositionRef.",
			args:   args{comps: []*un.Unstructured{a, b}, input: testutils.NewResource("example.org/v1", "XBucket", "x").WithSpec(map[string]any{"crossplane": map[string]any{"compositionRef": map[string]any{"name": "b"}}, "compositionRef": map[string]any{"name": "a"}}).Build()},
			want:   want{comp: "b"},
		},
		"V2FallsBackToSpecPath": {
			reason: "A v2 XR's spec.compositionRef is read when spec.crossplane has none, as the engine does.",
			args:   args{comps: []*un.Unstructured{a, b}, input: testutils.NewResource("example.org/v1", "XBucket", "x").WithSpec(map[string]any{"compositionRef": map[string]any{"name": "b"}}).Build()},
			want:   want{comp: "b"},
		},
		"LegacyIgnoresCrossplanePath": {
			reason: "A v1 XRD's XR reads spec.compositionRef only; spec.crossplane is not its field.",
			args:   args{comps: []*un.Unstructured{a, b}, input: testutils.NewResource("example.org/v1", "XLegacy", "x").WithSpec(map[string]any{"crossplane": map[string]any{"compositionRef": map[string]any{"name": "b"}}, "compositionRef": map[string]any{"name": "a"}}).Build()},
			want:   want{comp: "a"},
		},
		"ClaimIgnoresCrossplaneSelector": {
			reason: "A claim reads spec.compositionSelector only.",
			args:   args{comps: []*un.Unstructured{a, b}, input: testutils.NewResource("example.org/v1", "Bucket", "c").WithSpec(map[string]any{"crossplane": map[string]any{"compositionSelector": map[string]any{"matchLabels": map[string]any{"tier": "gold"}}}, "compositionSelector": map[string]any{"matchLabels": map[string]any{"tier": "silver"}}}).Build()},
			want:   want{comp: "b"},
		},
		"CompositionRefNotInProject": {
			reason: "A compositionRef to a Composition the project doesn't build, or of another type, fails.",
			args:   args{comps: []*un.Unstructured{a, other}, input: testutils.NewResource("example.org/v1", "XBucket", "x").WithSpec(map[string]any{"compositionRef": map[string]any{"name": "o"}}).Build()},
			want:   want{rejected: rejected("XBucket", "x", rejectNotInProject)},
		},
		"CompositionRefMalformed": {
			reason: "A compositionRef name of the wrong type fails rather than being ignored.",
			args:   args{comps: []*un.Unstructured{a}, input: testutils.NewResource("example.org/v1", "XBucket", "x").WithSpec(map[string]any{"compositionRef": map[string]any{"name": int64(3)}}).Build()},
			want:   want{rejected: rejected("XBucket", "x", rejectMalformed)},
		},
		"V2Selector": {
			reason: "A v2 compositionSelector matches labels among type-matching Compositions.",
			args:   args{comps: []*un.Unstructured{other, a, b}, input: testutils.NewResource("example.org/v1", "XBucket", "x").WithSpec(map[string]any{"crossplane": map[string]any{"compositionSelector": map[string]any{"matchLabels": map[string]any{"tier": "gold"}}}}).Build()},
			want:   want{comp: "a"},
		},
		"SelectorEmptyValueNeedsLabel": {
			reason: "An empty selector value matches no Composition lacking the label, as Kubernetes label selection requires the key.",
			args:   args{comps: []*un.Unstructured{a, b, untiered}, input: testutils.NewResource("example.org/v1", "XBucket", "x").WithSpec(map[string]any{"compositionSelector": map[string]any{"matchLabels": map[string]any{"tier": ""}}}).Build()},
			want:   want{rejected: rejected("XBucket", "x", rejectNoMatch)},
		},
		"SelectorEmptyValueMatchesEmptyLabel": {
			reason: "An empty selector value matches a Composition whose label is empty.",
			args:   args{comps: []*un.Unstructured{a, b, emptyTier}, input: testutils.NewResource("example.org/v1", "XBucket", "x").WithSpec(map[string]any{"compositionSelector": map[string]any{"matchLabels": map[string]any{"tier": ""}}}).Build()},
			want:   want{comp: "e"},
		},
		"V1ClaimSelector": {
			reason: "A claim's spec.compositionSelector matches labels.",
			args:   args{comps: []*un.Unstructured{a, b}, input: testutils.NewResource("example.org/v1", "Bucket", "c").WithSpec(map[string]any{"compositionSelector": map[string]any{"matchLabels": map[string]any{"tier": "silver"}}}).Build()},
			want:   want{comp: "b"},
		},
		"SelectorNoMatch": {
			reason: "A selector matching nothing fails, even if the XRD has a default.",
			args:   args{comps: []*un.Unstructured{a, b}, defalt: "a", input: testutils.NewResource("example.org/v1", "XBucket", "x").WithSpec(map[string]any{"compositionSelector": map[string]any{"matchLabels": map[string]any{"tier": "bronze"}}}).Build()},
			want:   want{rejected: rejected("XBucket", "x", rejectNoMatch)},
		},
		"SelectorAmbiguous": {
			reason: "A selector matching several Compositions fails naming them.",
			args:   args{comps: []*un.Unstructured{b, a}, input: testutils.NewResource("example.org/v1", "XBucket", "x").WithSpec(map[string]any{"compositionSelector": map[string]any{"matchLabels": map[string]any{"team": "x"}}}).Build()},
			want:   want{rejected: rejected("XBucket", "x", rejectAmbiguous)},
		},
		"EmptySelectorMatchesAll": {
			reason: "An empty matchLabels is still a selector, so Crossplane ignores the XRD default and it matches every Composition of the type: ambiguous.",
			args:   args{comps: []*un.Unstructured{a, b}, defalt: "b", input: testutils.NewResource("example.org/v1", "XBucket", "x").WithSpec(map[string]any{"compositionSelector": map[string]any{"matchLabels": map[string]any{}}}).Build()},
			want:   want{rejected: rejected("XBucket", "x", rejectAmbiguous)},
		},
		"SelectorLabelsMalformed": {
			reason: "A matchLabels that is not a map of strings fails rather than being ignored.",
			args:   args{comps: []*un.Unstructured{a}, input: testutils.NewResource("example.org/v1", "XBucket", "x").WithSpec(map[string]any{"compositionSelector": map[string]any{"matchLabels": "tier"}}).Build()},
			want:   want{rejected: rejected("XBucket", "x", rejectMalformed)},
		},
		"SelectorWithoutLabelsMatchesAll": {
			reason: "A compositionSelector with no matchLabels is still a selector, so the XRD default does not apply.",
			args:   args{comps: []*un.Unstructured{a, b}, defalt: "b", input: testutils.NewResource("example.org/v1", "XBucket", "x").WithSpec(map[string]any{"compositionSelector": map[string]any{}}).Build()},
			want:   want{rejected: rejected("XBucket", "x", rejectAmbiguous)},
		},
		"EnforcedCompositionRef": {
			reason: "The XRD's enforcedCompositionRef wins over the input's compositionRef, as in Crossplane.",
			args:   args{comps: []*un.Unstructured{a, b}, enforced: "b", input: testutils.NewResource("example.org/v1", "XBucket", "x").WithSpec(map[string]any{"compositionRef": map[string]any{"name": "a"}}).Build()},
			want:   want{comp: "b"},
		},
		"EnforcedNotInProject": {
			reason: "An enforcedCompositionRef the project doesn't build fails.",
			args:   args{comps: []*un.Unstructured{a}, enforced: "z", input: testutils.NewResource("example.org/v1", "XBucket", "x").Build()},
			want:   want{rejected: rejected("XBucket", "x", rejectNotInProject)},
		},
		"DefaultCompositionRef": {
			reason: "With no ref or selector, the XRD's defaultCompositionRef picks.",
			args:   args{comps: []*un.Unstructured{a, b}, defalt: "b", input: testutils.NewResource("example.org/v1", "Bucket", "c").Build()},
			want:   want{comp: "b"},
		},
		"DefaultNotInProject": {
			reason: "A defaultCompositionRef the project doesn't build fails.",
			args:   args{comps: []*un.Unstructured{a}, defalt: "z", input: testutils.NewResource("example.org/v1", "XBucket", "x").Build()},
			want:   want{rejected: rejected("XBucket", "x", rejectNotInProject)},
		},
		"SoleTypeMatch": {
			reason: "Otherwise the only Composition of the composite type picks; a claim matches via its XRD's composite.",
			args:   args{comps: []*un.Unstructured{other, a}, input: testutils.NewResource("example.org/v1", "Bucket", "c").Build()},
			want:   want{comp: "a"},
		},
		"NoTypeMatch": {
			reason: "No Composition of the type fails naming the input.",
			args:   args{comps: []*un.Unstructured{other}, input: testutils.NewResource("example.org/v1", "XBucket", "x").Build()},
			want:   want{rejected: rejected("XBucket", "x", rejectNoMatch)},
		},
		"TypeAmbiguous": {
			reason: "Several Compositions of the type fail asking for compositionRef.",
			args:   args{comps: []*un.Unstructured{a, b}, input: testutils.NewResource("example.org/v1", "XBucket", "x").Build()},
			want:   want{rejected: rejected("XBucket", "x", rejectAmbiguous)},
		},
		"LookupError": {
			reason: "An XRD lookup error is returned.",
			args:   args{comps: []*un.Unstructured{a}, input: testutils.NewResource("example.org/v1", "Other", "x").Build()},
			want:   want{err: errBoom},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := assignComposites(tc.args.comps, []*un.Unstructured{tc.args.input}, xrdFor(tc.args.defalt, tc.args.enforced))
			gotRejected, gotErr := splitRejected(err)
			if diff := cmp.Diff(tc.want.rejected, gotRejected, cmpInputError); diff != "" {
				t.Errorf("\n%s\nassignComposites(...): -want rejected input, +got rejected input:\n%s", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.err, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nassignComposites(...): -want err, +got err:\n%s", tc.reason, diff)
			}
			var want map[string][]*un.Unstructured
			if tc.want.rejected == nil && tc.want.err == nil {
				want = map[string][]*un.Unstructured{tc.want.comp: {tc.args.input}}
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("\n%s\nassignComposites(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestAssignInputs(t *testing.T) {
	// A v2 XRD is Namespaced; a v1 XRD has no scope, so it is LegacyCluster and its XRs get no namespace.
	xrd := func(apiVersion, def string, referenceable bool) *testutils.XRDBuilder {
		b := testutils.NewXRD("xbuckets.example.org", "example.org", "XBucket").
			WithAPIVersion(apiVersion).WithDefaultCompositionRef(def).WithVersion("v1", false, referenceable)
		if apiVersion == "apiextensions.crossplane.io/v2" {
			b = b.WithScope(xpextv1.CompositeResourceScopeNamespaced)
		}
		return b
	}
	gvk := func(kind string) schema.GroupVersionKind {
		return schema.GroupVersionKind{Group: "example.org", Version: "v1", Kind: kind}
	}
	a := testutils.NewComposition("a").WithCompositeTypeRef("example.org/v1", "XBucket").BuildAsUnstructured()
	b := testutils.NewComposition("b").WithCompositeTypeRef("example.org/v1", "XBucket").BuildAsUnstructured()

	type args struct {
		xrd    *un.Unstructured
		ns     string
		inputs []*un.Unstructured
	}
	type want struct {
		assigned map[string][]string // Composition -> namespace/name
		rejected *InputError
	}
	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"ClaimUsesCompositeKindAndDefault": {
			reason: "A claim is matched to Compositions of its XRD's composite kind, the XRD default picking one, and gets the context namespace.",
			args:   args{xrd: xrd("apiextensions.crossplane.io/v1", "b", true).BuildAsUnstructured(), inputs: []*un.Unstructured{testutils.NewResource("example.org/v1", "Bucket", "c").Build()}},
			want:   want{assigned: map[string][]string{"b": {"ctx-ns/c"}}},
		},
		"ClaimNoReferenceableVersion": {
			reason: "A claim whose XRD has no referenceable version fails, naming the input.",
			args:   args{xrd: xrd("apiextensions.crossplane.io/v1", "b", false).BuildAsUnstructured(), inputs: []*un.Unstructured{testutils.NewResource("example.org/v1", "Bucket", "c").Build()}},
			want:   want{rejected: rejected("Bucket", "c", rejectBadXRD)},
		},
		"ClaimFirstReferenceableVersion": {
			reason: "A claim composes its XRD's first referenceable version, as xr diff resolves it.",
			args:   args{xrd: xrd("apiextensions.crossplane.io/v1", "b", true).WithVersion("v2", false, true).BuildAsUnstructured(), inputs: []*un.Unstructured{testutils.NewResource("example.org/v1", "Bucket", "c").Build()}},
			want:   want{assigned: map[string][]string{"b": {"ctx-ns/c"}}},
		},
		"ClaimXRDMissingKind": {
			reason: "A claim whose XRD names no composite kind fails rather than matching an empty kind.",
			args: args{xrd: func() *un.Unstructured {
				x := xrd("apiextensions.crossplane.io/v1", "b", true).BuildAsUnstructured()
				un.RemoveNestedField(x.Object, "spec", "names", "kind")
				return x
			}(), inputs: []*un.Unstructured{testutils.NewResource("example.org/v1", "Bucket", "c").Build()}},
			want: want{rejected: rejected("Bucket", "c", rejectBadXRD)},
		},
		"V1XRDReadsSpecPathOnly": {
			reason: "An XR of a v1 XRD ignores spec.crossplane.compositionRef, so the XRD default applies.",
			args:   args{xrd: xrd("apiextensions.crossplane.io/v1", "b", true).BuildAsUnstructured(), inputs: []*un.Unstructured{testutils.NewResource("example.org/v1", "XBucket", "x").WithSpec(map[string]any{"crossplane": map[string]any{"compositionRef": map[string]any{"name": "a"}}}).Build()}},
			want:   want{assigned: map[string][]string{"b": {"/x"}}},
		},
		"V2LegacyClusterXRDReadsSpecPathOnly": {
			reason: "An XR of a LegacyCluster XRD read through the v2 API ignores spec.crossplane.compositionRef, as rendering does.",
			args:   args{xrd: xrd("apiextensions.crossplane.io/v2", "b", true).WithScope(xpextv1.CompositeResourceScopeLegacyCluster).BuildAsUnstructured(), inputs: []*un.Unstructured{testutils.NewResource("example.org/v1", "XBucket", "x").WithSpec(map[string]any{"crossplane": map[string]any{"compositionRef": map[string]any{"name": "a"}}}).Build()}},
			want:   want{assigned: map[string][]string{"b": {"/x"}}},
		},
		"V2XRDReadsCrossplanePath": {
			reason: "An XR of a v2 XRD reads spec.crossplane.compositionRef.",
			args:   args{xrd: xrd("apiextensions.crossplane.io/v2", "b", true).BuildAsUnstructured(), inputs: []*un.Unstructured{testutils.NewResource("example.org/v1", "XBucket", "x").WithSpec(map[string]any{"crossplane": map[string]any{"compositionRef": map[string]any{"name": "a"}}}).Build()}},
			want:   want{assigned: map[string][]string{"a": {"ctx-ns/x"}}},
		},
		"NamespaceFlagDefaults": {
			reason: "--namespace, when set, is the namespace an input that sets none gets.",
			args:   args{xrd: xrd("apiextensions.crossplane.io/v2", "b", true).BuildAsUnstructured(), ns: "team-a", inputs: []*un.Unstructured{testutils.NewResource("example.org/v1", "XBucket", "x").Build()}},
			want:   want{assigned: map[string][]string{"b": {"team-a/x"}}},
		},
		"DuplicateIdentity": {
			reason: "Two inputs of one group, kind, namespace and name fail rather than one silently winning.",
			args:   args{xrd: xrd("apiextensions.crossplane.io/v2", "b", true).BuildAsUnstructured(), inputs: []*un.Unstructured{testutils.NewResource("example.org/v1", "XBucket", "x").Build(), testutils.NewResource("example.org/v1", "XBucket", "x").Build()}},
			want:   want{rejected: rejected("XBucket", "x", rejectDuplicate)},
		},
		"SameNameOtherNamespace": {
			reason: "Inputs that differ only in namespace are distinct.",
			args:   args{xrd: xrd("apiextensions.crossplane.io/v2", "b", true).BuildAsUnstructured(), inputs: []*un.Unstructured{testutils.NewResource("example.org/v1", "XBucket", "x").Build(), testutils.NewResource("example.org/v1", "XBucket", "x").WithNamespace("other").Build()}},
			want:   want{assigned: map[string][]string{"b": {"ctx-ns/x", "other/x"}}},
		},
		"GenerateNameOnlyDistinct": {
			reason: "Inputs with only a generateName name no composite, so two of one kind and namespace are both accepted.",
			args:   args{xrd: xrd("apiextensions.crossplane.io/v2", "b", true).BuildAsUnstructured(), inputs: []*un.Unstructured{testutils.NewResource("example.org/v1", "XBucket", "").WithGenerateName("x-").Build(), testutils.NewResource("example.org/v1", "XBucket", "").WithGenerateName("x-").Build()}},
			want:   want{assigned: map[string][]string{"b": {"ctx-ns/", "ctx-ns/"}}},
		},
		"V1XRDEnforcedComposition": {
			reason: "A v1 XRD's spec.enforcedCompositionRef is read, and wins over the input's compositionRef.",
			args:   args{xrd: xrd("apiextensions.crossplane.io/v1", "b", true).WithEnforcedCompositionRef("b").BuildAsUnstructured(), inputs: []*un.Unstructured{testutils.NewResource("example.org/v1", "XBucket", "x").WithSpec(map[string]any{"compositionRef": map[string]any{"name": "a"}}).Build()}},
			want:   want{assigned: map[string][]string{"b": {"/x"}}},
		},
		"V2XRDEnforcedComposition": {
			reason: "A v2 XRD's spec.enforcedCompositionRef is read, and wins over the input's compositionRef.",
			args:   args{xrd: xrd("apiextensions.crossplane.io/v2", "b", true).WithEnforcedCompositionRef("b").BuildAsUnstructured(), inputs: []*un.Unstructured{testutils.NewResource("example.org/v1", "XBucket", "x").WithSpec(map[string]any{"crossplane": map[string]any{"compositionRef": map[string]any{"name": "a"}}}).Build()}},
			want:   want{assigned: map[string][]string{"b": {"ctx-ns/x"}}},
		},
		"NoXRDFailsFast": {
			reason: "An input no cluster XRD defines fails the check, worded for --resource-file.",
			args:   args{xrd: xrd("apiextensions.crossplane.io/v2", "", true).BuildAsUnstructured(), inputs: []*un.Unstructured{testutils.NewResource("example.org/v1", "Other", "o").Build()}},
			want:   want{rejected: rejected("Other", "o", rejectNoXRD)},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			builder := testutils.NewMockDefinitionClient()
			if tc.args.xrd != nil {
				builder = builder.WithXRDForGVK(gvk("XBucket"), tc.args.xrd).WithXRDForClaimGVK(gvk("Bucket"), tc.args.xrd)
			}
			defs := builder.Build()
			into := map[string][]*un.Unstructured{}
			err := assignInputs(context.Background(), defs, cmpstd.Or(tc.args.ns, "ctx-ns"), []*un.Unstructured{a, b}, tc.args.inputs, into)
			gotRejected, gotErr := splitRejected(err)
			if gotErr != nil {
				t.Errorf("\n%s\nassignInputs(...): unexpected error: %v", tc.reason, gotErr)
			}
			if diff := cmp.Diff(tc.want.rejected, gotRejected, cmpInputError); diff != "" {
				t.Errorf("\n%s\nassignInputs(...): -want rejected input, +got rejected input:\n%s", tc.reason, diff)
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

func TestSelectCompositionNotInProjectMessage(t *testing.T) {
	xr := schema.GroupVersionKind{Group: "example.org", Version: "v1", Kind: "XBucket"}
	b := testutils.NewComposition("b").WithCompositeTypeRef("example.org/v1", "XBucket").BuildAsUnstructured()
	a := testutils.NewComposition("a").WithCompositeTypeRef("example.org/v1", "XBucket").BuildAsUnstructured()
	in := testutils.NewResource("example.org/v1", "XBucket", "x").WithSpec(map[string]any{"compositionRef": map[string]any{"name": "z"}}).Build()

	type args struct {
		comps []*un.Unstructured
		xrd   xrdInfo
	}
	type want struct {
		rejected *InputError
		// fix is the clause saying which reference to correct, and to what.
		fix string
	}
	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"InputRef": {
			reason: "An input's compositionRef names the candidates to correct it to.",
			args:   args{comps: []*un.Unstructured{b, a}, xrd: xrdInfo{CompositeGVK: xr}},
			want:   want{rejected: rejected("XBucket", "x", rejectNotInProject), fix: "; correct its compositionRef to one of a, b"},
		},
		"EnforcedRef": {
			reason: "An XRD's enforcedCompositionRef says the XRD holds the reference.",
			args:   args{comps: []*un.Unstructured{a}, xrd: xrdInfo{CompositeGVK: xr, EnforcedComposition: "z"}},
			want:   want{rejected: rejected("XBucket", "x", rejectNotInProject), fix: "; correct the XRD's enforcedCompositionRef to one of a"},
		},
		"NoCandidates": {
			reason: "With no Composition of the type there are no candidates to name.",
			args:   args{xrd: xrdInfo{CompositeGVK: xr, DefaultComposition: "z"}},
			want:   want{rejected: rejected("XBucket", "x", rejectNotInProject), fix: "; correct its compositionRef"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := selectComposition(tc.args.comps, in, tc.args.xrd)
			got, other := splitRejected(err)
			if other != nil {
				t.Fatalf("\n%s\nselectComposition(...): want an InputError, got %v", tc.reason, other)
			}
			if diff := cmp.Diff(tc.want.rejected, got, cmpInputError); diff != "" {
				t.Errorf("\n%s\nselectComposition(...): -want error, +got error:\n%s", tc.reason, diff)
			}
			if got != nil && !strings.HasSuffix(got.Error(), tc.want.fix) {
				t.Errorf("\n%s\nselectComposition(...): want error ending %q, got %q", tc.reason, tc.want.fix, got.Error())
			}
		})
	}
}
