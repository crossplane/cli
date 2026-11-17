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

// Package project exposes the crossplane CLI's project machinery to host
// CLIs: parsing project files, building projects into package images, and
// generating language schemas. Terminal, configuration, and Kong binding
// helpers live in pkg/cli.
package project

import (
	"cmp"
	"context"
	"os"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/spf13/afero"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	pkgv1beta1 "github.com/crossplane/crossplane/apis/v2/pkg/v1beta1"

	devv1alpha1 "github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/internal/async"
	"github.com/crossplane/cli/v2/internal/config"
	"github.com/crossplane/cli/v2/internal/dependency"
	"github.com/crossplane/cli/v2/internal/git"
	internalproject "github.com/crossplane/cli/v2/internal/project"
	"github.com/crossplane/cli/v2/internal/project/functions"
	"github.com/crossplane/cli/v2/internal/project/projectfile"
	"github.com/crossplane/cli/v2/internal/schemas/generator"
	"github.com/crossplane/cli/v2/internal/schemas/manager"
	"github.com/crossplane/cli/v2/internal/schemas/runner"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"
)

// Config is the crossplane CLI configuration; see pkg/cli for loading it.
type Config = config.Config

// EventChannel carries asynchronous progress events during long-running
// operations such as builds.
type EventChannel = async.EventChannel

// FileName is the default name of a project's definition file.
const FileName = clixpkg.ProjectFile

// APIVersion and Kind identify the project file's type.
const (
	APIVersion = devv1alpha1.APIVersion
	Kind       = devv1alpha1.Kind
)

// ImageTagMap maps image tags to the images that should be pushed to them,
// as produced by a project build.
type ImageTagMap = internalproject.ImageTagMap

// ParseProject reads, validates, and defaults the project file at
// projFilePath within projFS.
func ParseProject(projFS afero.Fs, projFilePath string) (*devv1alpha1.Project, error) {
	return projectfile.Parse(projFS, projFilePath)
}

// SortImages analyzes an image map produced by a project build and picks out
// the configuration image and the embedded function images, grouped by the
// repository they belong to.
func SortImages(imgMap ImageTagMap, repo string) (v1.Image, map[name.Repository][]v1.Image, error) {
	return internalproject.SortImages(imgMap, repo)
}

// BuildOptions configures a project build.
type BuildOptions struct {
	// MaxConcurrency bounds concurrent embedded function builds. Defaults
	// to 8.
	MaxConcurrency uint
	// CacheDir is the xpkg dependency cache directory. Defaults to the
	// standard per-user crossplane cache location.
	CacheDir string
	// Keychain authenticates registry requests when fetching dependencies.
	// Defaults to the standard container credential helpers.
	Keychain authn.Keychain
	// GitUsername and GitToken authenticate git-based dependencies over
	// HTTPS.
	GitUsername string
	GitToken    string
	// EventChannel receives progress events during the build, if set.
	EventChannel async.EventChannel
	// Logger receives debug output during the build, if set.
	Logger logging.Logger
	// BasePath is the real on-disk base path of projFS, needed to build
	// embedded functions whose runtimes are built with Docker.
	BasePath string
}

// Build builds a project into its package images, generating schemas and
// resolving dependencies the same way `crossplane project build` does. It
// returns the built images by tag, the digests of the embedded function
// image indexes keyed by function repository (dependencies resolved from
// registries are not included), and a cleanup function releasing the build's
// temporary files. The images read from those files lazily, so call cleanup
// only after the images have been consumed (pushed, sideloaded, or written);
// it is safe to call always, including on error.
func Build(ctx context.Context, cfg *config.Config, proj *devv1alpha1.Project, projFS afero.Fs, o BuildOptions) (ImageTagMap, map[string]name.Digest, func(), error) {
	logger := o.Logger
	if logger == nil {
		logger = logging.NewNopLogger()
	}

	schemasFS := afero.NewBasePathFs(projFS, proj.Spec.Paths.Schemas)
	generators := generator.Filter(
		generator.AllLanguages(
			generator.WithGoModelAccessors(cfg.Features.GenerateGoModelAccessors),
			generator.WithGoRuntimeObjects(cfg.Features.GenerateGoRuntimeObjects),
		),
		proj.Spec.Schemas.GetLanguages(),
	)
	schemaRunner := runner.NewRealSchemaRunner(runner.WithImageConfig(proj.Spec.ImageConfigs))
	schemaMgr := manager.New(schemasFS, generators, schemaRunner)

	clientOpts := []clixpkg.ClientOption{
		clixpkg.WithCacheDir(afero.NewOsFs(), cmp.Or(o.CacheDir, dependency.DefaultCacheDir())),
		clixpkg.WithImageConfigs(proj.Spec.ImageConfigs),
	}
	if o.Keychain != nil {
		clientOpts = append(clientOpts, clixpkg.WithAuthKeychain(o.Keychain))
	}
	client, err := clixpkg.NewClient(clientOpts...)
	if err != nil {
		return nil, nil, noopCleanup, errors.Wrap(err, "cannot create xpkg client")
	}

	depOpts := []dependency.ManagerOption{
		dependency.WithProjectFile(FileName),
		dependency.WithSchemaFS(schemasFS),
		dependency.WithSchemaGenerators(generators),
		dependency.WithSchemaRunner(schemaRunner),
		dependency.WithXpkgClient(client),
	}
	if o.GitToken != "" {
		depOpts = append(depOpts, dependency.WithGitAuthProvider(&git.HTTPSAuthProvider{
			Username: o.GitUsername,
			Password: o.GitToken,
		}))
	}
	depMgr := dependency.NewManager(proj, projFS, depOpts...)

	// The builder may decompress function runtime tarballs into this
	// directory; the built images read from it lazily, so it is only removed
	// by the returned cleanup function, once the images have been consumed.
	tempDir, err := os.MkdirTemp("", "crossplane-build-")
	if err != nil {
		return nil, nil, noopCleanup, errors.Wrap(err, "failed to create temporary build directory")
	}
	cleanup := func() { _ = os.RemoveAll(tempDir) }

	b := internalproject.NewBuilder(
		internalproject.BuildWithMaxConcurrency(cmp.Or(o.MaxConcurrency, 8)),
		internalproject.BuildWithFunctionIdentifier(functions.DefaultIdentifier),
		internalproject.BuildWithSchemaManager(schemaMgr),
		internalproject.BuildWithDependencyManager(depMgr),
		internalproject.BuildWithTempDir(tempDir),
		internalproject.BuildWithBaseImageCacheDir(functions.DefaultBaseImageCacheDir()),
	)

	buildOpts := []internalproject.BuildOption{
		internalproject.BuildWithLogger(logger),
	}
	if o.EventChannel != nil {
		buildOpts = append(buildOpts, internalproject.BuildWithEventChannel(o.EventChannel))
	}
	if o.BasePath != "" {
		buildOpts = append(buildOpts, internalproject.BuildWithProjectBasePath(o.BasePath))
	}

	imgMap, deps, err := b.Build(ctx, proj, projFS, buildOpts...)
	if err != nil {
		cleanup()
		return nil, nil, noopCleanup, err
	}

	return imgMap, deps, cleanup, nil
}

// noopCleanup is returned alongside errors so callers can call cleanup
// unconditionally.
func noopCleanup() {}

// SchemaSource is a source of resources (CRDs/XRDs) for which language
// schemas can be generated.
type SchemaSource = manager.Source

// SchemaSourceType identifies the kind of resources a SchemaSource provides.
type SchemaSourceType = manager.SourceType

// SchemaSourceTypeCRD indicates a source containing CRDs/XRDs.
const SchemaSourceTypeCRD = manager.SourceTypeCRD

// AddSchemas generates language schemas for the resources provided by src
// into the project's schemas directory, using the same generators and layout
// as schema generation for the project's dependencies and APIs. Host CLIs
// use this to merge schemas for their own meta APIs into the project's
// models.
func AddSchemas(ctx context.Context, cfg *config.Config, proj *devv1alpha1.Project, projFS afero.Fs, src SchemaSource) error {
	schemasFS := afero.NewBasePathFs(projFS, proj.Spec.Paths.Schemas)
	generators := generator.Filter(
		generator.AllLanguages(
			generator.WithGoModelAccessors(cfg.Features.GenerateGoModelAccessors),
			generator.WithGoRuntimeObjects(cfg.Features.GenerateGoRuntimeObjects),
		),
		proj.Spec.Schemas.GetLanguages(),
	)
	schemaRunner := runner.NewRealSchemaRunner(runner.WithImageConfig(proj.Spec.ImageConfigs))

	return manager.New(schemasFS, generators, schemaRunner).Add(ctx, src)
}

// SchemaRunner runs language tooling in containers over a filesystem. It is
// used for schema generation and by host CLIs to execute language-based
// tests.
type SchemaRunner = runner.SchemaRunner

// SchemaRunnerOption configures a single SchemaRunner.Generate invocation.
type SchemaRunnerOption = runner.Option

// NewSchemaRunner returns a SchemaRunner that rewrites tooling images
// according to the given image configs. Environment variables with the given
// extra prefixes are forwarded into the tooling containers in addition to
// the default CROSSPLANE_ prefix.
func NewSchemaRunner(cfgs []pkgv1beta1.ImageConfig, extraEnvPrefixes ...string) SchemaRunner {
	return runner.NewRealSchemaRunner(
		runner.WithImageConfig(cfgs),
		runner.WithEnvPrefixes(extraEnvPrefixes...),
	)
}

// WithWorkDirectory sets the working directory for a Generate invocation.
func WithWorkDirectory(dir string) SchemaRunnerOption {
	return runner.WithWorkDirectory(dir)
}

// WithCopyToPath sets the container path the input filesystem is copied to.
func WithCopyToPath(path string) SchemaRunnerOption {
	return runner.WithCopyToPath(path)
}

// WithCopyFromPath sets the container path results are copied back from.
func WithCopyFromPath(path string) SchemaRunnerOption {
	return runner.WithCopyFromPath(path)
}

// WithVolumeMount mounts a named Docker volume at the given container path,
// e.g. to give the container a cache that outlives it.
func WithVolumeMount(name, containerPath string) SchemaRunnerOption {
	return runner.WithVolumeMount(name, containerPath)
}
