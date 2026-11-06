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
	"fmt"

	un "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	apiextensionsv1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1"

	"github.com/crossplane/cli/v2/internal/project/projectfile"
	ld "github.com/crossplane/cli/v2/pkg/load"
)

// Class is a kind of -f input. Each simulate command takes one class, and
// rejects the others with the same message.
type Class int

// The -f input classes.
const (
	// ClassUnknown is any resource no simulate command takes.
	ClassUnknown Class = iota
	// ClassProject is a project file, which project simulate takes.
	ClassProject
	// ClassComposition is a Composition, which composition simulate takes.
	ClassComposition
)

// taker returns the command that takes class c and what it takes, or false
// when no command takes c.
func taker(c Class) (command, what string, ok bool) {
	switch c {
	case ClassProject:
		return "project simulate", "a project file", true
	case ClassComposition:
		return "composition simulate", "Composition files", true
	case ClassUnknown:
	}
	return "", "", false
}

// classify returns the class of the -f input r.
func classify(r *un.Unstructured) Class {
	gvk := r.GroupVersionKind()
	switch {
	case gvk.GroupVersion().String() == projectfile.APIVersion && gvk.Kind == projectfile.Kind:
		return ClassProject
	case gvk == apiextensionsv1.CompositionGroupVersionKind:
		return ClassComposition
	default:
		return ClassUnknown
	}
}

// rejectInput returns the error for an -f input r that the command taking
// want doesn't take, pointing at the command that does take it, if any.
func rejectInput(r *un.Unstructured, want Class) error {
	gvk := r.GroupVersionKind()
	cmd, what, _ := taker(want)
	msg := fmt.Sprintf("-f inputs include %s %q (%s); %s takes %s", gvk.Kind, r.GetName(), gvk.GroupVersion(), cmd, what)
	if cmd, what, ok := taker(classify(r)); ok {
		msg += fmt.Sprintf("; %s takes %s", cmd, what)
	}
	if r.IsList() || gvk.Kind == "List" {
		msg += "; pass its items as separate YAML documents instead of a List"
	}
	return errors.New(msg)
}

// LoadInputs loads the -f inputs at paths ("-" is stdin) and rejects any
// that aren't of class want.
func LoadInputs(paths []string, want Class) ([]*un.Unstructured, error) {
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
		if classify(r) != want {
			return nil, rejectInput(r, want)
		}
	}
	if want == ClassComposition {
		if err := RejectDuplicateNames(resources); err != nil {
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
