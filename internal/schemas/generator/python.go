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
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/spf13/afero"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	xpv1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1"

	"github.com/crossplane/cli/v2/internal/crd"
	"github.com/crossplane/cli/v2/internal/filesystem"
	"github.com/crossplane/cli/v2/internal/schemas/runner"
)

const (
	pythonModelsFolder         = "models"
	pythonPackageRoot          = "models"
	pythonAdoptModelsStructure = "sorted"
	pythonGeneratedFolder      = "models/workdir"
	pythonImage                = "docker.io/koxudaxi/datamodel-code-generator:0.31.2"
)

var importRE = regexp.MustCompile(`^(from\s+)(\.*)([^\s]+)(.*)`)

type pythonGenerator struct{}

func (pythonGenerator) Language() string {
	return "python"
}

// GenerateFromCRD generates Python schema files from the XRDs and CRDs fromFS.
func (p pythonGenerator) GenerateFromCRD(ctx context.Context, fromFS afero.Fs, generator runner.SchemaRunner) (afero.Fs, error) { //nolint:gocognit // generation of schemas for python
	crdFS := afero.NewMemMapFs()
	schemaFS := afero.NewMemMapFs()
	baseFolder := "workdir"

	if err := crdFS.MkdirAll(baseFolder, 0o755); err != nil {
		return nil, err
	}

	var openAPIPaths []string

	if err := afero.Walk(fromFS, "", func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}
		ext := filepath.Ext(path)
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}

		var u metav1.TypeMeta
		bs, err := afero.ReadFile(fromFS, path)
		if err != nil {
			return errors.Wrapf(err, "failed to read file %q", path)
		}
		err = yaml.Unmarshal(bs, &u)
		if err != nil {
			return errors.Wrapf(err, "failed to parse file %q", path)
		}

		switch u.GroupVersionKind().Kind {
		case xpv1.CompositeResourceDefinitionKind:
			xrPath, claimPath, err := crd.ProcessXRD(crdFS, bs, path, baseFolder)
			if err != nil {
				return err
			}

			if xrPath != "" {
				bs, err := afero.ReadFile(crdFS, xrPath)
				if err != nil {
					return errors.Wrapf(err, "failed to read file %q", path)
				}
				paths, err := crd.FilesToOpenAPI(crdFS, bs, xrPath)
				if err != nil {
					return err
				}
				openAPIPaths = append(openAPIPaths, paths...)
			}
			if claimPath != "" {
				bs, err := afero.ReadFile(crdFS, claimPath)
				if err != nil {
					return errors.Wrapf(err, "failed to read file %q", path)
				}
				paths, err := crd.FilesToOpenAPI(crdFS, bs, claimPath)
				if err != nil {
					return err
				}
				openAPIPaths = append(openAPIPaths, paths...)
			}

		case "CustomResourceDefinition":
			paths, err := crd.FilesToOpenAPI(crdFS, bs, path)
			if err != nil {
				return err
			}
			openAPIPaths = append(openAPIPaths, paths...)
		}
		return nil
	}); err != nil {
		return nil, err
	}

	if len(openAPIPaths) == 0 {
		return nil, nil
	}

	if err := p.generatePythonSchemas(ctx, crdFS, baseFolder, generator); err != nil {
		return nil, err
	}

	if err := postTransformCRD(crdFS, pythonGeneratedFolder, pythonAdoptModelsStructure); err != nil {
		return nil, err
	}

	if err := filesystem.CopyFilesBetweenFs(afero.NewBasePathFs(crdFS, pythonAdoptModelsStructure), afero.NewBasePathFs(schemaFS, filepath.Join(pythonModelsFolder, pythonPackageRoot))); err != nil {
		return nil, err
	}

	if err := finalizePythonSchemas(schemaFS); err != nil {
		return nil, err
	}

	return schemaFS, nil
}

// GenerateFromOpenAPI generates Python schema files from OpenAPI specifications.
func (p pythonGenerator) GenerateFromOpenAPI(ctx context.Context, fromFS afero.Fs, generator runner.SchemaRunner) (afero.Fs, error) { //nolint:gocognit // generation of schemas for python
	openapiFS := afero.NewMemMapFs()
	schemaFS := afero.NewMemMapFs()
	baseFolder := "workdir"

	if err := openapiFS.MkdirAll(baseFolder, 0o755); err != nil {
		return nil, err
	}

	var openapiPaths []string

	if err := afero.Walk(fromFS, "", func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		if !strings.HasSuffix(strings.ToLower(path), ".json") {
			return nil
		}

		bs, err := afero.ReadFile(fromFS, path)
		if err != nil {
			return errors.Wrapf(err, "failed to read file %q", path)
		}

		loader := openapi3.NewLoader()
		doc, err := loader.LoadFromData(bs)
		if err != nil {
			processedContent := bs
			targetPath := filepath.Join(baseFolder, path)
			if err := openapiFS.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
				return errors.Wrapf(err, "failed to create directory for %q", targetPath)
			}
			if err := afero.WriteFile(openapiFS, targetPath, processedContent, 0o644); err != nil {
				return errors.Wrapf(err, "failed to write file %q", targetPath)
			}
			return nil
		}

		if shouldSkipOpenAPIFile(doc) {
			return nil
		}

		processedDoc := processOpenAPIContent(doc)
		processedContent, err := processedDoc.MarshalJSON()
		if err != nil {
			processedContent = bs
		}

		targetPath := filepath.Join(baseFolder, path)
		if err := openapiFS.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return err
		}
		if err := afero.WriteFile(openapiFS, targetPath, processedContent, 0o644); err != nil {
			return err
		}
		openapiPaths = append(openapiPaths, targetPath)

		return nil
	}); err != nil {
		return nil, err
	}

	if len(openapiPaths) == 0 {
		return nil, nil
	}

	if err := p.generatePythonSchemas(ctx, openapiFS, baseFolder, generator); err != nil {
		return nil, err
	}

	if err := postTransformOpenAPI(openapiFS, pythonGeneratedFolder, pythonAdoptModelsStructure); err != nil {
		return nil, err
	}

	if err := filesystem.CopyFilesBetweenFs(afero.NewBasePathFs(openapiFS, pythonAdoptModelsStructure), afero.NewBasePathFs(schemaFS, filepath.Join(pythonModelsFolder, pythonPackageRoot))); err != nil {
		return nil, err
	}

	if err := finalizePythonSchemas(schemaFS); err != nil {
		return nil, err
	}

	return schemaFS, nil
}

func (p pythonGenerator) generatePythonSchemas(ctx context.Context, inputFS afero.Fs, baseFolder string, generator runner.SchemaRunner) error {
	return generator.Generate(
		ctx,
		inputFS,
		baseFolder,
		"",
		pythonImage,
		[]string{
			"--input-file-type",
			"openapi",
			"--disable-timestamp",
			"--input",
			".",
			"--output-model-type",
			"pydantic_v2.BaseModel",
			"--target-python-version",
			"3.12",
			"--use-field-description",
			"--enum-field-as-literal",
			"all",
			"--use-one-literal-as-default",
			"--output",
			pythonModelsFolder,
		},
	)
}

func postTransformCRD(fs afero.Fs, sourceDir, targetDir string) error { //nolint:gocognit // python transforms
	v1MetaCopied := false
	createdInitFiles := make(map[string]bool)

	return afero.Walk(fs, sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return errors.Wrapf(err, "walking path %s", path)
		}

		if info.Name() == "v1.py" && strings.Contains(path, filepath.Join("io", "k8s", "apimachinery", "pkg", "apis", "meta")) {
			if !v1MetaCopied {
				destDir := filepath.Join(targetDir, "io", "k8s", "apimachinery", "pkg", "apis", "meta")
				destPath := filepath.Join(destDir, "v1.py")

				data, err := afero.ReadFile(fs, path)
				if err != nil {
					return errors.Wrapf(err, "failed to read %s", path)
				}

				fileInfo, err := fs.Stat(path)
				if err != nil {
					return errors.Wrapf(err, "failed to get file info for %s", path)
				}

				if err := afero.WriteFile(fs, destPath, data, fileInfo.Mode()); err != nil {
					return errors.Wrapf(err, "failed to write %s", destPath)
				}

				initFilePath := filepath.Join(destDir, "__init__.py")
				if err := afero.WriteFile(fs, initFilePath, []byte(""), os.ModePerm); err != nil {
					return errors.Wrapf(err, "failed to create __init__.py in %s", destDir)
				}

				v1MetaCopied = true
			}
			return nil
		}

		isDir := info.IsDir()
		isNotPythonFile := filepath.Ext(info.Name()) != ".py"
		skipPathSegment := filepath.Join("io", "k8s", "apimachinery", "pkg", "apis", "meta")
		isInSkipPath := strings.Contains(filepath.ToSlash(path), skipPathSegment)
		isInitFile := info.Name() == "__init__.py"

		if isDir || isNotPythonFile || isInSkipPath || isInitFile {
			return nil
		}

		relPath, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return errors.Wrap(err, "calculating relative path")
		}
		dirSegments := strings.Split(filepath.ToSlash(filepath.Dir(relPath)), "/")

		var apiVersion, rootFolder string
		var preVersionSegments []string
		for _, dirSegment := range dirSegments {
			for subSegment := range strings.SplitSeq(dirSegment, "_") {
				if isAPIVersion(subSegment) {
					apiVersion = subSegment
					rootFolder = dirSegment
					break
				}
				preVersionSegments = append(preVersionSegments, subSegment)
			}
			if apiVersion != "" {
				break
			}
		}

		if apiVersion == "" || rootFolder == "" {
			apiVersion = "unknown"
		}

		slices.Reverse(preVersionSegments)
		orderedPath := filepath.Join(preVersionSegments...)
		rootWithoutVersion := strings.ReplaceAll(rootFolder, apiVersion, "")
		rootParts := strings.Split(rootWithoutVersion, "_")
		kind := rootParts[len(rootParts)-1]

		newFileName := fmt.Sprintf("%s.py", apiVersion)
		var destinationDir string
		if orderedPath != "" && filepath.Base(orderedPath) == kind {
			destinationDir = filepath.Join(targetDir, orderedPath)
		} else {
			destinationDir = filepath.Join(targetDir, orderedPath, kind)
		}
		destinationPath := filepath.Join(destinationDir, newFileName)

		if err := fs.MkdirAll(destinationDir, os.ModePerm); err != nil {
			return errors.Wrapf(err, "creating directory %s", destinationDir)
		}

		data, err := afero.ReadFile(fs, path)
		if err != nil {
			return errors.Wrapf(err, "reading file %s", path)
		}
		if err := afero.WriteFile(fs, destinationPath, data, os.ModePerm); err != nil {
			return errors.Wrapf(err, "writing file %s", destinationPath)
		}
		if err := fs.Remove(path); err != nil {
			return errors.Wrapf(err, "deleting original file %s", path)
		}

		initFilePath := filepath.Join(destinationDir, "__init__.py")
		if !createdInitFiles[destinationDir] {
			if err := afero.WriteFile(fs, initFilePath, []byte(""), os.ModePerm); err != nil {
				return errors.Wrapf(err, "creating __init__.py in %s", destinationDir)
			}
			createdInitFiles[destinationDir] = true
		}

		if err := adjustImportsInFile(fs, destinationPath); err != nil {
			return errors.Wrapf(err, "adjusting imports in %s", destinationPath)
		}

		return nil
	})
}

func adjustImportsInFile(fs afero.Fs, filePath string) error {
	depth := strings.Count(filePath, string(os.PathSeparator))

	fileContent, err := afero.ReadFile(fs, filePath)
	if err != nil {
		return errors.Wrapf(err, "error reading file %s", filePath)
	}

	modifiedContent := []string{}
	scanner := bufio.NewScanner(strings.NewReader(string(fileContent)))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "k8s.apimachinery.pkg.apis.meta") {
			line = adjustLeadingDots(line, depth)
		}
		modifiedContent = append(modifiedContent, line)
	}

	return afero.WriteFile(fs, filePath, []byte(strings.Join(modifiedContent, "\n")), os.ModePerm)
}

func adjustLeadingDots(importLine string, depth int) string {
	dotPart := ""
	var basePath string
	if strings.Contains(importLine, "io.k8s.apimachinery.pkg.apis.meta") {
		basePath = "io.k8s.apimachinery.pkg.apis.meta"
		dotPart = strings.Repeat(".", depth)
	} else if strings.Contains(importLine, "k8s.apimachinery.pkg.apis.meta") {
		basePath = "k8s.apimachinery.pkg.apis.meta"
		if depth > 1 {
			dotPart = strings.Repeat(".", depth-1)
		}
	}

	if basePath != "" {
		parts := strings.SplitN(importLine, basePath, 2)
		return "from " + dotPart + basePath + parts[1]
	}

	return importLine
}

func shouldSkipOpenAPIFile(doc *openapi3.T) bool {
	if doc.Components == nil {
		return false
	}

	for _, schemaRef := range doc.Components.Schemas {
		if schemaRef == nil || schemaRef.Value == nil {
			continue
		}
		ext, ok := schemaRef.Value.Extensions["x-kubernetes-group-version-kind"]
		if !ok {
			continue
		}

		extBytes, err := json.Marshal(ext)
		if err != nil {
			continue
		}

		var gvkList []map[string]any
		if err := json.Unmarshal(extBytes, &gvkList); err != nil {
			continue
		}

		for _, gvk := range gvkList {
			if kindRaw, ok := gvk["kind"]; ok {
				if kind, ok := kindRaw.(string); ok {
					if kind == "APIVersions" || kind == "APIGroup" {
						return true
					}
				}
			}
		}
	}

	return false
}

func processOpenAPIContent(doc *openapi3.T) *openapi3.T { //nolint:gocognit // set default apiVersion and kind.
	if doc.Components == nil {
		return doc
	}

	for _, schemaRef := range doc.Components.Schemas {
		if schemaRef == nil || schemaRef.Value == nil {
			continue
		}
		schema := schemaRef.Value

		rawExt, ok := schema.Extensions["x-kubernetes-group-version-kind"]
		if !ok {
			continue
		}

		rawBytes, err := json.Marshal(rawExt)
		if err != nil {
			continue
		}

		var gvkList []map[string]any
		if err := json.Unmarshal(rawBytes, &gvkList); err != nil {
			continue
		}

		if len(gvkList) == 0 {
			continue
		}

		gvk := gvkList[0]
		group := ""
		if g, ok := gvk["group"].(string); ok {
			group = g
		}
		version := ""
		if v, ok := gvk["version"].(string); ok {
			version = v
		}
		kind := ""
		if k, ok := gvk["kind"].(string); ok {
			kind = k
		}

		apiVersion := version
		if group != "" {
			apiVersion = group + "/" + version
		}

		if schema.Properties != nil {
			if propSchemaRef, ok := schema.Properties["apiVersion"]; ok {
				if propSchemaRef != nil && propSchemaRef.Value != nil {
					propSchemaRef.Value.Default = apiVersion
				}
			}
			if propSchemaRef, ok := schema.Properties["kind"]; ok {
				if propSchemaRef != nil && propSchemaRef.Value != nil {
					propSchemaRef.Value.Default = kind
				}
			}
		}
	}

	return doc
}

func fixAliasedTypesInFile(fs afero.Fs, filePath string) error {
	fileContent, err := afero.ReadFile(fs, filePath)
	if err != nil {
		return errors.Wrapf(err, "reading file %s", filePath)
	}

	content := string(fileContent)
	content = strings.ReplaceAll(content, "bool_aliased", "bool")
	content = strings.ReplaceAll(content, "int_aliased", "int")

	return afero.WriteFile(fs, filePath, []byte(content), os.ModePerm)
}

func postTransformOpenAPI(fs afero.Fs, sourceDir, targetDir string) error {
	createdInitDirs := make(map[string]bool)

	return afero.Walk(fs, sourceDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return errors.Wrapf(walkErr, "walking path %s", path)
		}

		if shouldSkipFile(info) {
			return nil
		}

		relPath, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return errors.Wrap(err, "calculating relative path")
		}

		_, normalizedParts, include := normalizeAndFilterPath(relPath)
		if !include {
			return nil
		}

		destPath, destDir := computeDestPath(targetDir, normalizedParts)

		if isMetaV1File(destPath) {
			destPath, destDir = transformMetaV1Path(targetDir, destPath)
		}

		if err := copyFileWithInit(fs, path, destPath, destDir, createdInitDirs); err != nil {
			return err
		}

		if err := postProcessFile(fs, destPath); err != nil {
			return err
		}

		return transformMetaImportsInFile(fs, destPath)
	})
}

func shouldSkipFile(info os.FileInfo) bool {
	return info.IsDir() || info.Name() == "__init__.py" || filepath.Ext(info.Name()) != ".py"
}

func normalizeAndFilterPath(relPath string) (openapiFolder string, normalizedParts []string, include bool) {
	parts := strings.Split(filepath.ToSlash(relPath), "/")
	if len(parts) == 0 {
		return "", nil, false
	}

	for _, part := range parts {
		if strings.HasSuffix(part, "_openapi") {
			openapiFolder = part
			break
		}
	}

	var foundIO bool
	for i, part := range parts {
		if part == "io" && i+1 < len(parts) && parts[i+1] == "k8s" {
			normalizedParts = parts[i:]
			foundIO = true
			break
		}
	}
	if !foundIO {
		return "", nil, false
	}

	if len(normalizedParts) >= 3 && normalizedParts[2] == "apimachinery" {
		if openapiFolder != "api__v1_openapi" {
			return "", nil, false
		}
	}

	if openapiFolder != "" && strings.HasPrefix(openapiFolder, "apis__") {
		segments := strings.Split(openapiFolder, "__")
		if len(segments) >= 2 {
			apiGroup := segments[1]
			if len(normalizedParts) >= 4 && normalizedParts[2] == "api" {
				fileAPIGroup := normalizedParts[3]
				if (fileAPIGroup == "core" || fileAPIGroup == "authentication" ||
					fileAPIGroup == "autoscaling" || fileAPIGroup == "policy") &&
					fileAPIGroup != apiGroup {
					return "", nil, false
				}
			}
		}
	}

	return openapiFolder, normalizedParts, true
}

func computeDestPath(targetDir string, normalizedParts []string) (string, string) {
	destPath := filepath.Join(append([]string{targetDir}, normalizedParts...)...)
	destDir := filepath.Dir(destPath)
	return destPath, destDir
}

func copyFileWithInit(fs afero.Fs, srcPath, destPath, destDir string, created map[string]bool) error {
	if err := fs.MkdirAll(destDir, os.ModePerm); err != nil {
		return errors.Wrapf(err, "creating directory %s", destDir)
	}

	data, err := afero.ReadFile(fs, srcPath)
	if err != nil {
		return errors.Wrapf(err, "reading %s", srcPath)
	}

	if err := afero.WriteFile(fs, destPath, data, os.ModePerm); err != nil {
		return errors.Wrapf(err, "writing %s", destPath)
	}

	if !created[destDir] {
		initPath := filepath.Join(destDir, "__init__.py")
		if err := afero.WriteFile(fs, initPath, []byte(""), os.ModePerm); err != nil {
			return errors.Wrapf(err, "creating __init__.py in %s", destDir)
		}
		created[destDir] = true
	}

	return nil
}

func postProcessFile(fs afero.Fs, path string) error {
	if err := adjustImportsInFile(fs, path); err != nil {
		return errors.Wrapf(err, "adjusting imports")
	}
	return errors.Wrapf(fixAliasedTypesInFile(fs, path), "fixing aliased types")
}

func isMetaV1File(path string) bool {
	return strings.HasSuffix(filepath.ToSlash(path), "apis/meta/v1.py")
}

func transformMetaV1Path(targetDir, inPath string) (destPath, destDir string) {
	rel, _ := filepath.Rel(targetDir, inPath)
	rel = filepath.ToSlash(rel)

	newRel := strings.Replace(rel, "apis/meta/v1.py", "apis/core/meta/v1.py", 1)

	destPath = filepath.Join(targetDir, filepath.FromSlash(newRel))
	destDir = filepath.Dir(destPath)
	return
}

func transformMetaImport(importLine string) string {
	parts := importRE.FindStringSubmatch(importLine)
	if parts == nil {
		return importLine
	}
	prefix, dots, modPath, suffix := parts[1], parts[2], parts[3], parts[4]

	if !strings.Contains(modPath, "apis.meta") {
		return importLine
	}

	newPath := strings.Replace(modPath, "apis.meta", "apis.core.meta", 1)
	return prefix + dots + newPath + suffix
}

func transformMetaImportsInFile(fs afero.Fs, filePath string) error {
	fileContent, err := afero.ReadFile(fs, filePath)
	if err != nil {
		return errors.Wrapf(err, "error reading file %s", filePath)
	}

	isCoreMeta := strings.HasSuffix(filepath.ToSlash(filePath), "core/meta/v1.py")

	modifiedContent := []string{}
	scanner := bufio.NewScanner(strings.NewReader(string(fileContent)))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "apis.meta") {
			line = transformMetaImport(line)
		}
		if isCoreMeta {
			line = adjustRelativeImportsForCoreMeta(line)
		}
		modifiedContent = append(modifiedContent, line)
	}

	return afero.WriteFile(fs, filePath, []byte(strings.Join(modifiedContent, "\n")), os.ModePerm)
}

func adjustRelativeImportsForCoreMeta(line string) string {
	matches := importRE.FindStringSubmatch(line)
	if matches == nil {
		return line
	}

	prefix, dots, modPath, suffix := matches[1], matches[2], matches[3], matches[4]

	if len(dots) > 0 {
		dots = "." + dots
		return prefix + dots + modPath + suffix
	}

	return line
}

// pythonSchemasPyproject is the pyproject.toml emitted alongside the generated
// schemas tree so it is a pip-installable package named "crossplane-models"
// that exposes a single "models" package.
const pythonSchemasPyproject = `[build-system]
requires = ["hatchling"]
build-backend = "hatchling.build"

[project]
name = "crossplane-models"
version = "0.0.0"
requires-python = ">=3.11,<3.14"

[tool.hatch.build.targets.wheel]
packages = ["models"]
`

// finalizePythonSchemas makes the generated tree a pip-installable package by
// seeding empty __init__.py files in every directory under the package root
// that lacks one and writing a pyproject.toml at the schemas root.
func finalizePythonSchemas(fsys afero.Fs) error {
	pkgRoot := filepath.Join(pythonModelsFolder, pythonPackageRoot)
	if err := afero.Walk(fsys, pkgRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return nil
		}
		initPath := filepath.Join(path, "__init__.py")
		if exists, _ := afero.Exists(fsys, initPath); !exists {
			if err := afero.WriteFile(fsys, initPath, nil, 0o644); err != nil {
				return errors.Wrapf(err, "creating __init__.py in %s", path)
			}
		}
		return nil
	}); err != nil {
		return err
	}

	return afero.WriteFile(fsys, filepath.Join(pythonModelsFolder, "pyproject.toml"), []byte(pythonSchemasPyproject), 0o644)
}
