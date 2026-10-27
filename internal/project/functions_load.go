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

	devv1alpha1 "github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/internal/async"
	"github.com/crossplane/cli/v2/internal/dependency"
	"github.com/crossplane/cli/v2/internal/project/functions"
	"github.com/crossplane/cli/v2/internal/schemas/generator"
	"github.com/crossplane/cli/v2/internal/schemas/manager"
	"github.com/crossplane/cli/v2/internal/schemas/runner"
	"github.com/crossplane/cli/v2/internal/terminal"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"
)

// NewXpkgClient returns an xpkg client that caches packages in cacheDir, or
// in dependency.DefaultCacheDir() when cacheDir is empty, and a resolver
// backed by it.
func NewXpkgClient(cacheDir string, opts ...clixpkg.ClientOption) (runtimexpkg.Client, *clixpkg.Resolver, error) {
	if cacheDir == "" {
		cacheDir = dependency.DefaultCacheDir()
	}

	opts = append([]clixpkg.ClientOption{clixpkg.WithCacheDir(afero.NewOsFs(), cacheDir)}, opts...)
	client, err := clixpkg.NewClient(clixpkg.NewRemoteFetcher(), opts...)
	if err != nil {
		return nil, nil, errors.Wrap(err, "cannot create xpkg client")
	}
	return client, clixpkg.NewResolver(client), nil
}

// LoadFunctionsOptions configure LoadFunctions.
type LoadFunctionsOptions struct {
	Logger  logging.Logger
	Spinner terminal.SpinnerPrinter

	// Generators generate the language schemas the embedded functions build
	// against; nil generates none. Pass the ones the dependency manager was
	// built with, so both generate schemas the same way.
	Generators []generator.Interface

	// Digests computes the index digest of each embedded function, which
	// needs a project repository with an explicit registry.
	Digests bool

	// MaxConcurrency is the most embedded functions built at once.
	MaxConcurrency uint
}

// LoadFunctions resolves a project's function dependencies with depMgr and
// builds its embedded functions, loading their images into the docker daemon.
// It returns a Function for each dependency and each embedded function and,
// when o.Digests is set, the index digest of each embedded function, as
// EmbeddedFunctionDigests does.
func LoadFunctions(ctx context.Context, proj *devv1alpha1.Project, projFS afero.Fs, depMgr *dependency.Manager, o LoadFunctionsOptions) ([]pkgv1.Function, map[string]EmbeddedDigest, error) {
	log, sp := o.Logger, o.Spinner

	var fns []pkgv1.Function
	var digests map[string]EmbeddedDigest
	if err := sp.WrapWithSuccessSpinner("Resolving function dependencies", func() error {
		var err error
		fns, err = LoadFunctionDependencies(depMgr, proj)
		return err
	}); err != nil {
		return nil, nil, errors.Wrap(err, "cannot load project functions")
	}

	if err := sp.WrapAsyncWithSuccessSpinners(func(ch async.EventChannel) error {
		schemasFS := afero.NewBasePathFs(projFS, proj.Spec.Paths.Schemas)
		schemaRunner := runner.NewRealSchemaRunner(runner.WithImageConfig(proj.Spec.ImageConfigs))
		schemaMgr := manager.New(schemasFS, o.Generators, schemaRunner)

		tempDir, err := os.MkdirTemp("", "crossplane-build-")
		if err != nil {
			return errors.Wrap(err, "failed to create temporary build directory")
		}
		defer os.RemoveAll(tempDir) //nolint:errcheck // Best-effort cleanup of temporary files.

		b := NewBuilder(
			BuildWithMaxConcurrency(o.MaxConcurrency),
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

		if o.Digests {
			if digests, err = EmbeddedFunctionDigests(imgMap, proj.Spec.Repository); err != nil {
				return err
			}
		}

		embeddedFns, err := EmbeddedFunctionsToDaemon(ctx, imgMap)
		fns = append(fns, embeddedFns...)
		return err
	}); err != nil {
		return nil, nil, errors.Wrap(err, "cannot build embedded functions")
	}

	return fns, digests, nil
}
