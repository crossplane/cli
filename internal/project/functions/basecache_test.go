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
	"compress/gzip"
	"crypto/rand"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
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
	if diff := cmp.Diff("hello from a layer", readAll(t, l)); diff != "" {
		t.Errorf("layer contents (-want +got):\n%s", diff)
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

	plain := newAtomicCache(dir)
	l, err := plain.Put(testLayer())
	if err != nil {
		t.Fatalf("Put(): unexpected error: %v", err)
	}
	if _, err := l.Compressed(); err == nil {
		t.Skip("cache directory turned out to be writable; nothing to assert")
	}

	// Same directory, wrapped: the layer must read regardless.
	tolerant := tolerantCache{newAtomicCache(dir)}
	wrapped, err := tolerant.Put(testLayer())
	if err != nil {
		t.Fatalf("Put(): unexpected error: %v", err)
	}
	if diff := cmp.Diff("hello from a layer", readAll(t, wrapped)); diff != "" {
		t.Errorf("layer contents (-want +got):\n%s", diff)
	}
}

func TestTolerantCachePassesThroughOnSuccess(t *testing.T) {
	// A working cache must behave exactly as it would unwrapped: a miss is
	// still a miss, and a stored layer still reads back.
	c := tolerantCache{newAtomicCache(t.TempDir())}

	if _, err := c.Get(v1.Hash{Algorithm: "sha256", Hex: "cafe"}); !errors.Is(err, cache.ErrNotFound) {
		t.Errorf("Get() on empty cache: want cache.ErrNotFound, got %v", err)
	}

	l, err := c.Put(testLayer())
	if err != nil {
		t.Fatalf("Put(): unexpected error: %v", err)
	}
	if diff := cmp.Diff("hello from a layer", readAll(t, l)); diff != "" {
		t.Errorf("layer contents (-want +got):\n%s", diff)
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

// gzipLayer returns a layer whose compressed form is real gzip, as registry
// layers are, so the filesystem cache reads it back with the same digest.
func gzipLayer(t *testing.T, payload []byte) v1.Layer {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(payload); err != nil {
		t.Fatalf("gzip: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip: %v", err)
	}
	return static.NewLayer(buf.Bytes(), types.DockerLayer)
}

// slowLayer delivers its compressed bytes in small chunks with pauses, so
// concurrent readers reliably overlap the download.
type slowLayer struct{ v1.Layer }

func (l slowLayer) Compressed() (io.ReadCloser, error) {
	rc, err := l.Layer.Compressed()
	if err != nil {
		return nil, err
	}
	return slowReader{rc}, nil
}

type slowReader struct{ io.ReadCloser }

func (r slowReader) Read(b []byte) (int, error) {
	time.Sleep(time.Millisecond)
	return r.ReadCloser.Read(b[:min(len(b), 1024)])
}

func TestAtomicCachePartialReadLeavesNoEntry(t *testing.T) {
	noise := make([]byte, 1<<20)
	_, _ = rand.Read(noise)
	big := gzipLayer(t, noise)
	d, err := big.Digest()
	if err != nil {
		t.Fatalf("Digest(): %v", err)
	}

	tests := map[string]func(rc io.ReadCloser){
		"ClosedEarly": func(rc io.ReadCloser) {
			_, _ = io.CopyN(io.Discard, rc, 100)
		},
		"NeverRead": func(io.ReadCloser) {},
	}
	for name, consume := range tests {
		t.Run(name, func(t *testing.T) {
			c := newAtomicCache(t.TempDir())
			l, err := c.Put(big)
			if err != nil {
				t.Fatalf("Put(): %v", err)
			}
			rc, err := l.Compressed()
			if err != nil {
				t.Fatalf("Compressed(): %v", err)
			}
			consume(rc)
			_ = rc.Close()

			if _, err := c.Get(d); !errors.Is(err, cache.ErrNotFound) {
				t.Errorf("Get() after partial read: want cache.ErrNotFound, got %v", err)
			}
			if ents, _ := os.ReadDir(c.dir); len(ents) != 0 {
				t.Errorf("cache dir should be empty after partial read, got %d entries", len(ents))
			}
		})
	}
}

func TestAtomicCacheFullReadIsHit(t *testing.T) {
	c := newAtomicCache(t.TempDir())
	src := gzipLayer(t, []byte("hello from a layer"))
	l, err := c.Put(src)
	if err != nil {
		t.Fatalf("Put(): %v", err)
	}
	readAll(t, l)

	d, _ := src.Digest()
	wantSize, _ := src.Size()
	got, err := c.Get(d)
	if err != nil {
		t.Fatalf("Get(): %v", err)
	}
	gotDigest, _ := got.Digest()
	gotSize, _ := got.Size()
	if gotDigest != d || gotSize != wantSize {
		t.Errorf("Get(): want %v/%d, got %v/%d", d, wantSize, gotDigest, gotSize)
	}
}

func TestAtomicCacheConcurrentReadersNeverSeePartialLayers(t *testing.T) {
	payload := make([]byte, 256<<10)
	_, _ = rand.Read(payload)
	src := slowLayer{gzipLayer(t, payload)}
	d, _ := src.Digest()
	wantSize, _ := src.Size()

	c := newAtomicCache(t.TempDir())
	var wg sync.WaitGroup
	errs := make(chan error, 64)

	for range 8 {
		wg.Go(func() {
			l, err := c.Put(src)
			if err != nil {
				errs <- err
				return
			}
			rc, err := l.Compressed()
			if err != nil {
				errs <- err
				return
			}
			// Small reads keep the download slow enough to overlap Gets.
			buf := make([]byte, 4096)
			for {
				if _, err := rc.Read(buf); err != nil {
					break
				}
			}
			if err := rc.Close(); err != nil {
				errs <- err
			}
		})
	}
	for range 8 {
		wg.Go(func() {
			for range 200 {
				l, err := c.Get(d)
				if errors.Is(err, cache.ErrNotFound) {
					continue
				}
				if err != nil {
					errs <- err
					return
				}
				gotSize, _ := l.Size()
				gotDigest, _ := l.Digest()
				if gotSize != wantSize || gotDigest != d {
					errs <- errors.Errorf("Get() returned %v/%d, want %v/%d", gotDigest, gotSize, d, wantSize)
					return
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
