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

// Package dependency contains commands for managing project dependencies.
package dependency

import (
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	pkgmetav1 "github.com/crossplane/crossplane/apis/v2/pkg/meta/v1"

	"github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/internal/dependency"
	"github.com/crossplane/cli/v2/internal/project/metafile"
	"github.com/crossplane/cli/v2/internal/project/projectfile"
)

// Cmd contains commands for dependency management.
type Cmd struct {
	Add         addCmd         `cmd:"" help:"Add a dependency to the current project."                        novale:"Google.WordList[\"k8s\"],Google.Units,gitlab.Units"`
	UpdateCache updateCacheCmd `cmd:"" help:"Update the dependency cache for the current project or package."`
	CleanCache  cleanCacheCmd  `cmd:"" help:"Clean the dependency cache."`
}

// loadProject finds and loads the project or package metadata file at path,
// probing the working directory when path is empty.
// projFS is rooted at the file's directory; store writes back to the file.
func loadProject(path string, o Overrides) (proj *v1alpha1.Project, store dependency.Store, projFS afero.Fs, err error) {
	abs, err := projectfile.Resolve(path)
	if err != nil {
		return nil, nil, nil, err
	}
	projFS = afero.NewBasePathFs(afero.NewOsFs(), filepath.Dir(abs))

	proj, store, err = load(projFS, filepath.Base(abs), o)
	if err != nil {
		return nil, nil, nil, err
	}
	return proj, store, projFS, nil
}

// Overrides are settings supplied on the command line that take precedence over the file.
// They exist mainly because package metadata can't express them, but they apply to project files too.
// The file on disk is never changed.
type Overrides struct {
	// SchemasDir overrides paths.schemas. It must be a relative subdirectory of the file's directory.
	SchemasDir string
	// SchemaLanguages overrides schemas.languages. Each must be a supported schema language.
	SchemaLanguages []string
}

// load parses either a project file or a package metadata file (crossplane.yaml),
// applies the overrides, and returns a Project with defaults applied, plus the
// Store that writes dependencies back to the file.
// A metadata file is converted into an in-memory Project whose dependencies are the
// metadata package dependencies.
func load(fs afero.Fs, file string, o Overrides) (*v1alpha1.Project, dependency.Store, error) {
	// The schemas directory is removed by clean-cache, so it must be a strict
	// subdirectory of the project directory.
	if o.SchemasDir != "" && (!filepath.IsLocal(o.SchemasDir) || filepath.Clean(o.SchemasDir) == ".") {
		return nil, nil, errors.Errorf("schemas directory %q must be a relative subdirectory of the project directory", o.SchemasDir)
	}

	isProject, err := projectfile.IsProjectFile(fs, file)
	if err != nil {
		return nil, nil, err
	}

	var proj *v1alpha1.Project
	var store dependency.Store
	if isProject {
		proj, err = projectfile.ParseWithoutDefaults(fs, file)
		store = projectfile.NewStore(fs, file)
	} else {
		var pkg pkgmetav1.Pkg
		pkg, err = metafile.Parse(fs, file)
		if err == nil {
			proj = metafile.ToProject(pkg)
		}
		store = metafile.NewStore(fs, file)
	}
	if err != nil {
		return nil, nil, err
	}

	if o.SchemasDir != "" {
		if proj.Spec.Paths == nil {
			proj.Spec.Paths = &v1alpha1.ProjectPaths{}
		}
		proj.Spec.Paths.Schemas = o.SchemasDir
	}
	if len(o.SchemaLanguages) > 0 {
		if proj.Spec.Schemas == nil {
			proj.Spec.Schemas = &v1alpha1.ProjectSchemas{}
		}
		proj.Spec.Schemas.Languages = o.SchemaLanguages
		if err := errors.Join(proj.Spec.Schemas.Validate()...); err != nil {
			return nil, nil, err
		}
	}
	proj.Default()

	return proj, store, nil
}
