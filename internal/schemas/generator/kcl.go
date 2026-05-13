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
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/afero"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kube-openapi/pkg/spec3"
	"k8s.io/kube-openapi/pkg/validation/spec"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	xpv1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1"

	xcrd "github.com/crossplane/cli/v2/internal/crd"
	"github.com/crossplane/cli/v2/internal/filesystem"
	"github.com/crossplane/cli/v2/internal/schemas/runner"
)

const (
	kclModelsFolder         = "models"
	kclAdoptModelsStructure = "sorted"
	kclImage                = "docker.io/kcllang/kcl:v0.11.2"
)

type kclGenerator struct{}

func (kclGenerator) Language() string {
	return "kcl"
}

// GenerateFromCRD generates KCL schema files from the XRDs and CRDs fromFS.
func (kclGenerator) GenerateFromCRD(ctx context.Context, fromFS afero.Fs, generator runner.SchemaRunner) (afero.Fs, error) { //nolint:gocognit // generate kcl schemas
	crdFS := afero.NewMemMapFs()
	schemaFS := afero.NewMemMapFs()
	baseFolder := "workdir"

	if err := crdFS.MkdirAll(baseFolder, 0o755); err != nil {
		return nil, err
	}

	var crdPaths []string

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
			xrPath, claimPath, err := xcrd.ProcessXRD(crdFS, bs, path, baseFolder)
			if err != nil {
				return err
			}

			if xrPath != "" {
				crdPaths = append(crdPaths, xrPath)
			}
			if claimPath != "" {
				crdPaths = append(crdPaths, claimPath)
			}

		case "CustomResourceDefinition":
			if err := afero.WriteFile(crdFS, filepath.Join(baseFolder, path), bs, 0o644); err != nil {
				return err
			}
			crdPaths = append(crdPaths, filepath.Join(baseFolder, path))
		}

		return nil
	}); err != nil {
		return nil, err
	}

	if len(crdPaths) == 0 {
		return nil, nil
	}

	if err := generator.Generate(
		ctx,
		crdFS,
		baseFolder,
		"",
		kclImage,
		[]string{
			"sh", "-c",
			`find . -name "*.yaml" -exec kcl import -m crd -s {} \;`,
		},
	); err != nil {
		return nil, err
	}

	if err := transformStructureKcl(crdFS, kclModelsFolder, kclAdoptModelsStructure); err != nil {
		return nil, err
	}

	if err := filesystem.CopyFilesBetweenFs(afero.NewBasePathFs(crdFS, kclAdoptModelsStructure), afero.NewBasePathFs(schemaFS, kclModelsFolder)); err != nil {
		return nil, err
	}

	return schemaFS, nil
}

func transformStructureKcl(fs afero.Fs, sourceDir, targetDir string) error { //nolint:gocognit // transform kcl schemas
	if err := filesystem.CopyFileIfExists(fs, filepath.Join(sourceDir, "kcl.mod"), filepath.Join(targetDir, "kcl.mod")); err != nil {
		return errors.Wrap(err, "failed to copy kcl.mod")
	}

	if err := filesystem.CopyFileIfExists(fs, filepath.Join(sourceDir, "kcl.mod.lock"), filepath.Join(targetDir, "kcl.mod.lock")); err != nil {
		return errors.Wrap(err, "failed to copy kcl.mod.lock")
	}

	objectMetaPath := filepath.Join(sourceDir, "k8s", "apimachinery", "pkg", "apis", "meta", "v1", "object_meta.k")
	managedFieldsEntryPath := filepath.Join(sourceDir, "k8s", "apimachinery", "pkg", "apis", "meta", "v1", "managed_fields_entry.k")

	if _, err := fs.Stat(objectMetaPath); err == nil {
		content, err := afero.ReadFile(fs, objectMetaPath)
		if err != nil {
			return errors.Wrapf(err, "failed to read %s", objectMetaPath)
		}

		updatedContent := strings.ReplaceAll(string(content), "managedFields?: [ManagedFieldsEntry]", "managedFields?: any")

		if err := afero.WriteFile(fs, objectMetaPath, []byte(updatedContent), 0o644); err != nil {
			return errors.Wrapf(err, "failed to update %s", objectMetaPath)
		}
	}

	if _, err := fs.Stat(managedFieldsEntryPath); err == nil {
		if err := fs.Remove(managedFieldsEntryPath); err != nil {
			return errors.Wrapf(err, "failed to remove %s", managedFieldsEntryPath)
		}
	}

	k8sSourcePath := filepath.Join(sourceDir, "k8s")
	if err := filesystem.CopyFolder(fs, k8sSourcePath, filepath.Join(targetDir, "k8s")); err != nil {
		return errors.Wrap(err, "failed to copy k8s directory")
	}

	if err := afero.Walk(fs, sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() || strings.HasPrefix(path, filepath.Join(sourceDir, "k8s")) {
			return nil
		}

		filename := info.Name()
		parts := strings.Split(filename, "_")

		var versionIndex int
		foundVersion := false

		for i, part := range parts {
			if isAPIVersion(part) {
				versionIndex = i
				foundVersion = true
				break
			}
		}

		if !foundVersion || versionIndex == 0 {
			return nil
		}

		reversedParts := parts[:versionIndex]
		slices.Reverse(reversedParts)
		reversedParts = append(reversedParts, parts[versionIndex])

		newDir := filepath.Join(targetDir, filepath.Join(reversedParts...))

		if err := fs.MkdirAll(newDir, 0o755); err != nil {
			return errors.Wrapf(err, "failed to create directory %s", newDir)
		}

		transformedName := strings.ReplaceAll(strings.Join(parts[versionIndex+1:], ""), "_", "")
		transformedName = strings.ReplaceAll(transformedName, "swagger", "")

		newFilePath := filepath.Join(newDir, transformedName)

		srcFile, err := fs.Open(path)
		if err != nil {
			return errors.Wrapf(err, "failed to open source file %s", path)
		}

		destFile, err := fs.Create(newFilePath)
		if err != nil {
			return errors.Wrapf(err, "failed to create destination file %s", newFilePath)
		}

		_, err = io.Copy(destFile, srcFile)
		if err != nil {
			return errors.Wrapf(err, "failed to copy file from %s to %s", path, newFilePath)
		}

		return nil
	}); err != nil {
		return errors.Wrap(err, "error processing directory")
	}

	return nil
}

// GenerateFromOpenAPI generates KCL schema files from OpenAPI v3 specifications.
func (kclGenerator) GenerateFromOpenAPI(_ context.Context, fromFS afero.Fs, _ runner.SchemaRunner) (afero.Fs, error) {
	schemaFS := afero.NewMemMapFs()

	if err := schemaFS.MkdirAll(kclModelsFolder, 0o755); err != nil {
		return nil, errors.Wrap(err, "failed to create models directory")
	}

	openAPISpecs := make(map[string]*spec3.OpenAPI)

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

		var openAPI spec3.OpenAPI
		if err := json.Unmarshal(bs, &openAPI); err != nil {
			return nil //nolint:nilerr // Skip invalid files.
		}

		if openAPI.Components != nil && len(openAPI.Components.Schemas) > 0 {
			openAPISpecs[path] = &openAPI
		}

		return nil
	}); err != nil {
		return nil, errors.Wrap(err, "failed to walk OpenAPI filesystem")
	}

	if len(openAPISpecs) == 0 {
		return nil, nil
	}

	allSchemas := make(map[string]*spec.Schema)
	for _, oapi := range openAPISpecs {
		addKCLDefaults(oapi)

		if oapi.Components != nil {
			maps.Copy(allSchemas, oapi.Components.Schemas)
		}
	}

	for name, schema := range allSchemas {
		kclContent := generateKCLFile(name, schema, allSchemas)

		filename := filepath.Join(kclModelsFolder, toKCLFileName(name))

		dir := filepath.Dir(filename)
		if err := schemaFS.MkdirAll(dir, 0o755); err != nil {
			return nil, errors.Wrapf(err, "failed to create directory for %s", name)
		}

		if err := afero.WriteFile(schemaFS, filename, []byte(kclContent), 0o644); err != nil {
			return nil, errors.Wrapf(err, "failed to write KCL schema for %s", name)
		}
	}

	kclModContent := `[package]
name = "models"
edition = "v0.10.0"
version = "0.0.1"
`
	if err := afero.WriteFile(schemaFS, filepath.Join(kclModelsFolder, "kcl.mod"), []byte(kclModContent), 0o644); err != nil {
		return nil, errors.Wrap(err, "failed to write kcl.mod")
	}

	return schemaFS, nil
}

func toKCLFileName(name string) string {
	parts := strings.Split(name, ".")
	if len(parts) <= 1 {
		return name + ".k"
	}

	path := filepath.Join(parts[:len(parts)-1]...)
	filename := parts[len(parts)-1] + ".k"

	return filepath.Join(path, filename)
}

func extractSchemaName(ref string) string {
	if ref == "" {
		return ""
	}
	parts := strings.Split(ref, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return ""
}

func extractSimpleName(fullName string) string {
	parts := strings.Split(fullName, ".")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return fullName
}

func handleAllOfType(schema *spec.Schema, currentSchemaName string) (string, bool) {
	if len(schema.AllOf) == 0 {
		return "", false
	}

	for _, allOfSchema := range schema.AllOf {
		if allOfSchema.Ref.String() != "" {
			if kclType := processSchemaReference(allOfSchema.Ref.String(), currentSchemaName); kclType != "" {
				return kclType, true
			}
		}
	}
	return "", false
}

func processSchemaReference(ref string, currentSchemaName string) string {
	refName := extractSchemaName(ref)
	if refName == "" {
		return ""
	}

	if strings.HasSuffix(refName, "IntOrString") {
		return "int | str"
	}
	if strings.HasSuffix(refName, "Quantity") {
		return "str"
	}
	if strings.HasSuffix(refName, "Time") {
		return "str"
	}
	if strings.HasSuffix(refName, "RawExtension") {
		return "any"
	}
	return formatTypeReference(refName, currentSchemaName)
}

func handleArrayType(schema *spec.Schema, allSchemas map[string]*spec.Schema, currentSchemaName string) (string, bool) {
	if !schema.Type.Contains("array") || schema.Items == nil || schema.Items.Schema == nil {
		return "", false
	}

	itemType := convertOpenAPITypeToKCL(schema.Items.Schema, allSchemas, currentSchemaName)
	return "[" + itemType + "]", true
}

func handleObjectType(schema *spec.Schema, allSchemas map[string]*spec.Schema, currentSchemaName string) (string, bool) {
	if !schema.Type.Contains("object") {
		return "", false
	}

	if schema.AdditionalProperties != nil && schema.AdditionalProperties.Schema != nil {
		valueType := convertOpenAPITypeToKCL(schema.AdditionalProperties.Schema, allSchemas, currentSchemaName)
		return "{str:" + valueType + "}", true
	}
	if len(schema.Properties) == 0 {
		return "{str: any}", true
	}
	return "dict", true
}

func convertOpenAPITypeToKCL(schema *spec.Schema, allSchemas map[string]*spec.Schema, currentSchemaName string) string {
	if schema == nil {
		return "any"
	}

	if kclType, found := handleAllOfType(schema, currentSchemaName); found {
		return kclType
	}

	if schema.Ref.String() != "" {
		if kclType := processSchemaReference(schema.Ref.String(), currentSchemaName); kclType != "" {
			return kclType
		}
	}

	if kclType, found := handleArrayType(schema, allSchemas, currentSchemaName); found {
		return kclType
	}

	if kclType, found := handleObjectType(schema, allSchemas, currentSchemaName); found {
		return kclType
	}

	switch {
	case schema.Type.Contains("string"):
		return "str"
	case schema.Type.Contains("integer"):
		return "int"
	case schema.Type.Contains("number"):
		return "float"
	case schema.Type.Contains("boolean"):
		return "bool"
	default:
		return "any"
	}
}

func formatTypeReference(refName, currentSchemaName string) string {
	if strings.HasPrefix(refName, "io.k8s.api.") {
		lastDot := strings.LastIndex(refName, ".")
		if lastDot > 0 {
			packagePath := refName[:lastDot]
			if strings.Count(packagePath, ".") >= 4 {
				typeName := refName[lastDot+1:]

				if strings.HasPrefix(currentSchemaName, packagePath+".") {
					return typeName
				}

				alias := getImportAlias(packagePath)
				return alias + "." + typeName
			}
		}
	}

	if typeName, ok := strings.CutPrefix(refName, "io.k8s.apimachinery.pkg.apis.meta.v1."); ok {
		if strings.HasPrefix(currentSchemaName, "io.k8s.apimachinery.pkg.apis.meta.v1.") {
			return typeName
		}
		return "v1." + typeName
	}

	if typeName, ok := strings.CutPrefix(refName, "io.k8s.apimachinery.pkg.runtime."); ok {
		if strings.HasPrefix(currentSchemaName, "io.k8s.apimachinery.pkg.runtime.") {
			return typeName
		}
		return "runtime." + typeName
	}

	if strings.HasPrefix(refName, "io.k8s.apimachinery.pkg.") {
		parts := strings.Split(refName, ".")
		if len(parts) > 1 {
			packagePath := strings.Join(parts[:len(parts)-1], ".")
			typeName := parts[len(parts)-1]

			if strings.HasPrefix(currentSchemaName, packagePath+".") {
				return typeName
			}

			alias := getImportAlias(packagePath)
			return alias + "." + typeName
		}
	}

	return extractSimpleName(refName)
}

func getImportAlias(packagePath string) string {
	if packagePath == "io.k8s.apimachinery.pkg.apis.meta.v1" {
		return "v1"
	}

	if packagePath == "io.k8s.apimachinery.pkg.runtime" {
		return "runtime"
	}

	if strings.HasPrefix(packagePath, "io.k8s.api.") {
		parts := strings.Split(packagePath, ".")
		if len(parts) >= 5 {
			group := parts[3]
			version := parts[4]
			caser := cases.Title(language.English)
			versionTitle := caser.String(version)
			return group + versionTitle
		}
	}

	if strings.HasPrefix(packagePath, "io.k8s.apimachinery.pkg.") {
		parts := strings.Split(packagePath, ".")
		if len(parts) >= 5 {
			if parts[4] == "apis" && len(parts) >= 6 {
				group := parts[len(parts)-2]
				version := parts[len(parts)-1]
				caser := cases.Title(language.English)
				versionTitle := caser.String(version)
				return group + versionTitle
			}
			return parts[4]
		}
	}

	parts := strings.Split(packagePath, ".")
	if len(parts) >= 2 {
		lastPart := parts[len(parts)-1]
		secondLastPart := parts[len(parts)-2]
		caser := cases.Title(language.English)
		lastPartTitle := caser.String(lastPart)
		return secondLastPart + lastPartTitle
	}
	return "unknown"
}

func processSchemaProperties(schema *spec.Schema) (map[string]*spec.Schema, map[string]bool, []string) {
	properties := make(map[string]*spec.Schema)
	requiredSet := make(map[string]bool)

	for _, req := range schema.Required {
		requiredSet[req] = true
	}

	if len(schema.AllOf) > 0 {
		for _, allOfSchema := range schema.AllOf {
			if allOfSchema.Properties != nil {
				for k, v := range allOfSchema.Properties {
					propCopy := v
					properties[k] = &propCopy
				}
			}
			for _, req := range allOfSchema.Required {
				requiredSet[req] = true
			}
		}
	}

	if schema.Properties != nil {
		for k, v := range schema.Properties {
			propCopy := v
			properties[k] = &propCopy
		}
	}

	propNames := make([]string, 0, len(properties))
	for name := range properties {
		propNames = append(propNames, name)
	}
	sort.Strings(propNames)

	return properties, requiredSet, propNames
}

func generateDocStringHeader(sb *strings.Builder, schema *spec.Schema) {
	if schema.Description != "" || len(schema.Properties) > 0 {
		sb.WriteString("    \"\"\"\n")

		if schema.Description != "" {
			lines := strings.SplitSeq(strings.TrimSpace(schema.Description), "\n")
			for line := range lines {
				sb.WriteString("    " + strings.TrimSpace(line) + "\n")
			}
		}
	}
}

func generateAttributesDocumentation(sb *strings.Builder, propNames []string, properties map[string]*spec.Schema, requiredSet map[string]bool, allSchemas map[string]*spec.Schema, currentSchemaName string) {
	if len(propNames) == 0 {
		return
	}

	sb.WriteString("\n    Attributes\n")
	sb.WriteString("    ----------\n")

	for _, propName := range propNames {
		prop := properties[propName]

		docPropName := propName
		if propName == "type" {
			docPropName = "$type"
		}

		sb.WriteString("    " + docPropName + " : ")
		sb.WriteString(convertOpenAPITypeToKCL(prop, allSchemas, currentSchemaName))

		if prop.Default != nil {
			sb.WriteString(", default is ")
			sb.WriteString(formatDefaultValue(prop.Default))
		} else {
			sb.WriteString(", default is Undefined")
		}

		if requiredSet[propName] {
			sb.WriteString(", required")
		} else {
			sb.WriteString(", optional")
		}
		sb.WriteString("\n")

		if prop.Description != "" {
			lines := strings.SplitSeq(strings.TrimSpace(prop.Description), "\n")
			for line := range lines {
				sb.WriteString("        " + strings.TrimSpace(line) + "\n")
			}
		}
	}
}

func generatePropertyField(sb *strings.Builder, propName string, prop *spec.Schema, requiredSet map[string]bool, allSchemas map[string]*spec.Schema, currentSchemaName string) {
	fieldName := propName
	if propName == "type" {
		fieldName = "$type"
	}

	sb.WriteString("\n")
	sb.WriteString("    " + fieldName)

	if !requiredSet[propName] {
		sb.WriteString("?")
	}

	sb.WriteString(": ")
	propType := convertOpenAPITypeToKCL(prop, allSchemas, currentSchemaName)
	sb.WriteString(propType)

	if prop.Default != nil {
		sb.WriteString(" = ")
		sb.WriteString(formatDefaultValue(prop.Default))
	}
}

func generateKCLSchema(name string, schema *spec.Schema, allSchemas map[string]*spec.Schema, currentSchemaName string) string {
	var sb strings.Builder

	sb.WriteString("schema " + name + ":\n")

	generateDocStringHeader(&sb, schema)

	properties, requiredSet, propNames := processSchemaProperties(schema)

	generateAttributesDocumentation(&sb, propNames, properties, requiredSet, allSchemas, currentSchemaName)

	if schema.Description != "" || len(schema.Properties) > 0 {
		sb.WriteString("    \"\"\"\n\n")
	}

	for _, propName := range propNames {
		prop := properties[propName]
		generatePropertyField(&sb, propName, prop, requiredSet, allSchemas, currentSchemaName)
	}

	return sb.String()
}

func formatDefaultValue(value any) string {
	switch v := value.(type) {
	case string:
		return fmt.Sprintf("%q", v)
	case bool:
		if v {
			return "True"
		}
		return "False"
	case nil:
		return "None"
	case map[string]any:
		if len(v) == 0 {
			return "{}"
		}
		return fmt.Sprintf("%v", v)
	case []any:
		if len(v) == 0 {
			return "[]"
		}
		return fmt.Sprintf("%v", v)
	default:
		str := fmt.Sprintf("%v", v)
		if str == "map[]" {
			return "{}"
		}
		if str == "[]" {
			return "[]"
		}
		return str
	}
}

func generateKCLFile(fullSchemaName string, schema *spec.Schema, allSchemas map[string]*spec.Schema) string {
	name := extractSimpleName(fullSchemaName)
	var sb strings.Builder

	sb.WriteString(`"""
This file was generated by crossplane. DO NOT EDIT.
"""
`)

	imports := make(map[string]bool)
	visited := make(map[*spec.Schema]bool)

	if schema.Properties != nil {
		for _, prop := range schema.Properties {
			checkForImports(&prop, imports, visited)
		}
	}

	for _, allOfSchema := range schema.AllOf {
		if allOfSchema.Ref.String() != "" {
			refName := extractSchemaName(allOfSchema.Ref.String())
			addImportIfNeeded(refName, imports)
		}
	}

	if fullSchemaName != "" {
		lastDot := strings.LastIndex(fullSchemaName, ".")
		if lastDot > 0 {
			currentPackage := fullSchemaName[:lastDot]
			delete(imports, currentPackage)
		}
	}

	for imp := range imports {
		alias := getImportAlias(imp)
		sb.WriteString("import " + imp + " as " + alias + "\n")
	}
	if len(imports) > 0 {
		sb.WriteString("\n")
	}

	sb.WriteString(generateKCLSchema(name, schema, allSchemas, fullSchemaName))

	return sb.String()
}

func checkForImports(schema *spec.Schema, imports map[string]bool, visited map[*spec.Schema]bool) {
	if schema == nil {
		return
	}

	if visited[schema] {
		return
	}
	visited[schema] = true

	if len(schema.AllOf) > 0 {
		for _, allOfSchema := range schema.AllOf {
			if allOfSchema.Ref.String() != "" {
				refName := extractSchemaName(allOfSchema.Ref.String())
				if !strings.HasSuffix(refName, "IntOrString") && !strings.HasSuffix(refName, "RawExtension") && !strings.HasSuffix(refName, "Quantity") && !strings.HasSuffix(refName, "Time") {
					addImportIfNeeded(refName, imports)
				}
			}
		}
	}

	if schema.Ref.String() != "" {
		refName := extractSchemaName(schema.Ref.String())
		if !strings.HasSuffix(refName, "IntOrString") && !strings.HasSuffix(refName, "RawExtension") && !strings.HasSuffix(refName, "Quantity") && !strings.HasSuffix(refName, "Time") {
			addImportIfNeeded(refName, imports)
		}
		return
	}

	if schema.Items != nil && schema.Items.Schema != nil {
		checkForImports(schema.Items.Schema, imports, visited)
	}

	if schema.Properties != nil {
		for _, prop := range schema.Properties {
			checkForImports(&prop, imports, visited)
		}
	}

	if schema.AdditionalProperties != nil && schema.AdditionalProperties.Schema != nil {
		checkForImports(schema.AdditionalProperties.Schema, imports, visited)
	}
}

func addImportIfNeeded(refName string, imports map[string]bool) {
	if refName == "" {
		return
	}

	if strings.HasPrefix(refName, "io.k8s.") {
		lastDot := strings.LastIndex(refName, ".")
		if lastDot > 0 {
			packagePath := refName[:lastDot]
			imports[packagePath] = true
		}
	}
}

func addKCLDefaults(s *spec3.OpenAPI) {
	if s.Components == nil || s.Components.Schemas == nil {
		return
	}

	for _, schema := range s.Components.Schemas {
		processKCLSchemaDefaults(schema)
	}
}

func processKCLSchemaDefaults(schema *spec.Schema) {
	rawExt, ok := schema.Extensions["x-kubernetes-group-version-kind"]
	if !ok {
		return
	}

	gvkList := extractGVKList(rawExt)
	if len(gvkList) == 0 {
		return
	}

	group, version, kind := extractGVKInfo(gvkList[0])
	apiVersion := constructAPIVersion(group, version)
	addSchemaPropertyDefaultsKcl(schema, apiVersion, kind)
}

func addSchemaPropertyDefaultsKcl(schema *spec.Schema, apiVersion, kind string) {
	if schema.Properties == nil {
		return
	}

	if _, ok := schema.Properties["apiVersion"]; ok {
		propSchema := schema.Properties["apiVersion"]
		propSchema.Default = apiVersion
		propSchema.Enum = []any{apiVersion}
		schema.Properties["apiVersion"] = propSchema
	}

	if _, ok := schema.Properties["kind"]; ok {
		propSchema := schema.Properties["kind"]
		propSchema.Default = kind
		propSchema.Enum = []any{kind}
		schema.Properties["kind"] = propSchema
	}

	hasAPIVersion := false
	hasKind := false
	for _, req := range schema.Required {
		if req == "apiVersion" {
			hasAPIVersion = true
		}
		if req == "kind" {
			hasKind = true
		}
	}
	if !hasAPIVersion {
		schema.Required = append(schema.Required, "apiVersion")
	}
	if !hasKind {
		schema.Required = append(schema.Required, "kind")
	}
}

// isAPIVersion heuristically determines whether its argument is a Kubernetes
// API version.
func isAPIVersion(s string) bool {
	re := regexp.MustCompile("^v[1-9][0-9]*((alpha|beta)[1-9][0-9]*)?$")
	return re.MatchString(s)
}
