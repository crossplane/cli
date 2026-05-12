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

package project

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/daemon"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"

	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"

	devv1alpha1 "github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/internal/docker"
)

// A Resolver resolves a CLI-style package reference to an OCI reference.
type Resolver interface {
	ResolveRef(ref string) (name.Reference, error)
}

// LoadFunctionDependencies loads function manifests from a project's
// dependencies, resolving version constraints using the provided resolver.
func LoadFunctionDependencies(resolver Resolver, proj *devv1alpha1.Project) ([]pkgv1.Function, error) {
	fns := make([]pkgv1.Function, 0, len(proj.Spec.Dependencies))
	for _, dep := range proj.Spec.Dependencies {
		if dep.Type != devv1alpha1.DependencyTypeXpkg {
			continue
		}
		if dep.Xpkg == nil || dep.Xpkg.APIOnly {
			continue
		}

		var ref string
		if _, err := v1.NewHash(dep.Xpkg.Version); err == nil {
			ref = fmt.Sprintf("%s@%s", dep.Xpkg.Package, dep.Xpkg.Version)
		} else {
			ref = fmt.Sprintf("%s:%s", dep.Xpkg.Package, dep.Xpkg.Version)
		}

		resolved, err := resolver.ResolveRef(ref)
		if err != nil {
			return nil, errors.Wrapf(err, "cannot resolve function dependency %s", ref)
		}

		f := pkgv1.Function{
			ObjectMeta: metav1.ObjectMeta{
				Name: xpkg.ToDNSLabel(resolved.Context().RepositoryStr()),
			},
			Spec: pkgv1.FunctionSpec{
				PackageSpec: pkgv1.PackageSpec{
					Package: resolved.Name(),
				},
			},
		}
		fns = append(fns, f)
	}

	return fns, nil
}

// EmbeddedFunctionsToDaemon loads each compatible image in the ImageTagMap into
// the Docker daemon and returns Function manifests.
func EmbeddedFunctionsToDaemon(ctx context.Context, imageMap ImageTagMap) ([]pkgv1.Function, error) {
	targetArch := getDockerDaemonArchitecture(ctx)

	fns := make([]pkgv1.Function, 0, len(imageMap))
	for tag, img := range imageMap {
		cfgFile, err := img.ConfigFile()
		if err != nil {
			return nil, errors.Wrapf(err, "cannot get platform info for image %s", tag)
		}

		if cfgFile.Architecture != targetArch {
			continue
		}

		if _, err := daemon.Write(tag, img); err != nil {
			return nil, errors.Wrapf(err, "cannot push image %s to daemon", tag)
		}

		fns = append(fns, pkgv1.Function{
			ObjectMeta: metav1.ObjectMeta{
				Name: xpkg.ToDNSLabel(tag.Context().RepositoryStr()),
			},
			Spec: pkgv1.FunctionSpec{
				PackageSpec: pkgv1.PackageSpec{
					Package: tag.Name(),
				},
			},
		})
	}

	return fns, nil
}

// getDockerDaemonArchitecture detects the Docker daemon's architecture.
func getDockerDaemonArchitecture(ctx context.Context) string {
	dockerHost := os.Getenv("DOCKER_HOST")

	if dockerHost == "" || strings.HasPrefix(dockerHost, "unix://") {
		return runtime.GOARCH
	}

	cli, err := docker.NewClient()
	if err != nil {
		return runtime.GOARCH
	}
	defer cli.Close() //nolint:errcheck // best effort

	info, err := cli.Info(ctx)
	if err != nil {
		return runtime.GOARCH
	}

	return normalizeArchitecture(info.Architecture)
}

// normalizeArchitecture converts Docker's architecture naming to Go's GOARCH format.
func normalizeArchitecture(dockerArch string) string {
	switch dockerArch {
	case "x86_64":
		return "amd64"
	case "aarch64":
		return "arm64"
	default:
		return dockerArch
	}
}
