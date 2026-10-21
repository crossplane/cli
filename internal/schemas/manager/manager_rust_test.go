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

package manager

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/afero"

	devv1alpha1 "github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/internal/schemas/generator"
)

var rustModDeclRE = regexp.MustCompile(`(?m)^(?:pub )?mod (\w+);`)

// rustTestCRD returns a minimal CRD for the given group and kind.
func rustTestCRD(group, kind, plural string) string {
	return fmt.Sprintf(`apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: %[3]s.%[1]s
spec:
  group: %[1]s
  names:
    kind: %[2]s
    listKind: %[2]sList
    plural: %[3]s
    singular: %[3]s
  scope: Namespaced
  versions:
  - name: v1
    served: true
    storage: true
    schema:
      openAPIV3Schema:
        type: object
        properties:
          spec:
            type: object
            properties:
              size:
                type: integer
`, group, kind, plural)
}

// TestRustModuleTreeAcrossSources covers why the Rust generator is run-level:
// each source's crate declares only its own modules in src/lib.rs, the mod.rs
// files and Cargo.toml, so copying the sources' crates over each other would
// either conflict or leave declarations that describe one source. The merged
// crate has to declare every source's modules.
func TestRustModuleTreeAcrossSources(t *testing.T) {
	t.Parallel()

	gens := generator.Filter(generator.AllLanguages(), []string{devv1alpha1.SchemaLanguageRust})
	if len(gens) != 1 {
		t.Fatalf("want the Rust generator, got %d generators", len(gens))
	}

	// The project's own API, and two dependencies: one adds a kind to the
	// project's module and a module of its own, the other shares that module.
	api := &mockSource{id: "fs://apis", version: "v1", resources: map[string]string{
		"widget.yaml": rustTestCRD("example.com", "Widget", "widgets"),
	}}
	depA := &mockSource{id: "xpkg://example/a", version: "v1", resources: map[string]string{
		"gadget.yaml": rustTestCRD("example.com", "Gadget", "gadgets"),
		"bucket.yaml": rustTestCRD("s3.aws.example.io", "Bucket", "buckets"),
	}}
	depB := &mockSource{id: "xpkg://example/b", version: "v1", resources: map[string]string{
		"queue.yaml": rustTestCRD("s3.aws.example.io", "Queue", "queues"),
	}}

	testFS := afero.NewMemMapFs()
	m := New(testFS, gens, nil)
	if err := m.GenerateFromMultipleSources(t.Context(), []Source{depA, depB}, api); err != nil {
		t.Fatalf("GenerateFromMultipleSources(...): %v", err)
	}

	want := map[string][]string{
		"rust/src/lib.rs":                      {"com", "io"},
		"rust/src/com/example/v1/mod.rs":       {"gadget", "gadgetlist", "widget", "widgetlist"},
		"rust/src/io/example/aws/s3/v1/mod.rs": {"bucket", "bucketlist", "queue", "queuelist"},
	}
	for path, wantMods := range want {
		contents, err := afero.ReadFile(testFS, path)
		if err != nil {
			t.Errorf("reading %s: %v", path, err)
			continue
		}
		decls := rustModDeclRE.FindAllStringSubmatch(string(contents), -1)
		got := make([]string, 0, len(decls))
		for _, d := range decls {
			got = append(got, d[1])
		}
		if diff := cmp.Diff(wantMods, got); diff != "" {
			t.Errorf("%s: -want modules, +got modules:\n%s", path, diff)
		}
	}
}
