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

// Package projectfile reads and writes Crossplane project files.
package projectfile

import (
	"github.com/spf13/afero"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	"github.com/crossplane/cli/v2/apis/dev/v1alpha1"
)

const (
	// APIVersion is the supported API version for project files.
	APIVersion = "dev.crossplane.io/v1alpha1"
	// Kind is the supported Kind for project files.
	Kind = "Project"
)

// Parse parses and validates the project file, returning a Project with
// defaults applied.
func Parse(projFS afero.Fs, projFilePath string) (*v1alpha1.Project, error) {
	proj, err := ParseWithoutDefaults(projFS, projFilePath)
	if err != nil {
		return nil, err
	}

	proj.Default()

	return proj, nil
}

// ParseWithoutDefaults parses and validates the project file without applying
// defaults. Use this when reading a project file that will be modified and
// written back, to avoid persisting default values the user omitted.
func ParseWithoutDefaults(projFS afero.Fs, projFilePath string) (*v1alpha1.Project, error) {
	bs, err := afero.ReadFile(projFS, projFilePath)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to read project file %q", projFilePath)
	}

	var tm metav1.TypeMeta
	if err := yaml.Unmarshal(bs, &tm); err != nil {
		return nil, errors.Wrap(err, "failed to parse project file")
	}

	if tm.APIVersion != APIVersion {
		return nil, errors.Errorf("unsupported project apiVersion %q, expected %q", tm.APIVersion, APIVersion)
	}
	if tm.Kind != Kind {
		return nil, errors.Errorf("unsupported project kind %q, expected %q", tm.Kind, Kind)
	}

	var proj v1alpha1.Project
	if err := yaml.Unmarshal(bs, &proj); err != nil {
		return nil, errors.Wrap(err, "failed to parse project file")
	}

	if err := proj.Validate(); err != nil {
		return nil, errors.Wrap(err, "invalid project file")
	}

	return &proj, nil
}

// Update reads a project file without applying defaults, applies the given
// mutation function, and writes the result back. This allows the project to be
// updated on disk without injecting defaults. Note that the file will be
// reformatted by the YAML serializer (fields will be re-ordered and comments
// will be lost).
func Update(projFS afero.Fs, projFile string, fn func(*v1alpha1.Project)) error {
	proj, err := ParseWithoutDefaults(projFS, projFile)
	if err != nil {
		return err
	}
	fn(proj)
	bs, err := yaml.Marshal(proj)
	if err != nil {
		return errors.Wrap(err, "failed to marshal project")
	}
	return afero.WriteFile(projFS, projFile, bs, 0o644)
}
