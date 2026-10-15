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

package manager

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"maps"
	"slices"

	"github.com/spf13/afero"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	crversion "github.com/crossplane/crossplane-runtime/v2/pkg/version"
)

const (
	lockFileName = ".lock.json"

	// lockVersion is the format of the lock file this CLI writes. A lock with
	// a different (or missing) version is not understood, and reads as stale
	// so the project regenerates rather than trusting it.
	lockVersion = 2

	// sourceGroupDependencies is the group of every source the project's
	// dependencies contribute: each declared dependency and the transitive
	// xpkg dependencies their metadata declares.
	sourceGroupDependencies = "dependencies"

	// sourceGroupAPIs is the group of the project's own apis/ directory.
	sourceGroupAPIs = "apis"
)

// lock tracks the state of the generated schema tree. It is persisted to the
// manager's filesystem, and read back to decide whether the tree is fresh.
//
// The tree is assembled from per-group output caches (see the layout in
// manager.go). A group is fresh only when all of these hold:
//
//   - its source map exactly matches the current sources, so a removed
//     dependency is stale,
//   - the language set matches the configured generators,
//   - the generator fingerprint matches, so a CLI or toolchain upgrade that
//     changes generated code is stale,
//   - its cached output still exists.
type lock struct {
	// Version of the lock format. Anything other than lockVersion is not
	// understood and reads as stale.
	Version int `json:"version"`

	// Generator is a fingerprint of everything outside the sources that
	// determines what the generators produce: the CLI build version and each
	// generator's own fingerprint (its pinned toolchain image and options).
	Generator string `json:"generator,omitempty"`

	// Languages the schemas on disk were generated for, sorted.
	Languages []string `json:"languages,omitempty"`

	// Groups maps each source group name to the source ID to version map the
	// group was last generated from.
	Groups map[string]map[string]string `json:"groups"`
}

// groupFresh reports whether a group's cached output was generated from
// exactly these sources. An absent group -- a lock from before groups
// existed -- is never fresh.
func (l *lock) groupFresh(group string, sources map[string]string) bool {
	if l == nil {
		return false
	}
	return maps.Equal(l.Groups[group], sources)
}

// equal reports whether two locks record the same state.
func (l *lock) equal(o *lock) bool {
	return l.Version == o.Version &&
		l.Generator == o.Generator &&
		slices.Equal(l.Languages, o.Languages) &&
		maps.EqualFunc(l.Groups, o.Groups, maps.Equal)
}

// sourceMap builds the source ID to version map a freshness check compares
// against, from each source's recorded version.
func sourceMap(sources []Source, versions map[string]string) map[string]string {
	out := make(map[string]string, len(sources))
	for _, src := range sources {
		out[src.ID()] = versions[src.ID()]
	}
	return out
}

// readLock reads the persisted lock. A lock that cannot be read -- missing,
// unparseable, truncated by an interrupted write, or in a different format
// version -- reads as an empty lock, which means stale. The response to a
// lock we cannot trust is regeneration, not an error: every command that
// writes schemas can produce a fresh tree, so failing the build over the lock
// would only stand in the way of recovering from it.
func (m *Manager) readLock() *lock {
	bs, err := afero.ReadFile(m.fs, lockFileName)
	if err != nil {
		return &lock{}
	}

	var l lock
	if err := json.Unmarshal(bs, &l); err != nil {
		return &lock{}
	}

	if l.Version != lockVersion {
		return &lock{}
	}

	return &l
}

// writeLock persists the lock atomically -- a temp file first, then a rename
// -- so an interrupted write can never leave a truncated lock: it leaves a
// truncated temp file, which the next run overwrites and the lock itself
// stays the old, stale one.
func (m *Manager) writeLock(l *lock) error {
	bs, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return errors.Wrap(err, "failed to serialize schema lock")
	}
	// A trailing newline so the file ends cleanly and edits to the last entry
	// produce minimal diffs.
	bs = append(bs, '\n')

	if err := m.fs.MkdirAll("/", 0o750); err != nil {
		return errors.Wrap(err, "failed to ensure schema directory exists")
	}

	tmp := lockFileName + ".tmp"
	if err := afero.WriteFile(m.fs, tmp, bs, 0o600); err != nil {
		return errors.Wrap(err, "failed to write schema lock file")
	}
	if err := m.fs.Rename(tmp, lockFileName); err != nil {
		return errors.Wrap(err, "failed to put the schema lock file in place; the next build will regenerate the schemas")
	}
	return nil
}

// generatorFingerprint derives the lock's generator fingerprint from the
// configured generators. Each generator contributes its language and its own
// Fingerprint() -- its pinned toolchain image and options -- so any change
// that alters what a generator produces also changes the lock's fingerprint
// and invalidates the caches. m.languages() is sorted, so the parts are
// stable for the same generator set.
func (m *Manager) generatorFingerprint() string {
	parts := make([]string, 0, len(m.generators)+1)
	parts = append(parts, "cli="+crversion.New().GetVersionString())
	for _, g := range m.generators {
		parts = append(parts, g.Language()+"="+g.Fingerprint())
	}
	return hashParts(parts)
}

// hashParts returns a short hex digest of the given strings, each length-
// prefixed so no two different part lists can hash the same.
func hashParts(parts []string) string {
	h := sha256.New()
	var lenBuf [8]byte
	for _, p := range parts {
		binary.LittleEndian.PutUint64(lenBuf[:], uint64(len(p)))
		_, _ = h.Write(lenBuf[:])
		_, _ = h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}
