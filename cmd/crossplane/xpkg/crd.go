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

package xpkg

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/spf13/afero"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	"github.com/crossplane/cli/v2/cmd/crossplane/common/load"
	"github.com/crossplane/cli/v2/cmd/crossplane/validate"
)

const (
	errWriteOutput    = "cannot write output"
	jsonSchemaDraft07 = "http://json-schema.org/draft-07/schema#"
)

// Cmd arguments and flags for the crd subcommand.
type crdCmd struct {
	// Arguments.
	Extensions string `arg:"" help:"Extension sources as a comma-separated list of files, directories, or '-' for standard input."`

	// Flags. Keep them in alphabetical order.
	CacheDir        string `default:"~/.crossplane/cache"                                                                help:"Absolute path to the cache directory where downloaded schemas are stored."                predictor:"directory"`
	CleanCache      bool   `help:"Clean the cache directory before downloading package schemas."`
	CrossplaneImage string `help:"Specify the Crossplane image to be used for fetching the built-in schemas."`
	JSONSchema      bool   `help:"Write JSON Schema files instead of CRDs. Useful for YAML language server integration." name:"json-schema"`
	NoCache         bool   `help:"Disable caching entirely. Schemas are downloaded every time and not stored."`
	OutputDir       string `default:"."                                                                                  help:"Directory where CRD or JSON Schema files will be written. Defaults to current directory." name:"output-dir"     short:"o"`
	UpdateCache     bool   `default:"false"                                                                              help:"Update cached schemas by downloading the latest version that satisfies a constraint."`

	fs afero.Fs
}

// Help prints out the help for the crd command.
func (c *crdCmd) Help() string {
	return `
This command downloads CRDs from Crossplane package dependencies (providers, functions, configurations) and writes
them as YAML files to the specified output directory. With --json-schema, it extracts the OpenAPI v3 schemas from
CRDs and writes them as JSON Schema files suitable for use with YAML language servers.

It accepts the same extension sources as the validate command: crossplane.yaml files, directories containing package
manifests, or Provider/Function/Configuration resources.

Examples:

  # Download CRDs from a crossplane.yaml to the current directory
  crossplane xpkg crd crossplane.yaml

  # Download CRDs to a specific directory
  crossplane xpkg crd crossplane.yaml --output-dir ./crds

  # Download JSON Schemas for YAML language server
  crossplane xpkg crd crossplane.yaml --output-dir ./schemas --json-schema

  # Download CRDs from multiple sources
  crossplane xpkg crd crossplane.yaml,providers/ --output-dir ./crds

  # Force re-download of cached schemas
  crossplane xpkg crd crossplane.yaml --output-dir ./crds --clean-cache
`
}

// AfterApply implements kong.AfterApply.
func (c *crdCmd) AfterApply() error {
	c.fs = afero.NewOsFs()
	return nil
}

// Run downloads CRDs from package dependencies and writes them to the output directory.
func (c *crdCmd) Run(k *kong.Context, _ logging.Logger) error {
	extensionLoader, err := load.NewLoader(c.Extensions)
	if err != nil {
		return errors.Wrapf(err, "cannot load extensions from %q", c.Extensions)
	}

	extensions, err := extensionLoader.Load()
	if err != nil {
		return errors.Wrapf(err, "cannot load extensions from %q", c.Extensions)
	}

	if c.NoCache {
		tmpCache, err := afero.TempDir(c.fs, "", "crossplane-crd-*")
		if err != nil {
			return errors.Wrap(err, "cannot create temporary cache directory")
		}
		defer c.fs.RemoveAll(tmpCache) //nolint:errcheck // best-effort cleanup
		c.CacheDir = tmpCache
	} else if strings.HasPrefix(c.CacheDir, "~/") {
		homeDir, _ := os.UserHomeDir()
		c.CacheDir = filepath.Join(homeDir, c.CacheDir[2:])
	}

	opts := []validate.Option{
		validate.WithUpdateCache(c.UpdateCache),
	}
	if c.CrossplaneImage != "" {
		opts = append(opts, validate.WithCrossplaneImage(c.CrossplaneImage))
	}

	m := validate.NewManager(c.CacheDir, c.fs, k.Stdout, opts...)

	if err := m.PrepExtensions(extensions); err != nil {
		return errors.Wrap(err, "cannot prepare extensions")
	}

	if err := m.CacheAndLoad(c.CleanCache); err != nil {
		return errors.Wrap(err, "cannot download and load schemas")
	}

	if err := c.fs.MkdirAll(c.OutputDir, 0o755); err != nil {
		return errors.Wrapf(err, "cannot create output directory %q", c.OutputDir)
	}

	if c.JSONSchema {
		return c.writeJSONSchemas(k, m.CRDs())
	}

	return c.writeCRDs(k, m.CRDs())
}

// writeCRDs marshals each CRD to YAML and writes it to the output directory.
func (c *crdCmd) writeCRDs(k *kong.Context, crds []*extv1.CustomResourceDefinition) error {
	for _, crd := range crds {
		data, err := yaml.Marshal(crd)
		if err != nil {
			return errors.Wrapf(err, "cannot marshal CRD %q", crd.GetName())
		}

		filename := crd.GetName() + ".yaml"
		outPath := filepath.Join(c.OutputDir, filename)

		if err := afero.WriteFile(c.fs, outPath, data, 0o644); err != nil {
			return errors.Wrapf(err, "cannot write CRD to %q", outPath)
		}

		if _, err := fmt.Fprintf(k.Stdout, "wrote %s\n", outPath); err != nil {
			return errors.Wrap(err, errWriteOutput)
		}
	}

	if _, err := fmt.Fprintf(k.Stdout, "Total %d CRDs written to %s\n", len(crds), c.OutputDir); err != nil {
		return errors.Wrap(err, errWriteOutput)
	}

	return nil
}

// writeJSONSchemas extracts OpenAPI v3 schemas from CRD versions and writes
// them as JSON Schema files organized by group and version.
func (c *crdCmd) writeJSONSchemas(k *kong.Context, crds []*extv1.CustomResourceDefinition) error {
	count := 0

	for _, crd := range crds {
		group := crd.Spec.Group
		kind := crd.Spec.Names.Kind

		for _, ver := range crd.Spec.Versions {
			if ver.Schema == nil || ver.Schema.OpenAPIV3Schema == nil {
				continue
			}

			schema, err := openAPIToJSONSchema(ver.Schema.OpenAPIV3Schema, group, ver.Name, kind)
			if err != nil {
				return errors.Wrapf(err, "cannot convert schema for %s/%s %s", group, ver.Name, kind)
			}

			data, err := json.MarshalIndent(schema, "", "  ")
			if err != nil {
				return errors.Wrapf(err, "cannot marshal JSON Schema for %s/%s %s", group, ver.Name, kind)
			}

			dir := filepath.Join(c.OutputDir, group, ver.Name)
			if err := c.fs.MkdirAll(dir, 0o755); err != nil {
				return errors.Wrapf(err, "cannot create directory %q", dir)
			}

			filename := strings.ToLower(kind) + ".json"
			outPath := filepath.Join(dir, filename)

			if err := afero.WriteFile(c.fs, outPath, data, 0o644); err != nil {
				return errors.Wrapf(err, "cannot write JSON Schema to %q", outPath)
			}

			if _, err := fmt.Fprintf(k.Stdout, "wrote %s\n", outPath); err != nil {
				return errors.Wrap(err, errWriteOutput)
			}

			count++
		}
	}

	if _, err := fmt.Fprintf(k.Stdout, "Total %d JSON Schemas written to %s\n", count, c.OutputDir); err != nil {
		return errors.Wrap(err, errWriteOutput)
	}

	return nil
}

// openAPIToJSONSchema converts an OpenAPI v3 schema to a JSON Schema draft-07
// document with Kubernetes group-version-kind metadata.
func openAPIToJSONSchema(props *extv1.JSONSchemaProps, group, version, kind string) (map[string]any, error) {
	raw, err := json.Marshal(props)
	if err != nil {
		return nil, errors.Wrap(err, "cannot marshal OpenAPI schema")
	}

	schema := map[string]any{}
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, errors.Wrap(err, "cannot unmarshal OpenAPI schema")
	}

	schema["$schema"] = jsonSchemaDraft07
	schema["$id"] = fmt.Sprintf("%s/%s/%s.json", group, version, strings.ToLower(kind))
	schema["x-kubernetes-group-version-kind"] = []map[string]string{
		{
			"group":   group,
			"version": version,
			"kind":    kind,
		},
	}

	return schema, nil
}
