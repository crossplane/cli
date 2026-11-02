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

// Package metafile reads and edits Crossplane package metadata files
// (crossplane.yaml) for dependency management.
package metafile

import (
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/spf13/afero"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"

	pkgmetav1 "github.com/crossplane/crossplane/apis/v2/pkg/meta/v1"
	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"

	"github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/internal/project/projectfile"
)

// defaultXpkgVersion is the version constraint used for package metadata
// dependencies that don't specify a version.
const defaultXpkgVersion = ">=v0.0.0"

// Parse parses Configuration, Provider, or Function package metadata of any
// supported apiVersion, converting it to its v1 form.
func Parse(fs afero.Fs, file string) (pkgmetav1.Pkg, error) {
	bs, err := afero.ReadFile(fs, file)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to read package metadata file %q", file)
	}

	scheme, err := xpkg.BuildMetaScheme()
	if err != nil {
		return nil, errors.Wrap(err, "cannot build package meta scheme")
	}
	obj, _, err := serializer.NewCodecFactory(scheme).UniversalDeserializer().Decode(bs, nil, nil)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to parse package metadata file %q", file)
	}
	pkg, ok := xpkg.TryConvertToPkg(obj, &pkgmetav1.Configuration{}, &pkgmetav1.Provider{}, &pkgmetav1.Function{})
	if !ok {
		return nil, errors.Errorf("%q is not Configuration, Provider, or Function package metadata", file)
	}
	return pkg, nil
}

// ToProject converts package metadata into a Project
// whose dependencies are the metadata's package dependencies.
//
// TODO: review, we might want this now, or postpone to a separate PR:
// This does a lazy conversion, only the fields needed for dependency mgmt are populated.
// Package annotations, spec.crossplane and capabilities are dropped.
// It can be extended to map those fields when a consumer needs them, potentially a
// `crossplane project init --from crossplane.yaml`, or the same with `crossplane project build`.
func ToProject(pkg pkgmetav1.Pkg) *v1alpha1.Project {
	proj := &v1alpha1.Project{}
	proj.APIVersion, proj.Kind = projectfile.APIVersion, projectfile.Kind
	proj.Name = pkg.GetName()

	for _, dep := range pkg.GetDependencies() {
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

// upsertDependency adds dep to the dependsOn list of the
// package metadata file, replacing any existing entry for the
// same package. The file is edited as generic YAML so that fields are not
// defaulted; like Update, formatting and comments are not preserved.
func upsertDependency(fs afero.Fs, file string, dep v1alpha1.XpkgDependency) error {
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
		if m, ok := existing.(map[string]any); ok && sameRepo(metaDependencyRepo(m), dep.Package) {
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

// sameRepo reports whether two package references name the same repository,
// ignoring any tag or digest. Unparseable references are compared verbatim.
func sameRepo(a, b string) bool {
	ra, errA := name.ParseReference(a)
	rb, errB := name.ParseReference(b)
	if errA != nil || errB != nil {
		return a == b
	}
	return ra.Context().Name() == rb.Context().Name()
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

// NewStore returns a Store that records dependencies in a package metadata file.
func NewStore(fs afero.Fs, file string) *Store {
	return &Store{fs: fs, file: file}
}

// Store records dependencies in the spec.dependsOn list of a package metadata
// file.
type Store struct {
	fs   afero.Fs
	file string
}

// Supports accepts only runtime xpkg dependencies, the only kind package
// metadata can express.
func (s *Store) Supports(dep v1alpha1.Dependency) error {
	if dep.Xpkg == nil || dep.Xpkg.APIOnly {
		return errors.New("package metadata files support only runtime xpkg dependencies; use a project file for k8s, CRD, or API-only dependencies")
	}
	return nil
}

// Upsert writes dep to the spec.dependsOn list of the metadata file.
func (s *Store) Upsert(dep v1alpha1.Dependency) error {
	return upsertDependency(s.fs, s.file, *dep.Xpkg)
}
