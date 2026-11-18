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

package dependency

import (
	"strings"

	"github.com/google/go-containerregistry/pkg/name"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	"github.com/crossplane/cli/v2/apis/dev/v1alpha1"
)

// ParseSource builds a dependency from a source string: an xpkg OCI reference,
// k8s:<version>, an HTTP(S) URL, or a git repository URL when gitRef is set.
func ParseSource(source, gitRef, gitPath string) (v1alpha1.Dependency, error) {
	if version, found := strings.CutPrefix(source, "k8s:"); found {
		if version == "" {
			return v1alpha1.Dependency{}, errors.New("k8s version is required (e.g., k8s:v1.33.0)")
		}
		return v1alpha1.Dependency{
			Type: v1alpha1.DependencyTypeK8s,
			K8s:  &v1alpha1.K8sDependency{Version: version},
		}, nil
	}

	if gitRef != "" {
		if source == "" {
			return v1alpha1.Dependency{}, errors.New("repository URL is required for git-based CRD dependencies")
		}
		return v1alpha1.Dependency{
			Type: v1alpha1.DependencyTypeCRD,
			Git: &v1alpha1.GitDependency{
				Repository: source,
				Ref:        gitRef,
				Path:       gitPath,
			},
		}, nil
	}

	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		return v1alpha1.Dependency{
			Type: v1alpha1.DependencyTypeCRD,
			HTTP: &v1alpha1.HTTPDependency{URL: source},
		}, nil
	}

	// Default: xpkg dependency. We allow three formats:
	//
	// 1. registry.example.com/repo:<tag / semver constraint>
	// 2. registry.example.com/repo@<digest>
	// 3. registry.example.com/repo (implies a semver constraint of '>=v0.0.0').
	pkg := source
	version := ">=v0.0.0"
	if ref, err := name.NewDigest(source, name.StrictValidation); err == nil {
		pkg = ref.Context().String()
		version = ref.DigestStr()
	} else if repo, tag, ok := strings.Cut(source, ":"); ok {
		// NOTE(adamwg): This doesn't work properly if the dependency has a
		// colon in the registry part for a port number (e.g.,
		// example.com:5000/my-repo:v1.2.3). But there's no easy way to handle
		// that correctly in all cases (since we also allow
		// `example.com:5000/my-repo, with no tag/constraint), so leave the
		// corner case unhandled for now.
		pkg = repo
		version = tag
	}

	return v1alpha1.Dependency{
		Type: v1alpha1.DependencyTypeXpkg,
		Xpkg: &v1alpha1.XpkgDependency{
			Package: pkg,
			Version: version,
		},
	}, nil
}
