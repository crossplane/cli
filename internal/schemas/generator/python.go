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
	pythonAdoptModelsStructure = "sorted"
	pythonGeneratedFolder      = "models/workdir"
	pythonImage                = "xpkg.upbound.io/upbound/datamodel-code-generator:v0.31.2"
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

	// Walk the virtual filesystem to find and process target files
	if err := afero.Walk(fromFS, "", func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}
		// Ignore files without yaml extensions.
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
			// Process the XRD and get the paths
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
		// Return nil if no files were generated
		return nil, nil
	}

	// Generate Python schemas using common function
	if err := p.generatePythonSchemas(ctx, crdFS, baseFolder, generator); err != nil {
		return nil, err
	}

	// reorganization alignment https://github.com/koxudaxi/datamodel-code-generator/issues/2097
	if err := postTransformCRD(crdFS, pythonGeneratedFolder, pythonAdoptModelsStructure); err != nil {
		return nil, err
	}

	// Copy only the files from pythonAdoptModelsStructure into the schemaFs
	if err := filesystem.CopyFilesBetweenFs(afero.NewBasePathFs(crdFS, pythonAdoptModelsStructure), afero.NewBasePathFs(schemaFS, pythonModelsFolder)); err != nil {
		return nil, err
	}

	return schemaFS, nil
}

// GenerateFromOpenAPI generates Python schema files from OpenAPI specifications in fromFS.
func (p pythonGenerator) GenerateFromOpenAPI(ctx context.Context, fromFS afero.Fs, generator runner.SchemaRunner) (afero.Fs, error) {
	openapiFS := afero.NewMemMapFs()
	schemaFS := afero.NewMemMapFs()
	baseFolder := "workdir"

	if err := openapiFS.MkdirAll(baseFolder, 0o755); err != nil {
		return nil, err
	}

	var openapiPaths []string

	// Walk the virtual filesystem to find and process OpenAPI JSON files
	if err := afero.Walk(fromFS, "", func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		// Only process JSON files
		if !strings.HasSuffix(strings.ToLower(path), ".json") {
			return nil
		}

		// Read the file content
		bs, err := afero.ReadFile(fromFS, path)
		if err != nil {
			return errors.Wrapf(err, "failed to read file %q", path)
		}

		// Parse the OpenAPI document once
		loader := openapi3.NewLoader()
		doc, err := loader.LoadFromData(bs)
		if err != nil {
			// If parsing fails, use original content
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

		// Check if we should skip this file based on its pattern
		if shouldSkipOpenAPIFile(doc) {
			return nil
		}

		// Process the OpenAPI content to add default values
		processedDoc := processOpenAPIContent(doc)
		processedContent, err := processedDoc.MarshalJSON()
		if err != nil {
			// If marshaling fails, use original content
			processedContent = bs
		}

		// Write OpenAPI file to working directory
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
		// Return nil if no files were generated
		return nil, nil
	}

	// Generate Python schemas using common function
	if err := p.generatePythonSchemas(ctx, openapiFS, baseFolder, generator); err != nil {
		return nil, err
	}

	if err := postTransformOpenAPI(openapiFS, pythonGeneratedFolder, pythonAdoptModelsStructure); err != nil {
		return nil, err
	}

	// Copy the generated models to the schema filesystem
	if err := filesystem.CopyFilesBetweenFs(afero.NewBasePathFs(openapiFS, pythonAdoptModelsStructure), afero.NewBasePathFs(schemaFS, pythonModelsFolder)); err != nil {
		return nil, err
	}

	return schemaFS, nil
}

// generatePythonSchemas runs the datamodel code generator with common arguments.
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

// postTransformCRD combines the reorganization of Python files and the adjustment of import paths into one pass.
func postTransformCRD(fs afero.Fs, sourceDir, targetDir string) error { //nolint:gocognit // we need this python transforms
	v1MetaCopied := false // Flag to track if v1.py has already been moved
	createdInitFiles := make(map[string]bool)

	// Walk through the source directory to handle both reorganization and import adjustment
	return afero.Walk(fs, sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return errors.Wrapf(err, "walking path %s", path)
		}

		// If this is the `v1.py` file within `k8s/apimachinery/pkg/apis/meta`, move it once
		if info.Name() == "v1.py" && strings.Contains(path, filepath.Join("io", "k8s", "apimachinery", "pkg", "apis", "meta")) {
			if !v1MetaCopied {
				destDir := filepath.Join(targetDir, "io", "k8s", "apimachinery", "pkg", "apis", "meta")
				destPath := filepath.Join(destDir, "v1.py")

				// Read file content and write it to the new destination
				data, err := afero.ReadFile(fs, path)
				if err != nil {
					return errors.Wrapf(err, "failed to read %s", path)
				}

				// Get the source file's permissions
				fileInfo, err := fs.Stat(path)
				if err != nil {
					return errors.Wrapf(err, "failed to get file info for %s", path)
				}

				// Use the source file's permissions instead of os.ModePerm
				if err := afero.WriteFile(fs, destPath, data, fileInfo.Mode()); err != nil {
					return errors.Wrapf(err, "failed to write %s", destPath)
				}

				// Create __init__.py in the same directory if it doesn't exist
				initFilePath := filepath.Join(destDir, "__init__.py")
				if err := afero.WriteFile(fs, initFilePath, []byte(""), os.ModePerm); err != nil {
					return errors.Wrapf(err, "failed to create __init__.py in %s", destDir)
				}

				v1MetaCopied = true // Ensure we copy it only once
			}
			return nil // Skip subsequent meta v1.py files
		}

		// Process only schema files
		isDir := info.IsDir()
		isNotPythonFile := filepath.Ext(info.Name()) != ".py"
		// Define the path segment to skip
		skipPathSegment := filepath.Join("io", "k8s", "apimachinery", "pkg", "apis", "meta")
		isInSkipPath := strings.Contains(filepath.ToSlash(path), skipPathSegment)
		isInitFile := info.Name() == "__init__.py"

		if isDir || isNotPythonFile || isInSkipPath || isInitFile {
			return nil
		}

		// Process the reorganization logic
		relPath, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return errors.Wrap(err, "calculating relative path")
		}
		dirSegments := strings.Split(filepath.ToSlash(filepath.Dir(relPath)), "/")

		// Extract API version and segments before it
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

		// If no known API version is found, default to "unknown"
		if apiVersion == "" || rootFolder == "" {
			apiVersion = "unknown"
		}

		// Build the destination directory
		slices.Reverse(preVersionSegments)
		orderedPath := filepath.Join(preVersionSegments...)
		rootWithoutVersion := strings.ReplaceAll(rootFolder, apiVersion, "")
		rootParts := strings.Split(rootWithoutVersion, "_")
		kind := rootParts[len(rootParts)-1] // Extract the kind

		// Prepare destination path
		newFileName := fmt.Sprintf("%s.py", apiVersion)
		// Check if orderedPath already ends with kind to avoid duplication (e.g., gateway/gateway)
		var destinationDir string
		if orderedPath != "" && filepath.Base(orderedPath) == kind {
			// orderedPath already ends with kind, don't append it again
			destinationDir = filepath.Join(targetDir, orderedPath)
		} else {
			// Append kind to the path
			destinationDir = filepath.Join(targetDir, orderedPath, kind)
		}
		destinationPath := filepath.Join(destinationDir, newFileName)

		// Create the destination directory
		if err := fs.MkdirAll(destinationDir, os.ModePerm); err != nil {
			return errors.Wrapf(err, "creating directory %s", destinationDir)
		}

		// Read the file content and move it
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

		// Ensure an __init__.py is created in the destination directory if it doesn't exist
		initFilePath := filepath.Join(destinationDir, "__init__.py")
		if !createdInitFiles[destinationDir] {
			if err := afero.WriteFile(fs, initFilePath, []byte(""), os.ModePerm); err != nil {
				return errors.Wrapf(err, "creating __init__.py in %s", destinationDir)
			}
			createdInitFiles[destinationDir] = true
		}

		// Adjust the imports for the moved file
		if err := adjustImportsInFile(fs, destinationPath); err != nil {
			return errors.Wrapf(err, "adjusting imports in %s", destinationPath)
		}

		return nil
	})
}

// adjustImportsInFile modifies the import statements in the file to ensure correct depth.
func adjustImportsInFile(fs afero.Fs, filePath string) error {
	// Count the number of directories deep the file is
	depth := strings.Count(filePath, string(os.PathSeparator))

	// Read the file content
	fileContent, err := afero.ReadFile(fs, filePath)
	if err != nil {
		return errors.Wrapf(err, "error reading file %s", filePath)
	}

	// Modify the file line by line to adjust the specific imports
	modifiedContent := []string{}
	scanner := bufio.NewScanner(strings.NewReader(string(fileContent)))
	for scanner.Scan() {
		line := scanner.Text()
		// Adjust imports that contain `k8s.apimachinery.pkg.apis.meta`
		if strings.Contains(line, "k8s.apimachinery.pkg.apis.meta") {
			// Use adjustLeadingDots for CRD context
			line = adjustLeadingDots(line, depth)
		}
		modifiedContent = append(modifiedContent, line)
	}

	// Write back the modified file content
	if err := afero.WriteFile(fs, filePath, []byte(strings.Join(modifiedContent, "\n")), os.ModePerm); err != nil {
		return errors.Wrapf(err, "error writing modified file %s", filePath)
	}

	return nil
}

// Adjusts the number of leading dots in the `io.k8s.apimachinery.pkg.apis.meta` import statement
// based on the file's depth.
func adjustLeadingDots(importLine string, depth int) string {
	dotPart := ""
	// Check for either `io.k8s.apimachinery.pkg.apis.meta` or `k8s.apimachinery.pkg.apis.meta`
	var basePath string
	if strings.Contains(importLine, "io.k8s.apimachinery.pkg.apis.meta") {
		basePath = "io.k8s.apimachinery.pkg.apis.meta"
		// Add the correct number of leading dots based on depth
		dotPart = strings.Repeat(".", depth)
	} else if strings.Contains(importLine, "k8s.apimachinery.pkg.apis.meta") {
		basePath = "k8s.apimachinery.pkg.apis.meta"
		// Add the correct number of leading dots based on depth - 1 because "io" is same base-folder
		if depth > 1 {
			dotPart = strings.Repeat(".", depth-1)
		} else {
			dotPart = ""
		}
	}

	// Process the line if a valid base path is found
	if basePath != "" {
		// Split the line into parts: the leading dots + the import path
		parts := strings.SplitN(importLine, basePath, 2)

		// Construct the new line with the correct leading dots
		return "from " + dotPart + basePath + parts[1]
	}

	return importLine
}

// shouldSkipOpenAPIFile checks if the OpenAPI file should be skipped.
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

// processOpenAPIContent processes OpenAPI content to add default values for apiVersion and kind.
func processOpenAPIContent(doc *openapi3.T) *openapi3.T { //nolint:gocognit // set default apiVersion and kind.
	if doc.Components == nil {
		return doc
	}

	for _, schemaRef := range doc.Components.Schemas {
		if schemaRef == nil || schemaRef.Value == nil {
			continue
		}
		schema := schemaRef.Value

		// Look for x-kubernetes-group-version-kind extension
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

		// Add defaults to properties
		if schema.Properties != nil {
			// Add default to apiVersion property
			if propSchemaRef, ok := schema.Properties["apiVersion"]; ok {
				if propSchemaRef != nil && propSchemaRef.Value != nil {
					propSchemaRef.Value.Default = apiVersion
				}
			}
			// Add default to kind property
			if propSchemaRef, ok := schema.Properties["kind"]; ok {
				if propSchemaRef != nil && propSchemaRef.Value != nil {
					propSchemaRef.Value.Default = kind
				}
			}
		}
	}

	// Return the modified document
	return doc
}

// fixAliasedTypesInFile replaces bool_aliased and int_aliased with bool and int in Python files.
func fixAliasedTypesInFile(fs afero.Fs, filePath string) error {
	// Read the file content
	fileContent, err := afero.ReadFile(fs, filePath)
	if err != nil {
		return errors.Wrapf(err, "reading file %s", filePath)
	}

	// Replace bool_aliased with bool and int_aliased with int
	// https://github.com/koxudaxi/datamodel-code-generator/issues/2431
	content := string(fileContent)
	content = strings.ReplaceAll(content, "bool_aliased", "bool")
	content = strings.ReplaceAll(content, "int_aliased", "int")

	// Write back the modified content
	if err := afero.WriteFile(fs, filePath, []byte(content), os.ModePerm); err != nil {
		return errors.Wrapf(err, "writing modified file %s", filePath)
	}

	return nil
}

// postTransformOpenAPI consolidates the generated OpenAPI Python files into a unified structure.
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

		// Special handling for io/k8s/apimachinery/pkg/apis/meta/v1.py
		if isMetaV1File(destPath) {
			destPath, destDir = transformMetaV1Path(targetDir, destPath)
		}

		if err := copyFileWithInit(fs, path, destPath, destDir, createdInitDirs); err != nil {
			return err
		}

		if err := postProcessFile(fs, destPath); err != nil {
			return err
		}

		// Transform meta imports after postProcessFile
		if err := transformMetaImportsInFile(fs, destPath); err != nil {
			return err
		}

		return nil
	})
}

func shouldSkipFile(info os.FileInfo) bool {
	if info.IsDir() || info.Name() == "__init__.py" || filepath.Ext(info.Name()) != ".py" {
		return true
	}
	return false
}

func normalizeAndFilterPath(relPath string) (openapiFolder string, normalizedParts []string, include bool) {
	parts := strings.Split(filepath.ToSlash(relPath), "/")
	if len(parts) == 0 {
		return "", nil, false
	}

	// Identify the OpenAPI folder
	for _, part := range parts {
		if strings.HasSuffix(part, "_openapi") {
			openapiFolder = part
			break
		}
	}

	// Locate io/k8s onwards
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

	// Filtering rules
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
					fileAPIGroup == k8sPkgNameAutoscaling || fileAPIGroup == "policy") &&
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
	if err := fixAliasedTypesInFile(fs, path); err != nil {
		return errors.Wrapf(err, "fixing aliased types")
	}
	return nil
}

// isMetaV1File returns true if path ends in "apis/meta/v1.py"..
func isMetaV1File(path string) bool {
	// filepath.ToSlash makes sure we use "/" even on Windows
	return strings.HasSuffix(filepath.ToSlash(path), "apis/meta/v1.py")
}

// transformMetaV1Path replaces "/apis/meta/v1.py" with "/apis/core/meta/v1.py".
func transformMetaV1Path(targetDir, inPath string) (destPath, destDir string) {
	// Get the relative part after targetDir
	rel, _ := filepath.Rel(targetDir, inPath)
	rel = filepath.ToSlash(rel)

	// Replace apis/meta/v1.py with apis/core/meta/v1.py
	newRel := strings.Replace(rel,
		"apis/meta/v1.py",
		"apis/core/meta/v1.py",
		1,
	)

	// Build the full destination path
	destPath = filepath.Join(targetDir, filepath.FromSlash(newRel))
	destDir = filepath.Dir(destPath)
	return
}

// transformMetaImport turns "apis.meta" → "apis.core.meta" in OpenAPI contexts.
// otherwise meta schemas are overridden from crds which are different.
func transformMetaImport(importLine string) string {
	parts := importRE.FindStringSubmatch(importLine)
	if parts == nil {
		return importLine
	}
	prefix, dots, modPath, suffix := parts[1], parts[2], parts[3], parts[4]

	// only tweak if it's actually a meta import
	if !strings.Contains(modPath, "apis.meta") {
		return importLine
	}

	newPath := strings.Replace(modPath, "apis.meta", "apis.core.meta", 1)
	return prefix + dots + newPath + suffix
}

// transformMetaImportsInFile transforms imports of meta.v1 to core.meta.v1..
func transformMetaImportsInFile(fs afero.Fs, filePath string) error {
	// Read the file content
	fileContent, err := afero.ReadFile(fs, filePath)
	if err != nil {
		return errors.Wrapf(err, "error reading file %s", filePath)
	}

	// Check if this is the core/meta/v1.py file
	isCoreMeta := strings.HasSuffix(filepath.ToSlash(filePath), "core/meta/v1.py")

	// Modify the file line by line to transform meta imports
	modifiedContent := []string{}
	scanner := bufio.NewScanner(strings.NewReader(string(fileContent)))
	for scanner.Scan() {
		line := scanner.Text()
		// Transform imports that contain apis.meta
		if strings.Contains(line, "apis.meta") {
			line = transformMetaImport(line)
		}
		// If this is the core/meta/v1.py file, add one more dot to relative imports
		if isCoreMeta {
			line = adjustRelativeImportsForCoreMeta(line)
		}
		modifiedContent = append(modifiedContent, line)
	}

	// Write back the modified file content
	if err := afero.WriteFile(fs, filePath, []byte(strings.Join(modifiedContent, "\n")), os.ModePerm); err != nil {
		return errors.Wrapf(err, "error writing modified file %s", filePath)
	}

	return nil
}

// adjustRelativeImportsForCoreMeta adds one more dot to relative imports in core/meta/v1.py..
func adjustRelativeImportsForCoreMeta(line string) string {
	// Use regex to match relative imports
	matches := importRE.FindStringSubmatch(line)
	if matches == nil {
		return line
	}

	prefix, dots, modPath, suffix := matches[1], matches[2], matches[3], matches[4]

	// Only adjust if it's a relative import (has dots)
	if len(dots) > 0 {
		// Add one more dot since we're one directory deeper
		dots = "." + dots
		return prefix + dots + modPath + suffix
	}

	return line
}
