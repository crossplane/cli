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

package functions

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"path"
	"path/filepath"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/spf13/afero"
	"golang.org/x/sync/errgroup"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"

	pkgv1beta1 "github.com/crossplane/crossplane/apis/v2/pkg/v1beta1"

	"github.com/crossplane/cli/v2/internal/docker"
	"github.com/crossplane/cli/v2/internal/filesystem"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"
)

const (
	// pythonBuildImage is the image in which we build the function. Its python
	// version must match the python version of pythonRuntimeImage.
	pythonBuildImage = "docker.io/library/debian:13-slim"
	// pythonRuntimeImage is the distroless base used at runtime.
	pythonRuntimeImage = "gcr.io/distroless/python3-debian13:nonroot"
	// pythonBuildScript is the shell pipeline that runs in the build
	// container. Mirrors function-template-python's Dockerfile: install hatch
	// in a throwaway venv, build a wheel, install the wheel into a fresh venv
	// at /fn.
	//
	// TODO(adamwg): We should build an image with python3 and python3-venv
	// pre-installed so we don't have to install them for every build.
	pythonBuildScript = `set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends python3 python3-venv
python3 -m venv /build
/build/bin/pip install --quiet hatch
/build/bin/hatch build -t wheel /whl
python3 -m venv /fn
/fn/bin/pip install --quiet /whl/*.whl
`
)

// pythonBuilder builds Python composition functions.
//
// A Python embedded function is a full crossplane-function-sdk-python project
// (pyproject.toml + function/). We build it the same way function-template-
// python's Dockerfile does: in a throwaway debian build container we run
// `hatch build` to produce a wheel, install it into a fresh venv, then copy
// that venv onto a distroless python base.
type pythonBuilder struct {
	buildImage   string
	runtimeImage string
	transport    http.RoundTripper
	configStore  xpkg.ConfigStore
}

func (b *pythonBuilder) Name() string {
	return "python"
}

func (b *pythonBuilder) match(fromFS afero.Fs) (bool, error) {
	hasPyproject, err := afero.Exists(fromFS, "pyproject.toml")
	if err != nil {
		return false, err
	}
	hasFnDir, err := afero.DirExists(fromFS, "function")
	if err != nil {
		return false, err
	}
	return hasPyproject && hasFnDir, nil
}

func (b *pythonBuilder) Build(ctx context.Context, c BuildContext) ([]v1.Image, error) {
	if err := docker.Check(ctx); err != nil {
		return nil, errors.Wrap(err, "python builds require a Docker-compatible container runtime")
	}

	venvTar, err := b.buildVenv(ctx, c)
	if err != nil {
		return nil, err
	}

	runtimeImage := b.runtimeImage
	_, rewritten, err := b.configStore.RewritePath(ctx, b.runtimeImage)
	if err != nil {
		return nil, errors.Wrap(err, "failed to rewrite runtime image")
	}
	if rewritten != "" {
		runtimeImage = rewritten
	}

	runtimeRef, err := name.ParseReference(runtimeImage)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse python runtime base image")
	}

	images := make([]v1.Image, len(c.Architectures))
	eg, _ := errgroup.WithContext(ctx)
	for i, arch := range c.Architectures {
		eg.Go(func() error {
			baseImg, err := baseImageForArch(runtimeRef, arch, b.transport)
			if err != nil {
				return errors.Wrap(err, "failed to fetch python runtime base image")
			}

			venvLayer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(venvTar)), nil
			})
			if err != nil {
				return errors.Wrap(err, "failed to create venv layer")
			}

			img, err := mutate.AppendLayers(baseImg, venvLayer)
			if err != nil {
				return errors.Wrap(err, "failed to append venv layer")
			}

			img, err = configurePythonImage(img)
			if err != nil {
				return errors.Wrap(err, "failed to configure python image")
			}

			images[i] = img
			return nil
		})
	}

	return images, eg.Wait()
}

// buildVenv runs the build container against the function source and returns a
// tar of /fn suitable for use as an image layer (entries are rooted at
// /fn/...).
//
// The function source is staged at /<FunctionPath> and, if a python schemas
// tree exists, /<SchemasPath>/python/ — preserving the project's relative
// layout so that pip resolves the schemas path-dep from pyproject.toml.
func (b *pythonBuilder) buildVenv(ctx context.Context, c BuildContext) ([]byte, error) {
	fnFS := c.FunctionFS()
	// Exclude any venv the user might have created in the function directory
	// for local development, since (a) we don't need it, and (b) it will
	// contain symlinks, which we can't tar up.
	fnTar, err := filesystem.FSToTar(fnFS, c.FunctionPath, filesystem.WithExcludePrefix(".venv"))
	if err != nil {
		return nil, errors.Wrap(err, "failed to tar function source")
	}

	pySchemasRel := path.Join(c.SchemasPath, "python")
	pySchemasFS := afero.NewBasePathFs(c.ProjectFS, pySchemasRel)
	hasPySchemas, _ := afero.DirExists(pySchemasFS, ".")
	var schemasTar []byte
	if hasPySchemas {
		schemasTar, err = filesystem.FSToTar(pySchemasFS, pySchemasRel)
		if err != nil {
			return nil, errors.Wrap(err, "failed to tar python schemas")
		}
	}

	buildImage := b.buildImage
	_, rewritten, err := b.configStore.RewritePath(ctx, b.buildImage)
	if err != nil {
		return nil, errors.Wrap(err, "failed to rewrite build image")
	}
	if rewritten != "" {
		buildImage = rewritten
	}

	opts := []docker.StartContainerOption{
		docker.StartWithCopyFiles(fnTar, "/"),
		docker.StartWithCommand([]string{"sh", "-c", pythonBuildScript}),
		docker.StartWithWorkingDirectory("/" + filepath.ToSlash(c.FunctionPath)),
	}
	if schemasTar != nil {
		opts = append(opts, docker.StartWithCopyFiles(schemasTar, "/"))
	}

	cid, err := docker.StartContainer(ctx, "", buildImage, opts...)
	if err != nil {
		return nil, errors.Wrap(err, "failed to start python build container")
	}
	defer func() {
		_ = docker.StopContainerByID(ctx, cid)
	}()

	if err := docker.WaitForContainerByID(ctx, cid); err != nil {
		return nil, errors.Wrap(err, "python build container failed")
	}

	return docker.TarFromContainer(ctx, cid, "/fn")
}

// configurePythonImage sets the runtime configuration on the final image to
// match function-template-python: nonroot user, the function entrypoint, and
// the gRPC port.
func configurePythonImage(img v1.Image) (v1.Image, error) {
	cfgFile, err := img.ConfigFile()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get config file")
	}
	cfg := cfgFile.Config

	cfg.Entrypoint = []string{"/fn/bin/function"}
	cfg.Cmd = nil
	cfg.WorkingDir = "/"
	cfg.User = "nonroot:nonroot"
	if cfg.ExposedPorts == nil {
		cfg.ExposedPorts = map[string]struct{}{}
	}
	cfg.ExposedPorts["9443/tcp"] = struct{}{}

	return mutate.Config(img, cfg)
}

func newPythonBuilder(imageConfigs []pkgv1beta1.ImageConfig) *pythonBuilder {
	return &pythonBuilder{
		buildImage:   pythonBuildImage,
		runtimeImage: pythonRuntimeImage,
		transport:    http.DefaultTransport,
		configStore:  clixpkg.NewStaticImageConfigStore(imageConfigs),
	}
}
