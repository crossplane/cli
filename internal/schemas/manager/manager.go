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

// Package manager implements a schema manager for use in Crossplane projects.
package manager

import (
	"context"
	"encoding/json"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"sync"

	"github.com/invopop/jsonschema"
	"github.com/spf13/afero"
	"golang.org/x/sync/errgroup"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	devv1alpha1 "github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/internal/schemas/generator"
	"github.com/crossplane/cli/v2/internal/schemas/runner"
)

// Manager is a schema manager. It manages a directory of schemas and a lock
// that records, per source, the content hash it was generated from and the
// files that came out of it. Schemas are generated only when necessary.
type Manager struct {
	fs         afero.Fs
	generators []generator.Interface
	runner     runner.SchemaRunner
	flags      Flags

	// lockMu guards the lock file and the generated files it describes.
	lockMu sync.RWMutex
}

// Option configures a Manager.
type Option func(*Manager)

// WithFlags sets the generation flags recorded in the lock. Sources locked
// with different flags are regenerated.
func WithFlags(f Flags) Option {
	return func(m *Manager) {
		// Sort a copy so the same globs in another order don't regenerate.
		f.APIGroups = slices.Sorted(slices.Values(f.APIGroups))
		m.flags = f
	}
}

// FlagsFor returns the generation flags for a project's schema configuration.
// It is safe to call with nil schemas.
func FlagsFor(schemas *devv1alpha1.ProjectSchemas, goRuntimeObjects bool) Flags {
	return Flags{
		GoRuntimeObjects: goRuntimeObjects,
		APIGroups:        schemas.GetAPIGroups(),
	}
}

// DriftKind is the way a generated file differs from the lock.
type DriftKind string

const (
	// DriftMissing means the file is recorded in the lock but not on disk.
	DriftMissing DriftKind = "missing"
	// DriftModified means the file on disk differs from the recorded hash.
	DriftModified DriftKind = "modified"
)

// DriftedFile is a generated file that differs from the lock.
type DriftedFile struct {
	Path string
	Kind DriftKind
}

// ReconcileResult describes what Reconcile removed.
type ReconcileResult struct {
	// Sources are the keys of lock entries that were removed.
	Sources []string
	// Files are the generated files that were deleted with them.
	Files []string
}

// SourceStatus is the on-disk state of one locked source.
type SourceStatus struct {
	Key   string
	Drift []DriftedFile
}

// Add ensures schemas for resources in the given source are present in the
// managed directory and recorded in the lock. The source is regenerated if its
// hash, the generation flags or the set of languages changed. Files that
// merely drifted from the lock are left alone; see Manager.Generate.
func (m *Manager) Add(ctx context.Context, source Source) error {
	hash, err := source.Hash(ctx)
	if err != nil {
		return err
	}

	m.lockMu.RLock()
	l, err := m.getLock()
	m.lockMu.RUnlock()
	if err != nil {
		return err
	}

	if entry, ok := l.Sources[source.Key()]; ok && m.upToDate(source.Key(), entry, hash) {
		return nil
	}

	files, err := m.render(ctx, source)
	if err != nil {
		return err
	}
	return m.commit(ctx, source, hash, files)
}

// HasLock returns true if a usable schema lock exists. A lock written by an
// earlier version is not usable.
func (m *Manager) HasLock() (bool, error) {
	m.lockMu.RLock()
	defer m.lockMu.RUnlock()

	_, ok, err := m.readLock()
	return ok, err
}

// Locked returns the lock entry of every locked source, by source key.
func (m *Manager) Locked() (map[string]LockedSource, error) {
	m.lockMu.RLock()
	defer m.lockMu.RUnlock()

	l, err := m.getLock()
	if err != nil {
		return nil, err
	}

	locked := make(map[string]LockedSource, len(l.Sources))
	for key, src := range l.Sources {
		locked[key] = LockedSource{Pin: src.Pin, Version: src.Version, SourceHash: src.SourceHash}
	}
	return locked, nil
}

// SetDependenciesHash records the hash of the project's dependencies in the
// lock, leaving its sources untouched.
func (m *Manager) SetDependenciesHash(hash string) error {
	m.lockMu.Lock()
	defer m.lockMu.Unlock()

	l, err := m.getLock()
	if err != nil {
		return err
	}
	l.DependenciesHash = hash
	return m.updateLock(l)
}

// Reconcile removes every locked source that is not in liveSources, together
// with its generated files. It must be called with all live sources, not a
// subset, or sources that are still in use are removed.
func (m *Manager) Reconcile(_ context.Context, liveSources []Source) (*ReconcileResult, error) {
	live := make(map[string]struct{}, len(liveSources))
	for _, s := range liveSources {
		live[s.Key()] = struct{}{}
	}

	m.lockMu.Lock()
	defer m.lockMu.Unlock()

	l, err := m.getLock()
	if err != nil {
		return nil, err
	}

	res := &ReconcileResult{}
	var orphaned []lockFile
	for key, entry := range l.Sources {
		if _, ok := live[key]; ok {
			continue
		}
		res.Sources = append(res.Sources, key)
		orphaned = append(orphaned, entry.Files...)
		delete(l.Sources, key)
	}
	sort.Strings(res.Sources)
	if len(res.Sources) == 0 {
		return res, nil
	}

	res.Files, err = m.removeUnowned(l, orphaned)
	if err != nil {
		return nil, err
	}
	if err := m.rebuildIndexes(); err != nil {
		return nil, err
	}
	return res, m.updateLock(l)
}

// Status reports, for every locked source, which generated files differ from
// the lock. It only reads the schema directory; it never resolves sources.
func (m *Manager) Status(_ context.Context) ([]SourceStatus, error) {
	m.lockMu.RLock()
	defer m.lockMu.RUnlock()

	l, err := m.getLock()
	if err != nil {
		return nil, err
	}

	statuses := make([]SourceStatus, 0, len(l.Sources))
	for key, entry := range l.Sources {
		drift, err := m.drift(entry)
		if err != nil {
			return nil, err
		}
		statuses = append(statuses, SourceStatus{Key: key, Drift: drift})
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Key < statuses[j].Key })
	return statuses, nil
}

// Fetcher returns the locked source key again, fetched at the pin its lock
// entry records instead of being resolved anew.
type Fetcher func(ctx context.Context, key string, entry LockedSource) (Source, error)

// Verify checks that the lock describes the project and recreates generated
// files that are missing. It fails if the dependencies, a local source, the
// generation flags or the languages changed since the lock was written, or if a
// generated file was modified. It never changes the lock.
//
// The local sources are those of the project itself, which are always at hand.
// depsHash identifies the project's dependencies. Their sources are only
// fetched, by fetch, when one of their files has to be recreated.
func (m *Manager) Verify(ctx context.Context, local []Source, depsHash string, fetch Fetcher) error {
	return m.restore(ctx, local, depsHash, fetch, false)
}

// Generate recreates missing and modified generated files from the lock. The
// project must still match the lock; it fails otherwise and never changes the
// lock.
func (m *Manager) Generate(ctx context.Context, local []Source, depsHash string, fetch Fetcher) error {
	return m.restore(ctx, local, depsHash, fetch, true)
}

// Clean removes all generated schemas and the lock.
func (m *Manager) Clean() error {
	m.lockMu.Lock()
	defer m.lockMu.Unlock()

	entries, err := afero.ReadDir(m.fs, ".")
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return errors.Wrap(err, "failed to list schema directory")
	}
	for _, e := range entries {
		if err := m.fs.RemoveAll(e.Name()); err != nil {
			return errors.Wrapf(err, "failed to remove %s", e.Name())
		}
	}
	return nil
}

func (m *Manager) restore(ctx context.Context, local []Source, depsHash string, fetch Fetcher, repairModified bool) error {
	m.lockMu.Lock()
	defer m.lockMu.Unlock()

	l, err := m.getLock()
	if err != nil {
		return err
	}
	if l.DependenciesHash != depsHash {
		return errors.New("dependencies changed since the schema lock was written; run `crossplane schema update`")
	}

	locals := make(map[string]Source, len(local))
	for _, s := range local {
		locals[s.Key()] = s
	}
	if problems := m.stateProblems(ctx, l, locals); len(problems) > 0 {
		return errors.Join(problems...)
	}

	var problems []error
	rebuild := false
	for _, key := range sortedKeys(l.Sources) {
		entry := l.Sources[key]
		drift, err := m.drift(entry)
		if err != nil {
			return err
		}

		var repair []string
		for _, d := range drift {
			if d.Kind == DriftModified && !repairModified {
				problems = append(problems, errors.Errorf("generated file %s was modified; run `crossplane schema generate` to restore it", d.Path))
				continue
			}
			repair = append(repair, d.Path)
		}
		if len(repair) == 0 {
			continue
		}

		rebuild = true
		p, err := m.restoreSource(ctx, key, entry, locals[key], fetch, repair)
		if err != nil {
			return err
		}
		problems = append(problems, p...)
	}

	if rebuild {
		if err := m.rebuildIndexes(); err != nil {
			return err
		}
	}
	return errors.Join(problems...)
}

// stateProblems reports the reasons the lock does not describe the project:
// local sources that are not locked or changed, and locked sources generated
// with other flags or languages than the manager has now. It doesn't need the
// network.
func (m *Manager) stateProblems(ctx context.Context, l *lock, locals map[string]Source) []error {
	var problems []error
	for _, key := range sortedKeys(locals) {
		entry, ok := l.Sources[key]
		if !ok {
			problems = append(problems, errors.Errorf("source %s is not in the schema lock; run `crossplane schema update`", key))
			continue
		}
		hash, err := locals[key].Hash(ctx)
		if err != nil {
			problems = append(problems, errors.Wrapf(err, "cannot hash source %s", key))
			continue
		}
		if hash != entry.SourceHash {
			problems = append(problems, errors.Errorf("source %s changed since the schema lock was written; run `crossplane schema update`", key))
		}
	}
	for _, key := range sortedKeys(l.Sources) {
		entry := l.Sources[key]
		if !m.upToDate(key, entry, entry.SourceHash) {
			problems = append(problems, errors.Errorf("source %s was generated with other flags or languages than configured; run `crossplane schema update`", key))
		}
	}
	return problems
}

// restoreSource recreates the given files of one locked source, fetching the
// source at its pin unless it is local. It returns the reasons it could not
// restore something. An error means the attempt itself failed.
func (m *Manager) restoreSource(ctx context.Context, key string, entry lockSource, src Source, fetch Fetcher, paths []string) ([]error, error) {
	if src == nil {
		if fetch == nil {
			return nil, errors.Errorf("cannot recreate schemas of %s: no way to fetch it", key)
		}
		var err error
		src, err = fetch(ctx, key, LockedSource{Pin: entry.Pin, Version: entry.Version, SourceHash: entry.SourceHash})
		if err != nil {
			return nil, errors.Wrapf(err, "cannot fetch %s", key)
		}
		hash, err := src.Hash(ctx)
		if err != nil {
			return nil, errors.Wrapf(err, "cannot hash %s", key)
		}
		if hash != entry.SourceHash {
			return []error{errors.Errorf("source %s fetched at its pin does not match the schema lock; run `crossplane schema update`", key)}, nil
		}
	}
	return m.recreate(ctx, entry, src, paths)
}

// recreate renders the source again and writes the given files, provided they
// come out exactly as the lock recorded them.
func (m *Manager) recreate(ctx context.Context, entry lockSource, src Source, paths []string) ([]error, error) {
	files, err := m.render(ctx, src)
	if err != nil {
		return nil, err
	}
	locked := make(map[string]string, len(entry.Files))
	for _, f := range entry.Files {
		locked[f.Path] = f.SHA256
	}

	var problems []error
	for _, p := range paths {
		content, ok := files[p]
		if !ok || hashContent(content) != locked[p] {
			problems = append(problems, errors.Errorf("cannot recreate %s exactly as locked; run `crossplane schema update`", p))
			continue
		}
		if err := m.writeFile(p, content); err != nil {
			return problems, err
		}
	}
	return problems, nil
}

// upToDate returns true if the lock entry was generated from the given hash
// with the flags that apply to the source and for exactly the manager's
// languages.
func (m *Manager) upToDate(key string, entry lockSource, hash string) bool {
	return entry.SourceHash == hash &&
		entry.Flags.equal(m.flagsFor(key)) &&
		slices.Equal(entry.Languages, m.languages())
}

// languages returns the sorted languages the manager generates.
func (m *Manager) languages() []string {
	langs := make([]string, 0, len(m.generators))
	for _, gen := range m.generators {
		langs = append(langs, gen.Language())
	}
	slices.Sort(langs)
	return slices.Compact(langs)
}

// flagsFor returns the generation flags that apply to the source with the
// given key. API group filters never apply to local sources, so they are
// cleared for them.
func (m *Manager) flagsFor(key string) Flags {
	f := m.flags
	if isLocalKey(key) {
		f.APIGroups = nil
	}
	return f
}

// drift compares the generated files of a lock entry with the disk.
func (m *Manager) drift(entry lockSource) ([]DriftedFile, error) {
	var drift []DriftedFile
	for _, f := range entry.Files {
		content, err := afero.ReadFile(m.fs, f.Path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			drift = append(drift, DriftedFile{Path: f.Path, Kind: DriftMissing})
		case err != nil:
			return nil, errors.Wrapf(err, "failed to read %s", f.Path)
		case hashContent(content) != f.SHA256:
			drift = append(drift, DriftedFile{Path: f.Path, Kind: DriftModified})
		}
	}
	return drift, nil
}

// render runs the generators on the source and returns the generated files
// keyed by their slash-separated path relative to the managed directory, i.e.
// "<language>/<file>".
func (m *Manager) render(ctx context.Context, source Source) (map[string][]byte, error) {
	fromFS, err := source.Resources(ctx)
	if err != nil {
		return nil, err
	}
	if !isLocalKey(source.Key()) {
		fromFS, err = filterAPIGroups(fromFS, source.Type(), m.flags.APIGroups)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to filter %s by API group", source.Key())
		}
	}

	var schemasMu sync.Mutex
	schemas := make(map[string]afero.Fs)
	eg, egCtx := errgroup.WithContext(ctx)
	sourceType := source.Type()
	for _, gen := range m.generators {
		eg.Go(func() error {
			var schemaFS afero.Fs
			var err error

			switch sourceType {
			case SourceTypeCRD:
				schemaFS, err = gen.GenerateFromCRD(egCtx, fromFS, m.runner)
			case SourceTypeOpenAPI:
				schemaFS, err = gen.GenerateFromOpenAPI(egCtx, fromFS, m.runner)
			default:
				return errors.Errorf("unsupported source type %q", sourceType)
			}
			if err != nil {
				return err
			}

			if schemaFS != nil {
				schemasMu.Lock()
				schemas[gen.Language()] = schemaFS
				schemasMu.Unlock()
			}

			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}

	// Generators produce output into models/; we strip that prefix so files
	// land directly in the language directory.
	files := make(map[string][]byte)
	for lang, genFS := range schemas {
		root := genFS
		if fi, err := genFS.Stat("models"); err == nil && fi.IsDir() {
			root = afero.NewBasePathFs(genFS, "models")
		}
		err := afero.Walk(root, ".", func(p string, info fs.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}
			content, err := afero.ReadFile(root, p)
			if err != nil {
				return err
			}
			files[path.Join(lang, filepath.ToSlash(p))] = content
			return nil
		})
		if err != nil {
			return nil, errors.Wrapf(err, "failed to read %s schemas", lang)
		}
	}
	return files, nil
}

// commit writes the files of a source and records them in the lock. Files the
// source owned before and no longer produces are deleted, unless another
// source also records them.
func (m *Manager) commit(ctx context.Context, source Source, hash string, files map[string][]byte) error {
	key := source.Key()

	// Resolve the pin before taking the lock; it may need the network.
	var pin, version string
	if p, ok := source.(Pinned); ok {
		var err error
		if pin, err = p.Pin(ctx); err != nil {
			return errors.Wrapf(err, "cannot pin source %s", key)
		}
		version = p.Version()
	}

	m.lockMu.Lock()
	defer m.lockMu.Unlock()

	l, err := m.getLock()
	if err != nil {
		return err
	}

	entry := lockSource{SourceHash: hash, Pin: pin, Version: version, Flags: m.flagsFor(key), Languages: m.languages(), Files: make([]lockFile, 0, len(files))}
	for p, content := range files {
		if err := m.writeFile(p, content); err != nil {
			return err
		}
		entry.Files = append(entry.Files, lockFile{Path: p, SHA256: hashContent(content)})
	}
	sort.Slice(entry.Files, func(i, j int) bool { return entry.Files[i].Path < entry.Files[j].Path })

	var stale []lockFile
	for _, f := range l.Sources[key].Files {
		if _, ok := files[f.Path]; !ok {
			stale = append(stale, f)
		}
	}
	l.Sources[key] = entry
	if _, err := m.removeUnowned(l, stale); err != nil {
		return err
	}

	if err := m.rebuildIndexes(); err != nil {
		return err
	}
	return m.updateLock(l)
}

// removeUnowned deletes the given files unless a source in the lock still
// records them, and returns the paths it deleted.
func (m *Manager) removeUnowned(l *lock, files []lockFile) ([]string, error) {
	owned := l.owned()
	var removed []string
	for _, f := range files {
		if _, ok := owned[f.Path]; ok {
			continue
		}
		if err := m.fs.Remove(f.Path); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue // Already gone.
			}
			return removed, errors.Wrapf(err, "failed to remove %s", f.Path)
		}
		removed = append(removed, f.Path)
		m.removeEmptyParents(f.Path)
	}
	sort.Strings(removed)
	return removed, nil
}

// removeEmptyParents removes the directories above the file that became empty.
func (m *Manager) removeEmptyParents(filePath string) {
	for dir := path.Dir(filePath); dir != "." && dir != "/"; dir = path.Dir(dir) {
		entries, err := afero.ReadDir(m.fs, dir)
		if err != nil || len(entries) > 0 {
			return
		}
		if err := m.fs.Remove(dir); err != nil {
			return
		}
	}
}

func (m *Manager) writeFile(filePath string, content []byte) error {
	if err := m.fs.MkdirAll(path.Dir(filePath), 0o750); err != nil {
		return errors.Wrapf(err, "failed to create directory for %s", filePath)
	}
	return errors.Wrapf(afero.WriteFile(m.fs, filePath, content, 0o644), "failed to write %s", filePath)
}

// rebuildIndexes refreshes per-language files that are derived from all
// generated files, such as the JSON index schema.
func (m *Manager) rebuildIndexes() error {
	for _, gen := range m.generators {
		lang := gen.Language()
		if err := postProcessForLanguage(lang, afero.NewBasePathFs(m.fs, lang)); err != nil {
			return err
		}
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func postProcessForLanguage(language string, langFS afero.Fs) error {
	switch language {
	case devv1alpha1.SchemaLanguageJSON:
		if err := jsonBuildIndexSchema(langFS); err != nil {
			return errors.Wrap(err, "failed to build index schema for JSON")
		}
		return nil

	case devv1alpha1.SchemaLanguageRust:
		if err := generator.BuildRustModuleTree(langFS); err != nil {
			return errors.Wrap(err, "cannot finish generating Rust models; check the reported file and run schema generation again")
		}
		return nil

	default:
		return nil
	}
}

func jsonBuildIndexSchema(langFS afero.Fs) error {
	schemas, err := afero.Glob(langFS, "*.schema.json")
	if err != nil {
		return err
	}

	metaFile := "index.schema.json"
	if len(schemas) == 0 {
		// Nothing to index; don't leave a stale or empty index behind.
		if err := langFS.Remove(metaFile); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	var metaSchema jsonschema.Schema
	for _, schema := range schemas {
		if schema == metaFile {
			continue
		}
		metaSchema.AnyOf = append(metaSchema.AnyOf, &jsonschema.Schema{
			Ref: filepath.Base(schema),
		})
	}
	bs, err := json.Marshal(metaSchema)
	if err != nil {
		return err
	}

	return afero.WriteFile(langFS, metaFile, bs, 0o644)
}

func (m *Manager) getLock() (*lock, error) {
	l, ok, err := m.readLock()
	if err != nil {
		return nil, err
	}
	if !ok {
		return newLock(), nil
	}
	return l, nil
}

// readLock reads the lock file. It reports false if there is no usable lock:
// the file is missing, or was written by an earlier version.
func (m *Manager) readLock() (*lock, bool, error) {
	lf, err := m.fs.Open(lockFileName)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer func() { _ = lf.Close() }()

	var l lock
	if err := json.NewDecoder(lf).Decode(&l); err != nil {
		return nil, false, err
	}
	// Locks written by earlier versions are not migrated. Starting from an
	// empty lock regenerates every source once and rewrites the file.
	if l.LockVersion != lockVersion || l.Sources == nil {
		return nil, false, nil
	}

	return &l, true, nil
}

func (m *Manager) updateLock(l *lock) error {
	if err := m.fs.MkdirAll("/", 0o750); err != nil {
		return errors.Wrap(err, "failed to ensure schema directory exists")
	}

	bs, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return errors.Wrap(err, "failed to serialize schema lock")
	}
	// Append a trailing newline so the file ends cleanly and edits to the last
	// entry produce minimal diffs.
	bs = append(bs, '\n')

	if err := afero.WriteFile(m.fs, lockFileName, bs, 0o600); err != nil {
		return errors.Wrap(err, "failed to write schema lock file")
	}

	return nil
}

// New returns an initialized manager.
func New(fs afero.Fs, gens []generator.Interface, r runner.SchemaRunner, opts ...Option) *Manager {
	m := &Manager{
		fs:         fs,
		generators: gens,
		runner:     r,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}
