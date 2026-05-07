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

// Package dependency manages schema generation for Crossplane project
// dependencies.
package dependency

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/google/go-containerregistry/pkg/name"
	conregv1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/spf13/afero"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	runtimexpkg "github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"

	"github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/internal/async"
	"github.com/crossplane/cli/v2/internal/git"
	"github.com/crossplane/cli/v2/internal/project/projectfile"
	"github.com/crossplane/cli/v2/internal/schemas/generator"
	smanager "github.com/crossplane/cli/v2/internal/schemas/manager"
	"github.com/crossplane/cli/v2/internal/schemas/runner"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"
)

// Manager manages dependencies for a Crossplane project, including fetching
// packages, extracting CRDs, and generating schemas.
type Manager struct {
	proj     *v1alpha1.Project
	projFS   afero.Fs
	projFile string
	schemas  *smanager.Manager

	gitCloner       git.Cloner
	gitAuthProvider git.AuthProvider

	client   runtimexpkg.Client
	resolver *clixpkg.Resolver

	updateMutex sync.Mutex
}

// ManagerOption configures the dependency manager.
type ManagerOption func(*managerOptions)

type managerOptions struct {
	projFile         string
	schemaFS         afero.Fs
	schemaGenerators []generator.Interface
	schemaRunner     runner.SchemaRunner
	gitAuthProvider  git.AuthProvider
	client           runtimexpkg.Client
	resolver         *clixpkg.Resolver
}

// WithProjectFile sets the path to the project file.
func WithProjectFile(path string) ManagerOption {
	return func(opts *managerOptions) {
		opts.projFile = path
	}
}

// WithSchemaFS sets the filesystem to use for schemas.
func WithSchemaFS(fs afero.Fs) ManagerOption {
	return func(opts *managerOptions) {
		opts.schemaFS = fs
	}
}

// WithSchemaRunner sets the runner to use when generating schemas.
func WithSchemaRunner(r runner.SchemaRunner) ManagerOption {
	return func(opts *managerOptions) {
		opts.schemaRunner = r
	}
}

// WithSchemaGenerators sets the schema generators to call.
func WithSchemaGenerators(gs []generator.Interface) ManagerOption {
	return func(opts *managerOptions) {
		opts.schemaGenerators = gs
	}
}

// WithGitAuthProvider sets the auth provider for git operations.
func WithGitAuthProvider(p git.AuthProvider) ManagerOption {
	return func(opts *managerOptions) {
		opts.gitAuthProvider = p
	}
}

// WithXpkgClient sets the runtime xpkg.Client used to fetch and parse
// xpkg dependencies.
func WithXpkgClient(c runtimexpkg.Client) ManagerOption {
	return func(opts *managerOptions) {
		opts.client = c
	}
}

// WithResolver sets the package reference resolver used to translate
// semver constraints into concrete tags before calling Client.Get.
func WithResolver(r *clixpkg.Resolver) ManagerOption {
	return func(opts *managerOptions) {
		opts.resolver = r
	}
}

// NewManager returns an initialized dependency manager.
func NewManager(proj *v1alpha1.Project, projFS afero.Fs, opts ...ManagerOption) *Manager {
	options := &managerOptions{
		projFile:         "crossplane-project.yaml",
		schemaFS:         afero.NewBasePathFs(projFS, proj.Spec.Paths.Schemas),
		schemaGenerators: generator.AllLanguages(),
		schemaRunner: runner.NewRealSchemaRunner(
			runner.WithImageConfig(proj.Spec.ImageConfigs),
		),
		gitAuthProvider: &git.HTTPSAuthProvider{},
	}

	for _, opt := range opts {
		opt(options)
	}

	schemas := smanager.New(
		options.schemaFS,
		options.schemaGenerators,
		options.schemaRunner,
	)

	return &Manager{
		proj:            proj,
		projFS:          projFS,
		projFile:        options.projFile,
		schemas:         schemas,
		gitCloner:       &git.DefaultCloner{},
		gitAuthProvider: options.gitAuthProvider,
		client:          options.client,
		resolver:        options.resolver,
	}
}

// ResolveRef resolves a version constraint in an OCI ref to a concrete tag.  If
// the ref has no tag, has an exact semver version, or is not a valid
// constraint, it is returned unchanged.
func (m *Manager) ResolveRef(ref string) (name.Reference, error) {
	resolved, _, err := m.resolver.Resolve(context.Background(), ref)
	return resolved, err
}

// AddPackage adds a package to the dependency manager. If refresh is set, the
// package's ref will be re-resolved regardless of whether it is cached.
func (m *Manager) AddPackage(ctx context.Context, ref string, refresh bool) (*schema.GroupVersionKind, error) {
	return m.addPackage(ctx, ref, refresh)
}

func (m *Manager) addPackage(ctx context.Context, ref string, refresh bool) (*schema.GroupVersionKind, error) {
	resolvedRef, version, err := m.resolver.Resolve(ctx, ref)
	if err != nil {
		return nil, errors.Wrapf(err, "cannot resolve %s", ref)
	}

	pullPolicy := corev1.PullIfNotPresent
	if refresh {
		pullPolicy = corev1.PullAlways
	}
	pkg, err := m.client.Get(ctx, resolvedRef.String(), runtimexpkg.WithPullPolicy(pullPolicy))
	if err != nil {
		return nil, errors.Wrapf(err, "failed to fetch %s", ref)
	}

	gvk, err := runtimeGVKForPackage(pkg)
	if err != nil {
		return nil, errors.Wrapf(err, "cannot identify package %s", ref)
	}
	crdFS, err := clixpkg.CRDFilesystem(pkg.Package)
	if err != nil {
		return nil, errors.Wrapf(err, "cannot extract CRDs from %s", ref)
	}

	// Use the resolved version so constraint and exact-version inputs
	// collapse to one schema-lock entry. Digest-pinned refs (no resolved
	// version) intentionally use a digest-form ID and remain separate.
	id := pkg.Source + "@" + pkg.Digest
	if version != "" {
		id = pkg.Source + ":" + version
	}
	src := smanager.NewXpkgSource(id, pkg.Digest, crdFS)
	if err := m.schemas.Add(ctx, src); err != nil {
		return nil, err
	}
	return gvk, nil
}

func runtimeGVKForPackage(pkg *runtimexpkg.Package) (*schema.GroupVersionKind, error) {
	gvk := pkg.GetMeta().GetObjectKind().GroupVersionKind()

	// NOTE(adamwg): We assume all packages follow the existing Crossplane
	// convention where the meta and runtime kinds match, and the meta apiGroup
	// the runtime apiGroup prefixed with "meta.".
	group := strings.TrimPrefix(gvk.Group, "meta.")
	if group == gvk.Group {
		return nil, errors.Errorf("package metadata group %s does not start with \"meta.\"", gvk.Group)
	}

	return &schema.GroupVersionKind{
		Group:   group,
		Version: gvk.Version,
		Kind:    gvk.Kind,
	}, nil
}

// AddDependency adds a dependency, generates schemas for it, and persists the
// dependency to the project file.
func (m *Manager) AddDependency(ctx context.Context, dep *v1alpha1.Dependency) error {
	gvk, err := m.addDependencyNoWrite(ctx, dep, false)
	if err != nil {
		return err
	}

	// For xpkg dependencies, fill in the apiVersion and kind. We don't care
	// about these for development purposes, but they are required to install an
	// xpkg dependency into a cluster at runtime.
	if dep.Type == v1alpha1.DependencyTypeXpkg && dep.Xpkg != nil && gvk != nil {
		dep.Xpkg.APIVersion = gvk.GroupVersion().String()
		dep.Xpkg.Kind = gvk.Kind
	}

	m.updateMutex.Lock()
	defer m.updateMutex.Unlock()

	upsertDependency(m.proj, *dep)
	return projectfile.Update(m.projFS, m.projFile, func(p *v1alpha1.Project) {
		upsertDependency(p, *dep)
	})
}

// AddAll adds all dependencies configured in the project. If ch is non-nil,
// events will be sent for each dependency as it is processed.
func (m *Manager) AddAll(ctx context.Context, ch async.EventChannel) error {
	return m.addAll(ctx, ch, false)
}

// RefreshAll re-resolves every dependency's version constraint against the
// registry. Used by `dependency update-cache`.
func (m *Manager) RefreshAll(ctx context.Context, ch async.EventChannel) error {
	return m.addAll(ctx, ch, true)
}

func (m *Manager) addAll(ctx context.Context, ch async.EventChannel, refresh bool) error {
	eg, egCtx := errgroup.WithContext(ctx)

	for i := range m.proj.Spec.Dependencies {
		dep := &m.proj.Spec.Dependencies[i]
		desc := "Updating dependency " + GetSourceDescription(*dep)
		eg.Go(func() error {
			ch.SendEvent(desc, async.EventStatusStarted)
			if _, err := m.addDependencyNoWrite(egCtx, dep, refresh); err != nil {
				ch.SendEvent(desc, async.EventStatusFailure)
				return err
			}
			ch.SendEvent(desc, async.EventStatusSuccess)
			return nil
		})
	}

	return eg.Wait()
}

func (m *Manager) addDependencyNoWrite(ctx context.Context, dep *v1alpha1.Dependency, refresh bool) (*schema.GroupVersionKind, error) {
	switch {
	case dep.Type == v1alpha1.DependencyTypeXpkg:
		if dep.Xpkg == nil {
			return nil, errors.New("xpkg dependency has no package reference")
		}

		// If the version is a digest, format the OCI ref as
		// repo@digest. Otherwise, use repo:tag, where tag may be a semver
		// constraint.
		ref := dep.Xpkg.Package
		if _, err := conregv1.NewHash(dep.Xpkg.Version); err == nil {
			ref = fmt.Sprintf("%s@%s", ref, dep.Xpkg.Version)
		} else if dep.Xpkg.Version != "" {
			ref = fmt.Sprintf("%s:%s", ref, dep.Xpkg.Version)
		}

		return m.addPackage(ctx, ref, refresh)
	case dep.Git != nil:
		return nil, m.schemas.Add(ctx, smanager.NewGitSource(*dep, m.gitCloner, m.gitAuthProvider))
	case dep.HTTP != nil:
		return nil, m.schemas.Add(ctx, smanager.NewHTTPSource(*dep))
	case dep.K8s != nil:
		return nil, m.schemas.Add(ctx, smanager.NewK8sSource(*dep))
	default:
		return nil, errors.New("dependency has no source configured")
	}
}

// Clean removes all generated schemas.
func (m *Manager) Clean() error {
	return m.projFS.RemoveAll(m.proj.Spec.Paths.Schemas)
}

// CleanPackages removes the per-user xpkg cache directory at root.
func CleanPackages(root string, fs afero.Fs) error {
	if err := fs.RemoveAll(root); err != nil {
		return errors.Wrapf(err, "cannot remove xpkg cache at %s", root)
	}
	return nil
}

// GetSourceDescription returns a human-readable description of a dependency.
func GetSourceDescription(dep v1alpha1.Dependency) string {
	switch {
	case dep.Xpkg != nil:
		desc := dep.Xpkg.Package
		if dep.Xpkg.Version != "" {
			desc += ":" + dep.Xpkg.Version
		}
		return desc
	case dep.Git != nil:
		desc := dep.Git.Repository
		if dep.Git.Ref != "" {
			desc += " (" + dep.Git.Ref + ")"
		}
		if dep.Git.Path != "" {
			desc += " at " + dep.Git.Path
		}
		return desc
	case dep.HTTP != nil:
		return dep.HTTP.URL
	case dep.K8s != nil:
		return "Kubernetes API " + dep.K8s.Version
	default:
		return "unknown source"
	}
}

func upsertDependency(proj *v1alpha1.Project, dep v1alpha1.Dependency) {
	for i, existing := range proj.Spec.Dependencies {
		if matchesDependency(existing, dep) {
			proj.Spec.Dependencies[i] = dep
			return
		}
	}

	proj.Spec.Dependencies = append(proj.Spec.Dependencies, dep)
}

func matchesDependency(a, b v1alpha1.Dependency) bool {
	if a.Type != b.Type {
		return false
	}
	switch {
	case a.Xpkg != nil && b.Xpkg != nil:
		return a.Xpkg.Package == b.Xpkg.Package
	case a.Git != nil && b.Git != nil:
		return a.Git.Repository == b.Git.Repository
	case a.HTTP != nil && b.HTTP != nil:
		return a.HTTP.URL == b.HTTP.URL
	case a.K8s != nil && b.K8s != nil:
		return true // Only one k8s dep makes sense.
	}
	return false
}
