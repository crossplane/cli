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

package load

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

// StdinSource is the Source of a Document read from stdin.
const StdinSource = "-"

// A Document is one YAML document as it appears in its source.
type Document struct {
	// Source is the file the document was read from, or StdinSource.
	Source string

	// StartLine is the 1-based line in Source where Raw starts.
	StartLine int

	// Raw is the document's bytes, comments included.
	Raw []byte
}

// A DocumentLoader loads YAML documents without decoding them, keeping where
// each came from.
type DocumentLoader interface {
	Load() ([]Document, error)
}

// A DocumentLoaderOption configures a DocumentLoader.
type DocumentLoaderOption func(*documentLoaderOptions)

type documentLoaderOptions struct {
	stdin io.Reader
}

// WithStdin sets the reader the source "-" reads from. The default is
// os.Stdin.
func WithStdin(r io.Reader) DocumentLoaderOption {
	return func(o *documentLoaderOptions) { o.stdin = r }
}

// NewDocumentLoader returns a DocumentLoader for input. It accepts the same
// sources as NewLoader: a comma-separated list of files, directories (searched
// recursively for .yaml and .yml files) and "-" for stdin. Documents are split
// as Load splits them, but are not decoded, so empty documents and documents
// that are not Kubernetes objects are returned too.
func NewDocumentLoader(input string, opts ...DocumentLoaderOption) (DocumentLoader, error) {
	o := &documentLoaderOptions{stdin: os.Stdin}
	for _, fn := range opts {
		fn(o)
	}
	if o.stdin == nil {
		o.stdin = os.Stdin
	}

	sources := strings.Split(input, ",")
	loaders := make([]*documentLoader, 0, len(sources))
	for _, source := range sources {
		l, err := newDocumentLoader(source, o.stdin)
		if err != nil {
			if len(sources) == 1 {
				return nil, err
			}
			return nil, errors.Wrapf(err, "cannot create loader for %q", source)
		}
		loaders = append(loaders, l)
	}
	return multiDocumentLoader(loaders), nil
}

type documentLoader struct {
	source string
	dir    bool
	stdin  io.Reader
}

func newDocumentLoader(source string, stdin io.Reader) (*documentLoader, error) {
	if source == StdinSource {
		return &documentLoader{source: source, stdin: stdin}, nil
	}
	fi, err := os.Stat(source)
	if err != nil {
		return nil, errors.Wrap(err, "cannot stat input source")
	}
	return &documentLoader{source: source, dir: fi.IsDir()}, nil
}

type multiDocumentLoader []*documentLoader

// Load returns the documents of every source, in order.
func (m multiDocumentLoader) Load() ([]Document, error) {
	var docs []Document
	for _, l := range m {
		d, err := l.load()
		if err != nil {
			return nil, err
		}
		docs = append(docs, d...)
	}
	return docs, nil
}

func (l *documentLoader) load() ([]Document, error) {
	switch {
	case l.source == StdinSource:
		docs, err := SplitDocuments(StdinSource, l.stdin)
		return docs, errors.Wrap(err, "cannot load YAML stream from stdin")
	case l.dir:
		var docs []Document
		err := filepath.Walk(l.source, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !isYamlFile(info) {
				return nil
			}
			d, err := readDocuments(path)
			if err != nil {
				return err
			}
			docs = append(docs, d...)
			return nil
		})
		return docs, errors.Wrap(err, "cannot read folder")
	default:
		docs, err := readDocuments(l.source)
		return docs, errors.Wrap(err, "cannot read file")
	}
}

func readDocuments(path string) ([]Document, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, errors.Wrap(err, "cannot open file")
	}
	defer f.Close() //nolint:errcheck // Only open for reading.

	return SplitDocuments(path, f)
}

// SplitDocuments splits a YAML stream into documents exactly as YamlStream
// does, and records the line each one starts on. Lines are normalised as
// YamlStream normalises them: each ends in a single newline.
func SplitDocuments(source string, r io.Reader) ([]Document, error) {
	br := bufio.NewReader(r)
	var (
		docs   []Document
		buf    bytes.Buffer
		start  int
		lineNo int
	)
	emit := func() {
		docs = append(docs, Document{Source: source, StartLine: start, Raw: bytes.Clone(buf.Bytes())})
		buf.Reset()
	}

	for {
		line, err := readLine(br)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, errors.Wrap(err, "cannot parse YAML stream")
		}
		lineNo++

		// The separator rules mirror k8s.io/apimachinery's YAMLReader, which
		// YamlStream uses, so both split a stream into the same documents.
		if rest, ok := bytes.CutPrefix(line, []byte("---")); ok {
			trimmed := strings.TrimSpace(string(rest))
			if trimmed != "" && trimmed[0] != '#' {
				return nil, errors.Errorf("cannot parse YAML stream: invalid Yaml document separator: %s", trimmed)
			}
			if buf.Len() != 0 {
				emit()
				if errors.Is(err, io.EOF) {
					return docs, nil
				}
				continue
			}
			if errors.Is(err, io.EOF) {
				return docs, nil
			}
		}
		if errors.Is(err, io.EOF) {
			if buf.Len() != 0 {
				emit()
			}
			return docs, nil
		}
		if buf.Len() == 0 {
			start = lineNo
		}
		buf.Write(line)
	}
}

// readLine reads one line, of any length, and returns it with a single
// trailing newline, as YAMLReader's LineReader does.
func readLine(r *bufio.Reader) ([]byte, error) {
	var buf bytes.Buffer
	for {
		line, isPrefix, err := r.ReadLine()
		buf.Write(line)
		if err != nil || !isPrefix {
			buf.WriteByte('\n')
			return buf.Bytes(), err
		}
	}
}
