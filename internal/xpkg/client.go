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
	"github.com/spf13/afero"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xpkg/parser"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xpkg/signature"

	pkgv1beta1 "github.com/crossplane/crossplane/apis/v2/pkg/v1beta1"
)

type options struct {
	cacheFs      afero.Fs
	cacheDir     string
	imageConfigs []pkgv1beta1.ImageConfig
}

// ClientOption configures a new client.
type ClientOption func(*options)

// WithCacheDir configures the cache filesystem and directory for the client. If
// not provided, a non-caching client will be returned.
func WithCacheDir(fs afero.Fs, path string) ClientOption {
	return func(o *options) {
		o.cacheFs = fs
		o.cacheDir = path
	}
}

// WithImageConfigs injects image configs for the client.
func WithImageConfigs(ics []pkgv1beta1.ImageConfig) ClientOption {
	return func(o *options) {
		o.imageConfigs = ics
	}
}

// NewClient assembles an xpkg.Client.
func NewClient(fetcher xpkg.Fetcher, opts ...ClientOption) (xpkg.Client, error) {
	o := &options{}
	for _, opt := range opts {
		opt(o)
	}

	metaScheme, err := xpkg.BuildMetaScheme()
	if err != nil {
		return nil, errors.Wrap(err, "cannot build package meta scheme")
	}
	objScheme, err := xpkg.BuildObjectScheme()
	if err != nil {
		return nil, errors.Wrap(err, "cannot build package object scheme")
	}

	var cache xpkg.PackageCache = xpkg.NewNopCache()
	if o.cacheDir != "" {
		if err := o.cacheFs.MkdirAll(o.cacheDir, 0o755); err != nil {
			return nil, errors.Wrapf(err, "cannot create xpkg cache directory %s", o.cacheDir)
		}
		cache = xpkg.NewFsPackageCache(o.cacheDir, o.cacheFs)
	}

	client := xpkg.NewCachedClient(
		fetcher,
		parser.New(metaScheme, objScheme),
		cache,
		NewStaticImageConfigStore(o.imageConfigs),
		signature.NopValidator{},
	)

	return client, nil
}

// NewStaticImageConfigStore returns an xpkg.ConfigStore that uses the given set
// of ImageConfigs.
func NewStaticImageConfigStore(imageConfigs []pkgv1beta1.ImageConfig) xpkg.ConfigStore {
	objs := make([]runtime.Object, len(imageConfigs))
	for i := range imageConfigs {
		objs[i] = &imageConfigs[i]
	}
	configClient := fake.NewFakeClient(objs...)
	_ = pkgv1beta1.AddToScheme(configClient.Scheme())
	return xpkg.NewImageConfigStore(configClient, "")
}
