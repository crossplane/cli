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
	"slices"
	"strings"

	un "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	xpclient "github.com/crossplane/cli/v2/internal/diff/client/crossplane"
)

// assignInputs checks the --resource-file inputs against the cluster's XRDs
// (see checkComposites), then assigns each to one of comps (see
// assignComposites) into into, keyed by Composition name.
func assignInputs(ctx context.Context, defs xpclient.DefinitionClient, ns string, comps, inputs []*un.Unstructured, into map[string][]*un.Unstructured) error {
	if err := checkComposites(ctx, defs, FlagResourceFile, ns, inputs); err != nil {
		return err
	}
	byComp, err := assignComposites(comps, inputs, func(in *un.Unstructured) (xrdInfo, error) {
		return clusterXRDInfo(ctx, defs, in)
	})
	if err != nil {
		return err
	}
	for name, xrs := range byComp {
		into[name] = xrs
	}
	return nil
}

// clusterXRDInfo returns the xrdInfo of the cluster XRD that defines in, a
// claim or composite resource checkComposites accepted.
func clusterXRDInfo(ctx context.Context, defs xpclient.DefinitionClient, in *un.Unstructured) (xrdInfo, error) {
	gvk := in.GroupVersionKind()
	if xrd, err := defs.GetXRDForClaim(ctx, gvk); err == nil && xrd != nil {
		info := xrdInfo{DefaultComposition: defaultComposition(xrd), Legacy: true}
		info.CompositeGVK.Group, _, _ = un.NestedString(xrd.Object, "spec", "group")
		info.CompositeGVK.Kind, _, _ = un.NestedString(xrd.Object, "spec", "names", "kind")
		versions, _, _ := un.NestedSlice(xrd.Object, "spec", "versions")
		for _, v := range versions {
			if m, ok := v.(map[string]any); ok && m["referenceable"] == true {
				info.CompositeGVK.Version, _ = m["name"].(string)
			}
		}
		if info.CompositeGVK.Version == "" {
			return xrdInfo{}, rejectInput(in, FlagResourceFile, fmt.Sprintf("whose XRD %q has no referenceable version", xrd.GetName()), "")
		}
		return info, nil
	}
	xrd, err := defs.GetXRDForXR(ctx, gvk)
	if err != nil {
		return xrdInfo{}, errors.Wrapf(err, "cannot get the XRD for %s", gvk)
	}
	return xrdInfo{CompositeGVK: gvk, DefaultComposition: defaultComposition(xrd), Legacy: xrd.GetAPIVersion() == xpclient.CrossplaneAPIExtGroupV1}, nil
}

func defaultComposition(xrd *un.Unstructured) string {
	name, _, _ := un.NestedString(xrd.Object, "spec", "defaultCompositionRef", "name")
	return name
}

// xrdInfo is what assignComposites needs from the XRD that defines an input.
type xrdInfo struct {
	// CompositeGVK is the XRD's composite type. For an XR it equals the
	// input's GVK; for a claim it is the composite the claim makes.
	CompositeGVK schema.GroupVersionKind
	// DefaultComposition is spec.defaultCompositionRef.name, if set.
	DefaultComposition string
	// Legacy is true for a claim or an XR whose XRD is
	// apiextensions.crossplane.io/v1: its Composition fields are under spec
	// only, not spec.crossplane.
	Legacy bool
}

// assignComposites assigns each input to exactly one of comps (by name) the
// way Crossplane selects a Composition: compositionRef, then
// compositionSelector, then the XRD's defaultCompositionRef, then the sole
// Composition whose compositeTypeRef matches. xrdFor returns the XRD details
// for an input.
func assignComposites(comps, inputs []*un.Unstructured, xrdFor func(*un.Unstructured) (xrdInfo, error)) (map[string][]*un.Unstructured, error) {
	out := map[string][]*un.Unstructured{}
	for _, in := range inputs {
		xrd, err := xrdFor(in)
		if err != nil {
			return nil, err
		}
		name, err := selectComposition(comps, in, xrd)
		if err != nil {
			return nil, err
		}
		out[name] = append(out[name], in)
	}
	return out, nil
}

func selectComposition(comps []*un.Unstructured, in *un.Unstructured, xrd xrdInfo) (string, error) {
	var typed []*un.Unstructured
	for _, c := range comps {
		av, _, _ := un.NestedString(c.Object, "spec", "compositeTypeRef", "apiVersion")
		k, _, _ := un.NestedString(c.Object, "spec", "compositeTypeRef", "kind")
		if av == xrd.CompositeGVK.GroupVersion().String() && k == xrd.CompositeGVK.Kind {
			typed = append(typed, c)
		}
	}
	byName := func(name, field string) (string, error) {
		for _, c := range typed {
			if c.GetName() == name {
				return name, nil
			}
		}
		return "", rejectInput(in, FlagResourceFile, fmt.Sprintf("whose %s %q is not a project Composition composing %s", field, name, xrd.CompositeGVK.Kind), "")
	}

	ref, err := crossplaneField[string](in, xrd.Legacy, un.NestedString, "compositionRef", "name")
	if err != nil {
		return "", err
	}
	if ref != "" {
		return byName(ref, "compositionRef")
	}

	sel, err := crossplaneField[map[string]string](in, xrd.Legacy, un.NestedStringMap, "compositionSelector", "matchLabels")
	if err != nil {
		return "", err
	}
	if len(sel) > 0 {
		var match []*un.Unstructured
		for _, c := range typed {
			if labelsMatch(c.GetLabels(), sel) {
				match = append(match, c)
			}
		}
		return sole(in, match, "whose compositionSelector matches no project Composition composing "+xrd.CompositeGVK.Kind)
	}

	if xrd.DefaultComposition != "" {
		return byName(xrd.DefaultComposition, "XRD defaultCompositionRef")
	}

	return sole(in, typed, "but no project Composition composes its type "+xrd.CompositeGVK.String())
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

// crossplaneField reads field at spec.<path> if legacy, otherwise at
// spec.crossplane.<path> or, if not set there, spec.<path>, as the engine does
// (see getCrossplaneRefPaths in internal/diff/client/crossplane). A value of
// the wrong type fails.
func crossplaneField[T any](in *un.Unstructured, legacy bool, get func(map[string]any, ...string) (T, bool, error), path ...string) (T, error) {
	var zero T
	paths := [][]string{append([]string{"spec", "crossplane"}, path...), append([]string{"spec"}, path...)}
	if legacy {
		paths = paths[1:]
	}
	for _, p := range paths {
		v, found, err := get(in.Object, p...)
		if err != nil {
			return zero, rejectInput(in, FlagResourceFile, fmt.Sprintf("whose %s is malformed: %v", strings.Join(p, "."), err), "")
		}
		if found {
			return v, nil
		}
	}
	return zero, nil
}

func labelsMatch(labels, sel map[string]string) bool {
	for k, v := range sel {
		if labels[k] != v {
			return false
		}
	}
	return true
}
