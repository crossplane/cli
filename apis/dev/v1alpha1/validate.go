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
	"errors"
	"fmt"
	"path/filepath"
)

const (
	// AdditionalMetadataKeyPrefix is the required prefix for all additional metadata keys.
	AdditionalMetadataKeyPrefix = "meta.upbound.io/"
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

	// Validate dependencies
	for i, dep := range s.Dependencies {
		if err := dep.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("dependency %d: %w", i, err))
		}
	}

	return errors.Join(errs...)
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
