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

package dependency

import (
	"cmp"
	"context"

	"github.com/spf13/afero"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	"github.com/crossplane/cli/v2/internal/config"
	"github.com/crossplane/cli/v2/internal/dependency"
	"github.com/crossplane/cli/v2/internal/schemas/generator"
	"github.com/crossplane/cli/v2/internal/terminal"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"

	_ "embed"
)

//go:embed help/add.md
var addHelp string

// addCmd adds a dependency to the current project.
type addCmd struct {
	CacheDir string `env:"CROSSPLANE_XPKG_CACHE" help:"Directory for cached xpkg package contents."                                             name:"cache-dir"`
	Package  string `arg:""                      help:"Package to add (xpkg OCI reference, k8s:<version>, git repository URL, or HTTP(S) URL)."`

	ProjectFile     string   `help:"Path to the project file or package metadata file (crossplane.yaml). Autodetected if not set."                                           optional:""             predictor:"yaml_file" short:"f" type:"path"`
	SchemasDir      string   `help:"Directory for generated schemas, relative to the project file. Overrides the project's schemas path (default: schemas)."                 name:"schemas-dir"`
	SchemaLanguages []string `help:"Comma-separated schema languages to generate (go, json, kcl, python, rust). Overrides the project's schemas.languages; defaults to all." name:"schema-languages"`

	// Flags for specific dependency types.
	APIOnly bool   `help:"Mark an xpkg dependency as API-only (not a runtime dependency)." name:"api-only"`
	GitRef  string `help:"Git ref for CRD dependencies (branch, tag, or commit SHA)."      name:"git-ref"`
	GitPath string `help:"Path to CRDs in the git repository."                             name:"git-path"`
}

func (c *addCmd) Help() string {
	return addHelp
}

// Run executes the add command.
func (c *addCmd) Run(logger logging.Logger, sp terminal.SpinnerPrinter, cfg *config.Config) error {
	ctx := context.Background()

	proj, store, projFS, err := loadProject(c.ProjectFile, Overrides{
		SchemasDir:      c.SchemasDir,
		SchemaLanguages: c.SchemaLanguages,
	})
	if err != nil {
		return err
	}

	client, err := clixpkg.NewClient(clixpkg.WithCacheDir(afero.NewOsFs(), cmp.Or(c.CacheDir, dependency.DefaultCacheDir())), clixpkg.WithImageConfigs(proj.Spec.ImageConfigs))
	if err != nil {
		return errors.Wrap(err, "cannot create xpkg client")
	}

	m := dependency.NewManager(proj, projFS,
		dependency.WithStore(store),
		dependency.WithSchemaGenerators(generator.Filter(
			generator.AllLanguages(
				generator.WithGoModelAccessors(cfg.Features.GenerateGoModelAccessors),
				generator.WithGoRuntimeObjects(cfg.Features.GenerateGoRuntimeObjects),
			),
			proj.Spec.Schemas.GetLanguages(),
		)),
		dependency.WithXpkgClient(client),
	)

	dep, err := dependency.ParseSource(c.Package, c.GitRef, c.GitPath)
	if err != nil {
		return err
	}
	if c.APIOnly {
		if dep.Xpkg == nil {
			return errors.New("--api-only is only valid for xpkg dependencies")
		}
		dep.Xpkg.APIOnly = true
	}

	desc := dependency.GetSourceDescription(dep)
	logger.Debug("Adding dependency", "dependency", desc)
	return sp.WrapWithSuccessSpinner("Adding "+desc, func() error {
		return m.AddDependency(ctx, &dep)
	})
}
