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
	"strings"

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
	// rustBuildImage is the image in which we build the function. Its Debian
	// release must match rustRuntimeImage's: the binary links dynamically
	// against the build image's glibc, and only runs on that version or newer.
	rustBuildImage = "docker.io/library/rust:1-bookworm"
	// rustRuntimeImage is the distroless base used at runtime. The cc flavor
	// carries glibc and libgcc, which is all a Rust binary links against.
	rustRuntimeImage = "gcr.io/distroless/cc-debian12:nonroot"

	// rustBinaryPath is where the function's binary lives in the runtime image.
	rustBinaryPath = "/function"
	// rustBuildOutput is where the build container stages each architecture's
	// binary, as <rustBuildOutput>/<arch>/function.
	rustBuildOutput = "/out"
	// rustTargetDir keeps cargo's output out of the staged source tree, and in
	// a known place whatever the function's cargo configuration says.
	rustTargetDir = "/build/target"

	// rustBuildScript runs in the build container. Rust itself cross-compiles
	// to any installed target, but linking and the C code some crates build
	// (aws-lc-sys, which function-sdk-rust's TLS stack pulls in) need a C
	// toolchain for the target, so an architecture other than the container's
	// own gets Debian's cross gcc.
	//
	// We let cargo decide what the package's binaries are rather than reading
	// Cargo.toml: a binary can come from [[bin]], from src/main.rs under the
	// package name, or from src/bin.
	rustBuildScript = `set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
# Build with the toolchain the image ships. A rust-toolchain.toml in the
# function would otherwise have rustup download another one on every build.
RUSTUP_TOOLCHAIN=$(rustup default | cut -d' ' -f1)
export RUSTUP_TOOLCHAIN
host=$(uname -m)
for arch in $ARCHS ; do
  case "$arch" in
    amd64) cpu=x86_64 ;;
    arm64) cpu=aarch64 ;;
    *) echo "unsupported architecture: $arch" >&2 ; exit 1 ;;
  esac
  target=$cpu-unknown-linux-gnu
  if [ "$cpu" != "$host" ] ; then
    apt-get update --quiet=2
    apt-get install --quiet=2 --yes --no-install-recommends \
      "gcc-${cpu//_/-}-linux-gnu" "libc6-dev-$arch-cross" >/dev/null
    rustup target add "$target"
    env_target=${target//-/_}
    export "CARGO_TARGET_${env_target^^}_LINKER=$cpu-linux-gnu-gcc"
    export "CC_$env_target=$cpu-linux-gnu-gcc"
  fi
  cargo build --quiet --release --bins --target "$target"
  mapfile -t bins < <(find "$CARGO_TARGET_DIR/$target/release" -maxdepth 1 -type f -executable ! -name '*.so')
  if [ "${#bins[@]}" -ne 1 ] ; then
    echo "a function must build exactly one binary, found ${#bins[@]}: ${bins[*]##*/}" >&2
    exit 1
  fi
  install -D --mode=0755 "${bins[0]}" "$OUTPUT/$arch/function"
done
`
)

// rustBuilder builds Rust composition functions.
//
// A Rust embedded function is a function-sdk-rust project: a Cargo package that
// builds one binary. We build it the way function-sdk-rust's example Dockerfile
// does, with cargo in the official Rust image, and put the resulting binary on
// a distroless base. Unlike a Dockerfile build we compile every architecture in
// one container of the host's architecture, so nothing runs under emulation.
type rustBuilder struct {
	buildImage   string
	runtimeImage string
	transport    http.RoundTripper
	configStore  xpkg.ConfigStore
}

func (b *rustBuilder) Name() string {
	return "rust"
}

// match identifies a Rust function by its Cargo manifest alone. Where the
// binary's source lives is up to the manifest (src/main.rs, src/bin, or a
// [[bin]] path), and cargo reports it if there is none.
func (b *rustBuilder) match(fromFS afero.Fs) (bool, error) {
	return afero.Exists(fromFS, "Cargo.toml")
}

func (b *rustBuilder) Build(ctx context.Context, c BuildContext) ([]v1.Image, error) {
	// Reject an architecture we cannot build for before compiling the ones we
	// can, which takes minutes.
	for _, arch := range c.Architectures {
		if err := rustCheckArchitecture(arch); err != nil {
			return nil, err
		}
	}

	if err := docker.Check(ctx); err != nil {
		return nil, errors.Wrap(err, "rust builds require a Docker-compatible container runtime")
	}

	buildImage, err := b.rewriteImage(ctx, b.buildImage)
	if err != nil {
		return nil, errors.Wrap(err, "failed to rewrite build image")
	}

	binaryTars, err := b.buildBinaries(ctx, buildImage, c)
	if err != nil {
		return nil, err
	}

	runtimeImage, err := b.rewriteImage(ctx, b.runtimeImage)
	if err != nil {
		return nil, errors.Wrap(err, "failed to rewrite runtime image")
	}
	runtimeRef, err := name.ParseReference(runtimeImage)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse rust runtime base image")
	}

	images := make([]v1.Image, len(c.Architectures))
	eg, _ := errgroup.WithContext(ctx)
	for i, arch := range c.Architectures {
		eg.Go(func() error {
			baseImg, err := baseImageForArch(runtimeRef, arch, b.transport, c.BaseImageCacheDir)
			if err != nil {
				return errors.Wrap(err, "failed to fetch rust runtime base image")
			}

			binaryLayer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(binaryTars[arch])), nil
			})
			if err != nil {
				return errors.Wrap(err, "failed to create binary layer")
			}

			img, err := mutate.AppendLayers(baseImg, binaryLayer)
			if err != nil {
				return errors.Wrap(err, "failed to append binary layer")
			}

			img, err = configureRustImage(img)
			if err != nil {
				return errors.Wrap(err, "failed to configure rust image")
			}

			images[i] = img
			return nil
		})
	}

	return images, eg.Wait()
}

func (b *rustBuilder) rewriteImage(ctx context.Context, image string) (string, error) {
	_, rewritten, err := b.configStore.RewritePath(ctx, image)
	if err != nil {
		return "", err
	}
	if rewritten != "" {
		return rewritten, nil
	}
	return image, nil
}

// rustCheckArchitecture reports whether the build script knows how to build
// for an architecture.
func rustCheckArchitecture(arch string) error {
	switch arch {
	case "amd64", "arm64":
		return nil
	default:
		return errors.Errorf("unable to determine rust target for architecture %s", arch)
	}
}

// buildBinaries runs the build script against the staged sources in a throwaway
// container. It returns, for each architecture, a tar archive holding the
// function's binary as a single file named function.
func (b *rustBuilder) buildBinaries(ctx context.Context, buildImage string, c BuildContext) (map[string][]byte, error) {
	sourceTars, err := rustSourceTars(c)
	if err != nil {
		return nil, err
	}

	opts := []docker.StartContainerOption{
		docker.StartWithEnv(
			"ARCHS="+strings.Join(c.Architectures, " "),
			"OUTPUT="+rustBuildOutput,
			"CARGO_TARGET_DIR="+rustTargetDir,
		),
		docker.StartWithCommand([]string{"bash", "-c", rustBuildScript}),
		docker.StartWithWorkingDirectory("/" + filepath.ToSlash(c.FunctionPath)),
	}
	for _, t := range sourceTars {
		opts = append(opts, docker.StartWithCopyFiles(t, "/"))
	}

	cid, err := docker.StartContainer(ctx, "", buildImage, opts...)
	if err != nil {
		return nil, errors.Wrap(err, "failed to start rust build container")
	}
	defer func() {
		// The build is most likely to end early because ctx expired, and a
		// container that outlives us would go on compiling.
		_ = docker.StopContainerByID(context.WithoutCancel(ctx), cid)
	}()

	if err := docker.WaitForContainerByID(ctx, cid); err != nil {
		return nil, errors.Wrap(err, "rust build container failed")
	}

	ret := make(map[string][]byte, len(c.Architectures))
	for _, arch := range c.Architectures {
		// Copying a single file out of a container yields a tar holding just
		// that file, under its base name. Appended to the runtime image as a
		// layer, that puts the binary at rustBinaryPath.
		ret[arch], err = docker.TarFromContainer(ctx, cid, path.Join(rustBuildOutput, arch, path.Base(rustBinaryPath)))
		if err != nil {
			return nil, errors.Wrapf(err, "failed to retrieve built function for architecture %s", arch)
		}
	}

	return ret, nil
}

// rustSourceTars returns the tar archives to unpack at the root of the build
// container: the function's source at /<FunctionPath> and, if the project has
// generated Rust models, the models crate at /<SchemasPath>/rust. Preserving
// the project's layout is what lets cargo resolve the function's path
// dependency on the models.
//
// Both leave out target/, which holds build output for the host rather than
// anything the build needs, and is routinely gigabytes. Symlinks in the function
// are followed, which is how a function can share source with its siblings. The
// one thing that must not be a symlink is target itself: the exclusion matches
// the paths under target rather than target, so a symlink of that name is
// followed like any other and everything behind it is staged.
func rustSourceTars(c BuildContext) ([][]byte, error) {
	fnTar, err := filesystem.FSToTar(c.FunctionFS(), filepath.ToSlash(c.FunctionPath),
		filesystem.WithExcludePrefix("target/"),
		filesystem.WithSymlinkBasePath(c.OSBasePath),
	)
	if err != nil {
		return nil, errors.Wrap(err, "failed to tar function source")
	}
	tars := [][]byte{fnTar}

	modelsRel := path.Join(filepath.ToSlash(c.SchemasPath), "rust")
	modelsFS := afero.NewBasePathFs(c.ProjectFS, modelsRel)
	hasModels, err := afero.DirExists(modelsFS, ".")
	if err != nil {
		return nil, errors.Wrapf(err, "cannot check for rust schemas at %q", modelsRel)
	}
	if hasModels {
		modelsTar, err := filesystem.FSToTar(modelsFS, modelsRel, filesystem.WithExcludePrefix("target/"))
		if err != nil {
			return nil, errors.Wrap(err, "failed to tar rust schemas")
		}
		tars = append(tars, modelsTar)
	}

	return tars, nil
}

// configureRustImage sets the runtime configuration on the final image to match
// function-sdk-rust's example image: nonroot user, the function entrypoint, and
// the gRPC port.
func configureRustImage(img v1.Image) (v1.Image, error) {
	cfgFile, err := img.ConfigFile()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get config file")
	}
	cfg := cfgFile.Config

	cfg.Entrypoint = []string{rustBinaryPath}
	cfg.Cmd = nil
	cfg.WorkingDir = "/"
	cfg.User = "nonroot:nonroot"
	if cfg.ExposedPorts == nil {
		cfg.ExposedPorts = map[string]struct{}{}
	}
	cfg.ExposedPorts["9443/tcp"] = struct{}{}

	return mutate.Config(img, cfg)
}

func newRustBuilder(imageConfigs []pkgv1beta1.ImageConfig) *rustBuilder {
	return &rustBuilder{
		buildImage:   rustBuildImage,
		runtimeImage: rustRuntimeImage,
		transport:    http.DefaultTransport,
		configStore:  clixpkg.NewStaticImageConfigStore(imageConfigs),
	}
}
