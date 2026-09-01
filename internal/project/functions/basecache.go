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
	"io"
	"os"
	"path/filepath"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/cache"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

// DefaultBaseImageCacheDir returns the default per-user cache directory for
// function runtime base image layers, or "" to disable caching when there is no
// per-user directory to put it in. It sits beside the xpkg cache rather than
// inside it, since the two hold different kinds of artifact and would be pruned
// on different terms.
//
// Falling back to os.TempDir() would put the cache at a predictable path that
// another user on a shared machine could have created first, with permissions
// of their choosing. The layers are public registry content rather than
// anything sensitive, but a cache is not worth a world-readable directory
// someone else controls — and callers already treat "" as "do not cache", so
// declining is cheap.
//
// Nothing prunes this directory. Layers are keyed by content digest, so entries
// are never stale, but they are also never replaced: every base image version a
// user builds against accumulates. Users can delete the directory safely — the
// next build refetches what it needs — but the CLI should grow a retention
// policy or a prune command before this becomes the kind of thing people
// discover by running out of disk.
func DefaultBaseImageCacheDir() string {
	base, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "crossplane", "base-images")
}

// tolerantCache degrades to the registry instead of failing the build when the
// cache itself cannot be used.
//
// go-containerregistry's cache is not forgiving on its own. A read error that
// is not ErrNotFound propagates out of Compressed, and the filesystem cache
// creates its backing file lazily, so an unwritable or full cache directory
// surfaces as an error from the layer rather than a cache miss. Neither should
// break a build that could have fetched the layer remotely — especially since
// nothing prunes this cache, which makes a full disk a plausible way to reach
// it. See DefaultBaseImageCacheDir.
//
// One case is not recoverable here: if the directory is writable but fills up
// partway through a layer, the write error surfaces mid-stream, after the
// reader has been handed to the caller. Restarting from the remote layer at
// that point would mean re-reading bytes the caller already consumed.
type tolerantCache struct {
	cache.Cache
}

// Get reports any failure other than a genuine miss as a miss, so that an
// unreadable or corrupt entry sends the caller to the registry.
func (c tolerantCache) Get(h v1.Hash) (v1.Layer, error) {
	l, err := c.Cache.Get(h)
	if err != nil && !errors.Is(err, cache.ErrNotFound) {
		return nil, cache.ErrNotFound
	}
	return l, err
}

// Put keeps the original layer alongside the caching one so that a layer which
// cannot be written still reads.
func (c tolerantCache) Put(l v1.Layer) (v1.Layer, error) {
	cached, err := c.Cache.Put(l)
	if err != nil {
		// Deliberate: a cache that cannot store the layer is not a reason to
		// fail. Hand back the uncached layer and carry on.
		return l, nil //nolint:nilerr // Cache failures degrade to no caching.
	}
	return tolerantLayer{Layer: cached, uncached: l}, nil
}

// tolerantLayer reads through to an uncached layer when the caching layer
// cannot open its backing file.
type tolerantLayer struct {
	v1.Layer

	uncached v1.Layer
}

func (l tolerantLayer) Compressed() (io.ReadCloser, error) {
	rc, err := l.Layer.Compressed()
	if err != nil {
		return l.uncached.Compressed()
	}
	return rc, nil
}

func (l tolerantLayer) Uncompressed() (io.ReadCloser, error) {
	rc, err := l.Layer.Uncompressed()
	if err != nil {
		return l.uncached.Uncompressed()
	}
	return rc, nil
}
