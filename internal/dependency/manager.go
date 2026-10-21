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

// Package dependency manages the dependencies of Crossplane projects: it records
// them in the project file, fetches their packages into the shared package
// cache, and finds the schema sources they contribute. Generating schemas from
// those sources is the schema manager's job.
package dependency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/Masterminds/semver/v3"
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
	smanager "github.com/crossplane/cli/v2/internal/schemas/manager"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"
)

// Manager manages dependencies for a Crossplane project, including fetching
// packages and listing the schema sources they provide.
type Manager struct {
	proj     *v1alpha1.Project
	projFS   afero.Fs
	projFile string

	gitCloner       git.Cloner
	gitAuthProvider git.AuthProvider

	client   runtimexpkg.Client
	resolver *clixpkg.Resolver

	updateMutex sync.Mutex
}

// ManagerOption configures the dependency manager.
type ManagerOption func(*managerOptions)

type managerOptions struct {
	projFile        string
	gitAuthProvider git.AuthProvider
	client          runtimexpkg.Client
	resolver        *clixpkg.Resolver
}

// WithProjectFile sets the path to the project file.
func WithProjectFile(path string) ManagerOption {
	return func(opts *managerOptions) {
		opts.projFile = path
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
		projFile:        clixpkg.ProjectFile,
		gitAuthProvider: &git.HTTPSAuthProvider{},
	}

	for _, opt := range opts {
		opt(options)
	}

	return &Manager{
		proj:            proj,
		projFS:          projFS,
		projFile:        options.projFile,
		gitCloner:       &git.DefaultCloner{},
		gitAuthProvider: options.gitAuthProvider,
		client:          options.client,
		resolver:        options.resolver,
	}
}

// NewCachedManager returns a dependency manager for a project that fetches
// packages through the xpkg cache in cacheDir, or in DefaultCacheDir if it is
// empty. projFile is the project file's path; only its base name is used,
// because projFS is rooted at the project directory.
func NewCachedManager(proj *v1alpha1.Project, projFS afero.Fs, projFile, cacheDir string, opts ...ManagerOption) (*Manager, error) {
	if cacheDir == "" {
		cacheDir = DefaultCacheDir()
	}
	client, err := clixpkg.NewClient(
		clixpkg.NewRemoteFetcher(),
		clixpkg.WithCacheDir(afero.NewOsFs(), cacheDir),
		clixpkg.WithImageConfigs(proj.Spec.ImageConfigs),
	)
	if err != nil {
		return nil, err
	}
	opts = append([]ManagerOption{
		WithProjectFile(filepath.Base(projFile)),
		WithXpkgClient(client),
		WithResolver(clixpkg.NewResolver(client)),
	}, opts...)
	return NewManager(proj, projFS, opts...), nil
}

// ResolveRef resolves a version constraint in an OCI ref to a concrete tag.  If
// the ref has no tag, has an exact semver version, or is not a valid
// constraint, it is returned unchanged.
func (m *Manager) ResolveRef(ref string) (name.Reference, error) {
	resolved, _, err := m.resolver.Resolve(context.Background(), ref)
	return resolved, err
}

// maxResolveRounds caps the rounds of the fixed point in Resolve, so that
// requirements that keep changing each other's selections fail instead of
// looping.
const maxResolveRounds = 50

// ResolveOptions configures Resolve.
type ResolveOptions struct {
	// Refresh resolves version constraints against the registry and pulls
	// packages again, ignoring Locked.
	Refresh bool

	// Locked holds the pins of the schema lock, keyed by source key. Unless
	// Refresh is set, a locked version is kept as long as it still satisfies
	// the requirements, and a locked git commit is cloned instead of the ref.
	Locked map[string]smanager.LockedSource
}

// A Warning reports a requirement that the resolved dependencies do not meet.
type Warning struct {
	// Package is the repository of the direct dependency.
	Package string

	// Version is the version the direct dependency resolved to.
	Version string

	// Requirer is the package that requires a different version.
	Requirer string

	// Constraint is the version constraint that Version violates.
	Constraint string
}

// String returns the warning as a message.
func (w Warning) String() string {
	return fmt.Sprintf("dependency %s resolved to %s, which does not satisfy %s required by %s", w.Package, w.Version, w.Constraint, w.Requirer)
}

// A requirement is a version of a package that a requirer asks for. The
// project is the requirer of its direct dependencies, and has an empty name.
type requirement struct {
	requirer string
	version  string
}

func (r requirement) String() string {
	requirer, version := r.requirer, r.version
	if requirer == "" {
		requirer = "the project"
	}
	if version == "" {
		version = "any version"
	}
	return fmt.Sprintf("%s requires %s", requirer, version)
}

// A selection is the package version chosen for a repository.
type selection struct {
	ref     string
	version string
	pkg     *runtimexpkg.Package
	gvk     *schema.GroupVersionKind
	crdFS   afero.Fs

	// requires are the versions of the packages that this package depends on,
	// by repository.
	requires map[string]string
}

// A resolution is the outcome of resolving dependencies.
type resolution struct {
	sources  []smanager.Source
	warnings []Warning

	// gvks are the runtime GVKs of the resolved packages, by repository.
	gvks map[string]*schema.GroupVersionKind
}

// A resolver finds the package versions that satisfy the requirements of a set
// of dependencies. It is single use.
type resolver struct {
	m    *Manager
	opts ResolveOptions

	// direct are the versions the project asks for, by repository.
	direct map[string]string

	selected map[string]*selection

	// decided and fetched remember earlier work for the rest of the
	// resolution. They are only written between rounds.
	decided map[string]string
	fetched map[string]*selection
}

// Resolve returns the schema sources of every dependency in the project,
// direct and transitive, sorted by key. Packages are fetched, from the cache if
// present, to find their dependencies.
//
// The versions are chosen deterministically. A direct dependency's version wins
// over what other packages require, and a Warning is returned for each
// requirement it violates. Otherwise the highest version that satisfies every
// requirement wins. Unless opts.Refresh is set, the locked version is
// preferred when it still satisfies them.
//
// Callers that reconcile the schema lock must add the project's own sources,
// such as its XRDs, to the result.
func (m *Manager) Resolve(ctx context.Context, opts ResolveOptions) ([]smanager.Source, []Warning, error) {
	res, err := m.resolve(ctx, m.proj.Spec.Dependencies, opts)
	if err != nil {
		return nil, nil, err
	}
	return res.sources, res.warnings, nil
}

func (m *Manager) resolve(ctx context.Context, deps []v1alpha1.Dependency, opts ResolveOptions) (*resolution, error) {
	r := &resolver{
		m:        m,
		opts:     opts,
		direct:   make(map[string]string),
		selected: make(map[string]*selection),
		decided:  make(map[string]string),
		fetched:  make(map[string]*selection),
	}

	sources := make(map[string]smanager.Source)
	for _, dep := range deps {
		if dep.Type == v1alpha1.DependencyTypeXpkg {
			if dep.Xpkg == nil {
				return nil, errors.New("xpkg dependency has no package reference")
			}
			r.direct[dep.Xpkg.Package] = dep.Xpkg.Version
			continue
		}

		src, err := m.source(dep)
		if err != nil {
			return nil, err
		}
		// Clone the locked commit instead of whatever the ref points to now.
		if pin, ok := opts.Locked[src.Key()]; ok && !opts.Refresh && dep.Git != nil && git.CheckSHA1Hash(pin.Pin) {
			if src, err = m.pinnedSource(dep, pin.Pin); err != nil {
				return nil, err
			}
		}
		if _, ok := sources[src.Key()]; !ok {
			sources[src.Key()] = src
		}
	}

	if err := r.run(ctx); err != nil {
		return nil, err
	}

	res := &resolution{
		warnings: r.warnings(),
		gvks:     make(map[string]*schema.GroupVersionKind),
	}
	for repo, sel := range r.selected {
		res.gvks[repo] = sel.gvk
		src := smanager.NewXpkgSource(sel.pkg.Source, sel.version, sel.pkg.Digest, sel.crdFS)
		sources[src.Key()] = src
	}
	for _, k := range slices.Sorted(maps.Keys(sources)) {
		res.sources = append(res.sources, sources[k])
	}
	return res, nil
}

// source returns the schema source of a dependency that is not a package.
func (m *Manager) source(dep v1alpha1.Dependency) (smanager.Source, error) {
	switch {
	case dep.Git != nil:
		return smanager.NewGitSource(dep, m.gitCloner, m.gitAuthProvider), nil
	case dep.HTTP != nil:
		return smanager.NewHTTPSource(dep), nil
	case dep.K8s != nil:
		return smanager.NewK8sSource(dep), nil
	default:
		return nil, errors.New("dependency has no source configured")
	}
}

// pinnedSource returns the schema source of a dependency that is not a
// package. A git dependency is cloned at pin instead of its ref.
func (m *Manager) pinnedSource(dep v1alpha1.Dependency, pin string) (smanager.Source, error) {
	if dep.Git != nil {
		return smanager.NewPinnedGitSource(dep, pin, m.gitCloner, m.gitAuthProvider), nil
	}
	return m.source(dep)
}

// run selects package versions until a round changes nothing. Each round bases
// its choices on the previous round's selections only, so the order in which
// packages are fetched does not matter.
func (r *resolver) run(ctx context.Context) error {
	for range maxResolveRounds {
		reqs := r.requirements()
		repos := slices.Sorted(maps.Keys(reqs))

		type result struct {
			decision string
			sel      *selection
		}
		results := make([]result, len(repos))

		eg, egCtx := errgroup.WithContext(ctx)
		for i, repo := range repos {
			eg.Go(func() error {
				ref, err := r.decide(egCtx, repo, reqs[repo])
				if err != nil {
					return err
				}
				results[i].decision = ref
				if cur := r.selected[repo]; cur != nil && cur.ref == ref {
					return nil
				}
				if sel, ok := r.fetched[ref]; ok {
					results[i].sel = sel
					return nil
				}
				results[i].sel, err = r.fetch(egCtx, ref)
				return err
			})
		}
		if err := eg.Wait(); err != nil {
			return err
		}

		// Apply the results in a fixed order.
		changed := false
		for i, repo := range repos {
			r.decided[decisionKey(repo, reqs[repo])] = results[i].decision
			if results[i].sel == nil {
				continue
			}
			r.fetched[results[i].sel.ref] = results[i].sel
			r.selected[repo] = results[i].sel
			changed = true
		}
		if !changed {
			return nil
		}
	}
	return errors.New("dependency resolution does not converge")
}

// requirements returns what is required of each package that is still needed,
// sorted by requirer. A package is needed if the project depends on it, or if
// a needed package that has been selected depends on it. Selections of packages
// that are no longer needed are dropped.
func (r *resolver) requirements() map[string][]requirement {
	reqs := make(map[string][]requirement)
	needed := make(map[string]bool)

	queue := slices.Sorted(maps.Keys(r.direct))
	for _, repo := range queue {
		needed[repo] = true
		reqs[repo] = append(reqs[repo], requirement{version: r.direct[repo]})
	}
	for len(queue) > 0 {
		repo := queue[0]
		queue = queue[1:]

		sel := r.selected[repo]
		if sel == nil {
			continue
		}
		for _, dep := range slices.Sorted(maps.Keys(sel.requires)) {
			reqs[dep] = append(reqs[dep], requirement{requirer: repo, version: sel.requires[dep]})
			if !needed[dep] {
				needed[dep] = true
				queue = append(queue, dep)
			}
		}
	}

	for repo := range r.selected {
		if !needed[repo] {
			delete(r.selected, repo)
		}
	}
	for _, rs := range reqs {
		slices.SortFunc(rs, func(a, b requirement) int {
			return strings.Compare(a.requirer, b.requirer)
		})
	}
	return reqs
}

func decisionKey(repo string, reqs []requirement) string {
	parts := make([]string, 0, len(reqs)+1)
	parts = append(parts, repo)
	for _, req := range reqs {
		parts = append(parts, req.requirer+"="+req.version)
	}
	return strings.Join(parts, "\x00")
}

// decide returns the reference of the version of repo that satisfies reqs.
func (r *resolver) decide(ctx context.Context, repo string, reqs []requirement) (string, error) {
	if ref, ok := r.decided[decisionKey(repo, reqs)]; ok {
		return ref, nil
	}

	// A direct dependency wins over everything else.
	if reqs[0].requirer == "" {
		reqs = reqs[:1]
	}

	var digests, tags, constraints []string
	for _, req := range reqs {
		switch _, err := semver.NewConstraint(req.version); {
		case req.version == "":
		case isDigest(req.version):
			digests = append(digests, req.version)
		case err != nil:
			tags = append(tags, req.version)
		default:
			constraints = append(constraints, req.version)
		}
	}

	ref, err := r.choose(ctx, repo, digests, tags, constraints)
	if err != nil {
		descs := make([]string, len(reqs))
		for i, req := range reqs {
			descs[i] = req.String()
		}
		return "", errors.Wrapf(err, "cannot resolve %s (%s)", repo, strings.Join(descs, ", "))
	}
	return ref, nil
}

func isDigest(version string) bool {
	_, err := conregv1.NewHash(version)
	return err == nil
}

func (r *resolver) choose(ctx context.Context, repo string, digests, tags, constraints []string) (string, error) {
	switch {
	case len(digests) > 0:
		// Packages that agree on the digest agree on the version.
		if d := slices.Compact(slices.Sorted(slices.Values(digests))); len(d) > 1 {
			return "", errors.Errorf("conflicting digests %s", strings.Join(d, ", "))
		}
		dgst, err := name.NewDigest(xpkgRef(repo, digests[0]), name.StrictValidation)
		if err != nil {
			return "", err
		}
		return dgst.String(), nil

	case len(tags) > 0:
		if len(constraints) > 0 || len(slices.Compact(slices.Sorted(slices.Values(tags)))) > 1 {
			return "", errors.Errorf("incompatible versions %s", strings.Join(append(tags, constraints...), ", "))
		}
		tag, err := name.NewTag(xpkgRef(repo, tags[0]), name.StrictValidation)
		if err != nil {
			return "", err
		}
		return tag.String(), nil
	}

	if len(constraints) == 0 {
		constraints = []string{">=v0.0.0"}
	}

	// Stay on the locked version if it is still allowed, to not depend on the
	// registry.
	if locked, ok := r.opts.Locked["xpkg://"+repo]; ok && !r.opts.Refresh && locked.Version != "" {
		if v, err := semver.NewVersion(locked.Version); err == nil {
			if c, err := semver.NewConstraint(strings.Join(constraints, ", ")); err == nil && c.Check(v) {
				n, err := name.NewRepository(repo, name.StrictValidation)
				if err != nil {
					return "", err
				}
				return n.Tag(locked.Version).String(), nil
			}
		}
	}

	resolved, _, err := r.m.resolver.ResolveConstraints(ctx, repo, constraints)
	if err != nil {
		return "", err
	}
	return resolved.String(), nil
}

// fetch gets the package at ref and records what it requires.
func (r *resolver) fetch(ctx context.Context, ref string) (*selection, error) {
	pullPolicy := corev1.PullIfNotPresent
	if r.opts.Refresh {
		pullPolicy = corev1.PullAlways
	}
	pkg, err := r.m.client.Get(ctx, ref, runtimexpkg.WithPullPolicy(pullPolicy))
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

	// The tag is not part of the source key, so constraint, exact-version and
	// digest-pinned refs to one package share a schema-lock entry.
	var version string
	if tag, err := name.NewTag(ref, name.StrictValidation); err == nil {
		version = tag.TagStr()
	}

	requires := make(map[string]string)
	for _, dep := range pkg.GetDependencies() {
		if depRepo := dependencyRepo(dep); depRepo != "" {
			requires[depRepo] = dep.Version
		}
	}
	return &selection{ref: ref, version: version, pkg: pkg, gvk: gvk, crdFS: crdFS, requires: requires}, nil
}

// warnings returns the requirements that direct dependencies override.
func (r *resolver) warnings() []Warning {
	var warnings []Warning
	reqs := r.requirements()
	for _, repo := range slices.Sorted(maps.Keys(r.direct)) {
		sel := r.selected[repo]
		if sel == nil || sel.version == "" {
			continue
		}
		v, err := semver.NewVersion(sel.version)
		if err != nil {
			continue
		}
		for _, req := range reqs[repo] {
			if req.requirer == "" {
				continue
			}
			if c, err := semver.NewConstraint(req.version); err == nil && !c.Check(v) {
				warnings = append(warnings, Warning{Package: repo, Version: sel.version, Requirer: req.requirer, Constraint: req.version})
			}
		}
	}
	return warnings
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

// AddDependency checks that a dependency can be fetched, caches it if it is a
// package, and persists it to the project file. It does not generate schemas;
// `crossplane schema update` does.
func (m *Manager) AddDependency(ctx context.Context, dep *v1alpha1.Dependency) error {
	// Fetching the sources proves that the dependency exists, so a typo in a
	// URL or version is caught here and not at the next schema update. Nothing
	// is generated.
	res, err := m.resolve(ctx, []v1alpha1.Dependency{*dep}, ResolveOptions{})
	if err != nil {
		return err
	}
	if err := hashAll(ctx, res.sources); err != nil {
		return err
	}

	// For xpkg dependencies, fill in the apiVersion and kind. We don't care
	// about these for development purposes, but they are required to install an
	// xpkg dependency into a cluster at runtime.
	if dep.Type == v1alpha1.DependencyTypeXpkg && dep.Xpkg != nil {
		if gvk := res.gvks[dep.Xpkg.Package]; gvk != nil {
			dep.Xpkg.APIVersion = gvk.GroupVersion().String()
			dep.Xpkg.Kind = gvk.Kind
		}
	}

	m.updateMutex.Lock()
	defer m.updateMutex.Unlock()

	upsertDependency(m.proj, *dep)
	return projectfile.Update(m.projFS, m.projFile, func(p *v1alpha1.Project) {
		upsertDependency(p, *dep)
	})
}

// hashAll hashes the sources, which fetches their content.
func hashAll(ctx context.Context, sources []smanager.Source) error {
	eg, egCtx := errgroup.WithContext(ctx)
	for _, s := range sources {
		eg.Go(func() error {
			_, err := s.Hash(egCtx)
			return errors.Wrapf(err, "cannot fetch %s", s.Key())
		})
	}
	return eg.Wait()
}

// RefreshAll re-resolves every dependency's version constraint against the
// registry and refreshes the package cache. It also fetches the git, http and
// Kubernetes dependencies. Used by `dependency update-cache`.
func (m *Manager) RefreshAll(ctx context.Context, ch async.EventChannel) error {
	descs := make([]string, len(m.proj.Spec.Dependencies))
	for i, dep := range m.proj.Spec.Dependencies {
		descs[i] = "Updating dependency " + GetSourceDescription(dep)
		ch.SendEvent(descs[i], async.EventStatusStarted)
	}

	err := func() error {
		sources, _, err := m.Resolve(ctx, ResolveOptions{Refresh: true})
		if err != nil {
			return err
		}
		return hashAll(ctx, sources)
	}()

	status := async.EventStatusSuccess
	if err != nil {
		status = async.EventStatusFailure
	}
	for _, desc := range descs {
		ch.SendEvent(desc, status)
	}
	return err
}

// FetchPinned returns the source locked under key, fetched at the pin recorded
// in the lock instead of being resolved again. A package is taken from the
// cache if it is there.
func (m *Manager) FetchPinned(ctx context.Context, key string, entry smanager.LockedSource) (smanager.Source, error) {
	if strings.HasPrefix(key, "xpkg://") {
		pkg, err := m.client.Get(ctx, entry.Pin, runtimexpkg.WithPullPolicy(corev1.PullIfNotPresent))
		if err != nil {
			return nil, errors.Wrapf(err, "failed to fetch %s", entry.Pin)
		}
		crdFS, err := clixpkg.CRDFilesystem(pkg.Package)
		if err != nil {
			return nil, errors.Wrapf(err, "cannot extract CRDs from %s", entry.Pin)
		}
		return smanager.NewXpkgSource(pkg.Source, entry.Version, pkg.Digest, crdFS), nil
	}

	for _, dep := range m.proj.Spec.Dependencies {
		if dep.Type == v1alpha1.DependencyTypeXpkg {
			continue
		}
		src, err := m.source(dep)
		if err != nil {
			return nil, err
		}
		if src.Key() != key {
			continue
		}

		if dep.Git != nil {
			return m.pinnedSource(dep, entry.Pin)
		}
		if dep.HTTP != nil {
			hash, err := src.Hash(ctx)
			if err != nil {
				return nil, errors.Wrapf(err, "cannot fetch %s", key)
			}
			if hash != entry.Pin {
				return nil, errors.Errorf("%s changed: its hash is %s, but the lock has %s", key, hash, entry.Pin)
			}
		}
		return src, nil
	}
	return nil, errors.Errorf("no dependency in the project provides %s", key)
}

// CleanPackages removes the per-user xpkg cache directory at root.
func CleanPackages(root string, fs afero.Fs) error {
	if err := fs.RemoveAll(root); err != nil {
		return errors.Wrapf(err, "cannot remove xpkg cache at %s", root)
	}
	return nil
}

// DependenciesHash returns a hash of the project's dependencies. It does not
// depend on their order, and changes when any dependency changes.
func DependenciesHash(proj *v1alpha1.Project) (string, error) {
	deps := slices.Clone(proj.Spec.Dependencies)
	slices.SortStableFunc(deps, func(a, b v1alpha1.Dependency) int {
		return strings.Compare(GetSourceDescription(a), GetSourceDescription(b))
	})

	bs, err := json.Marshal(deps)
	if err != nil {
		return "", errors.Wrap(err, "cannot marshal dependencies")
	}
	sum := sha256.Sum256(bs)
	return hex.EncodeToString(sum[:]), nil
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
