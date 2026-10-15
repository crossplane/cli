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
	"context"
	"fmt"

	"github.com/alecthomas/kong"
	"github.com/spf13/afero"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	"github.com/crossplane/cli/v2/internal/async"
	"github.com/crossplane/cli/v2/internal/config"
	"github.com/crossplane/cli/v2/internal/dependency"
	"github.com/crossplane/cli/v2/internal/project/projectfile"
	"github.com/crossplane/cli/v2/internal/schemas/generator"
	"github.com/crossplane/cli/v2/internal/terminal"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"

	_ "embed"
)

//go:embed help/update-cache.md
var updateHelp string

// updateCacheCmd updates the dependency cache by regenerating all schemas.
type updateCacheCmd struct {
	CacheDir    string `env:"CROSSPLANE_XPKG_CACHE" help:"Directory for cached xpkg package contents." name:"cache-dir"`
	GitToken    string `env:"CROSSPLANE_GIT_TOKEN"  help:"Token for git HTTPS authentication."`
	GitUsername string `default:"x-access-token"    env:"CROSSPLANE_GIT_USERNAME"                      help:"Username for git HTTPS authentication."`

	ProjectFile     string   `help:"Path to the project file or package metadata file (crossplane.yaml). Autodetected if not set."                                                      optional:""             predictor:"yaml_file" short:"f" type:"path"`
	SchemasDir      string   `help:"Directory for generated schemas, relative to the project file. Overrides the project's schemas path (default: schemas)."                            name:"schemas-dir"`
	SchemaLanguages []string `help:"Comma-separated schema languages to generate (go, json, kcl, python). Overrides the project's schemas.languages; defaults to all."                  name:"schema-languages"`
	K8sVersion      string   `help:"Kubernetes version to generate core API schemas for (e.g. v1.33.0). Replaces the project's k8s dependency for this run; never written to the file." name:"k8s-version"`
}

func (c *updateCacheCmd) Help() string {
	return updateHelp
}

// Run executes the update-cache command.
func (c *updateCacheCmd) Run(logger logging.Logger, sp terminal.SpinnerPrinter, cfg *config.Config) error {
	ctx := context.Background()

	proj, projFS, projFile, err := loadProject(c.ProjectFile, projectfile.Overrides{
		SchemasDir:      c.SchemasDir,
		SchemaLanguages: c.SchemaLanguages,
		K8sVersion:      c.K8sVersion,
	})
	if err != nil {
		return err
	}

	cacheDir := c.CacheDir
	if cacheDir == "" {
		cacheDir = dependency.DefaultCacheDir()
	}

	client, err := clixpkg.NewClient(
		clixpkg.NewRemoteFetcher(),
		clixpkg.WithCacheDir(afero.NewOsFs(), cacheDir),
		clixpkg.WithImageConfigs(proj.Spec.ImageConfigs),
	)
	if err != nil {
		return err
	}
	resolver := clixpkg.NewResolver(client)

	opts := []dependency.ManagerOption{
		dependency.WithProjectFile(projFile),
		dependency.WithSchemaGenerators(generator.Filter(
			generator.AllLanguages(
				generator.WithGoModelAccessors(cfg.Features.GenerateGoModelAccessors),
				generator.WithGoRuntimeObjects(cfg.Features.GenerateGoRuntimeObjects),
			),
			proj.Spec.Schemas.GetLanguages(),
		)),
		dependency.WithXpkgClient(client),
		dependency.WithResolver(resolver),
	}

	if c.GitToken != "" {
		opts = append(opts, dependency.WithGitAuthProvider(&gitAuthProvider{
			username: c.GitUsername,
			token:    c.GitToken,
		}))
	}

	m := dependency.NewManager(proj, projFS, opts...)

	logger.Debug("Updating all dependencies")
	return sp.WrapAsyncWithSuccessSpinners(func(ch async.EventChannel) error {
		return m.RefreshAll(ctx, ch)
	})
}

//go:embed help/clean-cache.md
var cleanHelp string

// cleanCacheCmd removes all generated schemas.
type cleanCacheCmd struct {
	CacheDir     string `env:"CROSSPLANE_XPKG_CACHE"                                              help:"Directory for cached xpkg package contents." name:"cache-dir"`
	KeepPackages bool   `help:"Keep cached xpkg package contents; remove only generated schemas." name:"keep-packages"`

	ProjectFile string `help:"Path to the project file or package metadata file (crossplane.yaml). Autodetected if not set."                           optional:""        predictor:"yaml_file" short:"f" type:"path"`
	SchemasDir  string `help:"Directory for generated schemas, relative to the project file. Overrides the project's schemas path (default: schemas)." name:"schemas-dir"`
}

func (c *cleanCacheCmd) Help() string {
	return cleanHelp
}

// Run executes the clean-cache command.
func (c *cleanCacheCmd) Run(k *kong.Context, _ logging.Logger) error {
	proj, projFS, projFile, err := loadProject(c.ProjectFile, projectfile.Overrides{SchemasDir: c.SchemasDir})
	if err != nil {
		return err
	}

	m := dependency.NewManager(proj, projFS,
		dependency.WithProjectFile(projFile),
	)

	if err := m.Clean(); err != nil {
		return err
	}

	if !c.KeepPackages {
		cacheDir := c.CacheDir
		if cacheDir == "" {
			cacheDir = dependency.DefaultCacheDir()
		}
		if err := dependency.CleanPackages(cacheDir, afero.NewOsFs()); err != nil {
			return err
		}
	}

	fmt.Fprintln(k.Stdout, "Schema cache cleaned") //nolint:errcheck // TODO(adamwg): Clean up output.
	return nil
}
