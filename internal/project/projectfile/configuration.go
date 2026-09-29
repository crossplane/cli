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
	"os"
	"path/filepath"

	"github.com/spf13/afero"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	runtimexpkg "github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"

	pkgmetav1 "github.com/crossplane/crossplane/apis/v2/pkg/meta/v1"
	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"

	"github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"
)

// defaultXpkgVersion is the version constraint used for package metadata
// dependencies that don't specify a version.
const defaultXpkgVersion = ">=v0.0.0"

// Resolve returns the absolute path of the project or package metadata file to
// use. An explicit path is returned as-is (made absolute). Otherwise it probes
// for crossplane-project.yaml and then crossplane.yaml in the working
// directory.
func Resolve(path string) (string, error) {
	if path != "" {
		return filepath.Abs(path)
	}

	for _, name := range []string{clixpkg.ProjectFile, runtimexpkg.MetaFile} {
		abs, err := filepath.Abs(name)
		if err != nil {
			return "", errors.Wrapf(err, "cannot determine path for %q", name)
		}
		if _, err := os.Stat(abs); err == nil {
			return abs, nil
		}
	}

	return "", errors.Errorf("neither %s nor %s found in the current directory", clixpkg.ProjectFile, runtimexpkg.MetaFile)
}

// Load parses either a project file or a Configuration package metadata file (crossplane.yaml),
// returning a Project with defaults applied.
// A metadata file is converted into an in-memory Project whose dependencies are the
// metadata's package dependencies. If schemasDir is non-empty it overrides the
// project's schemas path; it must be relative and must not escape the file's
// directory.
func Load(fs afero.Fs, file, schemasDir string) (*v1alpha1.Project, error) {
	// The schemas directory is removed by clean-cache, so it must be a strict
	// subdirectory of the project directory.
	if schemasDir != "" && (!filepath.IsLocal(schemasDir) || filepath.Clean(schemasDir) == ".") {
		return nil, errors.Errorf("schemas directory %q must be a relative subdirectory of the project directory", schemasDir)
	}

	isProject, err := IsProjectFile(fs, file)
	if err != nil {
		return nil, err
	}

	var proj *v1alpha1.Project
	if isProject {
		proj, err = ParseWithoutDefaults(fs, file)
	} else {
		var cfg *pkgmetav1.Configuration
		cfg, err = clixpkg.ParseConfiguration(fs, file)
		if err == nil {
			proj = FromConfiguration(cfg)
		}
	}
	if err != nil {
		return nil, err
	}

	if schemasDir != "" {
		if proj.Spec.Paths == nil {
			proj.Spec.Paths = &v1alpha1.ProjectPaths{}
		}
		proj.Spec.Paths.Schemas = schemasDir
	}
	proj.Default()

	return proj, nil
}

// FromConfiguration converts Configuration package metadata into a Project
// whose dependencies are the metadata's package dependencies.
//
// TODO: review, we might want this now, or postpone to a separate PR:
// This does a lazy conversion, only the fields needed for dependency mgmt are populated.
// Package annotations, spec.crossplane and capabilities are dropped.
// It can be extended to map those fields when a consumer needs them, potentially a
// `crossplane project init --from crossplane.yaml`, or the same with `crossplane project build`.
func FromConfiguration(cfg *pkgmetav1.Configuration) *v1alpha1.Project {
	proj := &v1alpha1.Project{}
	proj.APIVersion, proj.Kind = APIVersion, Kind
	proj.Name = cfg.Name

	for _, dep := range cfg.Spec.DependsOn {
		x, ok := xpkgDependency(dep)
		if !ok {
			continue
		}
		proj.Spec.Dependencies = append(proj.Spec.Dependencies, v1alpha1.Dependency{
			Type: v1alpha1.DependencyTypeXpkg,
			Xpkg: x,
		})
	}

	return proj
}

// UpsertConfigurationDependency adds dep to the dependsOn list of the
// Configuration package metadata file, replacing any existing entry for the
// same package. The file is edited as generic YAML so that fields are not
// defaulted; like Update, formatting and comments are not preserved.
func UpsertConfigurationDependency(fs afero.Fs, file string, dep v1alpha1.XpkgDependency) error {
	bs, err := afero.ReadFile(fs, file)
	if err != nil {
		return errors.Wrapf(err, "failed to read package metadata file %q", file)
	}

	var doc map[string]any
	if err := yaml.Unmarshal(bs, &doc); err != nil {
		return errors.Wrap(err, "failed to parse package metadata file")
	}

	spec, _ := doc["spec"].(map[string]any)
	if spec == nil {
		spec = map[string]any{}
		doc["spec"] = spec
	}

	entry := map[string]any{"package": dep.Package, "version": dep.Version}
	if dep.APIVersion != "" {
		entry["apiVersion"] = dep.APIVersion
	}
	if dep.Kind != "" {
		entry["kind"] = dep.Kind
	}

	deps, _ := spec["dependsOn"].([]any)
	replaced := false
	for i, existing := range deps {
		if m, ok := existing.(map[string]any); ok && metaDependencyRepo(m) == dep.Package {
			deps[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		deps = append(deps, entry)
	}
	spec["dependsOn"] = deps

	out, err := yaml.Marshal(doc)
	if err != nil {
		return errors.Wrap(err, "failed to marshal package metadata")
	}
	return afero.WriteFile(fs, file, out, 0o644)
}

// metaDependencyRepo returns the package repository of a generic dependsOn
// entry, handling both the modern and deprecated field names.
func metaDependencyRepo(m map[string]any) string {
	for _, k := range []string{"package", "provider", "configuration", "function"} {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// xpkgDependency converts a package metadata dependency into an xpkg project
// dependency. It returns false if the dependency names no package.
func xpkgDependency(dep pkgmetav1.Dependency) (*v1alpha1.XpkgDependency, bool) {
	x := &v1alpha1.XpkgDependency{
		APIVersion: pkgv1.SchemeGroupVersion.String(),
		Version:    dep.Version,
	}

	switch {
	case dep.Package != nil:
		x.Package = *dep.Package
		if dep.APIVersion != nil {
			x.APIVersion = *dep.APIVersion
		}
		if dep.Kind != nil {
			x.Kind = *dep.Kind
		}
	case dep.Provider != nil:
		x.Package, x.Kind = *dep.Provider, pkgv1.ProviderKind
	case dep.Configuration != nil:
		x.Package, x.Kind = *dep.Configuration, pkgv1.ConfigurationKind
	case dep.Function != nil:
		x.Package, x.Kind = *dep.Function, pkgv1.FunctionKind
	default:
		return nil, false
	}

	if x.Version == "" {
		x.Version = defaultXpkgVersion
	}
	return x, true
}
