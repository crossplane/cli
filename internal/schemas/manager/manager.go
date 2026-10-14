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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/invopop/jsonschema"
	"github.com/spf13/afero"
	"golang.org/x/sync/errgroup"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	devv1alpha1 "github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/internal/filesystem"
	"github.com/crossplane/cli/v2/internal/schemas/generator"
	"github.com/crossplane/cli/v2/internal/schemas/runner"
)

// The layout under the managed schemas directory:
//
//	<lang>/                   the tree users (and function builds) consume
//	.cache/<group>/<lang>/    per-group generator output, the assembly inputs
//	.staging/                 per-run scratch, removed at the start of a pass
//
// The dot prefixes keep Go tooling out of the hidden directories, and both sit
// beside the language directories rather than inside them.
const (
	// cacheDirName holds the per-group generator output caches. Each group
	// (dependencies, apis) is generated as a unit into here, and the final
	// language trees are assembled from these caches, so an XRD edit
	// regenerates only the apis group and reuses the dependency output.
	cacheDirName = ".cache"

	// stagingDirName holds in-progress generation and assembly. A crash or a
	// generation failure leaves only scratch here: the previous tree, the
	// caches and the lock stay in place. Leftovers from a crashed run are
	// removed at the start of the next pass.
	stagingDirName = ".staging"
)

// Manager is a schema manager. It manages a directory of schemas, generating
// new schemas only when necessary.
//
// A Manager runs one schema-writing pass at a time: the pass reuses the
// manager's staging directory, so two concurrent passes on one Manager would
// corrupt each other's staging. Every command builds a fresh Manager per run,
// which is all the current call sites need.
type Manager struct {
	fs         afero.Fs
	generators []generator.Interface
	runner     runner.SchemaRunner
}

// GenerateFromMultipleSources generates schemas from every source at once:
// the sources the project's dependencies contribute, and the project's own
// apis/ directory (nil when there is none). The two are generated and cached
// as separate groups, so the caller says which is which rather than the
// manager guessing from source IDs.
//
// Schemas used to be written one source at a time, which could not work for
// a language whose generator describes an entire run in its output
// (TypeScript, #170: one npm package per run), and quietly rotted the tree
// for every language: copying never removed anything, so renamed kinds and
// dropped dependencies left stale models behind, and the lock recorded only
// versions, so changed languages never triggered regeneration.
//
// The pass caches each group's output and assembles the language trees from
// the caches. See generateSources for the full flow and the crash/failure
// semantics.
func (m *Manager) GenerateFromMultipleSources(ctx context.Context, dependencies []Source, apis Source) error {
	var apiSources []Source
	if apis != nil {
		apiSources = []Source{apis}
	}
	for _, src := range slices.Concat(dependencies, apiSources) {
		if src.Type() != SourceTypeCRD && src.Type() != SourceTypeOpenAPI {
			return errors.Errorf("cannot generate schemas for %s: source type %q is not supported; use a CRD or OpenAPI source", describeSource(src), src.Type())
		}
	}
	return m.generateSources(ctx, dependencies, apiSources)
}

// generateSources runs one full generation pass over the two source groups.
//
//  1. Remove leftover .staging from a crashed run: it is scratch by
//     construction.
//  2. For each group, decide freshness from the stamp in its cache (source
//     map, languages, generator fingerprint) and the cache's existence.
//     Regenerate the stale groups concurrently into .staging, stamp them,
//     then rename into .cache/<group>/, so a failure leaves the previous
//     cache in place.
//  3. If either group changed, or the on-disk tree does not match what the
//     lock describes, assemble each language directory in .staging from the
//     group caches and swap it in by rename.
//  4. Write the lock last, as a temp file plus rename.
//
// Until step 3's rename the previous tree stays in place and usable; a
// failure at any point leaves the previous tree, caches and lock untouched.
// A crash between the tree swap and the lock write leaves a new tree with an
// old lock, which the next pass reads as stale and reassembles -- from the
// caches, so that is cheap.
func (m *Manager) generateSources(ctx context.Context, dependencySources, apiSources []Source) error {
	if err := m.fs.RemoveAll(stagingDirName); err != nil {
		return errors.Wrapf(err, "failed to clean up the %s scratch directory", stagingDirName)
	}

	current := m.readLock()
	fingerprint := m.generatorFingerprint()
	languages := m.languages()

	type sourceGroup struct {
		name    string
		sources []Source
	}
	groups := []sourceGroup{
		{name: sourceGroupDependencies, sources: dependencySources},
		{name: sourceGroupAPIs, sources: apiSources},
	}

	// The lock this pass will record: both groups' source maps, filled in as
	// each group is processed. versions are computed up front so a failure
	// after a group's generation does not leave the lock half-updated.
	groupSources := map[string]map[string]string{}
	for _, group := range groups {
		versions, err := m.sourceVersions(ctx, group.sources)
		if err != nil {
			return err
		}
		groupSources[group.name] = sourceMap(group.sources, versions)
	}

	// Stale groups generate concurrently: they share nothing but the
	// generators, which already run one container per call. A failure in one
	// cancels the other; a group that finished first keeps its new cache and
	// stamp, which the next pass reuses if its sources still match.
	var (
		changed   bool
		changedMu sync.Mutex
	)
	eg, egCtx := errgroup.WithContext(ctx)
	for _, group := range groups {
		// A group is fresh when its cache's stamp records these sources,
		// languages and generator fingerprint, and the cache covers every
		// current language. Freshness is read from the cache's own stamp, not
		// the lock: a pass that regenerated one group and then failed leaves
		// that group's new cache behind with the old lock, and judging the
		// cache by the lock would assemble the new output under the old
		// sources' entry.
		want := groupStamp{Generator: fingerprint, Languages: languages, Sources: groupSources[group.name]}
		fresh := m.groupStampMatches(group.name, want) && m.groupCacheExists(group.name)

		if fresh {
			continue
		}
		eg.Go(func() error {
			if err := m.generateGroup(egCtx, group.name, group.sources, want); err != nil {
				return err
			}
			changedMu.Lock()
			changed = true
			changedMu.Unlock()
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return err
	}

	// Even with both groups fresh, the tree may not match the lock: it can be
	// partly deleted by hand, or the pass may have crashed after the tree
	// swap but before the lock write. If anything is off, assemble from the
	// (now certainly valid) caches.
	if changed || !m.treeMatchesLock(current, groupSources, languages, fingerprint) {
		if err := m.assembleTree(); err != nil {
			return err
		}
	}

	next := &lock{
		Version:   lockVersion,
		Generator: fingerprint,
		Languages: languages,
		Groups:    groupSources,
	}
	// A pass that changed nothing leaves the lock file alone, so a no-op
	// build does not touch the project's files.
	if next.equal(current) {
		return nil
	}
	return m.writeLock(next)
}

// sourceVersions computes each source's version, keyed by source ID.
func (m *Manager) sourceVersions(ctx context.Context, sources []Source) (map[string]string, error) {
	versions := make(map[string]string, len(sources))
	for _, src := range sources {
		v, err := src.Version(ctx)
		if err != nil {
			return nil, errors.Wrapf(err, "cannot load %s", describeSource(src))
		}
		versions[src.ID()] = v
	}
	return versions, nil
}

// groupCacheExists reports whether the group's cached output covers every
// current language. A cache that predates a newly added language is missing
// that language's output, so the existence check is per language, not just
// of the directory.
func (m *Manager) groupCacheExists(group string) bool {
	cacheGroup := afero.NewBasePathFs(m.fs, path.Join(cacheDirName, group))
	for _, lang := range m.languages() {
		ok, err := afero.DirExists(cacheGroup, lang)
		if err != nil || !ok {
			return false
		}
	}
	return true
}

// groupStamp records what a group's cache was generated from. It is written
// into the staged cache before the cache is swapped in, so a cache and its
// stamp are always replaced together.
type groupStamp struct {
	Generator string            `json:"generator"`
	Languages []string          `json:"languages"`
	Sources   map[string]string `json:"sources"`
}

// groupStampFileName sits beside the language directories in
// .cache/<group>/, so it never reaches an assembled tree.
const groupStampFileName = ".stamp.json"

// groupStampMatches reports whether the group's cache was generated from
// exactly what want describes. A missing or unreadable stamp does not match.
func (m *Manager) groupStampMatches(group string, want groupStamp) bool {
	bs, err := afero.ReadFile(m.fs, path.Join(cacheDirName, group, groupStampFileName))
	if err != nil {
		return false
	}
	var got groupStamp
	if err := json.Unmarshal(bs, &got); err != nil {
		return false
	}
	return got.Generator == want.Generator &&
		slices.Equal(got.Languages, want.Languages) &&
		maps.Equal(got.Sources, want.Sources)
}

// treeMatchesLock reports whether the on-disk language tree is the one the
// lock describes for these groups, languages and fingerprint: every language
// directory exists. (Its content is the assembly of the caches the lock
// describes, so directory presence is the observable contract; a hand-edited
// tree is out of scope, same as on main.)
func (m *Manager) treeMatchesLock(current *lock, groupSources map[string]map[string]string, languages []string, fingerprint string) bool {
	if !slices.Equal(current.Languages, languages) || current.Generator != fingerprint {
		return false
	}
	for group, sources := range groupSources {
		if !current.groupFresh(group, sources) {
			return false
		}
	}
	for _, lang := range languages {
		ok, err := afero.DirExists(m.fs, lang)
		if err != nil || !ok {
			return false
		}
	}
	return true
}

// generateGroup regenerates one group's output from its sources into the
// group cache. Sources are merged per source type into one generator run,
// and the output is staged then renamed into .cache/<group>/, so a failure
// during generation leaves the previous cache in place.
func (m *Manager) generateGroup(ctx context.Context, group string, sources []Source, stamp groupStamp) error {
	crdSources, openAPISources, err := splitByType(sources)
	if err != nil {
		return err
	}

	// Each source type is one generator run, and the two run concurrently.
	// Their output is collected per language rather than written
	// immediately: a language whose generator describes the whole run in its
	// output needs every source type's contribution before it has one
	// coherent tree, and the languages that produced no output for this group
	// still need an empty directory, so the cache always covers the full
	// language set.
	srcTypes := []struct {
		t       SourceType
		sources []Source
	}{
		{t: SourceTypeCRD, sources: crdSources},
		{t: SourceTypeOpenAPI, sources: openAPISources},
	}
	// Indexed by source type, so the parts keep CRD-then-OpenAPI order no
	// matter which run finishes first.
	results := make([]map[string]afero.Fs, len(srcTypes))
	eg, egCtx := errgroup.WithContext(ctx)
	for i, srcType := range srcTypes {
		if len(srcType.sources) == 0 {
			continue
		}
		eg.Go(func() error {
			mergedFS, err := m.collectSourceResources(egCtx, srcType.sources)
			if err != nil {
				return err
			}
			schemas, err := m.runGenerators(egCtx, mergedFS, srcType.t)
			if err != nil {
				return errors.Wrapf(err, "cannot generate schemas for %s", groupDescription(group))
			}
			results[i] = schemas
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return err
	}

	partsByLang := map[string][]afero.Fs{}
	for _, schemas := range results {
		for lang, schemaFS := range schemas {
			partsByLang[lang] = append(partsByLang[lang], schemaFS)
		}
	}

	stageDir := path.Join(stagingDirName, group)
	if err := m.fs.MkdirAll(stageDir, 0o750); err != nil {
		return errors.Wrapf(err, "failed to create the staging directory for %s", group)
	}

	for _, lang := range m.languages() {
		langStage := afero.NewBasePathFs(m.fs, path.Join(stageDir, lang))
		if err := langStage.MkdirAll(".", 0o750); err != nil {
			return errors.Wrapf(err, "failed to stage %s output for %s", lang, group)
		}
		parts, err := m.mergeParts(lang, partsByLang[lang])
		if err != nil {
			return errors.Wrapf(err, "cannot merge the %s schemas generated from the CRD and OpenAPI sources of %s", lang, groupDescription(group))
		}
		if err := copyParts(parts, langStage); err != nil {
			return errors.Wrapf(err, "cannot combine the %s schemas generated from the CRD and OpenAPI sources of %s", lang, groupDescription(group))
		}
	}

	bs, err := json.Marshal(stamp)
	if err != nil {
		return errors.Wrapf(err, "failed to serialize the %s cache stamp", group)
	}
	if err := afero.WriteFile(m.fs, path.Join(stageDir, groupStampFileName), bs, 0o644); err != nil {
		return errors.Wrapf(err, "failed to stamp the %s cache", group)
	}

	// Swap the staged group output into the cache. The old cache is only
	// removed once the new one is complete in staging.
	cacheDir := path.Join(cacheDirName, group)
	if err := m.fs.MkdirAll(path.Dir(cacheDir), 0o750); err != nil {
		return errors.Wrapf(err, "failed to create the cache directory for %s", group)
	}
	if err := m.fs.RemoveAll(cacheDir); err != nil {
		return errors.Wrapf(err, "failed to clear the old %s cache", group)
	}
	if err := m.fs.Rename(stageDir, cacheDir); err != nil {
		return errors.Wrapf(err, "failed to move the generated %s schemas into the cache", group)
	}
	return nil
}

// groupDescription names a source group for messages.
func groupDescription(group string) string {
	switch group {
	case sourceGroupDependencies:
		return "the project's dependencies"
	case sourceGroupAPIs:
		return "the project's own APIs"
	default:
		return "the " + group + " sources"
	}
}

// splitByType partitions sources into CRD and OpenAPI slices.
func splitByType(sources []Source) (crd, openAPI []Source, err error) {
	for _, src := range sources {
		switch src.Type() {
		case SourceTypeCRD:
			crd = append(crd, src)
		case SourceTypeOpenAPI:
			openAPI = append(openAPI, src)
		default:
			return nil, nil, errors.Errorf("cannot generate schemas for %s: source type %q is not supported; use a CRD or OpenAPI source", describeSource(src), src.Type())
		}
	}
	return crd, openAPI, nil
}

// assembleTree builds each language's tree from the group caches and swaps
// it in by rename. The dependencies group is written first and apis last,
// matching the copy order the two write passes had on main, so any path
// present in both (e.g. shared apimachinery models) resolves to the apis
// copy, just as it did before. Languages that neither group produced output
// for get an empty directory, and languages recorded in the last lock but no
// longer generated have their directories removed.
func (m *Manager) assembleTree() error {
	// The languages to remove a tree for: everything on disk's language set
	// (the lock's Languages, plus the current set) other than what this
	// manager still generates. Dropped languages need no assembly -- their
	// cache entries went with the last group regeneration -- only removal.
	generated := m.languages()
	var removed []string
	for _, lang := range m.generatedOrRecordedLanguages() {
		if !slices.Contains(generated, lang) {
			removed = append(removed, lang)
		}
	}

	if err := m.fs.MkdirAll(stagingDirName, 0o750); err != nil {
		return errors.Wrapf(err, "failed to create the %s assembly directory", stagingDirName)
	}

	for _, lang := range generated {
		treeStage := afero.NewBasePathFs(m.fs, path.Join(stagingDirName, "tree", lang))

		depCache := m.groupCacheFS(sourceGroupDependencies, lang)
		apiCache := m.groupCacheFS(sourceGroupAPIs, lang)

		// A run-level language (see schemaMerger) has its groups' parts
		// merged into one coherent tree; a per-kind language is assembled by
		// copying the groups in turn with conflict detection.
		if _, ok := m.generatorFor(lang).(schemaMerger); ok {
			parts, err := m.mergeParts(lang, []afero.Fs{depCache, apiCache})
			if err != nil {
				return errors.Wrapf(err, "cannot merge the %s schemas of the project's dependencies and its own APIs", lang)
			}
			if err := copyParts(parts, treeStage); err != nil {
				return errors.Wrapf(err, "failed to assemble the %s tree", lang)
			}
		} else {
			// Dependencies first, apis last, matching main's copy order.
			// The cache holds generator output verbatim; this copy defines
			// the on-disk layout, stripping the models/ prefix most
			// generators put their output under.
			if err := copyPartsStrippingModels([]afero.Fs{depCache, apiCache}, treeStage); err != nil {
				return errors.Wrapf(err, "cannot combine the %s schemas of the project's dependencies and its own APIs", lang)
			}
		}

		if err := postProcessForLanguage(lang, treeStage); err != nil {
			return err
		}
	}

	// Swap every language directory in by rename. Rename over an existing
	// directory is not possible, so the old directory is moved aside within
	// staging first (it is scratch already) and the staged tree renamed in.
	// If any swap fails, the ones already made are undone, so a failure here
	// leaves the previous tree in place like a failure at any earlier step.
	replacedDir := path.Join(stagingDirName, "replaced")
	if err := m.fs.MkdirAll(replacedDir, 0o750); err != nil {
		return errors.Wrap(err, "failed to prepare the schema swap")
	}
	var swaps []treeSwap
	for _, lang := range generated {
		sw := treeSwap{lang: lang, replaced: path.Join(replacedDir, lang)}
		moved, err := m.renameIfExists(lang, sw.replaced)
		if err != nil {
			return m.undoSwaps(swaps, errors.Wrapf(err, "failed to move the previous %s schemas aside", lang))
		}
		sw.hadPrevious = moved
		if err := m.fs.Rename(path.Join(stagingDirName, "tree", lang), lang); err != nil {
			return m.undoSwaps(append(swaps, sw), errors.Wrapf(err, "failed to swap the %s tree in", lang))
		}
		sw.swappedIn = true
		swaps = append(swaps, sw)
	}

	// Dropped languages: remove their trees outright.
	for _, lang := range removed {
		if err := m.fs.RemoveAll(lang); err != nil {
			return errors.Wrapf(err, "failed to remove the %s schemas of a language the project no longer generates", lang)
		}
	}

	// The staged trees are in place; the replaced old trees and any staging
	// remainders are scratch now. Leave cleanup of the whole staging dir to
	// the next pass (RemoveAll at its start), which tolerates a crash here.
	return nil
}

// mergeParts combines the parts of a run-level language (see schemaMerger)
// into one part, so its run-level files describe all of them. Empty parts
// (a group or source type that produced nothing) are dropped first, and a
// single remaining part needs no merging. Parts of any other language are
// returned unchanged, for the caller to copy with conflict detection.
func (m *Manager) mergeParts(lang string, parts []afero.Fs) ([]afero.Fs, error) {
	merger, ok := m.generatorFor(lang).(schemaMerger)
	if !ok {
		return parts, nil
	}
	var nonEmpty []afero.Fs
	for _, part := range parts {
		if part == nil {
			continue
		}
		empty, err := isEmptyFs(part)
		if err != nil {
			return nil, err
		}
		if !empty {
			nonEmpty = append(nonEmpty, part)
		}
	}
	if len(nonEmpty) < 2 {
		return nonEmpty, nil
	}
	merged, err := merger.MergeGeneratedSchemas(nonEmpty)
	if err != nil {
		return nil, err
	}
	return []afero.Fs{merged}, nil
}

// errFoundFile stops isEmptyFs's walk at the first file; afero.Walk does not
// support filepath.SkipAll.
var errFoundFile = errors.New("found a file")

// isEmptyFs reports whether fsys holds no files.
func isEmptyFs(fsys afero.Fs) (bool, error) {
	err := afero.Walk(fsys, ".", func(_ string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return errFoundFile
		}
		return nil
	})
	switch {
	case errors.Is(err, errFoundFile):
		return false, nil
	case err != nil:
		return false, err
	default:
		return true, nil
	}
}

// groupCacheFS returns the cached output filesystem for one group and
// language. The cache only ever holds this manager's languages (each group
// regeneration rewrites it for the current set), so it exists for exactly
// the languages assemblable here.
func (m *Manager) groupCacheFS(group, lang string) afero.Fs {
	return afero.NewBasePathFs(m.fs, path.Join(cacheDirName, group, lang))
}

// generatedOrRecordedLanguages returns the union of the current language set
// and the last lock's, so a dropped language is noticed and its tree
// removed.
func (m *Manager) generatedOrRecordedLanguages() []string {
	langs := m.languages()
	for _, lang := range m.readLock().Languages {
		if !slices.Contains(langs, lang) {
			langs = append(langs, lang)
		}
	}
	return langs
}

// treeSwap records one language directory's swap, so a failed swap can be
// undone.
type treeSwap struct {
	lang        string
	replaced    string // where the previous tree was moved aside to
	hadPrevious bool   // a previous tree existed and was moved aside
	swappedIn   bool   // the new tree was renamed into place
}

// undoSwaps reverses swaps, newest first: the new tree is removed and the
// previous one moved back. It is best effort -- a language that cannot be
// restored does not stop the others -- and returns cause, plus the languages
// that could not be restored.
func (m *Manager) undoSwaps(swaps []treeSwap, cause error) error {
	var failed []string
	for _, sw := range slices.Backward(swaps) {
		if sw.swappedIn {
			if err := m.fs.RemoveAll(sw.lang); err != nil {
				failed = append(failed, fmt.Sprintf("%s (%v)", sw.lang, err))
				continue
			}
		}
		if sw.hadPrevious {
			if err := m.fs.Rename(sw.replaced, sw.lang); err != nil {
				failed = append(failed, fmt.Sprintf("%s (%v)", sw.lang, err))
			}
		}
	}
	if len(failed) > 0 {
		return errors.Errorf("%v; the previous schemas could not be restored for %s, and the next build will regenerate them", cause, strings.Join(failed, ", "))
	}
	return cause
}

// renameIfExists moves "from" onto "to" when "from" exists, and reports
// whether it did; a missing "from" is not an error (a language that has never
// been generated).
func (m *Manager) renameIfExists(from, to string) (bool, error) {
	ok, err := afero.Exists(m.fs, from)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	return true, m.fs.Rename(from, to)
}

// copyParts copies generated output parts into dst. Parts go in verbatim --
// the same layout the generator produced, models/ prefix included -- so the
// group cache stores generator output unchanged and exactly one copy step
// (tree assembly) defines the on-disk layout by stripping the models/ prefix.
//
// A run-level part is one coherent tree; the non-nil parts of a per-kind
// language all belong to the same group, and a file conflict between two
// parts is an error rather than a silent last-write-wins.
func copyParts(parts []afero.Fs, dst afero.Fs) error {
	for _, part := range parts {
		if part == nil {
			continue
		}
		if err := copyTreeWithConflicts(part, dst); err != nil {
			return err
		}
	}
	return nil
}

// copyPartsStrippingModels assembles the on-disk tree from cached generator
// output: most generators put their output under a models/ directory, which
// the tree does not keep (main stripped it during copy, and the tree layout
// is part of the user-visible contract). This is the only place the prefix
// is removed, so it can never be stripped twice.
func copyPartsStrippingModels(parts []afero.Fs, dst afero.Fs) error {
	for _, part := range parts {
		if part == nil {
			continue
		}
		src := part
		if ok, err := afero.DirExists(part, "models"); err == nil && ok {
			src = afero.NewBasePathFs(part, "models")
		}
		if err := copyTreeWithConflicts(src, dst); err != nil {
			return err
		}
	}
	return nil
}

// copyTreeWithConflicts copies every file from src into dst, erroring when a
// path already exists in dst with different content. Identical content is
// allowed: some inputs are genuinely shared (KCL's kcl.mod, Go's apimachinery
// models) and are byte-for-byte the same from either group.
func copyTreeWithConflicts(src, dst afero.Fs) error {
	return afero.Walk(src, ".", func(p string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		content, err := afero.ReadFile(src, p)
		if err != nil {
			return errors.Wrapf(err, "failed to read %s", p)
		}

		existing, err := afero.ReadFile(dst, p)
		switch {
		case err == nil:
			if !bytes.Equal(existing, content) {
				return errors.Errorf("conflicting generated file %s: two generator runs produced different content for the same path; report this as a bug", p)
			}
			return nil
		case errors.Is(err, fs.ErrNotExist):
			// Not present in dst yet; write it below.
		default:
			return errors.Wrapf(err, "failed to read the existing %s", p)
		}

		if err := dst.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return errors.Wrapf(err, "failed to create the directory for %s", p)
		}
		return afero.WriteFile(dst, p, content, 0o644)
	})
}

// collectSourceResources merges resources from all sources into a single
// filesystem, each under a unique prefix so files from different sources
// cannot collide on name.
func (m *Manager) collectSourceResources(ctx context.Context, sources []Source) (afero.Fs, error) {
	mergedFS := afero.NewMemMapFs()

	for i, src := range sources {
		srcFS, err := src.Resources(ctx)
		if err != nil {
			return nil, errors.Wrapf(err, "cannot load %s", describeSource(src))
		}

		// Copy resources into merged filesystem under a unique prefix
		// to avoid file name collisions
		prefix := fmt.Sprintf("%04d_%s", i, sanitizeSourceID(src.ID()))
		prefixedFS := afero.NewBasePathFs(mergedFS, prefix)
		if err := filesystem.CopyFilesBetweenFs(srcFS, prefixedFS); err != nil {
			return nil, errors.Wrapf(err, "cannot read the resources of %s", describeSource(src))
		}
	}

	return mergedFS, nil
}

// runGenerators runs all generators on the merged filesystem and returns the generated schemas.
func (m *Manager) runGenerators(ctx context.Context, mergedFS afero.Fs, sourceType SourceType) (map[string]afero.Fs, error) {
	schemas := make(map[string]afero.Fs)
	var schemasMu sync.Mutex
	eg, egCtx := errgroup.WithContext(ctx)

	for _, gen := range m.generators {
		eg.Go(func() error {
			schemaFS, err := m.runGenerator(egCtx, gen, mergedFS, sourceType)
			if err != nil {
				return errors.Wrapf(err, "%s generator", gen.Language())
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

	return schemas, nil
}

// runGenerator runs a single generator on the merged filesystem.
func (m *Manager) runGenerator(ctx context.Context, gen generator.Interface, mergedFS afero.Fs, sourceType SourceType) (afero.Fs, error) {
	switch sourceType {
	case SourceTypeCRD:
		return gen.GenerateFromCRD(ctx, mergedFS, m.runner)
	case SourceTypeOpenAPI:
		return gen.GenerateFromOpenAPI(ctx, mergedFS, m.runner)
	default:
		return nil, errors.Errorf("unsupported source type %q", sourceType)
	}
}

// postProcessForLanguage runs language-specific finalization over an
// assembled tree (the JSON index schema).
func postProcessForLanguage(language string, langFS afero.Fs) error {
	switch language {
	case devv1alpha1.SchemaLanguageJSON:
		if err := jsonBuildIndexSchema(langFS); err != nil {
			return errors.Wrap(err, "failed to build index schema for JSON")
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

// schemaMerger is implemented by a generator whose output for one language
// describes an entire generation run rather than one source: the TypeScript
// generator's root index.js/index.d.ts and package.json describe every group
// the run saw, unlike the other generators' per-kind files. When a language
// has output from more than one group in its cache, assembling the tree
// combines them through this interface first, instead of letting the later
// group's copy silently replace the earlier one's root files.
type schemaMerger interface {
	MergeGeneratedSchemas(parts []afero.Fs) (afero.Fs, error)
}

// languages returns the sorted language identifiers this manager generates for.
func (m *Manager) languages() []string {
	langs := make([]string, 0, len(m.generators))
	for _, g := range m.generators {
		langs = append(langs, g.Language())
	}
	slices.Sort(langs)
	return langs
}

// generatorFor returns the configured generator for a language, or nil if
// none matches. lang always comes from a generator's own Language(), so a nil
// result would mean the generator set changed mid-pass.
func (m *Manager) generatorFor(lang string) generator.Interface {
	for _, g := range m.generators {
		if g.Language() == lang {
			return g
		}
	}
	return nil
}

// sanitizeSourceID converts a source ID to a safe directory name.
func sanitizeSourceID(id string) string {
	// Replace characters that are problematic in filesystem paths
	result := id
	for _, c := range []string{"://", ":", "/", "@"} {
		result = strings.ReplaceAll(result, c, "_")
	}
	return result
}

// New returns an initialized manager.
func New(fs afero.Fs, gens []generator.Interface, r runner.SchemaRunner) *Manager {
	return &Manager{
		fs:         fs,
		generators: gens,
		runner:     r,
	}
}
