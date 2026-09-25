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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/afero"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	xpv1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1"

	devv1alpha1 "github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/internal/crd"
	"github.com/crossplane/cli/v2/internal/schemas/runner"

	_ "embed"
)

const (
	typescriptModelsFolder = "models"
	// typescriptImage is the Docker image used to run crd-generate and
	// openapi-generate. Pinned to an exact tag: the toolchain is installed
	// from a lockfile, so a floating Node would leave generated output
	// dependent on when it was generated.
	typescriptImage = "docker.io/library/node:24.20.0-slim"
	// typescriptAllOpenAPIFile is the path, inside the container work tree,
	// of the merged OpenAPI definitions document fed to openapi-generate. It
	// must match the "openapi-generate".input entry in the pinned toolchain
	// package.json.
	typescriptAllOpenAPIFile = "all-openapi.json"
)

// The toolchain that turns CRDs and OpenAPI specs into TypeScript models is
// pinned by a committed package.json and package-lock.json rather than
// resolved at generation time, so the same CLI produces the same models.
// Renovate keeps the pair current; see the typescript-toolchain rule in
// renovate.json5.
//
//go:embed typescript-toolchain/package.json
var typescriptToolchainPackageJSON []byte

//go:embed typescript-toolchain/package-lock.json
var typescriptToolchainPackageLock []byte

type typescriptGenerator struct{}

func (typescriptGenerator) Language() string {
	return devv1alpha1.SchemaLanguageTypeScript
}

// GenerateFromCRD generates TypeScript schema files from the XRDs and CRDs in fromFS.
// It uses @kubernetes-models/crd-generate to produce proper TypeScript classes
// with constructors, interfaces, and runtime validation.
func (t typescriptGenerator) GenerateFromCRD(ctx context.Context, fromFS afero.Fs, r runner.SchemaRunner) (afero.Fs, error) {
	// Collect all CRD YAML files into a working filesystem
	workFS := afero.NewMemMapFs()
	crdsDir := "crds"

	if err := workFS.MkdirAll(crdsDir, 0o755); err != nil {
		return nil, errors.Wrap(err, "failed to create crds directory")
	}

	crdCount, err := t.collectCRDs(fromFS, workFS, crdsDir)
	if err != nil {
		return nil, err
	}

	if crdCount == 0 {
		return nil, nil
	}

	return t.generateFromCRDFiles(ctx, workFS, crdsDir, r)
}

// MergeGeneratedSchemas combines the output of running GenerateFromCRD and
// GenerateFromOpenAPI in the same generation pass into one npm package tree.
// Called by internal/schemas/manager (via an unexported interface there)
// only when a single pass produced TypeScript output from more than one
// source type: each toolchain invocation produces its own root
// index.js/index.d.ts and package.json describing everything it saw, so
// writing two invocations' output to the same directory would let the second
// overwrite the first's root files rather than combine with them.
//
// index.js and index.d.ts are combined independently, each from only its own
// same-named source lines -- never cross-mixed -- even though for this
// generator's output they always end up identical: the root barrel is
// nothing but `export * as <group> from "./<group>/index.js";` lines, which
// have no type-level content to differ on, so tsc emits the same text for
// both the .js and the .d.ts. Every other generated path is specific to the
// group it was generated for and so belongs to one part, but two parts
// producing different content at the same path, or a package.json that
// differs from another part's beyond the version this function re-stamps
// below, means the toolchain changed between invocations in a way this
// function does not know how to reconcile -- so it errors rather than
// silently keep whichever part happened to be seen first.
func (typescriptGenerator) MergeGeneratedSchemas(parts []afero.Fs) (afero.Fs, error) {
	m := newTypescriptMerge()

	for _, part := range parts {
		if part == nil {
			continue
		}

		err := afero.Walk(part, typescriptModelsFolder, func(p string, info fs.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			return m.addFile(part, p)
		})
		if err != nil {
			return nil, errors.Wrap(err, "failed to walk generated TypeScript files")
		}
	}

	return m.finish()
}

// typescriptMerge accumulates state while MergeGeneratedSchemas walks each
// part's generated output.
type typescriptMerge struct {
	merged  afero.Fs
	written map[string][]byte // path -> content already written, to detect conflicts.

	jsBarrel  *lineSet
	dtsBarrel *lineSet

	pkgJSON     []byte
	pkgJSONFrom string
}

func newTypescriptMerge() *typescriptMerge {
	return &typescriptMerge{
		merged:    afero.NewMemMapFs(),
		written:   map[string][]byte{},
		jsBarrel:  newLineSet(),
		dtsBarrel: newLineSet(),
	}
}

// addFile incorporates one file from one part: the root barrels and
// package.json get special handling (see MergeGeneratedSchemas' doc
// comment), everything else is copied once and checked for conflicts.
func (m *typescriptMerge) addFile(part afero.Fs, p string) error {
	if path.Dir(p) == typescriptModelsFolder {
		switch path.Base(p) {
		case "index.js":
			return m.addBarrelFile(part, p, m.jsBarrel)
		case "index.d.ts":
			return m.addBarrelFile(part, p, m.dtsBarrel)
		case "package.json":
			return m.addPackageJSON(part, p)
		}
	}
	return m.addGenericFile(part, p)
}

func (m *typescriptMerge) addBarrelFile(part afero.Fs, p string, barrel *lineSet) error {
	content, err := afero.ReadFile(part, p)
	if err != nil {
		return err
	}
	barrel.addLines(string(content))
	return nil
}

func (m *typescriptMerge) addPackageJSON(part afero.Fs, p string) error {
	content, err := afero.ReadFile(part, p)
	if err != nil {
		return err
	}
	if m.pkgJSON == nil {
		m.pkgJSON, m.pkgJSONFrom = content, p
		return nil
	}
	if !packageJSONsEquivalent(m.pkgJSON, content) {
		return errors.Errorf("generated package.json at %s differs from %s beyond its version; the pinned toolchain should produce the same manifest regardless of source type", p, m.pkgJSONFrom)
	}
	return nil
}

func (m *typescriptMerge) addGenericFile(part afero.Fs, p string) error {
	content, err := afero.ReadFile(part, p)
	if err != nil {
		return errors.Wrapf(err, "failed to read %s", p)
	}

	if existing, ok := m.written[p]; ok {
		if !bytes.Equal(existing, content) {
			return errors.Errorf("%s was generated with different content by more than one source type in the same pass", p)
		}
		return nil
	}
	m.written[p] = content

	return afero.WriteFile(m.merged, p, content, 0o644)
}

// finish writes the combined barrels and package.json, re-stamps the
// package's version now that it describes every part, and returns the
// merged filesystem.
func (m *typescriptMerge) finish() (afero.Fs, error) {
	if err := afero.WriteFile(m.merged, path.Join(typescriptModelsFolder, "index.js"), m.jsBarrel.bytes(), 0o644); err != nil {
		return nil, errors.Wrap(err, "failed to write merged index.js")
	}
	if err := afero.WriteFile(m.merged, path.Join(typescriptModelsFolder, "index.d.ts"), m.dtsBarrel.bytes(), 0o644); err != nil {
		return nil, errors.Wrap(err, "failed to write merged index.d.ts")
	}
	if m.pkgJSON != nil {
		if err := afero.WriteFile(m.merged, path.Join(typescriptModelsFolder, "package.json"), m.pkgJSON, 0o644); err != nil {
			return nil, errors.Wrap(err, "failed to write merged package.json")
		}
	}

	// Each part's package.json was already stamped with a version reflecting
	// only its own files; re-stamp now that the tree describes everything.
	if err := stampModelsVersion(m.merged); err != nil {
		return nil, err
	}

	return m.merged, nil
}

// lineSet collects trimmed, non-empty, deduplicated lines from one or more
// files, in a stable sorted order regardless of the order they were added.
type lineSet struct {
	seen  map[string]struct{}
	lines []string
}

func newLineSet() *lineSet {
	return &lineSet{seen: map[string]struct{}{}}
}

func (s *lineSet) addLines(content string) {
	for line := range strings.SplitSeq(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if _, ok := s.seen[line]; ok {
			continue
		}
		s.seen[line] = struct{}{}
		s.lines = append(s.lines, line)
	}
}

func (s *lineSet) bytes() []byte {
	sorted := slices.Clone(s.lines)
	slices.Sort(sorted)
	return []byte(strings.Join(sorted, "\n") + "\n")
}

// packageJSONsEquivalent reports whether two package.json documents are equal
// apart from "version": MergeGeneratedSchemas re-stamps it from the combined
// tree once merging is done, so each part's own previously stamped version
// legitimately differs and is not itself a conflict.
func packageJSONsEquivalent(a, b []byte) bool {
	var pa, pb map[string]any
	if err := json.Unmarshal(a, &pa); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &pb); err != nil {
		return false
	}
	delete(pa, "version")
	delete(pb, "version")

	// json.Marshal sorts map keys, so this compares by value regardless of
	// each document's original key order.
	ma, errA := json.Marshal(pa)
	mb, errB := json.Marshal(pb)
	return errA == nil && errB == nil && bytes.Equal(ma, mb)
}

// GenerateFromOpenAPI generates TypeScript models from OpenAPI v3 documents in
// fromFS, today produced only by the built-in Kubernetes API dependency type
// (see k8sOpenAPISource). It uses @kubernetes-models/openapi-generate, a
// sibling of crd-generate in the same toolchain, converted to the format that
// tool expects; see toLegacyOpenAPIDefinitions.
func (t typescriptGenerator) GenerateFromOpenAPI(ctx context.Context, fromFS afero.Fs, r runner.SchemaRunner) (afero.Fs, error) {
	schemas, err := t.collectOpenAPISchemas(fromFS)
	if err != nil {
		return nil, err
	}
	if len(schemas) == 0 {
		return nil, nil
	}

	definitions, err := toLegacyOpenAPIDefinitions(schemas)
	if err != nil {
		return nil, err
	}

	workFS := afero.NewMemMapFs()
	if err := afero.WriteFile(workFS, typescriptAllOpenAPIFile, definitions, 0o644); err != nil {
		return nil, errors.Wrap(err, "failed to write combined OpenAPI definitions file")
	}

	return t.runToolchain(ctx, workFS, r, "npx openapi-generate")
}

// openAPIDocument is the subset of an OpenAPI v3 document collectOpenAPISchemas
// reads: the named schemas under components, which is all openapi-generate
// consumes.
type openAPIDocument struct {
	Components struct {
		Schemas map[string]json.RawMessage `json:"schemas"`
	} `json:"components"`
}

// collectOpenAPISchemas walks fromFS and merges the components.schemas of
// every OpenAPI v3 document it finds, keyed by schema ID (e.g.
// "io.k8s.api.apps.v1.Deployment"). Files that aren't JSON, or don't parse as
// an OpenAPI document, are skipped rather than treated as an error: fromFS may
// contain other files alongside the specs.
func (t typescriptGenerator) collectOpenAPISchemas(fromFS afero.Fs) (map[string]json.RawMessage, error) {
	merged := map[string]json.RawMessage{}

	err := afero.Walk(fromFS, "", func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return errors.Wrapf(err, "cannot read %q while collecting OpenAPI schemas for TypeScript models", path)
		}
		if info.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}

		bs, err := afero.ReadFile(fromFS, path)
		if err != nil {
			return errors.Wrapf(err, "failed to read file %q", path)
		}

		var doc openAPIDocument
		if err := json.Unmarshal(bs, &doc); err != nil {
			return nil //nolint:nilerr // Skip files that aren't valid JSON.
		}

		maps.Copy(merged, doc.Components.Schemas)

		return nil
	})

	return merged, errors.Wrap(err, "failed to walk OpenAPI filesystem")
}

// toLegacyOpenAPIDefinitions converts a merged OpenAPI v3 components.schemas
// map into the Swagger 2 "definitions" document that
// @kubernetes-models/openapi-generate expects, rewriting $refs from
// "#/components/schemas/" to "#/definitions/" to match.
//
// It also strips x-kubernetes-group-version-kind from any apimachinery
// schema registered under more than one GVK, working around a bug in
// openapi-generate's alias output (tommy351/kubernetes-models-ts#281, fixed
// by #282). Once a release containing that fix is out and
// typescript-toolchain/package.json is bumped past it, this step can be
// removed.
func toLegacyOpenAPIDefinitions(schemas map[string]json.RawMessage) ([]byte, error) {
	for id, raw := range schemas {
		if !strings.HasPrefix(id, "io.k8s.apimachinery.") {
			continue
		}

		var withGVK struct {
			// The literal Kubernetes OpenAPI extension name, not ours to rename.
			GVK []json.RawMessage `json:"x-kubernetes-group-version-kind"` //nolint:tagliatelle // Kubernetes API field name.
		}
		if err := json.Unmarshal(raw, &withGVK); err != nil {
			return nil, errors.Wrapf(err, "failed to parse schema %q", id)
		}
		if len(withGVK.GVK) <= 1 {
			continue
		}

		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, errors.Wrapf(err, "failed to parse schema %q", id)
		}
		delete(fields, "x-kubernetes-group-version-kind")

		fixed, err := json.Marshal(fields)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to re-marshal schema %q", id)
		}
		schemas[id] = fixed
	}

	bs, err := json.Marshal(struct {
		Definitions map[string]json.RawMessage `json:"definitions"`
	}{Definitions: schemas})
	if err != nil {
		return nil, errors.Wrap(err, "failed to marshal OpenAPI definitions for TypeScript models")
	}

	return bytes.ReplaceAll(bs, []byte(`#/components/schemas/`), []byte(`#/definitions/`)), nil
}

// collectCRDs walks the input filesystem and collects all CRD YAML files into
// the working filesystem. XRDs are converted to CRDs using the crd package.
// Returns the number of CRDs collected.
func (t typescriptGenerator) collectCRDs(fromFS, workFS afero.Fs, crdsDir string) (int, error) {
	// Temporary filesystem for XRD processing
	xrdFS := afero.NewMemMapFs()
	xrdBaseFolder := workDir
	if err := xrdFS.MkdirAll(xrdBaseFolder, 0o755); err != nil {
		return 0, errors.Wrap(err, "cannot prepare TypeScript schema generation workspace")
	}

	crdCount := 0

	err := afero.Walk(fromFS, "", func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return errors.Wrapf(err, "cannot read %q while collecting API definitions for TypeScript models", path)
		}

		if info.IsDir() {
			return nil
		}

		// Only process YAML files
		ext := filepath.Ext(path)
		if ext != extYAML && ext != extYML {
			return nil
		}

		bs, err := afero.ReadFile(fromFS, path)
		if err != nil {
			return errors.Wrapf(err, "failed to read file %q", path)
		}

		var u metav1.TypeMeta
		if err := yaml.Unmarshal(bs, &u); err != nil {
			return errors.Wrapf(err, "failed to parse file %q", path)
		}

		switch u.GroupVersionKind().Kind {
		case xpv1.CompositeResourceDefinitionKind:
			n, err := t.processXRDFile(xrdFS, workFS, bs, path, xrdBaseFolder, crdsDir)
			if err != nil {
				return err
			}
			crdCount += n

		case "CustomResourceDefinition":
			if err := t.processCRDFile(workFS, bs, path, crdsDir); err != nil {
				return err
			}
			crdCount++
		}

		return nil
	})

	return crdCount, err
}

// processXRDFile converts an XRD to CRDs and writes them to the working filesystem.
// Returns the number of CRDs written.
func (t typescriptGenerator) processXRDFile(xrdFS, workFS afero.Fs, bs []byte, path, xrdBaseFolder, crdsDir string) (int, error) {
	xrPath, claimPath, err := crd.ProcessXRD(xrdFS, bs, path, xrdBaseFolder)
	if err != nil {
		return 0, errors.Wrapf(err, "cannot convert XRD %q to CRDs for TypeScript models; check that the XRD is valid", path)
	}

	count := 0

	if xrPath != "" {
		if err := copyGeneratedCRD(xrdFS, workFS, xrPath, crdsDir, path, "xrd"); err != nil {
			return 0, err
		}
		count++
	}

	if claimPath != "" {
		if err := copyGeneratedCRD(xrdFS, workFS, claimPath, crdsDir, path, "claim"); err != nil {
			return 0, err
		}
		count++
	}

	return count, nil
}

// copyGeneratedCRD copies a generated CRD file from the XRD filesystem to the working filesystem.
func copyGeneratedCRD(xrdFS, workFS afero.Fs, srcPath, crdsDir, origPath, suffix string) error {
	crdBS, err := afero.ReadFile(xrdFS, srcPath)
	if err != nil {
		return errors.Wrapf(err, "failed to read generated CRD %q", srcPath)
	}
	outPath := filepath.Join(crdsDir, stagedCRDPath(origPath, suffix))
	if err := afero.WriteFile(workFS, outPath, crdBS, 0o644); err != nil {
		return errors.Wrapf(err, "failed to write CRD %q", outPath)
	}
	return nil
}

// processCRDFile validates and writes a CRD file to the working filesystem.
func (t typescriptGenerator) processCRDFile(workFS afero.Fs, bs []byte, path, crdsDir string) error {
	// Validate it's a proper CRD before copying
	var c extv1.CustomResourceDefinition
	if err := yaml.Unmarshal(bs, &c); err != nil {
		return errors.Wrapf(err, "failed to unmarshal CRD file %q", path)
	}

	// Write the CRD to the crds directory
	outPath := filepath.Join(crdsDir, stagedCRDPath(path, ""))
	if err := afero.WriteFile(workFS, outPath, bs, 0o644); err != nil {
		return errors.Wrapf(err, "failed to write CRD %q", outPath)
	}
	return nil
}

func stagedCRDPath(sourcePath, suffix string) string {
	clean := filepath.ToSlash(filepath.Clean(sourcePath))
	clean = strings.TrimPrefix(clean, "./")
	clean = strings.TrimPrefix(clean, "/")
	// Add a stable hash of the original clean path so flattened names do not collide.
	sum := sha256.Sum256([]byte(clean))
	hash := hex.EncodeToString(sum[:])[:12]
	if suffix != "" {
		ext := filepath.Ext(clean)
		clean = strings.TrimSuffix(clean, ext) + "-" + suffix + ext
	}
	ext := filepath.Ext(clean)
	flat := strings.ReplaceAll(strings.TrimSuffix(clean, ext), "/", "_")
	return flat + "-" + hash + ext
}

// generateFromCRDFiles runs crd-generate on the collected CRD files and
// produces TypeScript models with proper classes and validation.
func (t typescriptGenerator) generateFromCRDFiles(ctx context.Context, workFS afero.Fs, crdsDir string, r runner.SchemaRunner) (afero.Fs, error) {
	// Concatenate all CRD files into a single YAML file.
	// The npm published version of @kubernetes-models/read-input only supports
	// individual files, not directories.
	allCRDsFile := "all-crds.yaml"
	var allCRDs []byte
	err := afero.Walk(workFS, crdsDir, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		ext := filepath.Ext(path)
		if ext != extYAML && ext != extYML {
			return nil
		}
		content, err := afero.ReadFile(workFS, path)
		if err != nil {
			return errors.Wrapf(err, "failed to read CRD file %q", path)
		}
		if len(allCRDs) > 0 {
			allCRDs = append(allCRDs, []byte("\n---\n")...)
		}
		allCRDs = append(allCRDs, content...)
		return nil
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to collect CRD files")
	}
	if err := afero.WriteFile(workFS, allCRDsFile, allCRDs, 0o644); err != nil {
		return nil, errors.Wrap(err, "failed to write combined CRD file")
	}

	return t.runToolchain(ctx, workFS, r, "npx crd-generate")
}

// runToolchain stages the pinned npm toolchain into workFS and runs
// generatorCmd to generate schemas from CRDs or OpenAPI definitions.
func (t typescriptGenerator) runToolchain(ctx context.Context, workFS afero.Fs, r runner.SchemaRunner, generatorCmd string) (afero.Fs, error) {
	// Stage the pinned toolchain manifest and lockfile so the container can
	// install with npm ci rather than resolving version ranges at runtime.
	if err := afero.WriteFile(workFS, "package.json", typescriptToolchainPackageJSON, 0o644); err != nil {
		return nil, errors.Wrap(err, "failed to write toolchain package.json")
	}
	if err := afero.WriteFile(workFS, "package-lock.json", typescriptToolchainPackageLock, 0o644); err != nil {
		return nil, errors.Wrap(err, "failed to write toolchain package-lock.json")
	}

	// Run the generator in a container.
	// The script:
	// 1. Installs the pinned toolchain from the staged lockfile
	// 2. Runs the generator to produce TypeScript source
	// 3. Compiles TypeScript to JavaScript
	script := fmt.Sprintf(`set -eu

# Install the pinned toolchain. package.json and package-lock.json are staged
# by the generator, so npm ci installs exactly the locked tree and fails if the
# two ever disagree.
npm ci --no-audit --no-fund

# Run the generator (reads its input/output config from package.json)
%s

# Create tsconfig.json for compilation. We deliberately don't emit sourceMap or
# declarationMap: only dist/ ships in the models package, so every map would
# point at a gen/*.ts source that isn't there, and tools that read maps (test
# runners, bundlers) would warn once per generated type.
cat > tsconfig.json << 'TSEOF'
{
  "compilerOptions": {
    "target": "ES2022",
    "module": "NodeNext",
    "moduleResolution": "NodeNext",
    "declaration": true,
    "strict": true,
    "esModuleInterop": true,
    "skipLibCheck": true,
    "rootDir": "gen",
    "outDir": "dist"
  },
  "include": ["gen/**/*.ts"]
}
TSEOF

# Compile TypeScript to JavaScript
npx tsc

# Copy generated files to models directory for output
mkdir -p models
cp -r dist/* models/

# The generator emits _schemas/ as pre-compiled JS (not TypeScript), so tsc
# does not process it and it never appears in dist/. Copy it directly from gen/.
if [ -d gen/_schemas ]; then
  cp -r gen/_schemas models/
fi

# Update package.json for distribution (remove devDependencies and generator config)
cat > models/package.json << 'DISTEOF'
{
  "name": "crossplane-models",
  "version": "0.0.0",
  "type": "module",
  "main": "index.js",
  "types": "index.d.ts",
  "exports": {
    ".": {
      "types": "./index.d.ts",
      "default": "./index.js"
    },
    "./*": {
      "types": "./*/index.d.ts",
      "default": "./*/index.js"
    }
  },
  "dependencies": {
    "@kubernetes-models/apimachinery": "^3.0.2",
    "@kubernetes-models/base": "^6.0.1"
  }
}
DISTEOF
`, generatorCmd)

	if err := r.Generate(
		ctx,
		workFS,
		".",
		"",
		typescriptImage,
		[]string{"sh", "-c", script},
	); err != nil {
		return nil, errors.Wrap(err, "failed to install npm dependencies and generate TypeScript schemas; see npm output above for details")
	}

	// Create output filesystem and copy the models directory
	schemaFS := afero.NewMemMapFs()

	// Check if models directory was created
	exists, err := afero.DirExists(workFS, typescriptModelsFolder)
	if err != nil {
		return nil, errors.Wrap(err, "failed to check models directory")
	}
	if !exists {
		// No TypeScript files were generated
		return schemaFS, nil
	}

	// Copy all files from models/ to the output filesystem
	err = afero.Walk(workFS, typescriptModelsFolder, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return schemaFS.MkdirAll(path, 0o755)
		}

		content, err := afero.ReadFile(workFS, path)
		if err != nil {
			return errors.Wrapf(err, "failed to read %s", path)
		}

		return afero.WriteFile(schemaFS, path, content, 0o644)
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to copy generated TypeScript files")
	}

	if err := stampModelsVersion(schemaFS); err != nil {
		return nil, err
	}

	return schemaFS, nil
}

// stampModelsVersion replaces the generated package's placeholder version with
// one derived from the content of the generated files.
//
// The scaffold sets install-links=true, so models are copied into node_modules
// rather than symlinked, and npm treats a file: dependency as satisfied while
// its spec is unchanged. A content-derived version is what lets `npm update`
// pick up regenerated models; with a constant 0.0.0 it does not.
func stampModelsVersion(fsys afero.Fs) error {
	pkgPath := path.Join(typescriptModelsFolder, "package.json")

	digest, err := hashModels(fsys, pkgPath)
	if err != nil {
		return err
	}

	bs, err := afero.ReadFile(fsys, pkgPath)
	if err != nil {
		// No manifest means nothing was generated, so there is nothing to stamp.
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return errors.Wrap(err, "failed to read generated models package.json")
	}

	var pkg map[string]any
	if err := json.Unmarshal(bs, &pkg); err != nil {
		return errors.Wrap(err, "generated models package.json is not valid JSON")
	}

	// A semver prerelease identifier, so the value stays a valid version that
	// npm will compare and order.
	pkg["version"] = "0.0.0-" + digest

	out, err := json.MarshalIndent(pkg, "", "  ")
	if err != nil {
		return errors.Wrap(err, "failed to serialize generated models package.json")
	}

	return errors.Wrap(afero.WriteFile(fsys, pkgPath, append(out, '\n'), 0o644), "failed to write generated models package.json")
}

// hashModels returns a digest over every generated file except the manifest,
// which is excluded because its own content depends on the result.
func hashModels(fsys afero.Fs, skip string) (string, error) {
	var paths []string
	if err := afero.Walk(fsys, typescriptModelsFolder, func(p string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || p == skip {
			return nil
		}
		paths = append(paths, p)
		return nil
	}); err != nil {
		return "", errors.Wrap(err, "failed to walk generated TypeScript files")
	}

	// Walk order is not guaranteed across filesystem implementations, and the
	// digest has to be stable for identical content.
	slices.Sort(paths)

	h := sha256.New()
	for _, p := range paths {
		content, err := afero.ReadFile(fsys, p)
		if err != nil {
			return "", errors.Wrapf(err, "failed to read %s", p)
		}
		// Include the path so that moving content between files changes the
		// digest.
		_, _ = h.Write([]byte(p))
		_, _ = h.Write(content)
	}

	return hex.EncodeToString(h.Sum(nil))[:12], nil
}
