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
	"context"
	"fmt"
	"path/filepath"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/spf13/afero"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	devv1alpha1 "github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/internal/async"
	"github.com/crossplane/cli/v2/internal/dependency"
	"github.com/crossplane/cli/v2/internal/project"
	"github.com/crossplane/cli/v2/internal/project/functions"
	"github.com/crossplane/cli/v2/internal/project/projectfile"
	"github.com/crossplane/cli/v2/internal/schemas/generator"
	"github.com/crossplane/cli/v2/internal/schemas/manager"
	"github.com/crossplane/cli/v2/internal/schemas/runner"
	"github.com/crossplane/cli/v2/internal/terminal"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"
)

// buildCmd builds a project into Crossplane packages.
type buildCmd struct {
	ProjectFile    string `default:"crossplane-project.yaml"                   help:"Path to project definition."                 short:"f"`
	Repository     string `help:"Override the repository in the project file." optional:""`
	OutputDir      string `default:"_output"                                   help:"Output directory for packages."              short:"o"`
	MaxConcurrency uint   `default:"8"                                         help:"Max concurrent function builds."`
	CacheDir       string `env:"CROSSPLANE_XPKG_CACHE"                         help:"Directory for cached xpkg package contents." name:"cache-dir"`

	proj   *devv1alpha1.Project
	projFS afero.Fs
}

// AfterApply parses flags and reads the project file.
func (c *buildCmd) AfterApply() error {
	projFilePath, err := filepath.Abs(c.ProjectFile)
	if err != nil {
		return err
	}
	projDirPath := filepath.Dir(projFilePath)
	c.projFS = afero.NewBasePathFs(afero.NewOsFs(), projDirPath)

	projFileName := filepath.Base(c.ProjectFile)
	prj, err := projectfile.Parse(c.projFS, projFileName)
	if err != nil {
		return errors.New("this is not a project directory")
	}
	c.proj = prj

	return nil
}

// Run executes the build command.
func (c *buildCmd) Run(logger logging.Logger, sp terminal.SpinnerPrinter) error {
	ctx := context.Background()

	if c.Repository != "" {
		ref, err := name.NewRepository(c.Repository)
		if err != nil {
			return errors.Wrap(err, "failed to parse repository")
		}
		c.proj.Spec.Repository = ref.String()
	}

	concurrency := max(1, c.MaxConcurrency)

	schemasFS := afero.NewBasePathFs(c.projFS, c.proj.Spec.Paths.Schemas)
	generators := generator.AllLanguages()
	schemaRunner := runner.NewRealSchemaRunner(runner.WithImageConfig(c.proj.Spec.ImageConfigs))
	schemaMgr := manager.New(schemasFS, generators, schemaRunner)
	cacheDir := c.CacheDir
	if cacheDir == "" {
		cacheDir = dependency.DefaultCacheDir()
	}

	client, err := clixpkg.NewClient(
		clixpkg.NewRemoteFetcher(),
		clixpkg.WithCacheDir(afero.NewOsFs(), cacheDir),
	)
	if err != nil {
		return err
	}
	resolver := clixpkg.NewResolver(client)

	depMgr := dependency.NewManager(c.proj, c.projFS,
		dependency.WithProjectFile(c.ProjectFile),
		dependency.WithSchemaFS(schemasFS),
		dependency.WithSchemaGenerators(generators),
		dependency.WithSchemaRunner(schemaRunner),
		dependency.WithXpkgClient(client),
		dependency.WithResolver(resolver),
	)

	b := project.NewBuilder(
		project.BuildWithMaxConcurrency(concurrency),
		project.BuildWithFunctionIdentifier(functions.DefaultIdentifier),
		project.BuildWithSchemaManager(schemaMgr),
		project.BuildWithDependencyManager(depMgr),
	)

	var imgMap project.ImageTagMap
	err = sp.WrapAsyncWithSuccessSpinners(func(ch async.EventChannel) error {
		var buildErr error
		imgMap, buildErr = b.Build(ctx, c.proj, c.projFS,
			project.BuildWithLogger(logger),
			project.BuildWithEventChannel(ch),
		)
		return buildErr
	})
	if err != nil {
		return err
	}

	outFile := filepath.Join(c.OutputDir, fmt.Sprintf("%s.xpkg", c.proj.Name))
	if err := sp.WrapWithSuccessSpinner("Writing packages to disk", func() error {
		outputFS := afero.NewOsFs()
		err = outputFS.MkdirAll(c.OutputDir, 0o755)
		if err != nil {
			return errors.Wrapf(err, "failed to create output directory %q", c.OutputDir)
		}

		f, err := outputFS.Create(outFile)
		if err != nil {
			return errors.Wrapf(err, "failed to create output file %q", outFile)
		}
		defer f.Close() //nolint:errcheck // Can't do anything useful with this error.

		err = tarball.MultiWrite(imgMap, f)
		if err != nil {
			return errors.Wrap(err, "failed to write package to file")
		}
		return nil
	}); err != nil {
		return err
	}

	logger.Debug("Build complete", "output", outFile)
	fmt.Printf("Built project %q to %s\n", c.proj.Name, outFile) //nolint:forbidigo // CLI output.

	return nil
}
