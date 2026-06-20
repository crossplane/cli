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

// Package project contains logic for building Crossplane projects.
package project

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/spf13/afero"
	"golang.org/x/sync/errgroup"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xpkg/parser"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xpkg/parser/examples"
	pyaml "github.com/crossplane/crossplane-runtime/v2/pkg/xpkg/parser/yaml"

	xpv1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1"
	extv1alpha1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1alpha1"
	xpv2 "github.com/crossplane/crossplane/apis/v2/apiextensions/v2"
	xpv1alpha1 "github.com/crossplane/crossplane/apis/v2/ops/v1alpha1"
	xpmetav1 "github.com/crossplane/crossplane/apis/v2/pkg/meta/v1"
	xpkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"

	devv1alpha1 "github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/internal/async"
	"github.com/crossplane/cli/v2/internal/dependency"
	"github.com/crossplane/cli/v2/internal/project/functions"
	"github.com/crossplane/cli/v2/internal/schemas/manager"
)

const (
	// ConfigurationTag is the tag used for the configuration image in the built
	// package.
	ConfigurationTag = "configuration"
)

// ImageTagMap is a map of container image tags to images.
type ImageTagMap map[name.Tag]v1.Image

// BuilderOption configures a Builder.
type BuilderOption func(b *Builder)

// BuildWithFunctionIdentifier sets the function identifier that will be used to
// find function builders for any functions in a project.
func BuildWithFunctionIdentifier(i functions.Identifier) BuilderOption {
	return func(b *Builder) {
		b.functionIdentifier = i
	}
}

// BuildWithMaxConcurrency sets the maximum concurrency for building embedded
// functions.
func BuildWithMaxConcurrency(n uint) BuilderOption {
	return func(b *Builder) {
		b.maxConcurrency = n
	}
}

// BuildWithSchemaManager sets the schema manager that will be used to generate
// language-specific schemas from XRDs before building functions.
func BuildWithSchemaManager(m *manager.Manager) BuilderOption {
	return func(b *Builder) {
		b.schemaManager = m
	}
}

// BuildWithDependencyManager sets the dependency manager that will be used to
// ensure schemas are present for the project's declared dependencies before
// building functions.
func BuildWithDependencyManager(m *dependency.Manager) BuilderOption {
	return func(b *Builder) {
		b.dependencyManager = m
	}
}

// BuildWithTempDir sets a directory the builder can use to hold temporary
// files. The images returned from Build() may depend on this directory, so
// callers should not remove it until they have finished consuming the images.
//
// If unset, the OS default temporary directory is to hold temporary files,
// which are not cleaned up by the builder.
func BuildWithTempDir(dir string) BuilderOption {
	return func(b *Builder) {
		b.tempDir = dir
	}
}

// BuildOption configures a build.
type BuildOption func(o *buildOptions)

type buildOptions struct {
	log             logging.Logger
	projectBasePath string
	eventCh         async.EventChannel
}

// BuildWithLogger provides a logger for progress updates during the build.
func BuildWithLogger(l logging.Logger) BuildOption {
	return func(o *buildOptions) {
		o.log = l
	}
}

// BuildWithEventChannel provides a channel for sending progress events during
// the build.
func BuildWithEventChannel(ch async.EventChannel) BuildOption {
	return func(o *buildOptions) {
		o.eventCh = ch
	}
}

// BuildWithProjectBasePath sets the real on-disk base path of the project. This
// path will be used for following symlinks. If not set it will be inferred from
// the project FS, which works only when the project FS is an afero.BasePathFs.
func BuildWithProjectBasePath(path string) BuildOption {
	return func(o *buildOptions) {
		o.projectBasePath = path
	}
}

// A Builder builds a project into a set of packages.
//
// The images Build returns may read lazily from temporary files on disk - for
// example, when a function's runtime is supplied as a gzipped tarball that is
// decompressed once during the build. These files are written to the temporary
// directory set by BuildWithTempDir, which the caller owns and is responsible
// for removing once it has finished consuming the returned images.
type Builder struct {
	functionIdentifier functions.Identifier
	maxConcurrency     uint
	schemaManager      *manager.Manager
	dependencyManager  *dependency.Manager
	tempDir            string
}

// Build builds a project into a set of packages. It returns a map containing
// images that were built from the project. The returned map will always include
// one image with the ConfigurationTag, which is the configuration package built
// from the APIs found in the project.
func (b *Builder) Build(ctx context.Context, project *devv1alpha1.Project, projectFS afero.Fs, opts ...BuildOption) (ImageTagMap, error) { //nolint:gocyclo // This is the main build orchestration.
	o := &buildOptions{
		log: logging.NewNopLogger(),
	}
	for _, opt := range opts {
		opt(o)
	}

	// Scaffold a configuration based on the metadata in the project.
	cfg := &xpmetav1.Configuration{
		TypeMeta: metav1.TypeMeta{
			APIVersion: xpmetav1.SchemeGroupVersion.String(),
			Kind:       xpmetav1.ConfigurationKind,
		},
		ObjectMeta: cfgMetaFromProject(project),
		Spec: xpmetav1.ConfigurationSpec{
			MetaSpec: xpmetav1.MetaSpec{
				Crossplane: project.Spec.Crossplane,
				DependsOn:  runtimeDependencies(project),
			},
		},
	}

	// Default to v2 constraint.
	if cfg.Spec.Crossplane == nil || cfg.Spec.Crossplane.Version == "" {
		cfg.Spec.Crossplane = &xpmetav1.CrossplaneConstraints{
			Version: ">=v2.0.0-rc.0",
		}
	}

	// Determine the set of functions to build. If the project explicitly
	// declares a Functions list we use it verbatim. Otherwise we auto-discover
	// by listing subdirectories of the functions path.
	fns, err := resolveFunctions(project, projectFS)
	if err != nil {
		return nil, errors.Wrap(err, "failed to resolve functions")
	}

	apisSource := projectFS
	apiExcludes := []string{
		project.Spec.Paths.Examples,
		project.Spec.Paths.Functions,
		project.Spec.Paths.Operations,
	}
	if project.Spec.Paths.APIs != "/" {
		apisSource = afero.NewBasePathFs(projectFS, project.Spec.Paths.APIs)
		apiExcludes = []string{}
	}

	// Not all projects have operations; ignore them if not present.
	operationsSource := afero.NewMemMapFs()
	opsExist, err := afero.DirExists(projectFS, project.Spec.Paths.Operations)
	if err != nil {
		return nil, err
	}
	if opsExist {
		operationsSource = afero.NewBasePathFs(projectFS, project.Spec.Paths.Operations)
	}

	// Collect resources (XRDs, MRAPs, compositions, and operations).
	packageFS := afero.NewMemMapFs()
	o.log.Debug("Collecting resources")
	o.eventCh.SendEvent("Collecting resources", async.EventStatusStarted)

	apiGVKs := []string{
		xpv1.CompositeResourceDefinitionGroupVersionKind.String(),
		xpv2.CompositeResourceDefinitionGroupVersionKind.String(),
		xpv1.CompositionGroupVersionKind.String(),
		extv1alpha1.ManagedResourceActivationPolicyGroupVersionKind.String(),
	}
	if err := collectResources(packageFS, apisSource, apiGVKs, apiExcludes); err != nil {
		o.eventCh.SendEvent("Collecting resources", async.EventStatusFailure)
		return nil, errors.Wrap(err, "failed to collect API resources")
	}

	opsGVKs := []string{
		xpv1alpha1.OperationGroupVersionKind.String(),
		xpv1alpha1.WatchOperationGroupVersionKind.String(),
		xpv1alpha1.CronOperationGroupVersionKind.String(),
	}
	if err := collectResources(packageFS, operationsSource, opsGVKs, nil); err != nil {
		o.eventCh.SendEvent("Collecting resources", async.EventStatusFailure)
		return nil, errors.Wrap(err, "failed to collect operation resources")
	}
	o.eventCh.SendEvent("Collecting resources", async.EventStatusSuccess)

	// Generate schemas for declared dependencies. The dependency manager
	// short-circuits sources whose recorded version still matches, so this is
	// cheap on the steady-state path.
	if b.dependencyManager != nil {
		if err := b.dependencyManager.AddAll(ctx, o.eventCh); err != nil {
			return nil, errors.Wrap(err, "failed to generate dependency schemas")
		}
	}

	// Generate language-specific schemas from XRDs.
	if b.schemaManager != nil {
		o.eventCh.SendEvent("Generating schemas", async.EventStatusStarted)
		if _, err := b.schemaManager.Generate(ctx, manager.NewFSSource(project.Spec.Paths.APIs, apisSource)); err != nil {
			o.eventCh.SendEvent("Generating schemas", async.EventStatusFailure)
			return nil, errors.Wrap(err, "failed to generate schemas")
		}
		o.eventCh.SendEvent("Generating schemas", async.EventStatusSuccess)
	}

	// Build the resolved functions.
	o.log.Debug("Building functions")
	imgMap, deps, err := b.buildFunctions(ctx, projectFS, project, fns, o.projectBasePath, o.eventCh)
	if err != nil {
		return nil, err
	}
	cfg.Spec.DependsOn = append(cfg.Spec.DependsOn, deps...)

	// Build the configuration package.
	o.log.Debug("Building configuration package")
	o.eventCh.SendEvent("Building configuration package", async.EventStatusStarted)

	y, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, errors.Wrap(err, "failed to marshal package metadata")
	}
	err = afero.WriteFile(packageFS, "/crossplane.yaml", y, 0o644)
	if err != nil {
		return nil, errors.Wrap(err, "failed to write package metadata")
	}

	pp, err := pyaml.New()
	if err != nil {
		return nil, errors.Wrap(err, "failed to create parser")
	}
	builder := xpkg.New(
		parser.NewFsBackend(packageFS, parser.FsDir("/")),
		parser.NewFsBackend(afero.NewBasePathFs(projectFS, project.Spec.Paths.Examples),
			parser.FsDir("/"),
			parser.FsFilters(parser.SkipNotYAML()),
		),
		pp,
		examples.New(),
	)

	img, _, err := builder.Build(ctx)
	if err != nil {
		o.eventCh.SendEvent("Building configuration package", async.EventStatusFailure)
		return nil, errors.Wrap(err, "failed to build package")
	}

	imgTag, err := name.NewTag(fmt.Sprintf("%s:%s", project.Spec.Repository, ConfigurationTag))
	if err != nil {
		o.eventCh.SendEvent("Building configuration package", async.EventStatusFailure)
		return nil, errors.Wrap(err, "failed to construct image tag")
	}
	imgMap[imgTag] = img

	o.eventCh.SendEvent("Building configuration package", async.EventStatusSuccess)

	return imgMap, nil
}

// resolveFunctions returns the list of functions to build for the project. If
// the project explicitly declares functions, that list is returned verbatim.
// Otherwise it auto-discovers Directory-source functions by listing
// subdirectories of the project's functions path.
func resolveFunctions(project *devv1alpha1.Project, projectFS afero.Fs) ([]devv1alpha1.Function, error) {
	if len(project.Spec.Functions) > 0 {
		return project.Spec.Functions, nil
	}

	infos, err := afero.ReadDir(projectFS, project.Spec.Paths.Functions)
	switch {
	case os.IsNotExist(err):
		return nil, nil
	case err != nil:
		return nil, errors.Wrap(err, "failed to list functions directory")
	}

	fns := make([]devv1alpha1.Function, 0, len(infos))
	for _, info := range infos {
		if !info.IsDir() {
			continue
		}
		fns = append(fns, devv1alpha1.Function{
			Source:    devv1alpha1.FunctionSourceDirectory,
			Directory: &devv1alpha1.FunctionDirectory{Name: info.Name()},
		})
	}
	return fns, nil
}

// buildFunctions builds the given list of embedded functions.
func (b *Builder) buildFunctions(ctx context.Context, projectFS afero.Fs, project *devv1alpha1.Project, fns []devv1alpha1.Function, basePath string, eventCh async.EventChannel) (ImageTagMap, []xpmetav1.Dependency, error) {
	var (
		imgMap = make(map[name.Tag]v1.Image)
		imgMu  sync.Mutex
	)

	deps := make([]xpmetav1.Dependency, len(fns))
	eg, ctx := errgroup.WithContext(ctx)

	sem := make(chan struct{}, b.maxConcurrency)
	for i, fn := range fns {
		eg.Go(func() error {
			sem <- struct{}{}
			defer func() {
				<-sem
			}()

			fnName := fn.Name()
			eventText := fmt.Sprintf("Building function %s", fnName)
			eventCh.SendEvent(eventText, async.EventStatusStarted)

			fnRepo := fmt.Sprintf("%s_%s", project.Spec.Repository, fnName)
			imgs, err := b.buildFunction(ctx, projectFS, project, fn, basePath)
			if err != nil {
				eventCh.SendEvent(eventText, async.EventStatusFailure)
				return errors.Wrapf(err, "failed to build function %q", fnName)
			}

			idx, imgs, err := BuildIndex(imgs...)
			if err != nil {
				return errors.Wrapf(err, "failed to construct index for function image %q", fnName)
			}
			dgst, err := idx.Digest()
			if err != nil {
				return errors.Wrapf(err, "failed to get index digest for function image %q", fnName)
			}
			deps[i] = xpmetav1.Dependency{
				APIVersion: new(xpkgv1.FunctionGroupVersionKind.GroupVersion().String()),
				Kind:       new(xpkgv1.FunctionKind),
				Package:    &fnRepo,
				Version:    dgst.String(),
			}

			for _, img := range imgs {
				cfgFile, err := img.ConfigFile()
				if err != nil {
					return errors.Wrapf(err, "failed to get config for function image %q", fnName)
				}

				tag := fmt.Sprintf("%s:%s", fnRepo, cfgFile.Architecture)
				imgTag, err := name.NewTag(tag)
				if err != nil {
					return errors.Wrapf(err, "failed to construct tag for function image %q", fnName)
				}
				imgMu.Lock()
				imgMap[imgTag] = img
				imgMu.Unlock()
			}

			eventCh.SendEvent(eventText, async.EventStatusSuccess)

			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		return nil, nil, err
	}

	return imgMap, deps, nil
}

// buildFunction builds the package images for a single function. It resolves
// the function's runtime images (either by building from source or by loading
// a pre-built tarball) and then wraps each one with the package metadata.
func (b *Builder) buildFunction(ctx context.Context, projectFS afero.Fs, project *devv1alpha1.Project, fn devv1alpha1.Function, basePath string) ([]v1.Image, error) {
	fnName := fn.Name()
	meta := &xpmetav1.Function{
		TypeMeta: metav1.TypeMeta{
			APIVersion: xpmetav1.SchemeGroupVersion.String(),
			Kind:       xpmetav1.FunctionKind,
		},
		ObjectMeta: fnMetaFromProject(project, fnName),
		Spec: xpmetav1.FunctionSpec{
			MetaSpec: xpmetav1.MetaSpec{
				Capabilities: []string{
					xpmetav1.FunctionCapabilityComposition,
					xpmetav1.FunctionCapabilityOperation,
				},
			},
		},
	}
	metaFS := afero.NewMemMapFs()
	y, err := yaml.Marshal(meta)
	if err != nil {
		return nil, errors.Wrap(err, "failed to marshal function metadata")
	}
	err = afero.WriteFile(metaFS, "/crossplane.yaml", y, 0o644)
	if err != nil {
		return nil, errors.Wrap(err, "failed to write function metadata")
	}

	// Source the examples from the function's own directory if it's a
	// Directory-source function. Tarball-source functions don't have a source
	// directory under functions/, so they have no examples to ship.
	examplesParser := parser.NewEchoBackend("")
	if fn.Source == devv1alpha1.FunctionSourceDirectory {
		examplesDir := filepath.Join(project.Spec.Paths.Functions, fn.Directory.Name, "examples")
		examplesExist, err := afero.IsDir(projectFS, examplesDir)
		switch {
		case err == nil, os.IsNotExist(err):
		default:
			return nil, errors.Wrap(err, "failed to check for examples")
		}
		if examplesExist {
			examplesParser = parser.NewFsBackend(projectFS,
				parser.FsDir(examplesDir),
				parser.FsFilters(parser.SkipNotYAML()),
			)
		}
	}

	pp, err := pyaml.New()
	if err != nil {
		return nil, errors.Wrap(err, "failed to create parser")
	}
	builder := xpkg.New(
		parser.NewFsBackend(metaFS, parser.FsDir("/")),
		examplesParser,
		pp,
		examples.New(),
	)

	runtimeImages, err := b.runtimeImages(ctx, projectFS, project, fn, basePath)
	if err != nil {
		return nil, err
	}

	pkgImages := make([]v1.Image, 0, len(runtimeImages))
	for _, img := range runtimeImages {
		pkgImage, _, err := builder.Build(ctx, xpkg.WithBase(img))
		if err != nil {
			return nil, errors.Wrap(err, "failed to build function package")
		}
		pkgImages = append(pkgImages, pkgImage)
	}

	return pkgImages, nil
}

// runtimeImages returns the per-architecture runtime images for a function. For
// Directory-source functions this dispatches to the appropriate builder. For
// Tarball-source functions it loads the supplied OCI tarball.
func (b *Builder) runtimeImages(ctx context.Context, projectFS afero.Fs, project *devv1alpha1.Project, fn devv1alpha1.Function, basePath string) ([]v1.Image, error) {
	switch fn.Source {
	case devv1alpha1.FunctionSourceDirectory:
		return b.buildDirectoryRuntime(ctx, projectFS, project, fn.Directory, basePath)
	case devv1alpha1.FunctionSourceTarball:
		return loadTarballRuntime(projectFS, fn.Tarball, project.Spec.Architectures, b.tempDir)
	default:
		// Should be caught at validation time, but be defensive.
		return nil, errors.Errorf("unsupported function source %q", fn.Source)
	}
}

// buildDirectoryRuntime invokes the appropriate language builder to produce
// runtime images from a function's source directory.
func (b *Builder) buildDirectoryRuntime(ctx context.Context, projectFS afero.Fs, project *devv1alpha1.Project, dir *devv1alpha1.FunctionDirectory, basePath string) ([]v1.Image, error) {
	fnFS := afero.NewBasePathFs(projectFS, filepath.Join(project.Spec.Paths.Functions, dir.Name))

	fnBasePath := ""
	if basePath != "" {
		fnBasePath = filepath.Join(basePath, project.Spec.Paths.Functions, dir.Name)
	}
	if bfs, ok := fnFS.(*afero.BasePathFs); ok && fnBasePath == "" {
		fnBasePath = afero.FullBaseFsPath(bfs, ".")
	}

	fnBuilder, err := b.functionIdentifier.Identify(fnFS, project.Spec.ImageConfigs)
	if err != nil {
		return nil, errors.Wrap(err, "failed to find a builder")
	}

	imgs, err := fnBuilder.Build(ctx, functions.BuildContext{
		ProjectFS:     projectFS,
		FunctionPath:  filepath.Join(project.Spec.Paths.Functions, dir.Name),
		SchemasPath:   project.Spec.Paths.Schemas,
		Architectures: project.Spec.Architectures,
		OSBasePath:    fnBasePath,
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to build runtime images")
	}
	return imgs, nil
}

// loadTarballRuntime reads one pre-built single-platform OCI image tarball per
// target architecture. For each architecture it looks for, in order:
//
//   - <pathPrefix>-<arch>.tar
//   - <pathPrefix>-<arch>.tar.gz
//
// The tarball format is the Docker-style image tarball produced by
// `docker save`, Nix's dockerTools.buildImage, Bazel's oci_tarball,
// `ko build --tarball`, etc. The gzipped variant is what most Nix image
// builders emit by default.
func loadTarballRuntime(projectFS afero.Fs, tb *devv1alpha1.FunctionTarball, architectures []string, tempDir string) ([]v1.Image, error) {
	images := make([]v1.Image, 0, len(architectures))
	for _, arch := range architectures {
		img, rel, err := loadRuntimeImage(projectFS, tb.PathPrefix, arch, tempDir)
		if err != nil {
			return nil, err
		}

		// The image's own config records the platform it was built for. If
		// it doesn't match the architecture we expected based on the file
		// name, the user has almost certainly made a packaging mistake;
		// fail loudly rather than producing a multi-arch index that lies.
		cfg, err := img.ConfigFile()
		if err != nil {
			return nil, errors.Wrapf(err, "failed to read config for runtime image %q", rel)
		}
		if cfg.Architecture != arch {
			return nil, errors.Errorf("runtime image %q reports architecture %q but was expected to be %q", rel, cfg.Architecture, arch)
		}

		images = append(images, img)
	}
	return images, nil
}

// loadRuntimeImage loads the runtime image for a single architecture. It tries
// each candidate tarball in turn, preferring the plain .tar over the gzipped
// .tar.gz, and loads the first one that exists. It returns the loaded image and
// the relative path it was loaded from (for error messages).
//
// The tarballs are read through the project filesystem rather than from a real
// on-disk path, so loading works the same whether the project FS is an
// afero.BasePathFs or an in-memory FS in tests. A gzipped tarball is decompressed
// once to a temporary file in tempDir; the image reads from that file lazily, so
// the caller must keep the directory until it has finished consuming the image.
func loadRuntimeImage(projectFS afero.Fs, prefix, arch, tempDir string) (v1.Image, string, error) {
	candidates := []struct {
		path string
		open func(fsys afero.Fs, path, tempDir string) (tarball.Opener, error)
	}{
		{path: fmt.Sprintf("%s-%s.tar", prefix, arch), open: fsOpener},
		{path: fmt.Sprintf("%s-%s.tar.gz", prefix, arch), open: gzipOpener},
	}

	tried := make([]string, 0, len(candidates))
	for _, c := range candidates {
		tried = append(tried, c.path)

		exists, err := afero.Exists(projectFS, c.path)
		if err != nil {
			return nil, c.path, errors.Wrapf(err, "failed to stat runtime image %q", c.path)
		}
		if !exists {
			continue
		}

		opener, err := c.open(projectFS, c.path, tempDir)
		if err != nil {
			return nil, c.path, errors.Wrapf(err, "failed to open runtime image for architecture %q from %q", arch, c.path)
		}

		img, err := tarball.Image(opener, nil)
		if err != nil {
			return nil, c.path, errors.Wrapf(err, "failed to load runtime image for architecture %q from %q", arch, c.path)
		}
		return img, c.path, nil
	}

	return nil, tried[0], errors.Errorf("no runtime image found for architecture %q: looked for %v", arch, tried)
}

// fsOpener returns a tarball.Opener that reads the plain tar at path directly
// from fsys. Each call returns a fresh reader at the start of the file, as
// tarball.Image requires. It needs no temporary directory; tempDir lets it share
// a signature with gzipOpener so loadRuntimeImage can treat both uniformly.
func fsOpener(fsys afero.Fs, path, _ string) (tarball.Opener, error) {
	return func() (io.ReadCloser, error) { return fsys.Open(path) }, nil
}

// gzipOpener decompresses the gzipped tar at path on fsys once into tempDir and
// returns a tarball.Opener that reads the decompressed file.
//
// tarball.Image calls the opener repeatedly - once for the manifest, once for
// the config, and once per layer - so decompressing once up front avoids
// re-running gzip over the whole image for every call.
func gzipOpener(fsys afero.Fs, path, tempDir string) (tarball.Opener, error) {
	f, err := fsys.Open(path)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to open %q", path)
	}
	defer f.Close() //nolint:errcheck // Read-only file we copy from below.

	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to read gzip header of %q", path)
	}
	defer gz.Close() //nolint:errcheck // Read-only reader.

	tmpPath, err := writeTempTarball(tempDir, gz)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to decompress %q", path)
	}
	return func() (io.ReadCloser, error) {
		return os.Open(tmpPath) //nolint:gosec // tmpPath is a file we created in our own temp directory.
	}, nil
}

func collectResources(toFS afero.Fs, fromFS afero.Fs, gvks []string, exclude []string) error {
	return afero.Walk(fromFS, "/", func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}

		for _, excl := range exclude {
			if strings.HasPrefix(path, excl) {
				return filepath.SkipDir
			}
		}

		if info.IsDir() {
			return nil
		}
		ext := filepath.Ext(path)
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}

		var u metav1.TypeMeta
		bs, err := afero.ReadFile(fromFS, path)
		if err != nil {
			return errors.Wrapf(err, "failed to read file %q", path)
		}
		err = yaml.Unmarshal(bs, &u)
		if err != nil {
			return errors.Wrapf(err, "failed to parse file %q", path)
		}

		if !slices.Contains(gvks, u.GroupVersionKind().String()) {
			return nil
		}

		err = toFS.MkdirAll(filepath.Dir(path), 0o755)
		if err != nil {
			return errors.Wrapf(err, "failed to create directory for %q", path)
		}

		err = afero.WriteFile(toFS, path, bs, 0o644)
		if err != nil {
			return errors.Wrapf(err, "failed to write file %q to package", path)
		}

		return nil
	})
}

// runtimeDependencies extracts the runtime (non-APIOnly) xpkg dependencies
// from a project and converts them to package metadata dependencies for use in
// the built Configuration package.
func runtimeDependencies(proj *devv1alpha1.Project) []xpmetav1.Dependency {
	deps := make([]xpmetav1.Dependency, 0, len(proj.Spec.Dependencies))
	for _, d := range proj.Spec.Dependencies {
		if d.Type != devv1alpha1.DependencyTypeXpkg {
			continue
		}
		if d.Xpkg == nil || d.Xpkg.APIOnly {
			continue
		}

		deps = append(deps, xpmetav1.Dependency{
			APIVersion: &d.Xpkg.APIVersion,
			Kind:       &d.Xpkg.Kind,
			Package:    &d.Xpkg.Package,
			Version:    d.Xpkg.Version,
		})
	}
	return deps
}

func cfgMetaFromProject(proj *devv1alpha1.Project) metav1.ObjectMeta {
	meta := proj.ObjectMeta.DeepCopy()

	if meta.Annotations == nil {
		meta.Annotations = make(map[string]string)
	}

	meta.Annotations["meta.crossplane.io/maintainer"] = proj.Spec.Maintainer
	meta.Annotations["meta.crossplane.io/source"] = proj.Spec.Source
	meta.Annotations["meta.crossplane.io/license"] = proj.Spec.License
	meta.Annotations["meta.crossplane.io/description"] = proj.Spec.Description
	meta.Annotations["meta.crossplane.io/readme"] = proj.Spec.Readme

	return *meta
}

func fnMetaFromProject(proj *devv1alpha1.Project, fnName string) metav1.ObjectMeta {
	meta := proj.ObjectMeta.DeepCopy()

	meta.Name = fmt.Sprintf("%s-%s", meta.Name, fnName)

	if meta.Annotations == nil {
		meta.Annotations = make(map[string]string)
	}

	meta.Annotations["meta.crossplane.io/maintainer"] = proj.Spec.Maintainer
	meta.Annotations["meta.crossplane.io/source"] = proj.Spec.Source
	meta.Annotations["meta.crossplane.io/license"] = proj.Spec.License
	meta.Annotations["meta.crossplane.io/description"] = fmt.Sprintf("Function %s from project %s", fnName, proj.Name)

	return *meta
}

// NewBuilder returns a new project builder. Callers that build functions whose
// runtime is supplied as a gzipped tarball should set BuildWithTempDir and
// remove the directory once they have finished consuming the images returned by
// Build.
func NewBuilder(opts ...BuilderOption) *Builder {
	b := &Builder{
		functionIdentifier: functions.DefaultIdentifier,
		maxConcurrency:     8,
	}

	for _, opt := range opts {
		opt(b)
	}

	return b
}

// writeTempTarball copies r to a new temporary file in dir and returns its path.
// If dir is empty the OS default temporary directory is used. The caller owns
// the file and is responsible for removing it (or the directory containing it).
func writeTempTarball(dir string, r io.Reader) (string, error) {
	f, err := os.CreateTemp(dir, "runtime-*.tar")
	if err != nil {
		return "", errors.Wrap(err, "failed to create temporary file")
	}
	// Close the file on the error paths below. The happy path closes it
	// explicitly to capture the flush error, leaving this a harmless second
	// close.
	defer f.Close() //nolint:errcheck // See comment above.

	// The tarball is a trusted, locally-built image, so we don't bound the
	// decompressed size.
	if _, err := io.Copy(f, r); err != nil {
		_ = os.Remove(f.Name())
		return "", errors.Wrap(err, "failed to copy tarball")
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", errors.Wrap(err, "failed to flush temporary file")
	}
	return f.Name(), nil
}
