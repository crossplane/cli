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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestLoadInputs(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	commaComp := write("a,b.yaml", "apiVersion: apiextensions.crossplane.io/v1\nkind: Composition\nmetadata:\n  name: comma\n")
	proj := write("proj/crossplane-project.yaml", "apiVersion: dev.crossplane.io/v1alpha1\nkind: Project\nmetadata:\n  name: p\n")
	comp := write("comp.yaml", `apiVersion: apiextensions.crossplane.io/v1
kind: Composition
metadata:
  name: c
spec:
  compositeTypeRef:
    apiVersion: example.org/v1
    kind: XThing
  mode: Pipeline
  pipeline:
  - step: render
    functionRef:
      name: function-go-templating
    input:
      apiVersion: gotemplating.fn.crossplane.io/v1beta1
      kind: GoTemplate
      source: Inline
`)
	compsPipeline := write("pipelinedir/p.yaml", `apiVersion: apiextensions.crossplane.io/v1
kind: Composition
metadata:
  name: p
spec:
  compositeTypeRef:
    apiVersion: example.org/v1
    kind: XThing
  mode: Pipeline
  pipeline:
  - step: render
    functionRef:
      name: function-go-templating
    input:
      apiVersion: gotemplating.fn.crossplane.io/v1beta1
      kind: GoTemplate
      source: Inline
`)
	// A step input that isn't an object is the engine's to report, not the
	// loader's.
	listInput := write("listinput.yaml", `apiVersion: apiextensions.crossplane.io/v1
kind: Composition
metadata:
  name: l
spec:
  compositeTypeRef:
    apiVersion: example.org/v1
    kind: XThing
  mode: Pipeline
  pipeline:
  - step: render
    functionRef:
      name: function-go-templating
    input: [1]
`)
	xr := write("xr.yaml", "apiVersion: example.org/v1\nkind: XThing\nmetadata:\n  name: x\n")
	// A GoTemplate of its own is a resource, not a pipeline input.
	goTemplate := write("gotemplate.yaml", "apiVersion: gotemplating.fn.crossplane.io/v1beta1\nkind: GoTemplate\nmetadata:\n  name: g\nsource: Inline\n")
	comps := write("comps/a.yaml", "apiVersion: apiextensions.crossplane.io/v1\nkind: Composition\nmetadata:\n  name: a\n")
	list := write("list.yaml", "apiVersion: v1\nkind: List\nitems:\n- apiVersion: apiextensions.crossplane.io/v1\n  kind: Composition\n  metadata:\n    name: a\n")
	empty := write("empty.yaml", "# nothing here\n")

	type want struct {
		names []string
		err   string
	}

	cases := map[string]struct {
		reason string
		paths  []string
		want   want
	}{
		"Composition": {
			reason: "A Composition should load; its pipeline inputs are part of it.",
			paths:  []string{comp},
			want:   want{names: []string{"c"}},
		},
		"CompositionListInput": {
			reason: "A Composition should be loaded without parsing its pipeline inputs as resources.",
			paths:  []string{listInput},
			want:   want{names: []string{"l"}},
		},
		"CompositionDirectory": {
			reason: "A directory of Compositions should load.",
			paths:  []string{filepath.Dir(comps)},
			want:   want{names: []string{"a"}},
		},
		"CompositionDirectoryPipelineInput": {
			reason: "A directory's Compositions should load without their pipeline inputs as resources.",
			paths:  []string{filepath.Dir(compsPipeline)},
			want:   want{names: []string{"p"}},
		},
		"Compositions": {
			reason: "Several Composition files should load in order.",
			paths:  []string{comp, comps},
			want:   want{names: []string{"c", "a"}},
		},
		"CommaInPath": {
			reason: "-f never splits on commas, so a,b.yaml is one path.",
			paths:  []string{commaComp},
			want:   want{names: []string{"comma"}},
		},
		"ProjectFile": {
			reason: "A project file should be rejected with a pointer to project simulate.",
			paths:  []string{proj},
			want:   want{err: `-f inputs include Project "p" (dev.crossplane.io/v1alpha1); composition simulate takes Composition files; project simulate takes a project file`},
		},
		"CompositionWithXR": {
			reason: "Compositions mixed with other resources should be rejected.",
			paths:  []string{comp, xr},
			want:   want{err: `-f inputs include XThing "x" (example.org/v1); composition simulate takes Composition files`},
		},
		"CompositionWithStandaloneGoTemplate": {
			reason: "A resource that only looks like a pipeline input should still be rejected.",
			paths:  []string{comp, goTemplate},
			want:   want{err: `-f inputs include GoTemplate "g"`},
		},
		"KubectlList": {
			reason: "A List, as kubectl get -o yaml writes, should be rejected with a hint to unwrap it.",
			paths:  []string{list},
			want:   want{err: `-f inputs include List "" (v1); composition simulate takes Composition files; pass its items as separate YAML documents instead of a List`},
		},
		"Empty": {
			reason: "Inputs with no resources should be rejected.",
			paths:  []string{empty},
			want:   want{err: "no resources found"},
		},
		"Missing": {
			reason: "A missing input should be rejected.",
			paths:  []string{filepath.Join(dir, "nope.yaml")},
			want:   want{err: "cannot load -f inputs"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := LoadInputs(tc.paths, ClassComposition)
			if tc.want.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want.err) {
					t.Fatalf("\n%s\nLoadInputs(%v): want error containing %q, got %v", tc.reason, tc.paths, tc.want.err, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("\n%s\nLoadInputs(%v): %v", tc.reason, tc.paths, err)
			}
			names := []string{}
			for _, r := range got {
				names = append(names, r.GetName())
			}
			if diff := cmp.Diff(tc.want.names, names); diff != "" {
				t.Errorf("\n%s\nLoadInputs(%v): -want, +got:\n%s", tc.reason, tc.paths, diff)
			}
		})
	}
}
