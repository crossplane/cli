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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
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
	// typescriptImage is the Docker image used to run crd-generate. Pinned to
	// an exact tag: the toolchain is installed from a lockfile, so a floating
	// Node would leave generated output dependent on when it was generated.
	typescriptImage = "docker.io/library/node:24.20.0-slim"
)

// The toolchain that turns CRDs into TypeScript models is pinned by a
// committed package.json and package-lock.json rather than resolved at
// generation time, so the same CLI produces the same models. Renovate keeps
// the pair current; see the typescript-toolchain rule in renovate.json5.
//
//go:embed typescript-toolchain/package.json
var typescriptToolchainPackageJSON []byte

//go:embed typescript-toolchain/package-lock.json
var typescriptToolchainPackageLock []byte

type typescriptGenerator struct{}

func (typescriptGenerator) Language() string {
	return devv1alpha1.SchemaLanguageTypescript
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

// GenerateFromOpenAPI is not supported for TypeScript - use GenerateFromCRD instead.
// The crd-generate tool requires CRD YAML files, not OpenAPI specs.
func (t typescriptGenerator) GenerateFromOpenAPI(_ context.Context, _ afero.Fs, _ runner.SchemaRunner) (afero.Fs, error) {
	// crd-generate works with CRD YAML files, not OpenAPI specs.
	// Return nil to indicate no schemas were generated.
	return nil, nil
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

	// Stage the pinned toolchain manifest and lockfile so the container can
	// install with npm ci rather than resolving version ranges at runtime.
	if err := afero.WriteFile(workFS, "package.json", typescriptToolchainPackageJSON, 0o644); err != nil {
		return nil, errors.Wrap(err, "failed to write toolchain package.json")
	}
	if err := afero.WriteFile(workFS, "package-lock.json", typescriptToolchainPackageLock, 0o644); err != nil {
		return nil, errors.Wrap(err, "failed to write toolchain package-lock.json")
	}

	// Run crd-generate in a container.
	// The script:
	// 1. Installs the pinned toolchain from the staged lockfile
	// 2. Runs crd-generate to produce TypeScript source
	// 3. Compiles TypeScript to JavaScript
	if err := r.Generate(
		ctx,
		workFS,
		".",
		"",
		typescriptImage,
		[]string{
			"sh", "-c",
			`set -eu

# Install the pinned toolchain. package.json and package-lock.json are staged
# by the generator, so npm ci installs exactly the locked tree and fails if the
# two ever disagree.
npm ci --no-audit --no-fund

# Run crd-generate (reads config from package.json)
npx crd-generate

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

# crd-generate emits _schemas/ as pre-compiled JS (not TypeScript), so tsc does
# not process it and it never appears in dist/. Copy it directly from gen/.
if [ -d gen/_schemas ]; then
  cp -r gen/_schemas models/
fi

# Update package.json for distribution (remove devDependencies and crd-generate config)
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
`,
		},
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
// The function scaffold sets install-links=true so the models are copied into
// node_modules rather than symlinked, and npm treats a file: dependency as
// satisfied while its spec is unchanged — so models generated after the first
// install do not reach the tree on their own.
//
// A changing version is what lets `npm update` recover from that. Measured
// against a build with a constant 0.0.0: `npm update` does not pick up new
// models, and does once the version tracks the content. `npm install
// crossplane-models` works either way, by naming the package and forcing npm to
// re-resolve it; bare `npm install`, `npm install --force`, `npm rebuild` and
// `npm update crossplane-models` all fail regardless.
//
// It also makes a stale copy visible, which matters because the symptom is a
// TS2307 pointing at the import rather than at the copy: comparing this version
// against the one in node_modules answers it in one step.
//
// `crossplane project build` is unaffected either way — it tars a fresh tree
// into the build container every time.
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
