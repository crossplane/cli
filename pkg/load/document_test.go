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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestSplitDocuments(t *testing.T) {
	type doc struct {
		StartLine int
		Raw       string
	}

	cases := map[string]struct {
		reason string
		input  string
		want   []doc
		err    bool
	}{
		"Single": {
			reason: "One document starts on line 1.",
			input:  "a: 1\nb: 2\n",
			want:   []doc{{StartLine: 1, Raw: "a: 1\nb: 2\n"}},
		},
		"Separated": {
			reason: "A document after a separator starts on the line after it.",
			input:  "a: 1\n---\nb: 2\n",
			want:   []doc{{StartLine: 1, Raw: "a: 1\n"}, {StartLine: 3, Raw: "b: 2\n"}},
		},
		"LeadingSeparator": {
			reason: "A leading separator stays at the start of the first document, as YamlStream keeps it.",
			input:  "---\na: 1\n---\nb: 2\n",
			want:   []doc{{StartLine: 1, Raw: "---\na: 1\n"}, {StartLine: 4, Raw: "b: 2\n"}},
		},
		"SeparatorWithComment": {
			reason: "A separator may carry a trailing comment.",
			input:  "a: 1\n--- # next\nb: 2\n",
			want:   []doc{{StartLine: 1, Raw: "a: 1\n"}, {StartLine: 3, Raw: "b: 2\n"}},
		},
		"LeadingComment": {
			reason: "Comments before the first document belong to it and keep its start at line 1.",
			input:  "# header\n# more\na: 1\n",
			want:   []doc{{StartLine: 1, Raw: "# header\n# more\na: 1\n"}},
		},
		"CommentBeforeSecond": {
			reason: "Comments after a separator belong to the next document.",
			input:  "a: 1\n---\n# about b\nb: 2\n",
			want:   []doc{{StartLine: 1, Raw: "a: 1\n"}, {StartLine: 3, Raw: "# about b\nb: 2\n"}},
		},
		"EmptyDocuments": {
			reason: "Back-to-back separators yield what YamlStream yields: a separator-only document and no empty ones.",
			input:  "a: 1\n---\n---\n---\nb: 2\n",
			want:   []doc{{StartLine: 1, Raw: "a: 1\n"}, {StartLine: 3, Raw: "---\n"}, {StartLine: 5, Raw: "b: 2\n"}},
		},
		"TrailingSeparator": {
			reason: "A trailing separator adds no document.",
			input:  "a: 1\n---\n",
			want:   []doc{{StartLine: 1, Raw: "a: 1\n"}},
		},
		"NoTrailingNewline": {
			reason: "A final line without a newline is normalised to end in one.",
			input:  "a: 1\n---\nb: 2",
			want:   []doc{{StartLine: 1, Raw: "a: 1\n"}, {StartLine: 3, Raw: "b: 2\n"}},
		},
		"CRLF": {
			reason: "Windows line endings are normalised as YamlStream normalises them, and lines still count.",
			input:  "a: 1\r\n---\r\nb: 2\r\n",
			want:   []doc{{StartLine: 1, Raw: "a: 1\n"}, {StartLine: 3, Raw: "b: 2\n"}},
		},
		"Empty": {
			reason: "An empty stream has no documents.",
			input:  "",
			want:   nil,
		},
		"InvalidSeparator": {
			reason: "Text after a separator, other than a comment, is an error, as it is for YamlStream.",
			input:  "a: 1\n--- b\n",
			err:    true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			docs, err := SplitDocuments("in.yaml", strings.NewReader(tc.input))
			_, streamErr := YamlStream(strings.NewReader(tc.input))
			if tc.err {
				if err == nil || streamErr == nil {
					t.Fatalf("\n%s\nSplitDocuments(): want error from both, got %v and YamlStream %v", tc.reason, err, streamErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("\n%s\nSplitDocuments(): %v", tc.reason, err)
			}

			var got []doc
			for _, d := range docs {
				if d.Source != "in.yaml" {
					t.Errorf("\n%s\nSource: want in.yaml, got %q", tc.reason, d.Source)
				}
				got = append(got, doc{StartLine: d.StartLine, Raw: string(d.Raw)})
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("\n%s\nSplitDocuments(): -want, +got:\n%s", tc.reason, diff)
			}

			// The documents must be exactly those YamlStream returns.
			stream, err := YamlStream(strings.NewReader(tc.input))
			if err != nil {
				t.Fatal(err)
			}
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

	if _, err := NewDocumentLoader(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Error("NewDocumentLoader(missing): want error, got nil")
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
