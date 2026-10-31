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

package project

import (
	"cmp"
	"context"
	"path/filepath"

	"github.com/alecthomas/kong"
	"github.com/spf13/afero"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	"github.com/crossplane/cli/v2/internal/config"
	"github.com/crossplane/cli/v2/internal/dependency"
	"github.com/crossplane/cli/v2/internal/project"
	"github.com/crossplane/cli/v2/internal/project/projectfile"
	"github.com/crossplane/cli/v2/internal/schemas/generator"
	"github.com/crossplane/cli/v2/internal/simulate"
	"github.com/crossplane/cli/v2/internal/terminal"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"

	_ "embed"
)

var (
	//go:embed help/simulate.md
	simulateHelp string
	//go:embed help/simulate_examples.md
	simulateExamples string
)

// simulateCmd previews how a project's compositions would change the
// composite resources in a cluster.
type simulateCmd struct {
	simulate.Flags `embed:""`

	ProjectFile string `default:"${project_file}" help:"Path to project definition." short:"f"`
}

func (c *simulateCmd) Help() string {
	return simulateHelp + simulate.Help(true) + simulateExamples
}

// Run previews the project's compositions against the cluster, with its
// functions built locally.
func (c *simulateCmd) Run(sigCtx context.Context, k *kong.Context, logger logging.Logger, sp terminal.SpinnerPrinter, cliCfg *config.Config) error {
	return c.Flags.Run(sigCtx, k, logger, sp, &simulate.Inputs{Build: func(ctx context.Context) (*simulate.Built, error) {
		return c.buildProject(ctx, logger, sp, cliCfg)
	}})
}

// buildProject returns the project's compositions, its locally built
// functions, and the packages its provider dependencies resolve through.
// --image-configs, when set, replaces the project's ImageConfigs for both.
func (c *simulateCmd) buildProject(ctx context.Context, logger logging.Logger, sp terminal.SpinnerPrinter, cliCfg *config.Config) (*simulate.Built, error) {
	projFilePath, err := filepath.Abs(c.ProjectFile)
	if err != nil {
		return nil, err
	}
	projFS := afero.NewBasePathFs(afero.NewOsFs(), filepath.Dir(projFilePath))
	projFileName := filepath.Base(projFilePath)

	proj, err := projectfile.Parse(projFS, projFileName)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to parse project file %q", c.ProjectFile)
	}

	comps, err := project.LoadCompositions(proj, projFS)
	if err != nil {
		return nil, errors.Wrap(err, "cannot load compositions from project")
	}
	if len(comps) == 0 {
		return nil, errors.Errorf("no Compositions found under the project's APIs path %q", proj.Spec.Paths.APIs)
	}

	ics := proj.Spec.ImageConfigs
	if c.ImageConfigs != "" {
		if ics, err = simulate.ImageConfigs(c.ImageConfigs, ""); err != nil {
			return nil, err
		}
	}
	client, err := clixpkg.NewClient(clixpkg.WithCacheDir(afero.NewOsFs(), cmp.Or(c.CacheDir, dependency.DefaultCacheDir())), clixpkg.WithImageConfigs(ics))
	if err != nil {
		return nil, errors.Wrap(err, "cannot create xpkg client")
	}
	generators := generator.AllLanguages(
		generator.WithGoModelAccessors(cliCfg.Features.GenerateGoModelAccessors),
		generator.WithGoRuntimeObjects(cliCfg.Features.GenerateGoRuntimeObjects),
	)
	depMgr := dependency.NewManager(proj, projFS,
		dependency.WithProjectFile(projFileName),
		dependency.WithSchemaGenerators(generators),
		dependency.WithXpkgClient(client),
	)

	logger.Debug("Loading functions from project", "project-file", projFilePath)
	fns, digests, err := project.LoadFunctions(ctx, proj, projFS, depMgr, project.LoadFunctionsOptions{
		Logger:         logger,
		Spinner:        sp,
		Generators:     generators,
		MaxConcurrency: c.MaxConcurrency,
		Digests:        true,
	})
	if err != nil {
		return nil, err
	}
	return &simulate.Built{Compositions: comps, Functions: fns, Digests: digests, Packages: &simulate.Packages{ICs: ics, Client: client, Proj: proj, DepMgr: depMgr}}, nil
}
