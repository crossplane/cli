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

package xpkg

import (
	"context"
	"fmt"
	"path"

	"github.com/spf13/afero"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	pkgmetav1 "github.com/crossplane/crossplane/apis/v2/pkg/meta/v1"
	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"
)

// ProjectFile is the conventional name for a Crossplane project definition
// file (crossplane-project.yaml).
const ProjectFile = "crossplane-project.yaml"

// ParseConfiguration parses a Configuration package metadata file and returns the Configuration.
func ParseConfiguration(fs afero.Fs, filePath string) (*pkgmetav1.Configuration, error) {
	bs, err := afero.ReadFile(fs, filePath)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to read configuration file %q", filePath)
	}

	var cfg pkgmetav1.Configuration
	if err := yaml.Unmarshal(bs, &cfg); err != nil {
		return nil, errors.Wrap(err, "failed to parse configuration file")
	}

	wantAPIVersion := pkgmetav1.SchemeGroupVersion.String()
	if cfg.APIVersion != wantAPIVersion {
		return nil, errors.Errorf("unsupported configuration apiVersion %q, expected %q", cfg.APIVersion, wantAPIVersion)
	}
	if cfg.Kind != pkgmetav1.ConfigurationKind {
		return nil, errors.Errorf("unsupported configuration kind %q, expected %q", cfg.Kind, pkgmetav1.ConfigurationKind)
	}

	return &cfg, nil
}

// ResolveConfigurationFunctions extracts Function dependencies from a Configuration and resolves
// their version constraints to concrete OCI references.
func ResolveConfigurationFunctions(ctx context.Context, cfg *pkgmetav1.Configuration, resolver *Resolver) ([]pkgv1.Function, error) {
	fns := make([]pkgv1.Function, 0, len(cfg.Spec.DependsOn))
	for _, dep := range cfg.Spec.DependsOn {
		ref, ok := functionDepRef(dep)
		if !ok {
			continue
		}

		if dep.Version != "" {
			ref = fmt.Sprintf("%s:%s", ref, dep.Version)
		}

		resolved, _, err := resolver.Resolve(ctx, ref)
		if err != nil {
			return nil, errors.Wrapf(err, "cannot resolve function dependency %q", ref)
		}

		fns = append(fns, pkgv1.Function{
			ObjectMeta: metav1.ObjectMeta{
				Name: path.Base(resolved.Context().RepositoryStr()),
			},
			Spec: pkgv1.FunctionSpec{
				PackageSpec: pkgv1.PackageSpec{
					Package: resolved.Name(),
				},
			},
		})
	}

	return fns, nil
}

// functionDepRef returns the OCI image ref for a function dependency,
// handling both the modern style (APIVersion + Kind + Package) and the
// deprecated style (Function field).
func functionDepRef(dep pkgmetav1.Dependency) (string, bool) {
	if dep.Kind != nil && *dep.Kind == pkgv1.FunctionKind && dep.Package != nil {
		return *dep.Package, true
	}
	if dep.Function != nil {
		return *dep.Function, true
	}
	return "", false
}
