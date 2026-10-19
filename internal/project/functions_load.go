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
	"os"

	"github.com/spf13/afero"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	runtimexpkg "github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"

	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"

	"github.com/crossplane/cli/v2/internal/async"
	"github.com/crossplane/cli/v2/internal/config"
	"github.com/crossplane/cli/v2/internal/dependency"
	"github.com/crossplane/cli/v2/internal/project/functions"
	"github.com/crossplane/cli/v2/internal/project/projectfile"
	"github.com/crossplane/cli/v2/internal/schemas/generator"
	"github.com/crossplane/cli/v2/internal/schemas/manager"
	"github.com/crossplane/cli/v2/internal/schemas/runner"
	"github.com/crossplane/cli/v2/internal/terminal"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"
)

// NewXpkgClientFn returns an xpkg client and resolver built with the given
// options.
type NewXpkgClientFn func(opts ...clixpkg.ClientOption) (runtimexpkg.Client, *clixpkg.Resolver, error)

// LoadFunctions resolves a project's function dependencies and builds its
// embedded functions, loading their images into the docker daemon. It returns
// a Function for each dependency and each embedded function.
func LoadFunctions(ctx context.Context, log logging.Logger, sp terminal.SpinnerPrinter, cfg *config.Config, projFS afero.Fs, projFilePath, projFileName string, maxConcurrency uint, newClient NewXpkgClientFn) ([]pkgv1.Function, error) {
	log.Debug("Loading functions from project", "project-file", projFilePath)

	proj, err := projectfile.Parse(projFS, projFileName)
	if err != nil {
		return nil, errors.Wrapf(err, "cannot parse project file %q", projFilePath)
	}

	xpkgClient, resolver, err := newClient(clixpkg.WithImageConfigs(proj.Spec.ImageConfigs))
	if err != nil {
		return nil, err
	}

	generators := generator.AllLanguages(
		generator.WithGoModelAccessors(cfg.Features.GenerateGoModelAccessors),
		generator.WithGoRuntimeObjects(cfg.Features.GenerateGoRuntimeObjects),
	)

	depMgr := dependency.NewManager(proj, projFS,
		dependency.WithProjectFile(projFileName),
		dependency.WithSchemaGenerators(generators),
		dependency.WithXpkgClient(xpkgClient),
		dependency.WithResolver(resolver),
	)

	var fns []pkgv1.Function
	if err := sp.WrapWithSuccessSpinner("Resolving function dependencies", func() error {
		var err error
		fns, err = LoadFunctionDependencies(depMgr, proj)
		return err
	}); err != nil {
		return nil, errors.Wrap(err, "cannot load project functions")
	}

	if err := sp.WrapAsyncWithSuccessSpinners(func(ch async.EventChannel) error {
		schemasFS := afero.NewBasePathFs(projFS, proj.Spec.Paths.Schemas)
		schemaRunner := runner.NewRealSchemaRunner(runner.WithImageConfig(proj.Spec.ImageConfigs))
		schemaMgr := manager.New(schemasFS, generators, schemaRunner)

		tempDir, err := os.MkdirTemp("", "crossplane-build-")
		if err != nil {
			return errors.Wrap(err, "failed to create temporary build directory")
		}
		defer os.RemoveAll(tempDir) //nolint:errcheck // Best-effort cleanup of temporary files.

		b := NewBuilder(
			BuildWithMaxConcurrency(maxConcurrency),
			BuildWithFunctionIdentifier(functions.DefaultIdentifier),
			BuildWithSchemaManager(schemaMgr),
			BuildWithDependencyManager(depMgr),
			BuildWithTempDir(tempDir),
			BuildWithBaseImageCacheDir(functions.DefaultBaseImageCacheDir()),
		)

		imgMap, err := b.Build(ctx, proj, projFS,
			BuildWithLogger(log),
			BuildWithEventChannel(ch),
		)
		if err != nil {
			return err
		}

		embeddedFns, err := EmbeddedFunctionsToDaemon(ctx, imgMap)
		fns = append(fns, embeddedFns...)
		return err
	}); err != nil {
		return nil, errors.Wrap(err, "cannot build embedded functions")
	}

	return fns, nil
}
