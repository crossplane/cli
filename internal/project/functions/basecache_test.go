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
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/cache"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

func testLayer() v1.Layer {
	return static.NewLayer([]byte("hello from a layer"), types.DockerLayer)
}

func readAll(t *testing.T, l v1.Layer) string {
	t.Helper()
	rc, err := l.Compressed()
	if err != nil {
		t.Fatalf("Compressed(): unexpected error: %v", err)
	}
	defer func() { _ = rc.Close() }()
	bs, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("reading layer: unexpected error: %v", err)
	}
	return string(bs)
}

// erroringCache fails every operation the way a corrupt or unreadable cache
// directory would, rather than reporting a miss.
type erroringCache struct{}

func (erroringCache) Get(v1.Hash) (v1.Layer, error)  { return nil, errors.New("boom") }
func (erroringCache) Put(v1.Layer) (v1.Layer, error) { return nil, errors.New("boom") }
func (erroringCache) Delete(v1.Hash) error           { return errors.New("boom") }

func TestTolerantCacheGetReportsFailuresAsMisses(t *testing.T) {
	// A read failure that is not a genuine miss must still look like a miss,
	// so the caller falls through to the registry instead of erroring out.
	c := tolerantCache{erroringCache{}}

	_, err := c.Get(v1.Hash{Algorithm: "sha256", Hex: "cafe"})
	if !errors.Is(err, cache.ErrNotFound) {
		t.Errorf("Get(): want cache.ErrNotFound, got %v", err)
	}
}

func TestTolerantCachePutFailureReturnsUsableLayer(t *testing.T) {
	// Put failing outright must hand back a layer that still reads.
	c := tolerantCache{erroringCache{}}

	l, err := c.Put(testLayer())
	if err != nil {
		t.Fatalf("Put(): unexpected error: %v", err)
	}
	if got, want := readAll(t, l), "hello from a layer"; got != want {
		t.Errorf("layer contents: got %q, want %q", got, want)
	}
}

func TestTolerantCacheUnwritableDirStillReads(t *testing.T) {
	// The regression this guards: an unwritable cache directory used to fail
	// the build, because the filesystem cache creates its backing file lazily
	// inside Compressed().
	if os.Geteuid() == 0 {
		t.Skip("running as root, which can write to a read-only directory")
	}

	dir := filepath.Join(t.TempDir(), "cache")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatalf("creating read-only cache dir: %v", err)
	}

	plain := cache.NewFilesystemCache(dir)
	l, err := plain.Put(testLayer())
	if err != nil {
		t.Fatalf("Put(): unexpected error: %v", err)
	}
	if _, err := l.Compressed(); err == nil {
		t.Skip("cache directory turned out to be writable; nothing to assert")
	}

	// Same directory, wrapped: the layer must read regardless.
	tolerant := tolerantCache{cache.NewFilesystemCache(dir)}
	wrapped, err := tolerant.Put(testLayer())
	if err != nil {
		t.Fatalf("Put(): unexpected error: %v", err)
	}
	if got, want := readAll(t, wrapped), "hello from a layer"; got != want {
		t.Errorf("layer contents: got %q, want %q", got, want)
	}
}

func TestTolerantCachePassesThroughOnSuccess(t *testing.T) {
	// A working cache must behave exactly as it would unwrapped: a miss is
	// still a miss, and a stored layer still reads back.
	c := tolerantCache{cache.NewFilesystemCache(t.TempDir())}

	if _, err := c.Get(v1.Hash{Algorithm: "sha256", Hex: "cafe"}); !errors.Is(err, cache.ErrNotFound) {
		t.Errorf("Get() on empty cache: want cache.ErrNotFound, got %v", err)
	}

	l, err := c.Put(testLayer())
	if err != nil {
		t.Fatalf("Put(): unexpected error: %v", err)
	}
	if got, want := readAll(t, l), "hello from a layer"; got != want {
		t.Errorf("layer contents: got %q, want %q", got, want)
	}

	// Reading the layer populates the cache, so the digest is now a hit.
	d, err := l.Digest()
	if err != nil {
		t.Fatalf("Digest(): unexpected error: %v", err)
	}
	if _, err := c.Get(d); err != nil {
		t.Errorf("Get() after populating cache: unexpected error: %v", err)
	}
}
