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
	"strings"
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

func TestTolerantCacheGet(t *testing.T) {
	cases := map[string]struct {
		reason string
		cache  cache.Cache
	}{
		"FailureIsMiss": {
			reason: "A read failure that is not a genuine miss must still look like a miss, so the caller falls through to the registry.",
			cache:  erroringCache{},
		},
		"EmptyCacheIsMiss": {
			reason: "A working cache must report a genuine miss as a miss.",
			cache:  newAtomicCache(t.TempDir()),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := tolerantCache{tc.cache}.Get(v1.Hash{Algorithm: "sha256", Hex: "cafe"})
			if !errors.Is(err, cache.ErrNotFound) {
				t.Errorf("\n%s\nGet(...): want cache.ErrNotFound, got %v", tc.reason, err)
			}
		})
	}
}

func TestTolerantCachePut(t *testing.T) {
	type want struct {
		contents string
		cached   bool
	}
	cases := map[string]struct {
		reason string
		cache  func(t *testing.T) cache.Cache
		want   want
	}{
		"PutFailureReturnsUsableLayer": {
			reason: "Put failing outright must hand back a layer that still reads.",
			cache:  func(*testing.T) cache.Cache { return erroringCache{} },
			want:   want{contents: "hello from a layer"},
		},
		"UnwritableDirStillReads": {
			// An unwritable cache directory used to fail the build, because the
			// filesystem cache creates its backing file lazily inside Compressed().
			reason: "A layer must read even when the cache directory is unwritable.",
			cache: func(t *testing.T) cache.Cache {
				t.Helper()
				if os.Geteuid() == 0 {
					t.Skip("running as root, which can write to a read-only directory")
				}
				dir := filepath.Join(t.TempDir(), "cache")
				if err := os.Mkdir(dir, 0o500); err != nil {
					t.Fatalf("creating read-only cache dir: %v", err)
				}
				l, err := newAtomicCache(dir).Put(testLayer())
				if err != nil {
					t.Fatalf("Put(): unexpected error: %v", err)
				}
				if _, err := l.Compressed(); err == nil {
					t.Skip("cache directory turned out to be writable; nothing to assert")
				}
				return newAtomicCache(dir)
			},
			want: want{contents: "hello from a layer"},
		},
		"PassesThroughOnSuccess": {
			reason: "A working cache must behave as it would unwrapped: reading the layer populates the cache.",
			cache: func(t *testing.T) cache.Cache {
				t.Helper()
				return newAtomicCache(t.TempDir())
			},
			want: want{contents: "hello from a layer", cached: true},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := tolerantCache{tc.cache(t)}
			l, err := c.Put(testLayer())
			if err != nil {
				t.Fatalf("\n%s\nPut(...): unexpected error: %v", tc.reason, err)
			}
			if diff := cmp.Diff(tc.want.contents, readAll(t, l)); diff != "" {
				t.Errorf("\n%s\nPut(...): layer contents -want, +got:\n%s", tc.reason, diff)
			}
			if !tc.want.cached {
				return
			}
			d, err := l.Digest()
			if err != nil {
				t.Fatalf("Digest(): unexpected error: %v", err)
			}
			if _, err := c.Get(d); err != nil {
				t.Errorf("\n%s\nGet(...) after populating cache: unexpected error: %v", tc.reason, err)
			}
		})
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

// closeErrLayer is a layer whose streams fail on Close.
type closeErrLayer struct{ v1.Layer }

func (l closeErrLayer) Compressed() (io.ReadCloser, error) {
	rc, err := l.Layer.Compressed()
	if err != nil {
		return nil, err
	}
	return closeErrReader{rc}, nil
}

type closeErrReader struct{ io.ReadCloser }

func (closeErrReader) Close() error { return errors.New("boom") }

func TestAtomicCacheReadOutcomes(t *testing.T) {
	noise := make([]byte, 1<<20)
	_, _ = rand.Read(noise)
	big := gzipLayer(t, noise)
	small := gzipLayer(t, []byte("hello from a layer"))

	type args struct {
		layer v1.Layer
		// blockFinalPath puts a non-empty directory at the entry's final path,
		// so publishing the entry fails.
		blockFinalPath bool
		consume        func(rc io.ReadCloser)
	}
	type want struct {
		closeErr bool
		hit      bool
	}
	readAllOf := func(rc io.ReadCloser) { _, _ = io.Copy(io.Discard, rc) }

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"ClosedEarly": {
			reason: "A stream closed before EOF must not leave a cache entry.",
			args:   args{layer: big, consume: func(rc io.ReadCloser) { _, _ = io.CopyN(io.Discard, rc, 100) }},
		},
		"NeverRead": {
			reason: "A stream never read must not leave a cache entry.",
			args:   args{layer: big, consume: func(io.ReadCloser) {}},
		},
		"FullReadIsHit": {
			reason: "A stream read to EOF and closed must be published as a hit.",
			args:   args{layer: small, consume: readAllOf},
			want:   want{hit: true},
		},
		"SourceCloseFailsAfterEOF": {
			reason: "A failing source Close must be returned and must not publish an entry.",
			args:   args{layer: closeErrLayer{small}, consume: readAllOf},
			want:   want{closeErr: true},
		},
		"RenameFailsAfterFullRead": {
			reason: "A cache-only failure after a full read must not fail Close, and must not leave a temp file.",
			args:   args{layer: small, blockFinalPath: true, consume: readAllOf},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := newAtomicCache(t.TempDir())
			d, _ := tc.args.layer.Digest()
			if tc.args.blockFinalPath {
				if err := os.MkdirAll(filepath.Join(cachePath(c.dir, d), "x"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			l, err := c.Put(tc.args.layer)
			if err != nil {
				t.Fatalf("\n%s\nPut(...): %v", tc.reason, err)
			}
			rc, err := l.Compressed()
			if err != nil {
				t.Fatalf("\n%s\nCompressed(): %v", tc.reason, err)
			}
			tc.args.consume(rc)
			if gotErr := rc.Close() != nil; gotErr != tc.want.closeErr {
				t.Errorf("\n%s\nClose(): want error %t, got error %t", tc.reason, tc.want.closeErr, gotErr)
			}

			ents, _ := os.ReadDir(c.dir)
			for _, e := range ents {
				if strings.HasPrefix(e.Name(), ".tmp-") {
					t.Errorf("\n%s\ntemp file %s left behind", tc.reason, e.Name())
				}
			}
			if tc.args.blockFinalPath {
				return
			}
			got, err := c.Get(d)
			if !tc.want.hit {
				if !errors.Is(err, cache.ErrNotFound) {
					t.Errorf("\n%s\nGet(...): want cache.ErrNotFound, got %v", tc.reason, err)
				}
				if len(ents) != 0 {
					t.Errorf("\n%s\ncache dir should be empty, got %d entries", tc.reason, len(ents))
				}
				return
			}
			if err != nil {
				t.Fatalf("\n%s\nGet(...): %v", tc.reason, err)
			}
			gotDigest, _ := got.Digest()
			gotSize, _ := got.Size()
			wantSize, _ := tc.args.layer.Size()
			if gotDigest != d || gotSize != wantSize {
				t.Errorf("\n%s\nGet(...): want %v/%d, got %v/%d", tc.reason, d, wantSize, gotDigest, gotSize)
			}
		})
	}
}

func TestAtomicReaderWriteFailureStillReturnsSourceBytes(t *testing.T) {
	reason := "A cache write failure mid-stream must not change what the caller reads."
	dir := t.TempDir()
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		t.Fatal(err)
	}
	// Closing the file up front makes every write to it fail.
	_ = f.Close()
	want := []byte("hello from a layer")
	r := &atomicReader{rc: io.NopCloser(bytes.NewReader(want)), f: f, final: filepath.Join(dir, "final")}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("\n%s\nReadAll(...): %v", reason, err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("\n%s\nReadAll(...): -want, +got:\n%s", reason, diff)
	}
	if err := r.Close(); err != nil {
		t.Errorf("\n%s\nClose(): %v", reason, err)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Errorf("\n%s\ndir should be empty, got %d entries", reason, len(ents))
	}
}

func TestAtomicCacheConcurrentReadersNeverSeePartialLayers(t *testing.T) {
	reason := "Readers must never see a partial layer, and the finished layer must be a hit."
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
					if !errors.Is(err, io.EOF) {
						errs <- err
					}
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
		t.Errorf("\n%s\nconcurrent reader: %v", reason, err)
	}

	got, err := c.Get(d)
	if err != nil {
		t.Fatalf("\n%s\nGet() after all writers finished: %v", reason, err)
	}
	gotSize, _ := got.Size()
	gotDigest, _ := got.Digest()
	if gotSize != wantSize || gotDigest != d {
		t.Errorf("\n%s\nGet() after all writers finished: want %v/%d, got %v/%d", reason, d, wantSize, gotDigest, gotSize)
	}
}
