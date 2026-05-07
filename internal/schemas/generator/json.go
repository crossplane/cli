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
	"io/fs"
	"maps"
	"path/filepath"
	"strings"

	"github.com/invopop/jsonschema"
	"github.com/spf13/afero"
	"k8s.io/kube-openapi/pkg/spec3"
	"k8s.io/kube-openapi/pkg/validation/spec"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	"github.com/crossplane/cli/v2/internal/schemas/runner"
)

type jsonGenerator struct{}

func (jsonGenerator) Language() string {
	return "json"
}

// GenerateFromCRD generates jsonschemas for the CRDs in the given filesystem.
func (jsonGenerator) GenerateFromCRD(_ context.Context, fromFS afero.Fs, _ runner.SchemaRunner) (afero.Fs, error) {
	openAPIs, err := goCollectOpenAPIs(fromFS)
	if err != nil {
		return nil, err
	}

	if len(openAPIs) == 0 {
		return nil, nil
	}

	schemaFS := afero.NewMemMapFs()
	if err := schemaFS.Mkdir("models", 0o755); err != nil {
		return nil, errors.Wrap(err, "failed to create models directory")
	}

	schemas := make(map[string]*spec.Schema)
	for _, oapi := range openAPIs {
		maps.Copy(schemas, oapi.spec.Components.Schemas)
	}

	for name, schema := range schemas {
		jschema, err := oapiSchemaToJSONSchema(schema)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to generate jsonschema for %s", name)
		}

		bs, err := json.Marshal(jschema)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to marshal jsonschema for %s", name)
		}

		fname := filepath.Join("models", strings.ReplaceAll(name, ".", "-")+".schema.json")
		if err := afero.WriteFile(schemaFS, fname, bs, 0o644); err != nil {
			return nil, errors.Wrapf(err, "failed to write jsonschema for %s", name)
		}
	}

	return schemaFS, nil
}

func oapiSchemaToJSONSchema(s *spec.Schema) (*jsonschema.Schema, error) {
	bs, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}

	var conv jsonschema.Schema
	if err := json.Unmarshal(bs, &conv); err != nil {
		return nil, err
	}

	return mutateJSONSchema(&conv), nil
}

func mutateJSONSchema(s *jsonschema.Schema) *jsonschema.Schema {
	if s.Type == "object" && s.AdditionalProperties == nil {
		s.AdditionalProperties = jsonschema.FalseSchema
	}

	if after, ok := strings.CutPrefix(s.Ref, "#/components/schemas/"); ok {
		s.Ref = after
		s.Ref = strings.ReplaceAll(s.Ref, ".", "-")
		s.Ref += ".schema.json"
	}

	for i, schema := range s.AllOf {
		s.AllOf[i] = mutateJSONSchema(schema)
	}
	for i, schema := range s.AnyOf {
		s.AnyOf[i] = mutateJSONSchema(schema)
	}
	for i, schema := range s.OneOf {
		s.OneOf[i] = mutateJSONSchema(schema)
	}
	if s.Not != nil {
		s.Not = mutateJSONSchema(s.Not)
	}

	if s.Items != nil {
		s.Items = mutateJSONSchema(s.Items)
	}

	if s.AdditionalProperties != nil {
		s.AdditionalProperties = mutateJSONSchema(s.AdditionalProperties)
	}

	for prop := s.Properties.Oldest(); prop != nil; prop = prop.Next() {
		s.Properties.Set(prop.Key, mutateJSONSchema(prop.Value))
	}

	return s
}

// GenerateFromOpenAPI generates jsonschemas from OpenAPI v3 specs.
func (jsonGenerator) GenerateFromOpenAPI(_ context.Context, fromFS afero.Fs, _ runner.SchemaRunner) (afero.Fs, error) {
	var openAPISpecs []*spec3.OpenAPI
	err := afero.Walk(fromFS, "", func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		if filepath.Ext(path) != ".json" {
			return nil
		}

		bs, err := afero.ReadFile(fromFS, path)
		if err != nil {
			return errors.Wrapf(err, "failed to read OpenAPI file %q", path)
		}

		var openAPI spec3.OpenAPI
		if err := json.Unmarshal(bs, &openAPI); err != nil {
			return nil //nolint:nilerr // Skip invalid files.
		}

		if openAPI.Components != nil && len(openAPI.Components.Schemas) > 0 {
			openAPISpecs = append(openAPISpecs, &openAPI)
		}

		return nil
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to walk OpenAPI filesystem")
	}

	if len(openAPISpecs) == 0 {
		return nil, nil
	}

	schemaFS := afero.NewMemMapFs()
	if err := schemaFS.Mkdir("models", 0o755); err != nil {
		return nil, errors.Wrap(err, "failed to create models directory")
	}

	schemas := make(map[string]*spec.Schema)
	for _, oapi := range openAPISpecs {
		maps.Copy(schemas, oapi.Components.Schemas)
	}

	for name, schema := range schemas {
		jschema, err := oapiSchemaToJSONSchema(schema)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to generate jsonschema for %s", name)
		}

		bs, err := json.Marshal(jschema)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to marshal jsonschema for %s", name)
		}

		fname := filepath.Join("models", strings.ReplaceAll(name, ".", "-")+".schema.json")
		if err := afero.WriteFile(schemaFS, fname, bs, 0o644); err != nil {
			return nil, errors.Wrapf(err, "failed to write jsonschema for %s", name)
		}
	}

	return schemaFS, nil
}
