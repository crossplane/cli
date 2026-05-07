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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pkgmetav1 "github.com/crossplane/crossplane/apis/v2/pkg/meta/v1"
	pkgv1beta1 "github.com/crossplane/crossplane/apis/v2/pkg/v1beta1"
)

// Dependency type constants.
const (
	// DependencyTypeK8s represents Kubernetes API dependencies.
	DependencyTypeK8s = "k8s"
	// DependencyTypeCRD represents Custom Resource Definition dependencies.
	DependencyTypeCRD = "crd"
	// DependencyTypeXpkg represents Crossplane package dependencies.
	DependencyTypeXpkg = "xpkg"
)

// Project defines a Crossplane Project, which can be built into a Crossplane
// Configuration package.
//
// +kubebuilder:object:root=true
type Project struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ProjectSpec `json:"spec"`
}

// ProjectSpec is the spec for a Project. Since a Project is not a Kubernetes
// resource there is no Status, only Spec.
type ProjectSpec struct {
	ProjectPackageMetadata `json:",inline"`

	// Repository is the OCI repository to which the configuration package built
	// from this project will be pushed. It is also used to form the OCI
	// repository paths for embedded functions in the project by appending an
	// underscore and the function name. The repository can be overridden at
	// build time, but the repository used for build and push must match in
	// order for dependencies on embedded functions to resolve correctly.
	Repository string `json:"repository"`

	// Crossplane defines the Crossplane version constraints for the
	// configuration package built from the project. If not specified, the
	// constraint will be '>=v2.0.0-rc.0' such that the packages support any
	// Crossplane 2.x release.
	Crossplane *pkgmetav1.CrossplaneConstraints `json:"crossplane,omitempty"`
	// Dependencies are built-time and runtime dependencies of the project.
	Dependencies []Dependency `json:"dependencies,omitempty"`
	// Paths defines the relative paths to various parts of the project.
	Paths *ProjectPaths `json:"paths,omitempty"`
	// Architectures indicates for which architectures embedded functions should
	// be built. If not specified, it defaults to [amd64, arm64].
	Architectures []string `json:"architectures,omitempty"`
	// ImageConfigs configure how images are fetched during
	// development. Currently, only rewriting is supported; other options will
	// be silently ignored. Note that these configs are for development only;
	// any necessary ImageConfigs for deployment into a cluster must be created
	// separately at deployment time.
	ImageConfigs []pkgv1beta1.ImageConfig `json:"imageConfigs,omitempty"`
}

// ProjectPackageMetadata holds metadata about the project, which will become
// package metadata when a project is built into a Crossplane package.
type ProjectPackageMetadata struct {
	Maintainer  string `json:"maintainer,omitempty"`
	Source      string `json:"source,omitempty"`
	License     string `json:"license,omitempty"`
	Description string `json:"description,omitempty"`
	Readme      string `json:"readme,omitempty"`
}

// ProjectPaths configures the locations of various parts of the project, for
// use at build time. All paths must be relative to the project root.
type ProjectPaths struct {
	// APIs is the directory holding the project's apis (XRDs and
	// compositions). If not specified, it defaults to `apis/`.
	APIs string `json:"apis,omitempty"`
	// Functions is the directory holding the project's functions. If not
	// specified, it defaults to `functions/`.
	Functions string `json:"functions,omitempty"`
	// Examples is the directory holding the project's examples. If not
	// specified, it defaults to `examples/`.
	Examples string `json:"examples,omitempty"`
	// Tests is the directory holding the project's tests. If not
	// specified, it defaults to `tests/`.
	Tests string `json:"tests,omitempty"`
	// Operations is the directory holding the project's operations. If not
	// specified, it defaults to `operations/`.
	Operations string `json:"operations,omitempty"`
	// Schemas is the directory holding language bindings for the project's XRDs
	// and dependencies. If not specified, it defaults to `schemas/`.
	Schemas string `json:"schemas,omitempty"`
}

// Dependency defines a dependency for a Crossplane project. The Type field
// determines which sub-fields are relevant.
type Dependency struct {
	// Type defines the type of dependency.
	// +kubebuilder:validation:Enum=k8s;crd;xpkg
	Type string `json:"type"`

	// Xpkg defines the Crossplane package reference for the dependency.
	// Only used when Type is "xpkg".
	// +optional
	Xpkg *XpkgDependency `json:"xpkg,omitempty"`

	// Git defines the git repository source for the dependency.
	// Only used when Type is "crd".
	// +optional
	Git *GitDependency `json:"git,omitempty"`

	// HTTP defines the HTTP source for the dependency.
	// Only used when Type is "crd".
	// +optional
	HTTP *HTTPDependency `json:"http,omitempty"`

	// K8s defines the Kubernetes API version for the dependency.
	// Only used when Type is "k8s".
	// +optional
	K8s *K8sDependency `json:"k8s,omitempty"`
}

// XpkgDependency defines the xpkg-specific fields for a package dependency.
type XpkgDependency struct {
	// APIVersion of the dependency package. This should be the package
	// apiVersion (e.g., pkg.crossplane.io/v1), not the package metadata type.
	APIVersion string `json:"apiVersion"`

	// Kind of the dependency package.
	Kind string `json:"kind"`

	// Package is the OCI image reference of the dependency package.
	Package string `json:"package"`

	// Version is the semantic version constraints for the dependency.
	Version string `json:"version"`

	// APIOnly indicates that this dependency is only needed for API/schema
	// purposes and should not be included as a runtime dependency in the
	// built package. Only xpkg dependencies can be runtime dependencies.
	// Default is false, meaning xpkg dependencies are runtime by default.
	// +optional
	APIOnly bool `json:"apiOnly,omitempty"`
}

// GitDependency defines a git repository source for an API dependency.
type GitDependency struct {
	// Repository is the git repository URL.
	Repository string `json:"repository"`

	// Ref is the git reference (branch, tag, or commit SHA).
	// +optional
	Ref string `json:"ref,omitempty"`

	// Path is the path within the repository to the API definition.
	// +optional
	Path string `json:"path,omitempty"`
}

// HTTPDependency defines an HTTP source for an API dependency.
type HTTPDependency struct {
	// URL is the HTTP/HTTPS URL to fetch the API dependency from.
	URL string `json:"url"`
}

// K8sDependency defines a Kubernetes API version reference.
type K8sDependency struct {
	// Version is the Kubernetes API version (e.g., "v1.33.0").
	Version string `json:"version"`
}
