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

package generator

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"k8s.io/kube-openapi/pkg/validation/spec"
)

func TestIsAPIVersion(t *testing.T) {
	t.Parallel()

	tcs := map[string]bool{
		"v1":          true,
		"v10":         true,
		"v01":         false,
		"v1alpha1":    true,
		"v1alpha10":   true,
		"v10alpha1":   true,
		"v10alpha10":  true,
		"v01alpha1":   false,
		"v1alpha01":   false,
		"notaversion": false,
		"v1.2.3":      false,
		"v1zeta1":     false,
	}

	for in, want := range tcs {
		t.Run(in, func(t *testing.T) {
			t.Parallel()

			got := isAPIVersion(in)
			if got != want {
				t.Errorf("isAPIVersion(...): got %v want %v", got, want)
			}
		})
	}
}

// The OpenAPI path writes kcl.mod itself, and the CRD path gets it from
// `kcl import` in kclImage. The two must agree on the edition, or a project
// with both CRD and OpenAPI sources reports their kcl.mod files as a
// conflict.
func TestKCLEditionMatchesImage(t *testing.T) {
	t.Parallel()

	if !strings.HasSuffix(kclImage, ":"+kclEdition) {
		t.Errorf("kclEdition %q does not match the tag of kclImage %q", kclEdition, kclImage)
	}
}

// Imports must come out in the same order on every run: the merged pass
// compares shared files byte for byte, and Go map order is random.
func TestGenerateKCLFileImportsSorted(t *testing.T) {
	t.Parallel()

	refs := []string{
		"io.k8s.api.core.v1.PodSpec",
		"io.k8s.apimachinery.pkg.apis.meta.v1.ObjectMeta",
		"io.k8s.api.apps.v1.DeploymentStrategy",
		"io.k8s.api.batch.v1.JobSpec",
		"io.k8s.api.networking.v1.IngressSpec",
	}
	schema := &spec.Schema{SchemaProps: spec.SchemaProps{Properties: map[string]spec.Schema{}}}
	for i, ref := range refs {
		schema.Properties[fmt.Sprintf("p%d", i)] = spec.Schema{
			SchemaProps: spec.SchemaProps{Ref: spec.MustCreateRef("#/components/schemas/" + ref)},
		}
	}

	first := generateKCLFile("io.example.v1.Widget", schema, map[string]*spec.Schema{})

	var imports []string
	for line := range strings.SplitSeq(first, "\n") {
		if strings.HasPrefix(line, "import ") {
			imports = append(imports, line)
		}
	}
	if len(imports) != len(refs) {
		t.Fatalf("got %d imports, want %d:\n%s", len(imports), len(refs), first)
	}
	if !slices.IsSorted(imports) {
		t.Errorf("imports are not sorted:\n%s", strings.Join(imports, "\n"))
	}

	for range 20 {
		if got := generateKCLFile("io.example.v1.Widget", schema, map[string]*spec.Schema{}); got != first {
			t.Fatalf("generateKCLFile output differs between runs:\n%s\nvs\n%s", first, got)
		}
	}
}
