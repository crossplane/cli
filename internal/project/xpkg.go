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
	"github.com/spf13/afero"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	"github.com/crossplane/cli/v2/internal/dependency"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"
)

// NewXpkgClient returns an xpkg client that caches packages in cacheDir, or
// in dependency.DefaultCacheDir() when cacheDir is empty.
func NewXpkgClient(cacheDir string, opts ...clixpkg.ClientOption) (clixpkg.Client, error) {
	if cacheDir == "" {
		cacheDir = dependency.DefaultCacheDir()
	}

	opts = append([]clixpkg.ClientOption{clixpkg.WithCacheDir(afero.NewOsFs(), cacheDir)}, opts...)
	client, err := clixpkg.NewClient(clixpkg.NewRemoteFetcher(), opts...)
	if err != nil {
		return nil, errors.Wrap(err, "cannot create xpkg client")
	}
	return client, nil
}
