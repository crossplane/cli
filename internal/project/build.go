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
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
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

// Builder is able to build a project into a set of packages.
type Builder interface {
	// Build builds a project into a set of packages. It returns a map
	// containing images that were built from the project. The returned map will
	// always include one image with the ConfigurationTag, which is the
	// configuration package built from the APIs found in the project.
	Build(ctx context.Context, project *devv1alpha1.Project, projectFS afero.Fs, opts ...BuildOption) (ImageTagMap, error)
}

// BuilderOption configures a builder.
type BuilderOption func(b *realBuilder)

// BuildWithFunctionIdentifier sets the function identifier that will be used to
// find function builders for any functions in a project.
func BuildWithFunctionIdentifier(i functions.Identifier) BuilderOption {
	return func(b *realBuilder) {
		b.functionIdentifier = i
	}
}

// BuildWithMaxConcurrency sets the maximum concurrency for building embedded
// functions.
func BuildWithMaxConcurrency(n uint) BuilderOption {
	return func(b *realBuilder) {
		b.maxConcurrency = n
	}
}

// BuildWithSchemaManager sets the schema manager that will be used to generate
// language-specific schemas from XRDs before building functions.
func BuildWithSchemaManager(m *manager.Manager) BuilderOption {
	return func(b *realBuilder) {
		b.schemaManager = m
	}
}

// BuildWithDependencyManager sets the dependency manager that will be used to
// ensure schemas are present for the project's declared dependencies before
// building functions.
func BuildWithDependencyManager(m *dependency.Manager) BuilderOption {
	return func(b *realBuilder) {
		b.dependencyManager = m
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

type realBuilder struct {
	functionIdentifier functions.Identifier
	maxConcurrency     uint
	schemaManager      *manager.Manager
	dependencyManager  *dependency.Manager
}

// Build implements the Builder interface.
func (b *realBuilder) Build(ctx context.Context, project *devv1alpha1.Project, projectFS afero.Fs, opts ...BuildOption) (ImageTagMap, error) { //nolint:gocyclo // This is the main build orchestration.
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

	functionsSource := afero.NewBasePathFs(projectFS, project.Spec.Paths.Functions)
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

	// Find and build embedded functions.
	o.log.Debug("Building functions")
	imgMap, deps, err := b.buildFunctions(ctx, projectFS, functionsSource, project, o.projectBasePath, o.eventCh)
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

// buildFunctions builds the embedded functions found in directories at the top
// level of the provided filesystem.
func (b *realBuilder) buildFunctions(ctx context.Context, projectFS, fromFS afero.Fs, project *devv1alpha1.Project, basePath string, eventCh async.EventChannel) (ImageTagMap, []xpmetav1.Dependency, error) {
	var (
		imgMap = make(map[name.Tag]v1.Image)
		imgMu  sync.Mutex
	)

	infos, err := afero.ReadDir(fromFS, "/")
	switch {
	case os.IsNotExist(err):
		return imgMap, nil, nil
	case err != nil:
		return nil, nil, errors.Wrap(err, "failed to list functions directory")
	}

	fnDirs := make([]string, 0, len(infos))
	for _, info := range infos {
		if info.IsDir() {
			fnDirs = append(fnDirs, info.Name())
		}
	}

	deps := make([]xpmetav1.Dependency, len(fnDirs))
	eg, ctx := errgroup.WithContext(ctx)

	sem := make(chan struct{}, b.maxConcurrency)
	for i, fnName := range fnDirs {
		eg.Go(func() error {
			sem <- struct{}{}
			defer func() {
				<-sem
			}()

			eventText := fmt.Sprintf("Building function %s", fnName)
			eventCh.SendEvent(eventText, async.EventStatusStarted)

			fnRepo := fmt.Sprintf("%s_%s", project.Spec.Repository, fnName)
			fnFS := afero.NewBasePathFs(fromFS, fnName)
			fnBasePath := ""
			if basePath != "" {
				fnBasePath = filepath.Join(basePath, project.Spec.Paths.Functions, fnName)
			}
			imgs, err := b.buildFunction(ctx, projectFS, fnFS, project, fnName, fnBasePath)
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

	err = eg.Wait()
	if err != nil {
		return nil, nil, err
	}

	return imgMap, deps, nil
}

// buildFunction builds images for a single function whose source resides in the
// given filesystem.
func (b *realBuilder) buildFunction(ctx context.Context, projectFS, fromFS afero.Fs, project *devv1alpha1.Project, fnName string, basePath string) ([]v1.Image, error) {
	fn := &xpmetav1.Function{
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
	y, err := yaml.Marshal(fn)
	if err != nil {
		return nil, errors.Wrap(err, "failed to marshal function metadata")
	}
	err = afero.WriteFile(metaFS, "/crossplane.yaml", y, 0o644)
	if err != nil {
		return nil, errors.Wrap(err, "failed to write function metadata")
	}

	examplesParser := parser.NewEchoBackend("")
	examplesExist, err := afero.IsDir(fromFS, "/examples")
	switch {
	case err == nil, os.IsNotExist(err):
	default:
		return nil, errors.Wrap(err, "failed to check for examples")
	}
	if examplesExist {
		examplesParser = parser.NewFsBackend(fromFS,
			parser.FsDir("/examples"),
			parser.FsFilters(parser.SkipNotYAML()),
		)
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

	fnBuilder, err := b.functionIdentifier.Identify(fromFS, project.Spec.ImageConfigs)
	if err != nil {
		return nil, errors.Wrap(err, "failed to find a builder")
	}

	if bfs, ok := fromFS.(*afero.BasePathFs); ok && basePath == "" {
		basePath = afero.FullBaseFsPath(bfs, ".")
	}

	runtimeImages, err := fnBuilder.Build(ctx, functions.BuildContext{
		ProjectFS:     projectFS,
		FunctionPath:  filepath.Join(project.Spec.Paths.Functions, fnName),
		SchemasPath:   project.Spec.Paths.Schemas,
		Architectures: project.Spec.Architectures,
		OSBasePath:    basePath,
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to build runtime images")
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

// NewBuilder returns a new project builder.
func NewBuilder(opts ...BuilderOption) Builder {
	b := &realBuilder{
		functionIdentifier: functions.DefaultIdentifier,
		maxConcurrency:     8,
	}

	for _, opt := range opts {
		opt(b)
	}

	return b
}
