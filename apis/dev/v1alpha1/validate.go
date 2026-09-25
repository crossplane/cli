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

package v1alpha1

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

// Validate validates a project.
func (p *Project) Validate() error {
	var errs []error

	if p.GetName() == "" {
		errs = append(errs, errors.New("name must not be empty"))
	}
	errs = append(errs, p.Spec.Validate())

	return errors.Join(errs...)
}

// Validate validates a project's spec.
func (s *ProjectSpec) Validate() error {
	var errs []error

	if s.Repository == "" {
		errs = append(errs, errors.New("repository must not be empty"))
	}

	if s.Paths != nil {
		if s.Paths.APIs != "" && filepath.IsAbs(s.Paths.APIs) {
			errs = append(errs, errors.New("apis path must be relative"))
		}
		if s.Paths.Functions != "" && filepath.IsAbs(s.Paths.Functions) {
			errs = append(errs, errors.New("functions path must be relative"))
		}
		if s.Paths.Examples != "" && filepath.IsAbs(s.Paths.Examples) {
			errs = append(errs, errors.New("examples path must be relative"))
		}
		if s.Paths.Tests != "" && filepath.IsAbs(s.Paths.Tests) {
			errs = append(errs, errors.New("tests path must be relative"))
		}
		if s.Paths.Operations != "" && filepath.IsAbs(s.Paths.Operations) {
			errs = append(errs, errors.New("operations path must be relative"))
		}
		if s.Paths.Schemas != "" && filepath.IsAbs(s.Paths.Schemas) {
			errs = append(errs, errors.New("schemas path must be relative"))
		}
	}

	if s.Architectures != nil && len(s.Architectures) == 0 {
		errs = append(errs, errors.New("architectures must not be empty"))
	}

	errs = append(errs, s.Schemas.Validate()...)

	// Validate dependencies
	for i, dep := range s.Dependencies {
		if err := dep.Validate(); err != nil {
			errs = append(errs, errors.Wrapf(err, "dependency %d", i))
		}
	}

	// Validate functions. Names must be unique across the list, regardless of
	// source, since the function name is used to derive both the package
	// metadata name and the OCI repository.
	seen := make(map[string]int, len(s.Functions))
	for i, fn := range s.Functions {
		if err := fn.Validate(); err != nil {
			errs = append(errs, errors.Wrapf(err, "function %d", i))
			continue
		}
		name := fn.Name()
		if first, ok := seen[name]; ok {
			errs = append(errs, errors.Errorf("function %d: name %q is already used by function %d", i, name, first))
			continue
		}
		seen[name] = i
	}

	return errors.Join(errs...)
}

// Validate returns errors for an invalid ProjectSchemas. A nil receiver is
// valid (it means "generate schemas for DefaultSchemaLanguages()"); an
// explicitly empty Languages list is rejected because it would disable all
// schema generation, which is almost certainly a mistake.
func (s *ProjectSchemas) Validate() []error {
	if s == nil {
		return nil
	}
	if s.Languages == nil {
		return nil
	}
	if len(s.Languages) == 0 {
		return []error{errors.New("schemas.languages must not be empty when specified")}
	}
	supported := SupportedSchemaLanguages()
	var errs []error
	for i, lang := range s.Languages {
		if !slices.Contains(supported, lang) {
			errs = append(errs, errors.Errorf("schemas.languages[%d]: %q is not a supported schema language; set it to one of %v", i, lang, supported))
		}
	}
	return errs
}

// Validate validates a dependency.
func (d *Dependency) Validate() error {
	var errs []error

	if d.Type == "" {
		errs = append(errs, errors.New("type must not be empty"))
	}

	// Count non-nil sources
	sourceCount := 0
	if d.Xpkg != nil {
		sourceCount++
		if err := d.Xpkg.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("xpkg: %w", err))
		}
	}
	if d.Git != nil {
		sourceCount++
		if err := d.Git.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("git: %w", err))
		}
	}
	if d.HTTP != nil {
		sourceCount++
		if err := d.HTTP.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("http: %w", err))
		}
	}
	if d.K8s != nil {
		sourceCount++
		if err := d.K8s.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("k8s: %w", err))
		}
	}

	if sourceCount != 1 {
		errs = append(errs, errors.New("exactly one source (xpkg, git, http, or k8s) must be specified"))
	}

	return errors.Join(errs...)
}

// Validate validates an xpkg dependency.
func (x *XpkgDependency) Validate() error {
	var errs []error

	if x.APIVersion == "" {
		errs = append(errs, errors.New("apiVersion must not be empty"))
	}
	if x.Kind == "" {
		errs = append(errs, errors.New("kind must not be empty"))
	}
	if x.Package == "" {
		errs = append(errs, errors.New("package must not be empty"))
	}
	if x.Version == "" {
		errs = append(errs, errors.New("version must not be empty"))
	}

	return errors.Join(errs...)
}

// Validate validates a git dependency.
func (g *GitDependency) Validate() error {
	var errs []error

	if g.Repository == "" {
		errs = append(errs, errors.New("repository must not be empty"))
	}

	return errors.Join(errs...)
}

// Validate validates an HTTP dependency.
func (h *HTTPDependency) Validate() error {
	var errs []error

	if h.URL == "" {
		errs = append(errs, errors.New("url must not be empty"))
	}

	return errors.Join(errs...)
}

// Validate validates a Kubernetes API dependency.
func (k *K8sDependency) Validate() error {
	var errs []error

	if k.Version == "" {
		errs = append(errs, errors.New("version must not be empty"))
	}

	return errors.Join(errs...)
}

// Validate validates a Function declaration.
func (f *Function) Validate() error {
	var errs []error

	// Count non-nil sources to enforce that exactly one matches the
	// discriminator.
	sourceCount := 0
	if f.Directory != nil {
		sourceCount++
	}
	if f.Tarball != nil {
		sourceCount++
	}
	if sourceCount != 1 {
		errs = append(errs, errors.New("exactly one source (directory or tarball) must be specified"))
	}

	switch f.Source {
	case FunctionSourceDirectory:
		if err := f.Directory.Validate(); err != nil {
			errs = append(errs, errors.Wrap(err, "directory"))
		}
	case FunctionSourceTarball:
		if err := f.Tarball.Validate(); err != nil {
			errs = append(errs, errors.Wrap(err, "tarball"))
		}
	case "":
		errs = append(errs, errors.New("source must not be empty"))
	default:
		errs = append(errs, errors.Errorf("source %q is not supported, must be one of %q or %q", f.Source, FunctionSourceDirectory, FunctionSourceTarball))
	}

	return errors.Join(errs...)
}

// Validate validates a FunctionDirectory. A nil receiver is invalid; this is
// the failure mode when a function is declared with source Directory but no
// directory field set.
func (d *FunctionDirectory) Validate() error {
	if d == nil {
		return errors.Errorf("source %q requires the directory field to be set", FunctionSourceDirectory)
	}

	var errs []error
	if d.Name == "" {
		errs = append(errs, errors.New("name must not be empty"))
	} else if msgs := validation.IsDNS1123Subdomain(d.Name); len(msgs) > 0 {
		errs = append(errs, errors.Errorf("name %q is not a valid function name: %s", d.Name, strings.Join(msgs, "; ")))
	}

	return errors.Join(errs...)
}

// Validate validates a FunctionTarball. A nil receiver is invalid; this is
// the failure mode when a function is declared with source Tarball but no
// tarball field set.
func (t *FunctionTarball) Validate() error {
	if t == nil {
		return errors.Errorf("source %q requires the tarball field to be set", FunctionSourceTarball)
	}

	var errs []error
	if t.Name == "" {
		errs = append(errs, errors.New("name must not be empty"))
	} else if msgs := validation.IsDNS1123Subdomain(t.Name); len(msgs) > 0 {
		errs = append(errs, errors.Errorf("name %q is not a valid function name: %s", t.Name, strings.Join(msgs, "; ")))
	}
	if t.PathPrefix == "" {
		errs = append(errs, errors.New("pathPrefix must not be empty"))
	} else if filepath.IsAbs(t.PathPrefix) {
		errs = append(errs, errors.New("pathPrefix must be relative"))
	}

	return errors.Join(errs...)
}
