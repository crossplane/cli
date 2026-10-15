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
	"io"
	"maps"
	"os"
	"slices"
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

	pkgmetav1 "github.com/crossplane/crossplane/apis/v2/pkg/meta/v1"

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

	// fetched caches packages by resolved ref for this Manager, so the
	// top-level walks -- GVK extraction, then source collection -- do not
	// each download the same package. The xpkg client's own cache is
	// per-invocation only when configured, and is keyed for extraction, not
	// for the GVK and dependency walk both callers need.
	fetched   map[string]*runtimexpkg.Package
	fetchedMu sync.Mutex

	// visited tracks package refs already processed during this invocation,
	// deduplicating shared transitive dependencies and breaking cycles.
	visited   map[string]struct{}
	visitedMu sync.Mutex

	// collected records, per package repository, every version source
	// collection resolved it to, so CollectSources can warn when the
	// project's dependencies pull in one package at two versions. Guarded by
	// visitedMu and reset with visited.
	collected map[string][]string

	// warnings receives non-fatal problems found while collecting sources.
	warnings io.Writer
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
	warnings         io.Writer
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

// WithWarningWriter sets where non-fatal problems found while collecting
// schema sources are written. The default is os.Stderr.
func WithWarningWriter(w io.Writer) ManagerOption {
	return func(opts *managerOptions) {
		opts.warnings = w
	}
}

// NewManager returns an initialized dependency manager. The default schema
// generators are configured with every generator option off, so a caller that
// generates schemas must pass WithSchemaGenerators to honor the feature flags
// in the user's config.
func NewManager(proj *v1alpha1.Project, projFS afero.Fs, opts ...ManagerOption) *Manager {
	options := &managerOptions{
		projFile:         clixpkg.ProjectFile,
		schemaFS:         afero.NewBasePathFs(projFS, proj.Spec.Paths.Schemas),
		schemaGenerators: generator.AllLanguages(),
		schemaRunner: runner.NewRealSchemaRunner(
			runner.WithImageConfig(proj.Spec.ImageConfigs),
		),
		gitAuthProvider: &git.HTTPSAuthProvider{},
		warnings:        os.Stderr,
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
		visited:         make(map[string]struct{}),
		collected:       make(map[string][]string),
		fetched:         make(map[string]*runtimexpkg.Package),
		warnings:        options.warnings,
	}
}

// ResolveRef resolves a version constraint in an OCI ref to a concrete tag.  If
// the ref has no tag, has an exact semver version, or is not a valid
// constraint, it is returned unchanged.
func (m *Manager) ResolveRef(ref string) (name.Reference, error) {
	resolved, _, err := m.resolver.Resolve(context.Background(), ref)
	return resolved, err
}

// addPackage fetches a package by ref -- walking its transitive dependencies
// -- and returns its runtime GVK. It writes nothing: callers that add a
// dependency to the project finish with GenerateAllSchemas so the tree always
// describes the whole project.
func (m *Manager) addPackage(ctx context.Context, ref string, refresh bool) (*schema.GroupVersionKind, error) {
	// Record the ref unconditionally: top-level calls must always fully
	// process the package so callers get its GVK, while transitive walks
	// consult the visited set before recursing.
	m.claim(ref)

	resolvedRef, _, err := m.resolver.Resolve(ctx, ref)
	if err != nil {
		return nil, errors.Wrapf(err, "cannot resolve package %q; check that the package exists and that the version or digest is valid", ref)
	}

	pullPolicy := corev1.PullIfNotPresent
	if refresh {
		pullPolicy = corev1.PullAlways
	}
	pkg, err := m.getPackage(ctx, resolvedRef.String(), pullPolicy)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to fetch %s", ref)
	}

	gvk, err := runtimeGVKForPackage(pkg)
	if err != nil {
		return nil, errors.Wrapf(err, "cannot identify package %s", ref)
	}
	if err := m.addTransitiveDeps(ctx, pkg, refresh); err != nil {
		return nil, err
	}
	return gvk, nil
}

// addTransitiveDeps generates schemas for the dependencies declared in the
// package's metadata. Transitive dependencies are not added to the project
// file.
func (m *Manager) addTransitiveDeps(ctx context.Context, pkg *runtimexpkg.Package, refresh bool) error {
	for _, dep := range pkg.GetDependencies() {
		repo := dependencyRepo(dep)
		if repo == "" {
			continue
		}
		ref := xpkgRef(repo, dep.Version)
		if !m.claim(ref) {
			continue // Already processed (or in progress) this invocation.
		}
		if _, err := m.addPackage(ctx, ref, refresh); err != nil {
			return errors.Wrapf(err, "cannot add transitive dependency %s of %s", ref, pkg.Source)
		}
	}
	return nil
}

// getPackage fetches a package by resolved ref, memoizing the result so the
// GVK-extraction walk and the source-collection walk of the same top-level
// call do not each download it. A refresh fetch bypasses and replaces the
// memoized package; the current contents are what the ref should resolve to
// now.
func (m *Manager) getPackage(ctx context.Context, resolvedRef string, pullPolicy corev1.PullPolicy) (*runtimexpkg.Package, error) {
	if pullPolicy != corev1.PullAlways {
		m.fetchedMu.Lock()
		pkg, ok := m.fetched[resolvedRef]
		m.fetchedMu.Unlock()
		if ok {
			return pkg, nil
		}
	}

	pkg, err := m.client.Get(ctx, resolvedRef, runtimexpkg.WithPullPolicy(pullPolicy))
	if err != nil {
		return nil, err
	}

	m.fetchedMu.Lock()
	m.fetched[resolvedRef] = pkg
	m.fetchedMu.Unlock()

	return pkg, nil
}

// claim records ref as processed and reports whether it was newly claimed.
func (m *Manager) claim(ref string) bool {
	m.visitedMu.Lock()
	defer m.visitedMu.Unlock()
	if _, ok := m.visited[ref]; ok {
		return false
	}
	m.visited[ref] = struct{}{}
	return true
}

// resetVisited clears the set claim consults, so each top-level traversal
// (RefreshAll, CollectSources) starts with none of its dependencies
// already claimed. Without this, a Manager reused for a second traversal --
// a retry, or a caller that calls CollectSources more than once -- would see
// every ref the first traversal claimed as already visited and silently skip
// it, rather than traverse it again.
func (m *Manager) resetVisited() {
	m.visitedMu.Lock()
	defer m.visitedMu.Unlock()
	m.visited = make(map[string]struct{})
	m.collected = make(map[string][]string)
}

// dependencyRepo returns the OCI repository of a package metadata dependency,
// or "" if none is set.
func dependencyRepo(dep pkgmetav1.Dependency) string {
	switch {
	case dep.Package != nil:
		return *dep.Package
	case dep.Configuration != nil:
		return *dep.Configuration
	case dep.Provider != nil:
		return *dep.Provider
	case dep.Function != nil:
		return *dep.Function
	}
	return ""
}

// xpkgRef formats an OCI reference from a repository and a version that may be
// a digest, an exact tag, or a semver constraint (resolved later by the
// resolver).
func xpkgRef(repo, version string) string {
	if _, err := conregv1.NewHash(version); err == nil {
		return fmt.Sprintf("%s@%s", repo, version)
	}
	if version != "" {
		return fmt.Sprintf("%s:%s", repo, version)
	}
	return repo
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

// AddDependency adds a dependency to the project file and regenerates the
// schemas for the whole project from every source together. Schemas are
// generated in one merged pass -- not just for the new dependency -- because
// a per-source write would leave the tree describing a different set of
// sources than the project has.
func (m *Manager) AddDependency(ctx context.Context, dep *v1alpha1.Dependency) error {
	m.resetVisited()

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

	// Add the dependency in memory and generate before touching the project
	// file. Generation is what fetches git, HTTP and k8s sources, so a
	// dependency that cannot be fetched or generated is never persisted.
	prev := slices.Clone(m.proj.Spec.Dependencies)
	upsertDependency(m.proj, *dep)
	if err := m.GenerateAllSchemas(ctx, nil); err != nil {
		m.proj.Spec.Dependencies = prev
		return err
	}

	// If this write fails, the schemas describe a dependency the project file
	// lacks; the lock then no longer matches the project, so the next pass
	// regenerates.
	return projectfile.Update(m.projFS, m.projFile, func(p *v1alpha1.Project) {
		upsertDependency(p, *dep)
	})
}

// RefreshAll re-resolves every dependency's version constraint against the
// registry, re-pulls each package, and regenerates the schemas. Used by
// `dependency update-cache`.
func (m *Manager) RefreshAll(ctx context.Context, ch async.EventChannel) error {
	m.resetVisited()

	eg, egCtx := errgroup.WithContext(ctx)

	for i := range m.proj.Spec.Dependencies {
		dep := &m.proj.Spec.Dependencies[i]
		desc := "Updating dependency " + GetSourceDescription(*dep)
		eg.Go(func() error {
			ch.SendEvent(desc, async.EventStatusStarted)
			if _, err := m.addDependencyNoWrite(egCtx, dep, true); err != nil {
				ch.SendEvent(desc, async.EventStatusFailure)
				return err
			}
			ch.SendEvent(desc, async.EventStatusSuccess)
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		return err
	}

	// Fetching and re-resolving never writes schemas; finish with one merged
	// generation pass over every source so the tree describes the whole
	// project, not whichever dependency happened to be processed last.
	return m.GenerateAllSchemas(ctx, ch)
}

// GenerateAllSchemas collects every source the project's dependencies
// contribute, adds the project's own apis/ directory, and runs one merged
// schema generation pass. This is the only way the dependency manager writes
// schemas: a per-source write would clobber the run-level files (root index,
// package manifest) of generators whose output describes the whole run, and
// leaves stale models for every language when dependencies are removed.
func (m *Manager) GenerateAllSchemas(ctx context.Context, ch async.EventChannel) error {
	sources, err := m.CollectSources(ctx, ch)
	if err != nil {
		return err
	}

	// The project's own apis/ directory is the second source group. A project
	// declares it even when the directory does not exist yet (a Configuration
	// with no XRDs of its own); the source tolerates a missing directory and
	// simply contributes no resources, so creating it later is a source change
	// the next pass regenerates from.
	apis := smanager.NewProjectAPIsSource(m.projFS, m.proj.Spec.Paths)

	return m.schemas.GenerateFromMultipleSources(ctx, sources, apis)
}

// addDependencyNoWrite fetches (or validates) a dependency without writing
// any schemas. For xpkg dependencies it fetches the package and walks its
// transitive dependencies, so callers get the package's GVK; git, HTTP and
// k8s sources need no fetching ahead of time -- the merged pass reads their
// resources through the sources CollectSources returns.
func (m *Manager) addDependencyNoWrite(ctx context.Context, dep *v1alpha1.Dependency, refresh bool) (*schema.GroupVersionKind, error) {
	switch {
	case dep.Type == v1alpha1.DependencyTypeXpkg:
		if dep.Xpkg == nil {
			return nil, errors.New("xpkg dependency has no package reference")
		}

		return m.addPackage(ctx, xpkgRef(dep.Xpkg.Package, dep.Xpkg.Version), refresh)
	case dep.Git != nil:
		return nil, nil
	case dep.HTTP != nil:
		return nil, nil
	case dep.K8s != nil:
		return nil, nil
	default:
		return nil, errors.New("dependency has no source configured")
	}
}

// CollectSources returns all schema sources from the project's dependencies
// without generating schemas. This allows the caller to merge sources and
// generate schemas in a single pass, which the TypeScript generator requires:
// its output is one npm package per run, not per source, so generating
// dependencies one at a time would overwrite it on every source but the last.
func (m *Manager) CollectSources(ctx context.Context, ch async.EventChannel) ([]smanager.Source, error) {
	m.resetVisited()

	eg, egCtx := errgroup.WithContext(ctx)

	// One slice per project dependency: a package contributes its own CRDs and
	// those of everything its metadata depends on. Indexing by position keeps
	// the flattened result in project-file order.
	sourcesByIndex := make([][]smanager.Source, len(m.proj.Spec.Dependencies))

	for i := range m.proj.Spec.Dependencies {
		dep := &m.proj.Spec.Dependencies[i]
		desc := "Updating dependency " + GetSourceDescription(*dep)
		eg.Go(func() error {
			ch.SendEvent(desc, async.EventStatusStarted)
			srcs, err := m.collectSource(egCtx, dep)
			if err != nil {
				ch.SendEvent(desc, async.EventStatusFailure)
				return err
			}
			ch.SendEvent(desc, async.EventStatusSuccess)

			sourcesByIndex[i] = srcs
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		return nil, err
	}

	var sources []smanager.Source
	for _, srcs := range sourcesByIndex {
		sources = append(sources, srcs...)
	}

	m.warnMultipleVersions()

	// Sort by ID. Project dependencies are collected concurrently and a
	// transitive dependency shared by two of them lands under whichever
	// goroutine claimed it first, so the flattened order is otherwise a race.
	// The merged filesystem prefixes each source by its position, so a stable
	// order keeps that tree reproducible between runs.
	slices.SortFunc(sources, func(a, b smanager.Source) int {
		return strings.Compare(a.ID(), b.ID())
	})

	return sources, nil
}

// recordCollected notes that source collection resolved repo to version (a
// ":tag" or "@digest" suffix).
func (m *Manager) recordCollected(repo, version string) {
	m.visitedMu.Lock()
	defer m.visitedMu.Unlock()
	m.collected[repo] = append(m.collected[repo], version)
}

// warnMultipleVersions warns about every package the project's dependencies
// resolved to more than one version. Both versions' CRDs go into the same
// generator run, so the kinds they share can get conflicting models; on main
// the copy from whichever source sorted last silently won.
func (m *Manager) warnMultipleVersions() {
	m.visitedMu.Lock()
	defer m.visitedMu.Unlock()
	for _, repo := range slices.Sorted(maps.Keys(m.collected)) {
		versions := slices.Sorted(slices.Values(m.collected[repo]))
		if len(versions) < 2 {
			continue
		}
		for i := range versions {
			versions[i] = strings.TrimLeft(versions[i], ":@")
		}
		_, _ = fmt.Fprintf(m.warnings, "warning: the project's dependencies resolve %s to more than one version (%s); schemas are generated from all of them, which can produce conflicting models for the kinds they share. Align the version constraints so they resolve to one version.\n", repo, strings.Join(versions, ", "))
	}
}

// collectSource returns the schema source for a dependency without generating schemas.
func (m *Manager) collectSource(ctx context.Context, dep *v1alpha1.Dependency) ([]smanager.Source, error) {
	desc := GetSourceDescription(*dep)
	switch {
	case dep.Type == v1alpha1.DependencyTypeXpkg:
		if dep.Xpkg == nil || dep.Xpkg.Package == "" {
			return nil, errors.Errorf("xpkg dependency %q is missing xpkg.package; set xpkg.package to a valid package reference", desc)
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

		return m.collectPackageSource(ctx, ref)
	case dep.Git != nil:
		return []smanager.Source{smanager.NewGitSource(*dep, m.gitCloner, m.gitAuthProvider)}, nil
	case dep.HTTP != nil:
		return []smanager.Source{smanager.NewHTTPSource(*dep)}, nil
	case dep.K8s != nil:
		return []smanager.Source{smanager.NewK8sSource(*dep)}, nil
	default:
		return nil, errors.Errorf("dependency %q has no source configured; set exactly one of xpkg, git, http, or k8s", desc)
	}
}

// collectPackageSource fetches a package and returns its CRD source, followed
// by the sources of everything its metadata depends on. The merged schema pass
// generates from exactly what this returns, so a dependency missing here
// contributes no schemas. claim() gives cycle protection and dedupes diamonds.
func (m *Manager) collectPackageSource(ctx context.Context, ref string) ([]smanager.Source, error) {
	if !m.claim(ref) {
		// Already collected this invocation, directly or through another
		// package's dependencies.
		return nil, nil
	}

	resolvedRef, version, err := m.resolver.Resolve(ctx, ref)
	if err != nil {
		return nil, errors.Wrapf(err, "cannot resolve package %q; check that the package exists and that the version or digest is valid", ref)
	}

	// The claim above is on the reference as written, so one package named both
	// by constraint and by exact version passes it twice and would be generated
	// from twice. Claiming before the fetch also skips the dropped copy's
	// download.
	if !m.claim("resolved:" + resolvedRef.String()) {
		return nil, nil
	}

	pullPolicy := corev1.PullIfNotPresent
	pkg, err := m.getPackage(ctx, resolvedRef.String(), pullPolicy)
	if err != nil {
		return nil, errors.Wrapf(err, "cannot download package %q; check registry access and credentials", ref)
	}

	crdFS, err := clixpkg.CRDFilesystem(pkg.Package)
	if err != nil {
		return nil, errors.Wrapf(err, "cannot extract CRDs from package %q; check that it is a valid Crossplane package", ref)
	}

	// Use the resolved version so constraint and exact-version inputs
	// collapse to one schema-lock entry.
	id := pkg.Source + "@" + pkg.Digest
	if version != "" {
		id = pkg.Source + ":" + version
	}

	sources := []smanager.Source{smanager.NewXpkgSource(id, pkg.Digest, crdFS)}
	m.recordCollected(pkg.Source, strings.TrimPrefix(id, pkg.Source))

	// Depth first, in metadata order, so the result is deterministic for a
	// given dependency graph.
	for _, dep := range pkg.GetDependencies() {
		repo := dependencyRepo(dep)
		if repo == "" {
			continue
		}
		depSources, err := m.collectPackageSource(ctx, xpkgRef(repo, dep.Version))
		if err != nil {
			return nil, errors.Wrapf(err, "cannot collect transitive dependency %s of %s", repo, pkg.Source)
		}
		sources = append(sources, depSources...)
	}

	return sources, nil
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
