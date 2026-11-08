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

	un "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	xpclient "github.com/crossplane/cli/v2/internal/diff/client/crossplane"
	ld "github.com/crossplane/cli/v2/pkg/load"
)

// RejectInput returns the error for an -f input r that the command doesn't
// take. takes describes what the command does take, e.g. "composition
// simulate takes Composition files".
func RejectInput(r *un.Unstructured, takes string) error {
	return rejectInput(r, "", takes)
}

// rejectInput is RejectInput with a reason, if not empty, that says why r was
// rejected and follows the input. takes may be empty if reason says enough.
func rejectInput(r *un.Unstructured, reason, takes string) error {
	gvk := r.GroupVersionKind()
	msg := fmt.Sprintf("-f inputs include %s %q (%s)", gvk.Kind, r.GetName(), gvk.GroupVersion())
	if reason != "" {
		msg += ", " + reason
	}
	if takes != "" {
		msg += "; " + takes
	}
	if r.IsList() || gvk.Kind == "List" {
		msg += "; pass its items as separate YAML documents instead of a List"
	}
	return errors.New(msg)
}

// LoadInputs loads the -f inputs at paths ("-" is stdin) and rejects any
// for which validate returns an error.
func LoadInputs(paths []string, validate func(*un.Unstructured) error) ([]*un.Unstructured, error) {
	// Pipeline step inputs are part of their Composition, not resources of
	// their own.
	loader, err := ld.NewExactCompositeLoader(paths, ld.WithoutPipelineInputs())
	if err != nil {
		return nil, errors.Wrap(err, "cannot load -f inputs")
	}
	resources, err := loader.Load()
	if err != nil {
		return nil, errors.Wrap(err, "cannot load -f inputs")
	}
	for _, r := range resources {
		if err := validate(r); err != nil {
			return nil, err
		}
	}
	return resources, nil
}

// RejectDuplicateNames returns an error if two of the Compositions share a
// name, since both would be diffed against the same live Composition.
func RejectDuplicateNames(comps []*un.Unstructured) error {
	seen := map[string]bool{}
	for _, c := range comps {
		if seen[c.GetName()] {
			return errors.Errorf("Composition %q is defined more than once; Composition names must be unique", c.GetName())
		}
		seen[c.GetName()] = true
	}
	return nil
}

// checkComposites rejects resources that no XRD in the cluster defines as a
// composite resource or claim, such as package metadata, a Provider or a
// ConfigMap, which the engine would otherwise fail on with no hint at what -f
// accepts. It also gives a claim or namespaced composite resource with no
// namespace the kubeconfig context namespace ns, as kubectl apply would. The
// definition client caches the XRDs when it initializes, so the check costs
// no extra calls.
func checkComposites(ctx context.Context, defs xpclient.DefinitionClient, ns string, resources []*un.Unstructured) error {
	for _, r := range resources {
		gvk := r.GroupVersionKind()
		if xrd, err := defs.GetXRDForClaim(ctx, gvk); err == nil && xrd != nil {
			defaultNamespace(r, ns)
			continue
		}
		if xrd, err := defs.GetXRDForXR(ctx, gvk); err == nil && xrd != nil {
			// Only a Namespaced scope is namespaced; Cluster and LegacyCluster are not.
			if scope, _, _ := un.NestedString(xrd.Object, "spec", "scope"); scope == "Namespaced" {
				defaultNamespace(r, ns)
			}
			continue
		}
		return rejectInput(r, "which no XRD in the cluster defines as a composite resource or claim (if it is one, check the kubeconfig context, its apiVersion and that its XRD is installed)", "")
	}
	return nil
}

// defaultNamespace sets r's namespace to ns if it has none.
func defaultNamespace(r *un.Unstructured, ns string) {
	if r.GetNamespace() == "" {
		r.SetNamespace(ns)
	}
}
