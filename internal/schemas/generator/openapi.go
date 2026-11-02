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
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kube-openapi/pkg/spec3"
	"k8s.io/kube-openapi/pkg/validation/spec"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	xpv1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1"

	"github.com/crossplane/cli/v2/internal/crd"
)

// The helpers in this file collect the OpenAPI documents the generators work
// from and normalize them. They are shared: the Go generator uses them all,
// the JSON generator collects CRDs with them, and the Rust generator runs the
// same normalization, so a schema reads the same whichever language it is
// generated for.

type crdOpenAPI struct {
	crd     *extv1.CustomResourceDefinition
	version string
	spec    *spec3.OpenAPI
}

func collectCRDOpenAPIs(fromFS afero.Fs) ([]crdOpenAPI, error) { //nolint:gocognit // Hard to split this up, and it's not too long to read.
	crdFS := afero.NewMemMapFs()
	baseFolder := "workdir"

	if err := crdFS.MkdirAll(baseFolder, 0o755); err != nil {
		return nil, err
	}

	var openAPIs []crdOpenAPI
	return openAPIs, afero.Walk(fromFS, "", func(path string, info fs.FileInfo, err error) error {
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
					return errors.Wrapf(err, "failed to read file %q", xrPath)
				}

				var c extv1.CustomResourceDefinition
				if err := yaml.Unmarshal(bs, &c); err != nil {
					return errors.Wrapf(err, "failed to unmarshal CRD file %q", xrPath)
				}

				oapis, err := crd.ToOpenAPI(&c)
				if err != nil {
					return err
				}
				for version, oapi := range oapis {
					openAPIs = append(openAPIs, crdOpenAPI{spec: oapi, version: version, crd: &c})
				}
			}
			if claimPath != "" {
				bs, err := afero.ReadFile(crdFS, claimPath)
				if err != nil {
					return errors.Wrapf(err, "failed to read file %q", claimPath)
				}

				var c extv1.CustomResourceDefinition
				if err := yaml.Unmarshal(bs, &c); err != nil {
					return errors.Wrapf(err, "failed to unmarshal CRD file %q", claimPath)
				}

				oapis, err := crd.ToOpenAPI(&c)
				if err != nil {
					return err
				}
				for version, oapi := range oapis {
					openAPIs = append(openAPIs, crdOpenAPI{spec: oapi, version: version, crd: &c})
				}
			}

		case "CustomResourceDefinition":
			var c extv1.CustomResourceDefinition
			if err := yaml.Unmarshal(bs, &c); err != nil {
				return errors.Wrapf(err, "failed to unmarshal CRD file %q", path)
			}

			oapis, err := crd.ToOpenAPI(&c)
			if err != nil {
				return err
			}
			for version, oapi := range oapis {
				openAPIs = append(openAPIs, crdOpenAPI{spec: oapi, version: version, crd: &c})
			}
		}
		return nil
	})
}

// collectOpenAPISpecs walks through the filesystem to find and parse OpenAPI JSON files.
func collectOpenAPISpecs(fromFS afero.Fs) ([]*spec3.OpenAPI, error) {
	var openAPISpecs []*spec3.OpenAPI

	err := afero.Walk(fromFS, "", func(path string, info fs.FileInfo, err error) error {
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

		// Parse as OpenAPI spec
		var spec spec3.OpenAPI
		if err := json.Unmarshal(bs, &spec); err != nil {
			// Skip files that aren't valid OpenAPI specs
			return nil //nolint:nilerr // See comment above.
		}

		// Check if it has components/schemas
		if spec.Components == nil || len(spec.Components.Schemas) == 0 {
			return nil
		}

		openAPISpecs = append(openAPISpecs, &spec)
		return nil
	})

	return openAPISpecs, err
}

// addGVKDefaults adds default values for apiVersion and kind properties based on
// x-kubernetes-group-version-kind extension.
func addGVKDefaults(s *spec3.OpenAPI) {
	if s.Components == nil || s.Components.Schemas == nil {
		return
	}

	for _, schema := range s.Components.Schemas {
		processSchemaDefaults(schema)
	}
}

func processSchemaDefaults(schema *spec.Schema) {
	// Look for x-kubernetes-group-version-kind extension
	rawExt, ok := schema.Extensions["x-kubernetes-group-version-kind"]
	if !ok {
		return
	}

	// Convert the extension to a usable format
	gvkList := extractGVKList(rawExt)
	if len(gvkList) == 0 {
		return
	}

	// Extract group, version, and kind from the first GVK
	group, version, kind := extractGVKInfo(gvkList[0])

	// Construct apiVersion
	apiVersion := constructAPIVersion(group, version)

	// Add defaults to properties
	addSchemaPropertyDefaults(schema, apiVersion, kind)
}

func extractGVKList(rawExt any) []map[string]any {
	var gvkList []map[string]any
	switch ext := rawExt.(type) {
	case []any:
		for _, item := range ext {
			if gvk, ok := item.(map[string]any); ok {
				gvkList = append(gvkList, gvk)
			}
		}
	case []map[string]any:
		gvkList = ext
	}
	return gvkList
}

func extractGVKInfo(gvk map[string]any) (group, version, kind string) {
	if g, ok := gvk["group"].(string); ok {
		group = g
	}
	if v, ok := gvk["version"].(string); ok {
		version = v
	}
	if k, ok := gvk["kind"].(string); ok {
		kind = k
	}
	return group, version, kind
}

func constructAPIVersion(group, version string) string {
	if group != "" {
		return group + "/" + version
	}
	return version
}

func addSchemaPropertyDefaults(schema *spec.Schema, apiVersion, kind string) {
	if schema.Properties == nil {
		return
	}

	// Add default to apiVersion property
	if propSchema, ok := schema.Properties["apiVersion"]; ok {
		propSchema.Default = apiVersion
		propSchema.Enum = []any{apiVersion}
		schema.Properties["apiVersion"] = propSchema
	}

	// Add default to kind property
	if propSchema, ok := schema.Properties["kind"]; ok {
		propSchema.Default = kind
		propSchema.Enum = []any{kind}
		schema.Properties["kind"] = propSchema
	}
}

// removeValidationOnlyCombinators removes anyOf/oneOf combinators that carry
// no structural type information. Kubernetes structural schemas allow these
// junctors only for validation (e.g. "exactly one of endpointSelector and
// nodeSelector must be set"), where each variant contains only `required`
// constraints and empty property schemas. oapi-codegen generates a union
// member type named <TypeName><index> for each variant, so a schema with both
// anyOf and oneOf produces colliding type names (two <TypeName>0). The
// variants don't affect the generated Go structs, so we drop them.
// Combinators with typed variants (e.g. x-kubernetes-int-or-string's
// anyOf: [{type: integer}, {type: string}]) are kept.
func removeValidationOnlyCombinators(s *spec3.OpenAPI) {
	for _, schema := range s.Components.Schemas {
		removeSchemaValidationOnlyCombinators(schema)
	}
}

func removeSchemaValidationOnlyCombinators(schema *spec.Schema) {
	if schema == nil {
		return
	}

	if combinatorIsValidationOnly(schema.AnyOf) {
		schema.AnyOf = nil
	}
	if combinatorIsValidationOnly(schema.OneOf) {
		schema.OneOf = nil
	}

	for i := range schema.AllOf {
		removeSchemaValidationOnlyCombinators(&schema.AllOf[i])
	}
	for i := range schema.AnyOf {
		removeSchemaValidationOnlyCombinators(&schema.AnyOf[i])
	}
	for i := range schema.OneOf {
		removeSchemaValidationOnlyCombinators(&schema.OneOf[i])
	}
	for name, prop := range schema.Properties {
		removeSchemaValidationOnlyCombinators(&prop)
		schema.Properties[name] = prop
	}
	if schema.Items != nil && schema.Items.Schema != nil {
		removeSchemaValidationOnlyCombinators(schema.Items.Schema)
	}
	if schema.AdditionalProperties != nil && schema.AdditionalProperties.Schema != nil {
		removeSchemaValidationOnlyCombinators(schema.AdditionalProperties.Schema)
	}
}

// combinatorIsValidationOnly returns true if all the given anyOf/oneOf
// variants constrain validation without describing any structure.
func combinatorIsValidationOnly(variants []spec.Schema) bool {
	if len(variants) == 0 {
		return false
	}
	for i := range variants {
		if !schemaIsValidationOnly(&variants[i]) {
			return false
		}
	}
	return true
}

func schemaIsValidationOnly(s *spec.Schema) bool {
	if len(s.Type) > 0 || s.Ref.String() != "" || s.Format != "" ||
		s.Items != nil || s.AdditionalProperties != nil ||
		len(s.Enum) > 0 || s.Default != nil ||
		len(s.AllOf) > 0 || len(s.AnyOf) > 0 || len(s.OneOf) > 0 || s.Not != nil {
		return false
	}
	for name := range s.Properties {
		prop := s.Properties[name]
		if !schemaIsValidationOnly(&prop) {
			return false
		}
	}
	return true
}
