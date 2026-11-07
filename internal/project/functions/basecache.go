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
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

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

// atomicCache is a cache.Cache whose entries only become visible once complete.
//
// go-containerregistry's filesystem cache creates a layer's final file up front
// and tees the download into it, so a concurrent Get (another build, possibly
// another process, sharing the directory) can find a half-written file and
// treat it as a hit, taking the layer's size from the partial bytes. Writing
// that layer to a tarball then fails with "archive/tar: write too long".
//
// atomicCache instead writes each layer to a temporary file in the cache
// directory and renames it to its final path only after the whole stream has
// been read to EOF and closed without error. Any other outcome removes the
// temporary file. Concurrent writers of one digest are harmless: rename is
// atomic and the content is identical. Get and Delete use the filesystem
// cache, and the final paths are the same, so the on-disk layout is unchanged
// and existing caches stay valid. A process killed mid-download can leave a
// ".tmp-*" file behind; nothing reads those.
type atomicCache struct {
	cache.Cache

	dir string
}

func newAtomicCache(dir string) atomicCache {
	return atomicCache{Cache: cache.NewFilesystemCache(dir), dir: dir}
}

// Put returns a layer that populates the cache as it is read.
func (c atomicCache) Put(l v1.Layer) (v1.Layer, error) {
	digest, err := l.Digest()
	if err != nil {
		return nil, err
	}
	diffID, err := l.DiffID()
	if err != nil {
		return nil, err
	}
	return &atomicLayer{Layer: l, dir: c.dir, digest: digest, diffID: diffID}, nil
}

type atomicLayer struct {
	v1.Layer

	dir            string
	digest, diffID v1.Hash
}

func (l *atomicLayer) Compressed() (io.ReadCloser, error) {
	return l.tee(l.digest, l.Layer.Compressed)
}

func (l *atomicLayer) Uncompressed() (io.ReadCloser, error) {
	return l.tee(l.diffID, l.Layer.Uncompressed)
}

func (l *atomicLayer) tee(h v1.Hash, open func() (io.ReadCloser, error)) (io.ReadCloser, error) {
	if err := os.MkdirAll(l.dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(l.dir, ".tmp-*")
	if err != nil {
		return nil, err
	}
	rc, err := open()
	if err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, err
	}
	return &atomicReader{rc: rc, f: f, final: cachePath(l.dir, h)}, nil
}

// atomicReader copies what it reads into a temporary file, and publishes the
// file only if the stream was read to EOF without error.
type atomicReader struct {
	rc    io.ReadCloser
	f     *os.File
	final string

	complete bool
	failed   bool
}

func (r *atomicReader) Read(b []byte) (int, error) {
	n, err := r.rc.Read(b)
	if n > 0 {
		if _, werr := r.f.Write(b[:n]); werr != nil {
			r.failed = true
			return n, werr
		}
	}
	switch {
	case errors.Is(err, io.EOF):
		r.complete = true
	case err != nil:
		r.failed = true
	}
	return n, err
}

func (r *atomicReader) Close() error {
	rerr := r.rc.Close()
	ferr := r.f.Close()
	if r.failed || !r.complete || rerr != nil || ferr != nil {
		_ = os.Remove(r.f.Name())
		if rerr != nil {
			return rerr
		}
		return ferr
	}
	if err := os.Rename(r.f.Name(), r.final); err != nil {
		_ = os.Remove(r.f.Name())
		return err
	}
	return nil
}

// cachePath mirrors go-containerregistry's filesystem cache layout.
func cachePath(dir string, h v1.Hash) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(dir, fmt.Sprintf("%s-%s", h.Algorithm, h.Hex))
	}
	return filepath.Join(dir, h.String())
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
