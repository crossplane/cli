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

package projectfile

import (
	"github.com/spf13/afero"

	"github.com/crossplane/cli/v2/apis/dev/v1alpha1"
)

// Store records dependencies in a project file.
type Store struct {
	fs   afero.Fs
	file string
}

// NewStore returns a Store backed by the project file.
func NewStore(fs afero.Fs, file string) *Store {
	return &Store{fs: fs, file: file}
}

// Supports accepts every dependency type.
func (s *Store) Supports(_ v1alpha1.Dependency) error { return nil }

// Upsert writes dep to the project file.
func (s *Store) Upsert(dep v1alpha1.Dependency) error {
	return Update(s.fs, s.file, func(p *v1alpha1.Project) {
		UpsertDependency(p, dep)
	})
}

// UpsertDependency adds dep to the project's dependencies, replacing any
// existing entry for the same source.
func UpsertDependency(proj *v1alpha1.Project, dep v1alpha1.Dependency) {
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
