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

package schema

import (
	"cmp"
	"context"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	"github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/internal/config"
	"github.com/crossplane/cli/v2/internal/dependency"
	"github.com/crossplane/cli/v2/internal/schemas/generator"
	"github.com/crossplane/cli/v2/internal/terminal"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"

	_ "embed"
)

//go:embed help/generate.md
var generateHelp string

// generateCmd generates schemas for a single source without touching any
// project or package metadata file.
type generateCmd struct {
	Source string `arg:"" help:"Source to generate schemas for (xpkg OCI reference, k8s:<version>, git repository URL, or HTTP(S) URL)."`

	OutputDir string   `default:"schemas"                                                                                   help:"Directory to write schemas to."              short:"o"        type:"path"`
	Languages []string `help:"Comma-separated schema languages to generate (go, json, kcl, python, rust). Defaults to all."`
	CacheDir  string   `env:"CROSSPLANE_XPKG_CACHE"                                                                         help:"Directory for cached xpkg package contents." name:"cache-dir"`
	GitRef    string   `help:"Git ref for CRD sources (branch, tag, or commit SHA)."                                        name:"git-ref"`
	GitPath   string   `help:"Path to CRDs in the git repository."                                                          name:"git-path"`
}

func (c *generateCmd) Help() string {
	return generateHelp
}

// Run executes the generate command.
func (c *generateCmd) Run(logger logging.Logger, sp terminal.SpinnerPrinter, cfg *config.Config) error {
	if err := errors.Join((&v1alpha1.ProjectSchemas{Languages: c.Languages}).Validate()...); err != nil {
		return err
	}

	dep, err := dependency.ParseSource(c.Source, c.GitRef, c.GitPath)
	if err != nil {
		return err
	}

	outDir, err := filepath.Abs(c.OutputDir)
	if err != nil {
		return errors.Wrapf(err, "cannot determine path for %q", c.OutputDir)
	}

	client, err := clixpkg.NewClient(clixpkg.WithCacheDir(afero.NewOsFs(), cmp.Or(c.CacheDir, dependency.DefaultCacheDir())))
	if err != nil {
		return errors.Wrap(err, "cannot create xpkg client")
	}

	// The manager needs a project; an empty defaulted one is never written.
	proj := &v1alpha1.Project{}
	proj.Default()
	m := dependency.NewManager(proj, afero.NewOsFs(),
		dependency.WithSchemaFS(afero.NewBasePathFs(afero.NewOsFs(), outDir)),
		dependency.WithSchemaGenerators(generator.Filter(
			generator.AllLanguages(
				generator.WithGoModelAccessors(cfg.Features.GenerateGoModelAccessors),
				generator.WithGoRuntimeObjects(cfg.Features.GenerateGoRuntimeObjects),
			),
			c.Languages,
		)),
		dependency.WithXpkgClient(client),
	)

	desc := dependency.GetSourceDescription(dep)
	logger.Debug("Generating schemas", "source", desc, "output", outDir)
	return sp.WrapWithSuccessSpinner("Generating schemas for "+desc, func() error {
		return m.GenerateSchemas(context.Background(), &dep)
	})
}
