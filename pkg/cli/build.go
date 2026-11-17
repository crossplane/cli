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

package cli

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

	devv1alpha1 "github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/internal/dependency"
	"github.com/crossplane/cli/v2/internal/git"
	"github.com/crossplane/cli/v2/internal/project"
	"github.com/crossplane/cli/v2/internal/project/functions"
	"github.com/crossplane/cli/v2/internal/project/projectfile"
	"github.com/crossplane/cli/v2/internal/schemas/generator"
	"github.com/crossplane/cli/v2/internal/schemas/manager"
	"github.com/crossplane/cli/v2/internal/schemas/runner"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"
)

// ImageTagMap maps image tags to the images that should be pushed to them,
// as produced by a project build.
type ImageTagMap = project.ImageTagMap

// ParseProject reads, validates, and defaults the project file at
// projFilePath within projFS.
func ParseProject(projFS afero.Fs, projFilePath string) (*devv1alpha1.Project, error) {
	return projectfile.Parse(projFS, projFilePath)
}

// SortImages analyzes an image map produced by a project build and picks out
// the configuration image and the embedded function images, grouped by the
// repository they belong to.
func SortImages(imgMap ImageTagMap, repo string) (v1.Image, map[name.Repository][]v1.Image, error) {
	return project.SortImages(imgMap, repo)
}

// BuildOptions configures a project build.
type BuildOptions struct {
	// ProjectFile is the path of the project file, used to resolve
	// dependency updates. Defaults to the ProjectFile constant.
	ProjectFile string
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
	EventChannel EventChannel
	// Logger receives debug output during the build, if set.
	Logger logging.Logger
	// BasePath is the real on-disk base path of projFS, needed to build
	// embedded functions whose runtimes are built with Docker.
	BasePath string
}

// Build builds a project into its package images, generating schemas and
// resolving dependencies the same way `crossplane project build` does. It
// returns the built images by tag along with the digests of any dependencies
// that were resolved during the build.
func Build(ctx context.Context, cfg *Config, proj *devv1alpha1.Project, projFS afero.Fs, o BuildOptions) (ImageTagMap, map[string]name.Digest, error) {
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
		return nil, nil, errors.Wrap(err, "cannot create xpkg client")
	}

	depOpts := []dependency.ManagerOption{
		dependency.WithProjectFile(cmp.Or(o.ProjectFile, ProjectFile)),
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
	// once the images have been consumed. Callers that push or sideload the
	// returned images synchronously are unaffected.
	tempDir, err := os.MkdirTemp("", "crossplane-build-")
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to create temporary build directory")
	}

	b := project.NewBuilder(
		project.BuildWithMaxConcurrency(cmp.Or(o.MaxConcurrency, 8)),
		project.BuildWithFunctionIdentifier(functions.DefaultIdentifier),
		project.BuildWithSchemaManager(schemaMgr),
		project.BuildWithDependencyManager(depMgr),
		project.BuildWithTempDir(tempDir),
		project.BuildWithBaseImageCacheDir(functions.DefaultBaseImageCacheDir()),
	)

	buildOpts := []project.BuildOption{
		project.BuildWithLogger(logger),
	}
	if o.EventChannel != nil {
		buildOpts = append(buildOpts, project.BuildWithEventChannel(o.EventChannel))
	}
	if o.BasePath != "" {
		buildOpts = append(buildOpts, project.BuildWithProjectBasePath(o.BasePath))
	}

	imgMap, deps, err := b.Build(ctx, proj, projFS, buildOpts...)
	if err != nil {
		_ = os.RemoveAll(tempDir)
		return nil, nil, err
	}

	return imgMap, deps, nil
}
