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

// Package xpkgtest builds images for tests of xpkg packaging and pushing.
package xpkgtest

import (
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"

	"github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"
)

// An ImageOption changes the config of an image Image builds.
type ImageOption func(tb testing.TB, img v1.Image, cfg *v1.ConfigFile)

// WithPlatform sets the image's OS and architecture.
func WithPlatform(os, arch string) ImageOption {
	return func(_ testing.TB, _ v1.Image, cfg *v1.ConfigFile) {
		cfg.OS = os
		cfg.Architecture = arch
	}
}

// WithPackageLabel labels the image's first layer as an xpkg package layer in
// the image config, the way xpkg build does.
func WithPackageLabel() ImageOption {
	return func(tb testing.TB, img v1.Image, cfg *v1.ConfigFile) {
		tb.Helper()
		layers, err := img.Layers()
		if err != nil {
			tb.Fatalf("failed to read image layers: %v", err)
		}
		d, err := layers[0].Digest()
		if err != nil {
			tb.Fatalf("failed to read layer digest: %v", err)
		}
		if cfg.Config.Labels == nil {
			cfg.Config.Labels = map[string]string{}
		}
		cfg.Config.Labels[xpkg.Label(d.String())] = xpkg.PackageAnnotation
	}
}

// Image returns a random single-layer image, changed by opts. With no opts it
// has no platform and no xpkg labels.
func Image(tb testing.TB, opts ...ImageOption) v1.Image {
	tb.Helper()
	img, err := random.Image(64, 1)
	if err != nil {
		tb.Fatalf("failed to build random image: %v", err)
	}
	if len(opts) == 0 {
		return img
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		tb.Fatalf("failed to read config file: %v", err)
	}
	cfg = cfg.DeepCopy()
	for _, o := range opts {
		o(tb, img, cfg)
	}
	img, err = mutate.ConfigFile(img, cfg)
	if err != nil {
		tb.Fatalf("failed to set config file: %v", err)
	}
	return img
}
