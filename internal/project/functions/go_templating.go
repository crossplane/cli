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
	"io/fs"
	"net/http"
	"path/filepath"
	"slices"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/spf13/afero"
	"golang.org/x/sync/errgroup"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"

	pkgv1beta1 "github.com/crossplane/crossplane/apis/v2/pkg/v1beta1"

	"github.com/crossplane/cli/v2/internal/filesystem"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"
)

// goTemplatingBuilder builds "functions" written in go templating by injecting
// their code into a function-go-templating base image.
type goTemplatingBuilder struct {
	baseImage   string
	transport   http.RoundTripper
	configStore xpkg.ConfigStore
}

func (b *goTemplatingBuilder) Name() string {
	return "go-templating"
}

func (b *goTemplatingBuilder) match(fromFS afero.Fs) (bool, error) {
	goTemplatingExtensions := []string{
		".gotmpl",
		".tmpl",
	}

	matches := false
	err := afero.Walk(fromFS, ".", func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.Mode().IsDir() {
			return nil
		}

		if !info.Mode().IsRegular() {
			matches = false
			return fs.SkipAll
		}

		if !slices.Contains(goTemplatingExtensions, filepath.Ext(path)) {
			matches = false
			return fs.SkipAll
		}

		matches = true
		return nil
	})

	if errors.Is(err, fs.SkipAll) {
		err = nil
	}

	return matches, err
}

func (b *goTemplatingBuilder) Build(ctx context.Context, c BuildContext) ([]v1.Image, error) {
	baseImage := b.baseImage
	_, rewritten, err := b.configStore.RewritePath(ctx, b.baseImage)
	if err != nil {
		return nil, errors.Wrap(err, "failed to rewrite base image")
	}
	if rewritten != "" {
		baseImage = rewritten
	}

	baseRef, err := name.NewTag(baseImage)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse go-templating base image tag")
	}

	fnFS := c.FunctionFS()

	images := make([]v1.Image, len(c.Architectures))
	eg, _ := errgroup.WithContext(ctx)
	for i, arch := range c.Architectures {
		eg.Go(func() error {
			baseImg, err := baseImageForArch(baseRef, arch, b.transport)
			if err != nil {
				return errors.Wrap(err, "failed to fetch go-templating base image")
			}

			src, err := filesystem.FSToTar(fnFS, "/src",
				filesystem.WithSymlinkBasePath(c.OSBasePath),
			)
			if err != nil {
				return errors.Wrap(err, "failed to tar layer contents")
			}

			codeLayer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(src)), nil
			})
			if err != nil {
				return errors.Wrap(err, "failed to create code layer")
			}

			img, err := mutate.AppendLayers(baseImg, codeLayer)
			if err != nil {
				return errors.Wrap(err, "failed to add code to image")
			}

			img, err = setImageEnvvars(img, map[string]string{
				"FUNCTION_GO_TEMPLATING_DEFAULT_SOURCE": "/src",
			})
			if err != nil {
				return errors.Wrap(err, "failed to configure go-templating source path")
			}

			images[i] = img
			return nil
		})
	}

	return images, eg.Wait()
}

func newGoTemplatingBuilder(imageConfigs []pkgv1beta1.ImageConfig) *goTemplatingBuilder {
	return &goTemplatingBuilder{
		transport:   http.DefaultTransport,
		baseImage:   "xpkg.crossplane.io/crossplane-contrib/function-go-templating:v0.12.0",
		configStore: clixpkg.NewStaticImageConfigStore(imageConfigs),
	}
}
