/*
Copyright 2025 The Crossplane Authors.

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

// Package ref handles parsing and formatting of [namespace/]name composite
// references supplied on the command line. Parse and Format are
// inverses: Parse turns a user-typed string into a NamespacedName, Format
// turns a NamespacedName back into the user's original spelling
// (bare "name" for cluster-scoped, "namespace/name" for namespaced).
package ref

import (
	"strings"

	k8stypes "k8s.io/apimachinery/pkg/types"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

// Parse parses a "[namespace/]name" CLI arg into a NamespacedName.
// Bare "name" (no slash) means cluster-scoped (v1 XRs, v2 cluster-scoped XRs).
// "ns/name" means namespaced (Claims, v2 namespaced XRs).
// "/name" (empty namespace before slash) is rejected because the user's intent is clearly namespaced.
func Parse(value string) (k8stypes.NamespacedName, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return k8stypes.NamespacedName{}, errors.New("must not be empty")
	}

	parts := strings.Split(trimmed, "/")
	// Trim each part so inputs like "default / my-claim" — which would otherwise
	// parse as namespace "default " and fail downstream with a confusing error —
	// are normalized at the point of CLI parsing. Kubernetes names/namespaces
	// can't contain whitespace, so per-part trimming never loses information.
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}

	switch len(parts) {
	case 1:
		return k8stypes.NamespacedName{Name: parts[0]}, nil
	case 2:
		ns, name := parts[0], parts[1]
		if ns == "" {
			return k8stypes.NamespacedName{}, errors.New("namespace must not be empty (use bare name for cluster-scoped composites)")
		}

		if name == "" {
			return k8stypes.NamespacedName{}, errors.New("name must not be empty")
		}

		return k8stypes.NamespacedName{Namespace: ns, Name: name}, nil
	default:
		return k8stypes.NamespacedName{}, errors.Errorf("expected [namespace/]name format, got %d slashes", len(parts)-1)
	}
}

// Format renders a NamespacedName the way the user typed it on the command line:
// bare "name" for cluster-scoped, "namespace/name" for namespaced.
//
// NamespacedName.String() always renders "namespace/name" (so "/foo" for
// cluster-scoped), which is wrong for human-facing output where users expect
// their original spelling.
func Format(n k8stypes.NamespacedName) string {
	switch n.Namespace {
	case "":
		return n.Name
	default:
		return n.Namespace + "/" + n.Name
	}
}
