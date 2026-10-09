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
	"encoding/json"
	"path"
	"strings"
	"testing"

	"github.com/spf13/afero"
)

// newModelsFS builds a synthetic generator output tree, in the same
// "models/"-prefixed shape GenerateFromCRD and GenerateFromOpenAPI return, for
// testing MergeGeneratedSchemas without running the real toolchain.
func newModelsFS(t *testing.T, files map[string]string) afero.Fs {
	t.Helper()
	fsys := afero.NewMemMapFs()
	for p, content := range files {
		full := path.Join(typescriptModelsFolder, p)
		if err := fsys.MkdirAll(path.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := afero.WriteFile(fsys, full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return fsys
}

const testPackageJSON = `{
  "name": "crossplane-models",
  "version": "0.0.0",
  "dependencies": {
    "@kubernetes-models/apimachinery": "^3.0.2",
    "@kubernetes-models/base": "^6.0.1"
  }
}`

func TestMergeGeneratedSchemas(t *testing.T) {
	shared := `export * as apimachinery from "./apimachinery/index.js";` + "\n"
	sameObjectMeta := "export declare class ObjectMeta {}"

	cases := map[string]struct {
		// One newModelsFS input per part.
		parts []map[string]string
		// If set, MergeGeneratedSchemas must fail and check is not called.
		wantErr bool
		// Extra assertions against a successful merge's output.
		check func(t *testing.T, merged afero.Fs)
	}{
		"CombinesDistinctGroups": {
			// A CRD pass (one group) and an OpenAPI pass (a different
			// group), each with its own barrel and a package.json identical
			// to the other's except its (already-stamped) version.
			parts: []map[string]string{
				{
					"index.js":                `export * as exampleCom from "./example.com/index.js";` + "\n",
					"index.d.ts":              `export * as exampleCom from "./example.com/index.js";` + "\n",
					"example.com/index.js":    `export * from "./Widget.js";`,
					"example.com/Widget.js":   "export class Widget {}",
					"example.com/Widget.d.ts": "export declare class Widget {}",
					"package.json":            strings.Replace(testPackageJSON, `"version": "0.0.0"`, `"version": "0.0.0-crdstamp"`, 1),
				},
				{
					"index.js":          `export * as v1 from "./v1/index.js";` + "\n",
					"index.d.ts":        `export * as v1 from "./v1/index.js";` + "\n",
					"v1/index.js":       `export * from "./Namespace.js";`,
					"v1/Namespace.js":   "export class Namespace {}",
					"v1/Namespace.d.ts": "export declare class Namespace {}",
					"package.json":      strings.Replace(testPackageJSON, `"version": "0.0.0"`, `"version": "0.0.0-openapistamp"`, 1),
				},
			},
			check: func(t *testing.T, merged afero.Fs) {
				t.Helper()
				// Both groups must be reachable from the root barrel, in both files.
				for _, barrelPath := range []string{"index.js", "index.d.ts"} {
					content := readModelsFile(t, merged, barrelPath)
					for _, want := range []string{`export * as exampleCom from "./example.com/index.js";`, `export * as v1 from "./v1/index.js";`} {
						if !strings.Contains(content, want) {
							t.Errorf("%s missing %q:\n%s", barrelPath, want, content)
						}
					}
				}

				// Every per-group file from both parts must survive untouched.
				for _, p := range []string{"example.com/Widget.js", "example.com/Widget.d.ts", "v1/Namespace.js", "v1/Namespace.d.ts"} {
					if ok, _ := afero.Exists(merged, path.Join(typescriptModelsFolder, p)); !ok {
						t.Errorf("missing %s in merged output", p)
					}
				}

				// package.json survives (re-stamped, so just check it still
				// parses and still names the runtime dependencies both
				// parts agreed on).
				pkg := readModelsFile(t, merged, "package.json")
				if !strings.Contains(pkg, "@kubernetes-models/base") {
					t.Errorf("merged package.json lost a dependency:\n%s", pkg)
				}
			},
		},
		"DeduplicatesSharedBarrelLines": {
			parts: []map[string]string{
				{
					"index.js":     shared + `export * as exampleCom from "./example.com/index.js";` + "\n",
					"index.d.ts":   shared + `export * as exampleCom from "./example.com/index.js";` + "\n",
					"package.json": testPackageJSON,
				},
				{
					"index.js":     shared + `export * as v1 from "./v1/index.js";` + "\n",
					"index.d.ts":   shared + `export * as v1 from "./v1/index.js";` + "\n",
					"package.json": testPackageJSON,
				},
			},
			check: func(t *testing.T, merged afero.Fs) {
				t.Helper()
				for _, barrelPath := range []string{"index.js", "index.d.ts"} {
					content := readModelsFile(t, merged, barrelPath)
					if n := strings.Count(content, shared); n != 1 {
						t.Errorf("%s: shared line appears %d times, want 1 (deduplicated):\n%s", barrelPath, n, content)
					}
				}
			},
		},
		// The root barrel is combined independently for index.js and
		// index.d.ts -- never cross-mixed -- even though for the real
		// generator they always end up identical. This part deliberately
		// makes them not, to prove a line from one never leaks into the
		// other.
		"KeepsJSAndDTSBarrelsIndependent": {
			parts: []map[string]string{
				{
					"index.js":     `export * as onlyInJS from "./a/index.js";` + "\n",
					"index.d.ts":   `export * as onlyInDTS from "./b/index.js";` + "\n",
					"package.json": testPackageJSON,
				},
			},
			check: func(t *testing.T, merged afero.Fs) {
				t.Helper()
				js := readModelsFile(t, merged, "index.js")
				dts := readModelsFile(t, merged, "index.d.ts")
				if strings.Contains(js, "onlyInDTS") {
					t.Errorf("index.js picked up a line from index.d.ts:\n%s", js)
				}
				if strings.Contains(dts, "onlyInJS") {
					t.Errorf("index.d.ts picked up a line from index.js:\n%s", dts)
				}
			},
		},
		"IdenticalFileContentIsNotAConflict": {
			parts: []map[string]string{
				{"apimachinery/ObjectMeta.d.ts": sameObjectMeta, "package.json": testPackageJSON},
				{"apimachinery/ObjectMeta.d.ts": sameObjectMeta, "package.json": testPackageJSON},
			},
		},
		"ConflictingFileContentErrors": {
			parts: []map[string]string{
				{"apimachinery/ObjectMeta.d.ts": "export declare class ObjectMeta { a: string }", "package.json": testPackageJSON},
				{"apimachinery/ObjectMeta.d.ts": "export declare class ObjectMeta { b: string }", "package.json": testPackageJSON},
			},
			wantErr: true,
		},
		"PackageJSONVersionOnlyDifferenceIsNotAConflict": {
			parts: []map[string]string{
				{"package.json": strings.Replace(testPackageJSON, `"version": "0.0.0"`, `"version": "0.0.0-aaa"`, 1)},
				{"package.json": strings.Replace(testPackageJSON, `"version": "0.0.0"`, `"version": "0.0.0-bbb"`, 1)},
			},
		},
		"PackageJSONContentMismatchErrors": {
			parts: []map[string]string{
				{"package.json": testPackageJSON},
				{"package.json": strings.Replace(testPackageJSON, `"@kubernetes-models/base": "^6.0.1"`, `"@kubernetes-models/base": "^7.0.0"`, 1)},
			},
			wantErr: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			parts := make([]afero.Fs, len(tc.parts))
			for i, files := range tc.parts {
				parts[i] = newModelsFS(t, files)
			}

			merged, err := typescriptGenerator{}.MergeGeneratedSchemas(parts)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error, got none")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.check != nil {
				tc.check(t, merged)
			}
		})
	}
}

func readModelsFile(t *testing.T, fsys afero.Fs, p string) string {
	t.Helper()
	content, err := afero.ReadFile(fsys, path.Join(typescriptModelsFolder, p))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func TestCollectOpenAPISchemas(t *testing.T) {
	inputFS := afero.NewBasePathFs(afero.FromIOFS{FS: testdataJSONFS}, "testdata")

	schemas, err := typescriptGenerator{}.collectOpenAPISchemas(inputFS)
	if err != nil {
		t.Fatal(err)
	}

	// These come from different testdata files (the core API and the
	// resource.k8s.io group), so finding both confirms multiple documents
	// were merged into one map rather than only the last one read.
	for _, id := range []string{
		"io.k8s.api.core.v1.Pod",
		"io.k8s.apimachinery.pkg.apis.meta.v1.ObjectMeta",
	} {
		if _, ok := schemas[id]; !ok {
			t.Errorf("collectOpenAPISchemas() did not return %q", id)
		}
	}
}

func TestToLegacyOpenAPIDefinitions(t *testing.T) {
	inputFS := afero.NewBasePathFs(afero.FromIOFS{FS: testdataJSONFS}, "testdata")

	schemas, err := typescriptGenerator{}.collectOpenAPISchemas(inputFS)
	if err != nil {
		t.Fatal(err)
	}

	out, err := toLegacyOpenAPIDefinitions(schemas)
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(out), "#/components/schemas/") {
		t.Error("toLegacyOpenAPIDefinitions() did not rewrite all #/components/schemas/ refs to #/definitions/")
	}

	var doc struct {
		Definitions map[string]json.RawMessage `json:"definitions"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("toLegacyOpenAPIDefinitions() did not produce a {\"definitions\": ...} document: %v", err)
	}

	// A ref inside a schema that survived the rewrite should now point at
	// "#/definitions/", the only prefix openapi-generate's ref resolution
	// (kubernetes-models-ts, v6.1.1) understands.
	pod, ok := doc.Definitions["io.k8s.api.core.v1.Pod"]
	if !ok {
		t.Fatal("toLegacyOpenAPIDefinitions() dropped io.k8s.api.core.v1.Pod")
	}
	if !strings.Contains(string(pod), `"#/definitions/io.k8s.apimachinery.pkg.apis.meta.v1.ObjectMeta"`) {
		t.Errorf("Pod schema ref was not rewritten to #/definitions/: %s", pod)
	}

	// DeleteOptions is registered under every API group's version as a
	// generic meta kind, so it has more than one
	// x-kubernetes-group-version-kind entry. That combination trips a bug in
	// openapi-generate's alias output (see toLegacyOpenAPIDefinitions), so
	// its GVK extension must be stripped entirely.
	deleteOptions, ok := doc.Definitions["io.k8s.apimachinery.pkg.apis.meta.v1.DeleteOptions"]
	if !ok {
		t.Fatal("toLegacyOpenAPIDefinitions() dropped io.k8s.apimachinery.pkg.apis.meta.v1.DeleteOptions")
	}
	if strings.Contains(string(deleteOptions), "x-kubernetes-group-version-kind") {
		t.Error("toLegacyOpenAPIDefinitions() did not strip the multi-GVK extension from DeleteOptions")
	}

	// A schema with at most one GVK is unaffected by the bug and must keep
	// its extension, since it is what lets openapi-generate populate the
	// apiVersion/kind enum on the generated type.
	pod = doc.Definitions["io.k8s.api.core.v1.Pod"]
	if !strings.Contains(string(pod), "x-kubernetes-group-version-kind") {
		t.Error("toLegacyOpenAPIDefinitions() incorrectly stripped the GVK extension from Pod, which has only one")
	}
}
