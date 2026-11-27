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
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestSplitDocuments(t *testing.T) {
	type doc struct {
		StartLine int
		Raw       string
	}

	type args struct {
		input string
	}

	type want struct {
		docs []doc
		err  error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"Single": {
			reason: "One document starts on line 1.",
			args:   args{input: "a: 1\nb: 2\n"},
			want:   want{docs: []doc{{StartLine: 1, Raw: "a: 1\nb: 2\n"}}},
		},
		"Separated": {
			reason: "A document after a separator starts on the line after it.",
			args:   args{input: "a: 1\n---\nb: 2\n"},
			want:   want{docs: []doc{{StartLine: 1, Raw: "a: 1\n"}, {StartLine: 3, Raw: "b: 2\n"}}},
		},
		"LeadingSeparator": {
			reason: "A leading separator stays at the start of the first document, as YamlStream keeps it.",
			args:   args{input: "---\na: 1\n---\nb: 2\n"},
			want:   want{docs: []doc{{StartLine: 1, Raw: "---\na: 1\n"}, {StartLine: 4, Raw: "b: 2\n"}}},
		},
		"SeparatorWithComment": {
			reason: "A separator may carry a trailing comment.",
			args:   args{input: "a: 1\n--- # next\nb: 2\n"},
			want:   want{docs: []doc{{StartLine: 1, Raw: "a: 1\n"}, {StartLine: 3, Raw: "b: 2\n"}}},
		},
		"LeadingComment": {
			reason: "Comments before the first document belong to it and keep its start at line 1.",
			args:   args{input: "# header\n# more\na: 1\n"},
			want:   want{docs: []doc{{StartLine: 1, Raw: "# header\n# more\na: 1\n"}}},
		},
		"CommentBeforeSecond": {
			reason: "Comments after a separator belong to the next document.",
			args:   args{input: "a: 1\n---\n# about b\nb: 2\n"},
			want:   want{docs: []doc{{StartLine: 1, Raw: "a: 1\n"}, {StartLine: 3, Raw: "# about b\nb: 2\n"}}},
		},
		"EmptyDocuments": {
			reason: "Back-to-back separators yield what YamlStream yields: a separator-only document and no empty ones.",
			args:   args{input: "a: 1\n---\n---\n---\nb: 2\n"},
			want:   want{docs: []doc{{StartLine: 1, Raw: "a: 1\n"}, {StartLine: 3, Raw: "---\n"}, {StartLine: 5, Raw: "b: 2\n"}}},
		},
		"TrailingSeparator": {
			reason: "A trailing separator adds no document.",
			args:   args{input: "a: 1\n---\n"},
			want:   want{docs: []doc{{StartLine: 1, Raw: "a: 1\n"}}},
		},
		"NoTrailingNewline": {
			reason: "A final line without a newline is normalised to end in one.",
			args:   args{input: "a: 1\n---\nb: 2"},
			want:   want{docs: []doc{{StartLine: 1, Raw: "a: 1\n"}, {StartLine: 3, Raw: "b: 2\n"}}},
		},
		"CRLF": {
			reason: "Windows line endings are normalised as YamlStream normalises them, and lines still count.",
			args:   args{input: "a: 1\r\n---\r\nb: 2\r\n"},
			want:   want{docs: []doc{{StartLine: 1, Raw: "a: 1\n"}, {StartLine: 3, Raw: "b: 2\n"}}},
		},
		"Empty": {
			reason: "An empty stream has no documents.",
			args:   args{input: ""},
			want:   want{},
		},
		"InvalidSeparator": {
			reason: "Text after a separator, other than a comment, is an error, as it is for YamlStream.",
			args:   args{input: "a: 1\n--- b\n"},
			want:   want{err: cmpopts.AnyError},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			docs, err := SplitDocuments("in.yaml", strings.NewReader(tc.args.input))
			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nSplitDocuments(): -want error, +got error:\n%s", tc.reason, diff)
			}
			stream, streamErr := YamlStream(strings.NewReader(tc.args.input))
			if diff := cmp.Diff(tc.want.err, streamErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nYamlStream(): -want error, +got error:\n%s", tc.reason, diff)
			}
			if err != nil || streamErr != nil {
				return
			}

			var got []doc
			for _, d := range docs {
				if d.Source != "in.yaml" {
					t.Errorf("\n%s\nSource: want in.yaml, got %q", tc.reason, d.Source)
				}
				got = append(got, doc{StartLine: d.StartLine, Raw: string(d.Raw)})
			}
			if diff := cmp.Diff(tc.want.docs, got); diff != "" {
				t.Errorf("\n%s\nSplitDocuments(): -want, +got:\n%s", tc.reason, diff)
			}

			// The documents must be exactly those YamlStream returns.
			raws := make([][]byte, 0, len(docs))
			for _, d := range docs {
				raws = append(raws, d.Raw)
			}
			if diff := cmp.Diff(stream, raws, cmp.Comparer(bytes.Equal)); diff != "" && len(stream)+len(raws) > 0 {
				t.Errorf("\n%s\nSplitDocuments() and YamlStream() disagree: -YamlStream, +SplitDocuments:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestSplitDocumentsErrors(t *testing.T) {
	errBoom := errors.New("boom")

	type args struct {
		source string
		r      io.Reader
	}

	type want struct {
		err error
		msg string
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"ReadError": {
			reason: "A failing reader's error is returned.",
			args:   args{source: "in.yaml", r: iotest.ErrReader(errBoom)},
			want: want{
				err: errBoom,
				msg: "cannot parse YAML stream: boom",
			},
		},
		"InvalidSeparatorInFile": {
			reason: "An invalid separator error names the file and line, and says what a separator may contain.",
			args:   args{source: "in.yaml", r: strings.NewReader("a: 1\n--- b\n")},
			want: want{
				err: cmpopts.AnyError,
				msg: `cannot parse YAML stream: invalid document separator "--- b" on line 2 of in.yaml: "---" may be followed only by whitespace or a comment`,
			},
		},
		"InvalidSeparatorOnStdin": {
			reason: "An invalid separator error names stdin, not its - source.",
			args:   args{source: StdinSource, r: strings.NewReader("--- b\n")},
			want: want{
				err: cmpopts.AnyError,
				msg: `cannot parse YAML stream: invalid document separator "--- b" on line 1 of stdin: "---" may be followed only by whitespace or a comment`,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := SplitDocuments(tc.args.source, tc.args.r)
			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nSplitDocuments(): -want error, +got error:\n%s", tc.reason, diff)
			}
			if err == nil {
				return
			}
			if diff := cmp.Diff(tc.want.msg, err.Error()); diff != "" {
				t.Errorf("\n%s\nSplitDocuments(): -want message, +got message:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestNewDocumentLoader(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, data string) string {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	a := write("a.yaml", "a: 1\n---\nb: 2\n")
	write("tree/x/one.yaml", "x: 1\n")
	write("tree/y/two.yml", "y: 1\n")
	write("tree/notes.txt", "not yaml\n")

	type loaded struct {
		Source    string
		StartLine int
	}

	l, err := NewDocumentLoader(a + "," + filepath.Join(dir, "tree"))
	if err != nil {
		t.Fatalf("NewDocumentLoader(): %v", err)
	}
	docs, err := l.Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	got := make([]loaded, 0, len(docs))
	for _, d := range docs {
		got = append(got, loaded{Source: d.Source, StartLine: d.StartLine})
	}
	want := []loaded{
		{Source: a, StartLine: 1},
		{Source: a, StartLine: 3},
		{Source: filepath.Join(dir, "tree/x/one.yaml"), StartLine: 1},
		{Source: filepath.Join(dir, "tree/y/two.yml"), StartLine: 1},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Load(): -want, +got:\n%s", diff)
	}
}

func TestNewDocumentLoaderStdin(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		reason string
		input  string
		want   []Document
	}{
		"Stdin": {
			reason: "The source - reads the reader WithStdin supplies.",
			input:  "-",
			want: []Document{
				{Source: StdinSource, StartLine: 1, Raw: []byte("a: 1\n")},
				{Source: StdinSource, StartLine: 3, Raw: []byte("b: 2\n")},
			},
		},
		"StdinInList": {
			reason: "- works in a comma-separated list; a second - finds the reader already drained, as with os.Stdin.",
			input:  "-,-",
			want: []Document{
				{Source: StdinSource, StartLine: 1, Raw: []byte("a: 1\n")},
				{Source: StdinSource, StartLine: 3, Raw: []byte("b: 2\n")},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			l, err := NewDocumentLoader(tc.input, WithStdin(strings.NewReader("a: 1\n---\nb: 2\n")))
			if err != nil {
				t.Fatalf("\n%s\nNewDocumentLoader(): %v", tc.reason, err)
			}
			got, err := l.Load()
			if err != nil {
				t.Fatalf("\n%s\nLoad(): %v", tc.reason, err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("\n%s\nLoad(): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestNewDocumentLoaderDefaultStdin(t *testing.T) {
	l, err := NewDocumentLoader(StdinSource, WithStdin(nil))
	if err != nil {
		t.Fatalf("NewDocumentLoader(): %v", err)
	}
	m, ok := l.(multiDocumentLoader)
	if !ok || len(m) != 1 {
		t.Fatalf("NewDocumentLoader(): want one multiDocumentLoader entry, got %#v", l)
	}
	if m[0].stdin != os.Stdin {
		t.Errorf("WithStdin(nil): want os.Stdin, got %v", m[0].stdin)
	}
}

func TestDocumentLoaderErrors(t *testing.T) {
	t.Parallel()

	type args struct {
		// setup returns the loader input, and a func run between creating the
		// loader and calling Load.
		setup func(t *testing.T, dir string) (string, func())
	}

	type want struct {
		newErr  error
		loadErr error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"Missing": {
			reason: "A missing source fails loader creation.",
			args: args{setup: func(t *testing.T, dir string) (string, func()) {
				t.Helper()
				return filepath.Join(dir, "missing.yaml"), nil
			}},
			want: want{newErr: fs.ErrNotExist},
		},
		"MissingInList": {
			reason: "A missing source in a comma-separated list fails loader creation.",
			args: args{setup: func(t *testing.T, dir string) (string, func()) {
				t.Helper()
				good := filepath.Join(dir, "good.yaml")
				if err := os.WriteFile(good, []byte("a: 1\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return good + "," + filepath.Join(dir, "missing.yaml"), nil
			}},
			want: want{newErr: fs.ErrNotExist},
		},
		"InvalidFile": {
			reason: "An invalid document in one of several sources fails Load.",
			args: args{setup: func(t *testing.T, dir string) (string, func()) {
				t.Helper()
				good := filepath.Join(dir, "good.yaml")
				bad := filepath.Join(dir, "bad.yaml")
				if err := os.WriteFile(good, []byte("a: 1\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(bad, []byte("a: 1\n--- b\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return good + "," + bad, nil
			}},
			want: want{loadErr: cmpopts.AnyError},
		},
		"InvalidFileInDir": {
			reason: "An invalid document in a directory fails Load.",
			args: args{setup: func(t *testing.T, dir string) (string, func()) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte("a: 1\n--- b\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return dir, nil
			}},
			want: want{loadErr: cmpopts.AnyError},
		},
		"DirRemoved": {
			reason: "A directory removed after the loader is created fails Load.",
			args: args{setup: func(t *testing.T, dir string) (string, func()) {
				t.Helper()
				sub := filepath.Join(dir, "sub")
				if err := os.Mkdir(sub, 0o755); err != nil {
					t.Fatal(err)
				}
				return sub, func() {
					if err := os.RemoveAll(sub); err != nil {
						t.Fatal(err)
					}
				}
			}},
			want: want{loadErr: fs.ErrNotExist},
		},
		"FileRemoved": {
			reason: "A file removed after the loader is created fails Load.",
			args: args{setup: func(t *testing.T, dir string) (string, func()) {
				t.Helper()
				f := filepath.Join(dir, "gone.yaml")
				if err := os.WriteFile(f, []byte("a: 1\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return f, func() {
					if err := os.Remove(f); err != nil {
						t.Fatal(err)
					}
				}
			}},
			want: want{loadErr: fs.ErrNotExist},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			input, between := tc.args.setup(t, t.TempDir())

			l, err := NewDocumentLoader(input)
			if diff := cmp.Diff(tc.want.newErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nNewDocumentLoader(): -want error, +got error:\n%s", tc.reason, diff)
			}
			if err != nil {
				return
			}
			if between != nil {
				between()
			}
			_, err = l.Load()
			if diff := cmp.Diff(tc.want.loadErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nLoad(): -want error, +got error:\n%s", tc.reason, diff)
			}
		})
	}
}
