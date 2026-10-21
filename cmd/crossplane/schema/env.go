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

package schema

import (
	"github.com/spf13/afero"

	"github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/internal/config"
	"github.com/crossplane/cli/v2/internal/dependency"
	"github.com/crossplane/cli/v2/internal/git"
	"github.com/crossplane/cli/v2/internal/project"
	"github.com/crossplane/cli/v2/internal/project/projectfile"
	"github.com/crossplane/cli/v2/internal/schemas/manager"
)

// ProjectFlags are the flags every schema command needs to find the project.
type ProjectFlags struct {
	ProjectFile string `default:"${project_file}" help:"Path to project definition file." short:"f"`
}

// SourceFlags are the flags commands need to fetch the project's dependencies.
type SourceFlags struct {
	git.HTTPSAuthFlags `embed:""`

	CacheDir string `env:"CROSSPLANE_XPKG_CACHE" help:"Directory for cached xpkg package contents." name:"cache-dir"`
}

// env is everything a schema command works with: the project, its schema
// manager, and, for commands that fetch dependencies, its dependency manager.
type env struct {
	proj    *v1alpha1.Project
	projFS  afero.Fs
	schemas *manager.Manager
	deps    *dependency.Manager
}

// newEnv loads the project. The dependency manager is only set up if src is
// non-nil, so commands that work on the schema directory alone need no
// registry access.
func newEnv(projectFile string, cfg *config.Config, src *SourceFlags) (*env, error) {
	proj, projFS, err := projectfile.Load(projectFile)
	if err != nil {
		return nil, err
	}

	e := &env{
		proj:    proj,
		projFS:  projFS,
		schemas: project.NewSchemaManager(proj, projFS, cfg.Features),
	}
	if src == nil {
		return e, nil
	}

	e.deps, err = dependency.NewCachedManager(proj, projFS, projectFile, src.CacheDir,
		dependency.WithGitAuthProvider(src.AuthProvider()),
	)
	if err != nil {
		return nil, err
	}
	return e, nil
}
