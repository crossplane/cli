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
	"fmt"
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
	// typescriptBuildImage is the image in which we build the function.
	typescriptBuildImage = "docker.io/library/node:24-slim"
	// typescriptRuntimeImage is the distroless base used at runtime.
	typescriptRuntimeImage = "gcr.io/distroless/nodejs24-debian13"
)

// typescriptBuilder builds TypeScript composition functions.
//
// A TypeScript embedded function is a full function-sdk-typescript project
// (package.json + tsconfig.json). We build it by running npm install and npm run build
// (which invokes tsgo) in a Node.js build container, then copy the dist/
// and node_modules/ onto a distroless Node.js base.
type typescriptBuilder struct {
	buildImage   string
	runtimeImage string
	transport    http.RoundTripper
	configStore  xpkg.ConfigStore
}

func (b *typescriptBuilder) Name() string {
	return "typescript"
}

func (b *typescriptBuilder) match(fromFS afero.Fs) (bool, error) {
	hasPackageJSON, err := afero.Exists(fromFS, "package.json")
	if err != nil {
		return false, err
	}
	hasTSConfig, err := afero.Exists(fromFS, "tsconfig.json")
	if err != nil {
		return false, err
	}
	return hasPackageJSON && hasTSConfig, nil
}

func (b *typescriptBuilder) Build(ctx context.Context, c BuildContext) ([]v1.Image, error) {
	if err := docker.Check(ctx); err != nil {
		return nil, errors.Wrap(err, "typescript builds require a Docker-compatible container runtime")
	}

	functionTar, err := b.buildFunction(ctx, c)
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
		return nil, errors.Wrap(err, "failed to parse typescript runtime base image")
	}

	images := make([]v1.Image, len(c.Architectures))
	eg, _ := errgroup.WithContext(ctx)
	for i, arch := range c.Architectures {
		eg.Go(func() error {
			baseImg, err := baseImageForArch(runtimeRef, arch, b.transport)
			if err != nil {
				return errors.Wrap(err, "failed to fetch typescript runtime base image")
			}

			functionLayer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(functionTar)), nil
			})
			if err != nil {
				return errors.Wrap(err, "failed to create function layer")
			}

			img, err := mutate.AppendLayers(baseImg, functionLayer)
			if err != nil {
				return errors.Wrap(err, "failed to append function layer")
			}

			img, err = configureTypescriptImage(img)
			if err != nil {
				return errors.Wrap(err, "failed to configure typescript image")
			}

			images[i] = img
			return nil
		})
	}

	return images, eg.Wait()
}

// buildFunction runs the build container against the function source and returns a
// tar of /function suitable for use as an image layer.
//
// The function source is staged at /<FunctionPath> in the build container and, if a
// typescript schemas tree exists, /<SchemasPath>/typescript/models/ — preserving
// the project's relative layout so that npm resolves the schemas path-dep from
// package.json. After building, we copy the built artifacts to /function and tar
// that directory for the runtime layer.
//
//nolint:contextcheck // The defer uses context.Background() intentionally for cleanup.
func (b *typescriptBuilder) buildFunction(ctx context.Context, c BuildContext) ([]byte, error) {
	fnFS := c.FunctionFS()
	// Exclude node_modules the user might have created locally.
	// Use the function path as the tar prefix so files end up at /<FunctionPath> in the container.
	fnTar, err := filesystem.FSToTar(fnFS, c.FunctionPath, filesystem.WithExcludePrefix("node_modules"))
	if err != nil {
		return nil, errors.Wrap(err, "failed to tar function source")
	}

	// Check if TypeScript schemas exist and tar them if so.
	// The schemas are placed at /<SchemasPath>/typescript/ to match
	// the relative path in package.json (e.g., "file:../../schemas/typescript").
	tsSchemasRel := path.Join(c.SchemasPath, "typescript")
	tsSchemasFS := afero.NewBasePathFs(c.ProjectFS, tsSchemasRel)
	hasTSSchemas, err := afero.DirExists(tsSchemasFS, ".")
	if err != nil {
		return nil, errors.Wrapf(err, "cannot check for TypeScript schemas at %q", tsSchemasRel)
	}
	var schemasTar []byte
	if hasTSSchemas {
		schemasTar, err = filesystem.FSToTar(tsSchemasFS, tsSchemasRel)
		if err != nil {
			return nil, errors.Wrap(err, "failed to tar typescript schemas")
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

	// Build script that:
	// 1. Runs npm install and build in the function's original path (so relative deps resolve)
	// 2. Copies the built artifacts to /function for the runtime layer
	fnPath := "/" + filepath.ToSlash(c.FunctionPath)
	tsSchemasPath := "/" + filepath.ToSlash(tsSchemasRel)
	buildScript := fmt.Sprintf(`set -eu
# First, install dependencies for the schemas package so TypeScript can resolve the base types
if [ -d "%s" ] && [ -f "%s/package.json" ]; then
    cd %s && npm install --no-fund
    cd -
fi
npm install --no-fund
npm run build
# Use -L to dereference symlinks so file: dependencies (like crossplane-models)
# are copied as actual files, not symlinks that won't resolve at runtime.
cp -rL . /function
`, tsSchemasPath, tsSchemasPath, tsSchemasPath)

	opts := []docker.StartContainerOption{
		docker.StartWithCopyFiles(fnTar, "/"),
		docker.StartWithCommand([]string{"sh", "-c", buildScript}),
		docker.StartWithWorkingDirectory(fnPath),
	}
	if schemasTar != nil {
		opts = append(opts, docker.StartWithCopyFiles(schemasTar, "/"))
	}

	cid, err := docker.StartContainer(ctx, "", buildImage, opts...)
	if err != nil {
		return nil, errors.Wrap(err, "failed to start typescript build container")
	}
	defer func() {
		// Use context.Background() so container cleanup happens even if ctx is cancelled.
		_ = docker.StopContainerByID(context.Background(), cid)
	}()

	if err := docker.WaitForContainerByID(ctx, cid); err != nil {
		return nil, errors.Wrap(err, "typescript build container failed")
	}

	return docker.TarFromContainer(ctx, cid, "/function")
}

// configureTypescriptImage sets the runtime configuration on the final image:
// the function entrypoint and the gRPC port.
func configureTypescriptImage(img v1.Image) (v1.Image, error) {
	cfgFile, err := img.ConfigFile()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get config file")
	}
	cfg := cfgFile.Config

	cfg.Entrypoint = []string{"/nodejs/bin/node", "dist/main.js"}
	cfg.Cmd = nil
	cfg.WorkingDir = "/function"
	if cfg.ExposedPorts == nil {
		cfg.ExposedPorts = map[string]struct{}{}
	}
	cfg.ExposedPorts["9443/tcp"] = struct{}{}

	return mutate.Config(img, cfg)
}

func newTypescriptBuilder(imageConfigs []pkgv1beta1.ImageConfig) *typescriptBuilder {
	return &typescriptBuilder{
		buildImage:   typescriptBuildImage,
		runtimeImage: typescriptRuntimeImage,
		transport:    http.DefaultTransport,
		configStore:  clixpkg.NewStaticImageConfigStore(imageConfigs),
	}
}
