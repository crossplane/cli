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
	"github.com/crossplane/cli/v2/internal/dependency"
	"github.com/crossplane/cli/v2/internal/git"
	"github.com/crossplane/cli/v2/internal/project/projectfile"
	"github.com/crossplane/cli/v2/internal/terminal"

	_ "embed"
)

//go:embed help/update-cache.md
var updateHelp string

// updateCacheCmd refreshes the shared xpkg package cache and fetches the
// project's other dependencies.
type updateCacheCmd struct {
	git.HTTPSAuthFlags `embed:""`

	ProjectFile string `default:"${project_file}"   help:"Path to project definition file."            short:"f"`
	CacheDir    string `env:"CROSSPLANE_XPKG_CACHE" help:"Directory for cached xpkg package contents." name:"cache-dir"`
}

func (c *updateCacheCmd) Help() string {
	return updateHelp
}

// Run executes the update-cache command.
func (c *updateCacheCmd) Run(logger logging.Logger, sp terminal.SpinnerPrinter) error {
	ctx := context.Background()

	proj, projFS, err := projectfile.Load(c.ProjectFile)
	if err != nil {
		return err
	}

	m, err := dependency.NewCachedManager(proj, projFS, c.ProjectFile, c.CacheDir, dependency.WithGitAuthProvider(c.gitAuth()))
	if err != nil {
		return err
	}

	logger.Debug("Updating all dependencies")
	return sp.WrapAsyncWithSuccessSpinners(func(ch async.EventChannel) error {
		return m.RefreshAll(ctx, ch)
	})
}

// gitAuth returns the git authentication configured by the command's flags.
func (c *updateCacheCmd) gitAuth() git.AuthProvider {
	return c.AuthProvider()
}

//go:embed help/clean-cache.md
var cleanHelp string

// cleanCacheCmd removes the shared xpkg package cache.
type cleanCacheCmd struct {
	CacheDir string `env:"CROSSPLANE_XPKG_CACHE" help:"Directory for cached xpkg package contents." name:"cache-dir"`
}

func (c *cleanCacheCmd) Help() string {
	return cleanHelp
}

// Run executes the clean-cache command.
func (c *cleanCacheCmd) Run(k *kong.Context, _ logging.Logger) error {
	cacheDir := c.CacheDir
	if cacheDir == "" {
		cacheDir = dependency.DefaultCacheDir()
	}
	if err := dependency.CleanPackages(cacheDir, afero.NewOsFs()); err != nil {
		return err
	}

	fmt.Fprintln(k.Stdout, "Package cache cleaned") //nolint:errcheck // TODO(adamwg): Clean up output.
	return nil
}
