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

// Package functions contains functions for building embedded functions.
package functions

import (
	"context"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/spf13/afero"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	pkgv1beta1 "github.com/crossplane/crossplane/apis/v2/pkg/v1beta1"
)

// Identifier knows how to identify an appropriate builder for a function based
// on its source code.
type Identifier interface {
	// Identify returns a suitable builder for the function whose source lives
	// in the given filesystem. It returns an error if no such builder is
	// available.
	Identify(fromFS afero.Fs, imageConfigs []pkgv1beta1.ImageConfig) (Builder, error)
}

type realIdentifier struct{}

// DefaultIdentifier is the default builder identifier, suitable for production
// use.
//
//nolint:gochecknoglobals // we want to keep this global
var DefaultIdentifier Identifier = realIdentifier{}

func (realIdentifier) Identify(fromFS afero.Fs, imageConfigs []pkgv1beta1.ImageConfig) (Builder, error) {
	builders := []Builder{
		newKCLBuilder(imageConfigs),
		newPythonBuilder(imageConfigs),
		newGoBuilder(imageConfigs),
		newGoTemplatingBuilder(imageConfigs),
	}
	for _, b := range builders {
		ok, err := b.match(fromFS)
		if err != nil {
			return nil, errors.Wrapf(err, "builder %q returned an error", b.Name())
		}
		if ok {
			return b, nil
		}
	}

	return nil, errors.New("no suitable builder found")
}

// BuildContext bundles the inputs that function builders work from. Each
// builder slices the parts of the project it needs: the function
// subdirectory for Go/KCL/go-templating, plus the schemas dir for Python.
type BuildContext struct {
	// ProjectFS is the project root filesystem.
	ProjectFS afero.Fs
	// FunctionPath is the function's path relative to ProjectFS root,
	// e.g. "functions/my-fn".
	FunctionPath string
	// SchemasPath is the schemas dir relative to ProjectFS root, e.g.
	// "schemas". Used by Python to stage schemas/python/ alongside the
	// function source so the relative path-dep resolves at build time.
	SchemasPath string
	// Architectures is the list of architectures to build for.
	Architectures []string
	// OSBasePath is the absolute on-disk path of the function directory.
	// Used by FSToTar to resolve symlinks.
	OSBasePath string
}

// FunctionFS returns a filesystem rooted at the function's source directory.
func (c BuildContext) FunctionFS() afero.Fs {
	return afero.NewBasePathFs(c.ProjectFS, c.FunctionPath)
}

// Builder knows how to build a particular kind of function.
type Builder interface {
	// Name returns a name for this builder.
	Name() string
	// Build builds the function described by the given context, returning
	// an image for each architecture. This image will *not* include
	// package metadata; it's just the runtime image for the function.
	Build(ctx context.Context, c BuildContext) ([]v1.Image, error)
	// match returns true if this builder can build the function whose source
	// lives in the given filesystem.
	match(fromFS afero.Fs) (bool, error)
}

type nopIdentifier struct{}

// FakeIdentifier is an identifier that always returns a fake builder. This is
// for use in tests where we don't want to do real builds.
//
//nolint:gochecknoglobals // we want to keep this global
var FakeIdentifier Identifier = nopIdentifier{}

func (nopIdentifier) Identify(_ afero.Fs, _ []pkgv1beta1.ImageConfig) (Builder, error) {
	return &fakeBuilder{}, nil
}

type fakeBuilder struct{}

func (b *fakeBuilder) Name() string {
	return "fake"
}

func (b *fakeBuilder) match(_ afero.Fs) (bool, error) {
	return true, nil
}

func (b *fakeBuilder) Build(_ context.Context, c BuildContext) ([]v1.Image, error) {
	images := make([]v1.Image, len(c.Architectures))
	for i, arch := range c.Architectures {
		baseImg := empty.Image
		cfg := &v1.ConfigFile{
			OS:           "linux",
			Architecture: arch,
		}
		img, err := mutate.ConfigFile(baseImg, cfg)
		if err != nil {
			return nil, err
		}
		images[i] = img
	}

	return images, nil
}
