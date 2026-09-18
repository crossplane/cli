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
// manager, and the languages those schemas were generated for.
type lock struct {
	// Languages the schemas on disk were generated for, sorted. Adding a
	// language leaves every source version untouched, so without this nothing
	// would notice it had been added.
	Languages []string `json:"languages,omitempty"`

	// FromMergedPass records that the language directories hold the output of a
	// merged pass over exactly the sources in Packages. Set only by
	// recordGeneration and cleared by any single-source write, which overwrites
	// part of that tree while leaving every recorded version matching.
	FromMergedPass bool `json:"fromMergedPass,omitempty"`

	Packages map[string]string `json:"packages"`
}

func newLock() *lock {
	return &lock{
		Packages: make(map[string]string),
	}
}
