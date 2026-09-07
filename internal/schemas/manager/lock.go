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

const lockFileName = ".lock.json"

// lock tracks the versions of sources whose schemas are present in the
// manager, and the languages those schemas were generated for. It is persisted
// to the manager's filesystem.
type lock struct {
	// Languages the schemas on disk were generated for, sorted.
	//
	// Adding a language leaves every source version untouched, so without this
	// nothing would notice: the sources would all look current and generation
	// would be skipped, leaving the new language with no schemas. An absent
	// value reads as a mismatch, so a lock written before this field existed
	// regenerates once.
	Languages []string `json:"languages,omitempty"`

	// FromMergedPass records that the language directories currently hold the
	// output of a merged generation pass over exactly the sources in Packages.
	//
	// Packages serves two callers. The merged pass replaces the whole map,
	// while Add and Generate write single entries into it. Without this field,
	// a single-source write leaves every recorded version matching its source
	// while the tree on disk is the one that write produced: copying is
	// write-over, so a language whose index is a single file enumerating every
	// group loses the groups the merged pass had put there. The next merged
	// pass would then read as fresh and write nothing, and the only way back
	// would be deleting this file by hand.
	//
	// Only recordGeneration sets it; any single-source write clears it. An
	// absent value reads as "not a merged tree", so a lock written before this
	// field existed regenerates once.
	FromMergedPass bool `json:"fromMergedPass,omitempty"`

	Packages map[string]string `json:"packages"`
}

func newLock() *lock {
	return &lock{
		Packages: make(map[string]string),
	}
}
