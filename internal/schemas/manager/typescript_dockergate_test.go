//go:build dockergate

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
	"context"
	"embed"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"

	"github.com/crossplane/cli/v2/internal/schemas/generator"
	"github.com/crossplane/cli/v2/internal/schemas/runner"
)

//go:embed testdata/*.yaml testdata/*.json
var typescriptDockergateTestdataFS embed.FS

// dockergateOpenAPISource is a filesystem-backed OpenAPI source, mirroring
// the shape of k8sOpenAPISource without hitting the network.
type dockergateOpenAPISource struct {
	id string
	fs afero.Fs
}

func (s *dockergateOpenAPISource) ID() string { return s.id }

func (s *dockergateOpenAPISource) Version(_ context.Context) (string, error) { return "v1", nil }

func (s *dockergateOpenAPISource) Resources(_ context.Context) (afero.Fs, error) { return s.fs, nil }

func (s *dockergateOpenAPISource) Type() SourceType { return SourceTypeOpenAPI }

// tsGeneratorOnly returns just the typescript generator, so this test isn't
// slowed down generating every other language too.
func tsGeneratorOnly() generator.Interface {
	for _, g := range generator.AllLanguages() {
		if g.Language() == "typescript" {
			return g
		}
	}
	panic("no typescript generator")
}

// TestGenerateFromMultipleSourcesTypeScriptRealToolchain is the
// real-toolchain counterpart to
// TestGenerateFromMultipleSources_MergesAcrossSourceTypes: that test proves
// the manager's merge dispatch with a mock generator; this proves the actual
// TypeScript generator's MergeGeneratedSchemas produces a correct, compilable
// package when a project has both a CRD/XRD source (its own APIs, or a CRD
// dependency) and an OpenAPI source (a k8s: dependency) -- the case a real
// project is likely to have.
//
// It runs the real TypeScript toolchain in a container, so it can't run in
// the hermetic Nix sandbox that runs our unit tests -- run it locally with a
// Docker daemon available:
//
//	go test -tags dockergate ./internal/schemas/manager/... -run TestGenerateFromMultipleSourcesTypeScriptRealToolchain -v
func TestGenerateFromMultipleSourcesTypeScriptRealToolchain(t *testing.T) {
	testdataFS := afero.NewBasePathFs(afero.FromIOFS{FS: typescriptDockergateTestdataFS}, "testdata")

	crdSrc := NewFSSource("widget", testdataFS)
	openAPISrc := &dockergateOpenAPISource{id: "k8s://v1", fs: testdataFS}

	outFS := afero.NewMemMapFs()
	m := New(outFS, []generator.Interface{tsGeneratorOnly()}, runner.NewRealSchemaRunner(runner.WithImageConfig(nil)))

	if err := m.GenerateFromMultipleSources(t.Context(), []Source{crdSrc, openAPISrc}); err != nil {
		t.Fatal(err)
	}

	// The CRD-sourced kind and the OpenAPI-sourced kind must both be
	// reachable by subpath import (the form every generated function and
	// the README use), and both must be named in the root barrel (the "."
	// export) -- which is exactly what silently lost the CRD-sourced group
	// before MergeGeneratedSchemas existed.
	mustExist := []string{
		"typescript/package.json",
		"typescript/index.js",
		"typescript/index.d.ts",
		"typescript/example.com/v1/Widget.js",
		"typescript/v1/Namespace.js",
	}
	for _, p := range mustExist {
		if ok, err := afero.Exists(outFS, p); err != nil {
			t.Fatal(err)
		} else if !ok {
			t.Errorf("missing expected file %s", p)
		}
	}

	rootIndex, err := afero.ReadFile(outFS, "typescript/index.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rootIndex), "example.com") {
		t.Errorf("root index.js does not re-export the CRD-sourced group example.com:\n%s", rootIndex)
	}
	if !strings.Contains(string(rootIndex), "./v1/index.js") {
		t.Errorf("root index.js does not re-export the OpenAPI-sourced group v1:\n%s", rootIndex)
	}

	assertMergedPackageTypechecks(t, outFS, "typescript")
}

// assertMergedPackageTypechecks writes the merged package to a real
// directory and runs the real TypeScript compiler against a consumer file
// that imports the CRD-sourced group and the OpenAPI-sourced group through
// the merged root barrel -- the file MergeGeneratedSchemas rebuilds. String
// assertions on that barrel's content (above) can't tell a valid merge from
// one whose text happens to mention the right group names; only tsc can.
func assertMergedPackageTypechecks(t *testing.T, mergedFS afero.Fs, subPath string) {
	t.Helper()

	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm not on PATH; skipping the typecheck of the merged package")
	}

	dir := t.TempDir()
	pkgFS := afero.NewBasePathFs(mergedFS, subPath)
	if err := afero.Walk(pkgFS, "", func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return os.MkdirAll(filepath.Join(dir, p), 0o755)
		}
		content, err := afero.ReadFile(pkgFS, p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, p), content, 0o644)
	}); err != nil {
		t.Fatal(err)
	}

	// A consumer importing both groups only through the merged root barrel:
	// exactly the file MergeGeneratedSchemas rebuilds, not the per-group
	// files a plain copy would have left untouched either way.
	consumer := `import { exampleCom, v1 } from "./index.js";

new exampleCom.v1.Widget();
new v1.Namespace();
`
	if err := os.WriteFile(filepath.Join(dir, "consumer.ts"), []byte(consumer), 0o644); err != nil {
		t.Fatal(err)
	}

	tsconfig := `{
  "compilerOptions": {
    "target": "ES2022",
    "module": "NodeNext",
    "moduleResolution": "NodeNext",
    "strict": true,
    "esModuleInterop": true,
    "skipLibCheck": true,
    "noEmit": true
  },
  "include": ["consumer.ts", "index.d.ts", "*/**/*.d.ts"]
}`
	if err := os.WriteFile(filepath.Join(dir, "tsconfig.json"), []byte(tsconfig), 0o644); err != nil {
		t.Fatal(err)
	}

	run := func(name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %s failed: %v\n%s", name, strings.Join(args, " "), err, out)
		}
	}

	run("npm", "install", "--no-audit", "--no-fund")
	run("npm", "install", "--no-save", "--no-audit", "--no-fund", "typescript@5.9.3")
	run("npx", "tsc", "--noEmit")
}
