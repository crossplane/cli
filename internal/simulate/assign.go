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
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	un "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	xpclient "github.com/crossplane/cli/v2/internal/diff/client/crossplane"
	"github.com/crossplane/cli/v2/internal/diff/result"
)

// assignInputs checks the --resource-file inputs against the cluster's XRDs
// (see checkComposites), which also gives namespaced inputs that set none ns,
// rejects two named inputs of one group, kind, namespace and name, then assigns each to one of comps (see
// assignComposites) into into, keyed by Composition name.
func assignInputs(ctx context.Context, defs xpclient.DefinitionClient, ns string, comps, inputs []*un.Unstructured, into map[string][]*un.Unstructured) error {
	if err := checkComposites(ctx, defs, FlagResourceFile, ns, inputs); err != nil {
		return err
	}
	seen := map[result.CompositeKey]bool{}
	for _, in := range inputs {
		// An input with only a generateName names no composite, so it never duplicates one.
		if in.GetName() == "" {
			continue
		}
		id := result.CompositeKeyOf(in)
		if seen[id] {
			return rejectInput(in, FlagResourceFile, "which another --resource-file input also names", "pass each composite once")
		}
		seen[id] = true
	}
	byComp, err := assignComposites(ctx, comps, inputs, func(in *un.Unstructured) (xrdInfo, error) {
		return clusterXRDInfo(ctx, defs, in)
	})
	if err != nil {
		return err
	}
	maps.Copy(into, byComp)
	return nil
}

// clusterXRDInfo returns the xrdInfo of the cluster XRD that defines in, a
// claim or composite resource checkComposites accepted.
func clusterXRDInfo(ctx context.Context, defs xpclient.DefinitionClient, in *un.Unstructured) (xrdInfo, error) {
	gvk := in.GroupVersionKind()
	if xrd, err := defs.GetXRDForClaim(ctx, gvk); err == nil && xrd != nil {
		xr, err := xpclient.XRTypeFromXRD(xrd)
		if err != nil {
			return xrdInfo{}, rejectInput(in, FlagResourceFile, fmt.Sprintf("whose XRD %q gives no composite type: %v", xrd.GetName(), err), "")
		}
		return xrdInfo{CompositeGVK: xr, Refs: xpclient.XRDSelectionRefs(xrd), Legacy: true}, nil
	}
	xrd, err := defs.GetXRDForXR(ctx, gvk)
	if err != nil {
		return xrdInfo{}, errors.Wrapf(err, "cannot get the XRD for %s", gvk)
	}
	return xrdInfo{CompositeGVK: gvk, Refs: xpclient.XRDSelectionRefs(xrd), Legacy: xrd.GetAPIVersion() == xpclient.CrossplaneAPIExtGroupV1}, nil
}

// xrdInfo is what assignComposites needs from the XRD that defines an input.
type xrdInfo struct {
	// CompositeGVK is the XRD's composite type. For an XR it equals the
	// input's GVK; for a claim it is the composite the claim makes.
	CompositeGVK schema.GroupVersionKind
	// Refs are the XRD's enforcedCompositionRef and defaultCompositionRef.
	Refs xpclient.SelectionRefs
	// Legacy is true for a claim or an XR whose XRD is
	// apiextensions.crossplane.io/v1: its Composition fields are under spec
	// only, not spec.crossplane.
	Legacy bool
}

// assignComposites assigns each input to exactly one of comps (by name) the
// way Crossplane selects a Composition (see selectComposition). xrdFor returns
// the XRD details for an input.
func assignComposites(ctx context.Context, comps, inputs []*un.Unstructured, xrdFor func(*un.Unstructured) (xrdInfo, error)) (map[string][]*un.Unstructured, error) {
	out := map[string][]*un.Unstructured{}
	for _, in := range inputs {
		xrd, err := xrdFor(in)
		if err != nil {
			return nil, err
		}
		name, err := selectComposition(ctx, comps, in, xrd)
		if err != nil {
			return nil, err
		}
		out[name] = append(out[name], in)
	}
	return out, nil
}

// selectComposition returns the name of the project Composition in comps that
// Crossplane would select for in, in xpclient.SelectComposition's order.
func selectComposition(ctx context.Context, comps []*un.Unstructured, in *un.Unstructured, xrd xrdInfo) (string, error) {
	var typed []*un.Unstructured
	for _, c := range comps {
		av, _, _ := un.NestedString(c.Object, "spec", "compositeTypeRef", "apiVersion")
		k, _, _ := un.NestedString(c.Object, "spec", "compositeTypeRef", "kind")
		if av == xrd.CompositeGVK.GroupVersion().String() && k == xrd.CompositeGVK.Kind {
			typed = append(typed, c)
		}
	}
	apiVersion := xpclient.CrossplaneAPIExtGroupV2
	if xrd.Legacy {
		apiVersion = xpclient.CrossplaneAPIExtGroupV1
	}
	return xpclient.SelectComposition(ctx, in, apiVersion, xrd.Refs, projectLookup{in: in, typed: typed, gvk: xrd.CompositeGVK})
}

// projectLookup is the xpclient.CompositionLookup selectComposition selects
// with: the names of typed, the project Compositions of gvk, in's type.
type projectLookup struct {
	in    *un.Unstructured
	typed []*un.Unstructured
	gvk   schema.GroupVersionKind
}

func (l projectLookup) ByName(_ context.Context, name, field string) (string, error) {
	for _, c := range l.typed {
		if c.GetName() == name {
			return name, nil
		}
	}
	return "", rejectInput(l.in, FlagResourceFile, fmt.Sprintf("whose %s %q is not a project Composition composing %s", field, name, l.gvk.Kind), "")
}

func (l projectLookup) ByLabels(_ context.Context, sel map[string]string) (string, error) {
	var match []*un.Unstructured
	for _, c := range l.typed {
		if xpclient.LabelsMatch(sel, c.GetLabels()) {
			match = append(match, c)
		}
	}
	return sole(l.in, match, "whose compositionSelector matches no project Composition composing "+l.gvk.Kind)
}

func (l projectLookup) ByType(context.Context) (string, error) {
	return sole(l.in, l.typed, "but no project Composition composes its type "+l.gvk.String())
}

func (l projectLookup) Malformed(err error, field string, subpath ...string) error {
	return rejectInput(l.in, FlagResourceFile, fmt.Sprintf("whose %s is malformed: %v", strings.Join(append([]string{field}, subpath...), "."), errors.Cause(err)), "")
}

// sole returns the name of the only Composition in match, or an error naming
// the input: none, the reason; several, the candidates.
func sole(in *un.Unstructured, match []*un.Unstructured, none string) (string, error) {
	switch len(match) {
	case 0:
		return "", rejectInput(in, FlagResourceFile, none, "")
	case 1:
		return match[0].GetName(), nil
	}
	names := make([]string, 0, len(match))
	for _, c := range match {
		names = append(names, c.GetName())
	}
	slices.Sort(names)
	return "", rejectInput(in, FlagResourceFile, "which matches several project Compositions ("+strings.Join(names, ", ")+")", "set its compositionRef to pick one")
}
