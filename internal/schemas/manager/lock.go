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
	"encoding/hex"
	"slices"
)

const (
	lockFileName = ".lock.json"

	// lockVersion is the current lock file format. A lock with any other
	// version is ignored and overwritten on the next change.
	lockVersion = 1
)

// Flags are the generation options, beyond languages, that change the content
// of the generated files. They are recorded per source so that changing one
// regenerates the schemas it affects.
type Flags struct {
	GoRuntimeObjects bool `json:"goRuntimeObjects"`
	// APIGroups are the globs resources are filtered by, sorted. Empty means
	// no filtering.
	APIGroups []string `json:"apiGroups,omitempty"`
}

func (f Flags) equal(o Flags) bool {
	return f.GoRuntimeObjects == o.GoRuntimeObjects && slices.Equal(f.APIGroups, o.APIGroups)
}

// lock tracks the sources whose schemas are present in the manager and the
// files generated from each. It is persisted to the manager's filesystem.
type lock struct {
	LockVersion int `json:"lockVersion"`
	// DependenciesHash is the hash of the project's dependencies the lock was
	// last updated for. It lets Verify notice changed dependencies without the
	// network.
	DependenciesHash string                `json:"dependenciesHash,omitempty"`
	Sources          map[string]lockSource `json:"sources"`
}

// lockSource is the lock entry of a single source.
type lockSource struct {
	SourceHash string `json:"sourceHash"`
	// Pin is an immutable reference to fetch the source's content by, if the
	// source has one.
	Pin string `json:"pin,omitempty"`
	// Version is the resolved tag or ref, shown to people. It is optional.
	Version string `json:"version,omitempty"`
	Flags   Flags  `json:"flags"`
	// Languages are the sorted languages the source was generated for. They
	// are recorded because a language may produce no files for a source.
	Languages []string   `json:"languages"`
	Files     []lockFile `json:"files"`
}

// LockedSource is the read-only view of a source's lock entry.
type LockedSource struct {
	Pin        string
	Version    string
	SourceHash string
}

// lockFile is a generated file, identified by its slash-separated path
// relative to the managed directory, and the hash of its content.
type lockFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

func newLock() *lock {
	return &lock{
		LockVersion: lockVersion,
		Sources:     make(map[string]lockSource),
	}
}

// owned returns the set of file paths recorded by any source.
func (l *lock) owned() map[string]struct{} {
	owned := make(map[string]struct{})
	for _, src := range l.Sources {
		for _, f := range src.Files {
			owned[f.Path] = struct{}{}
		}
	}
	return owned
}

func hashContent(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
