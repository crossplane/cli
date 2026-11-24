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
	"path/filepath"

	"github.com/alecthomas/kong"
	"github.com/spf13/afero"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	"github.com/crossplane/cli/v2/cmd/crossplane/common"
	"github.com/crossplane/cli/v2/internal/async"
	"github.com/crossplane/cli/v2/internal/config"
	"github.com/crossplane/cli/v2/internal/dependency"
	"github.com/crossplane/cli/v2/internal/project/projectfile"
	"github.com/crossplane/cli/v2/internal/terminal"
	"github.com/crossplane/cli/v2/pkg/project"

	_ "embed"
)

//go:embed help/update-cache.md
var updateHelp string

// UpdateCacheCmd updates the dependency cache by regenerating all schemas.
// It is exported so host CLIs embedding the dependency commands can wrap it,
// e.g. to generate additional schemas after the update.
type UpdateCacheCmd struct {
	common.GitFlags

	ProjectFile string `default:"${project_file}"   help:"Path to project definition file."            short:"f"`
	CacheDir    string `env:"CROSSPLANE_XPKG_CACHE" help:"Directory for cached xpkg package contents." name:"cache-dir"`
}

// Help returns help for the update-cache command.
func (c *UpdateCacheCmd) Help() string {
	return updateHelp
}

// Run executes the update-cache command.
func (c *UpdateCacheCmd) Run(logger logging.Logger, sp terminal.SpinnerPrinter, cfg *config.Config) error {
	ctx := context.Background()

	projFilePath, err := filepath.Abs(c.ProjectFile)
	if err != nil {
		return err
	}
	projDirPath := filepath.Dir(projFilePath)
	projFS := afero.NewBasePathFs(afero.NewOsFs(), projDirPath)

	proj, err := projectfile.Parse(projFS, filepath.Base(c.ProjectFile))
	if err != nil {
		return err
	}

	logger.Debug("Updating all dependencies")
	return sp.WrapAsyncWithSuccessSpinners(func(ch async.EventChannel) error {
		return project.UpdateSchemas(ctx, cfg, proj, projFS, project.UpdateSchemasOptions{
			CacheDir:           c.CacheDir,
			GitUsername:        c.GitUsername,
			GitToken:           c.GitToken,
			GitProxy:           c.GitProxy,
			GitInsecureHostKey: c.GitInsecureHostKey,
			Refresh:            true,
			EventChannel:       ch,
		})
	})
}

//go:embed help/clean-cache.md
var cleanHelp string

// CleanCacheCmd removes all generated schemas.
type CleanCacheCmd struct {
	ProjectFile  string `default:"${project_file}"                                                help:"Path to project definition file."            short:"f"`
	CacheDir     string `env:"CROSSPLANE_XPKG_CACHE"                                              help:"Directory for cached xpkg package contents." name:"cache-dir"`
	KeepPackages bool   `help:"Keep cached xpkg package contents; remove only generated schemas." name:"keep-packages"`
}

// Help returns help for the clean-cache command.
func (c *CleanCacheCmd) Help() string {
	return cleanHelp
}

// Run executes the clean-cache command.
func (c *CleanCacheCmd) Run(k *kong.Context, _ logging.Logger) error {
	projFilePath, err := filepath.Abs(c.ProjectFile)
	if err != nil {
		return err
	}
	projDirPath := filepath.Dir(projFilePath)
	projFS := afero.NewBasePathFs(afero.NewOsFs(), projDirPath)

	proj, err := projectfile.Parse(projFS, filepath.Base(c.ProjectFile))
	if err != nil {
		return err
	}

	m := dependency.NewManager(proj, projFS,
		dependency.WithProjectFile(c.ProjectFile),
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
