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
	"io/fs"
	"path/filepath"
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
)

const (
	typescriptModelsFolder = "models"
	// typescriptImage is the Docker image used to run crd-generate.
	// We use a Node.js image and install the tool at runtime.
	typescriptImage = "docker.io/library/node:22-slim"
)

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

	// Run crd-generate in a container.
	// The script:
	// 1. Creates package.json with crd-generate config
	// 2. Installs crd-generate and dependencies
	// 3. Runs crd-generate to produce TypeScript source
	// 4. Compiles TypeScript to JavaScript
	if err := r.Generate(
		ctx,
		workFS,
		".",
		"",
		typescriptImage,
		[]string{
			"sh", "-c",
			`set -eu

# Create package.json with crd-generate config and dependencies
cat > package.json << 'PKGEOF'
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
  },
  "devDependencies": {
    "@kubernetes-models/crd-generate": "^6.1.0",
    "typescript": "^5.0.0"
  },
  "crd-generate": {
    "input": ["./all-crds.yaml"],
    "output": "./gen"
  }
}
PKGEOF

# Install dependencies (including crd-generate)
npm install

# Run crd-generate (reads config from package.json)
npx crd-generate

# Create tsconfig.json for compilation
cat > tsconfig.json << 'TSEOF'
{
  "compilerOptions": {
    "target": "ES2022",
    "module": "NodeNext",
    "moduleResolution": "NodeNext",
    "declaration": true,
    "declarationMap": true,
    "sourceMap": true,
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

	return schemaFS, nil
}
