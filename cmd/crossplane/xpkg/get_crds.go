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
	"github.com/crossplane/cli/v2/internal/schemas/generator"

	_ "embed"
)

//go:embed help/get-crds.md
var helpGetCRDs string

const errWriteOutput = "cannot write output"

// Cmd arguments and flags for the get-crds subcommand.
type getCRDsCmd struct {
	// Arguments.
	Extensions string `arg:"" help:"Extension sources as a comma-separated list of files, directories, or '-' for standard input."`

	// Flags. Keep them in alphabetical order.
	CacheDir        string `default:"~/.crossplane/cache"                                                                   help:"Absolute path to the cache directory holding downloaded schemas."                     predictor:"directory"`
	CleanCache      bool   `help:"Clean the cache directory before downloading package schemas."`
	CrossplaneImage string `help:"Specify the Crossplane image for fetching the built-in schemas."`
	Flat            bool   `help:"Write files to a flat directory instead of organizing by group and version."`
	JSONSchema      bool   `help:"Write JSON Schema files instead of CRDs. Useful for YAML language server integration."    name:"json-schema"`
	NoCache         bool   `help:"Disable caching entirely. The command downloads schemas every time without storing them."`
	OutputDir       string `default:"."                                                                                     help:"Directory that receives the CRD or JSON Schema files. Defaults to current directory." name:"output-dir"     short:"o"`
	UpdateCache     bool   `default:"false"                                                                                 help:"Update cached schemas by downloading the latest version that satisfies a constraint."`

	fs afero.Fs
}

// Help prints out the help for the get-crds command.
func (c *getCRDsCmd) Help() string {
	return helpGetCRDs
}

// AfterApply implements kong.AfterApply.
func (c *getCRDsCmd) AfterApply() error {
	c.fs = afero.NewOsFs()
	return nil
}

// Run downloads CRDs from package dependencies and writes them to the output directory.
func (c *getCRDsCmd) Run(k *kong.Context, _ logging.Logger) error {
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
// By default, files are organized by group and version. With --flat, files are
// written directly to the output directory using the CRD name.
func (c *getCRDsCmd) writeCRDs(k *kong.Context, crds []*extv1.CustomResourceDefinition) error {
	for _, crd := range crds {
		data, err := yaml.Marshal(crd)
		if err != nil {
			return errors.Wrapf(err, "cannot marshal CRD %q", crd.GetName())
		}

		outPath := c.outputPath(crd.GetName(), crd.Spec.Group, storageVersion(crd), crd.Spec.Names.Kind, ".yaml")

		if err := c.fs.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			return errors.Wrapf(err, "cannot create directory for %q", outPath)
		}

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

func storageVersion(crd *extv1.CustomResourceDefinition) string {
	for _, v := range crd.Spec.Versions {
		if v.Storage {
			return v.Name
		}
	}
	if len(crd.Spec.Versions) > 0 {
		return crd.Spec.Versions[0].Name
	}
	return ""
}

// outputPath returns the file path for a resource. flatName is used as the
// filename in --flat mode. In structured mode, files are organized by group
// and version.
func (c *getCRDsCmd) outputPath(flatName, group, version, kind, ext string) string {
	if c.Flat {
		return filepath.Join(c.OutputDir, flatName+ext)
	}
	return filepath.Join(c.OutputDir, group, version, strings.ToLower(kind)+ext)
}

// writeJSONSchemas extracts OpenAPI v3 schemas from CRD versions and writes
// them as JSON Schema files organized by group and version. It applies the
// shared schema mutations from internal/schemas/generator for YAML language
// server compatibility (additionalProperties: false on object types, etc.).
func (c *getCRDsCmd) writeJSONSchemas(k *kong.Context, crds []*extv1.CustomResourceDefinition) error {
	schemas, err := generator.CRDsToJSONSchemas(crds)
	if err != nil {
		return err
	}

	for _, s := range schemas {
		flatName := fmt.Sprintf("%s_%s_%s", s.Group, s.Version, strings.ToLower(s.Kind))
		outPath := c.outputPath(flatName, s.Group, s.Version, s.Kind, ".json")

		if err := c.fs.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			return errors.Wrapf(err, "cannot create directory for %q", outPath)
		}

		if err := afero.WriteFile(c.fs, outPath, s.Data, 0o644); err != nil {
			return errors.Wrapf(err, "cannot write JSON Schema to %q", outPath)
		}

		if _, err := fmt.Fprintf(k.Stdout, "wrote %s\n", outPath); err != nil {
			return errors.Wrap(err, errWriteOutput)
		}
	}

	if _, err := fmt.Fprintf(k.Stdout, "Total %d JSON Schemas written to %s\n", len(schemas), c.OutputDir); err != nil {
		return errors.Wrap(err, errWriteOutput)
	}

	return nil
}
