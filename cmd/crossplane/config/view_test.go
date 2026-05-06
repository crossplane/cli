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

package config

import (
	"bytes"
	"io"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/google/go-cmp/cmp"
	"github.com/spf13/afero"
)

func TestViewRun(t *testing.T) {
	type args struct {
		preExisting string
		path        ConfigPath
	}

	cases := map[string]struct {
		reason string
		args   args
		want   string
	}{
		"MissingFile": {
			reason: "View on a missing file should print the zero default.",
			args:   args{path: "/c.yaml"},
			want:   "version: 1\n",
		},
		"EmptyPath": {
			reason: "View with an empty path should still print the zero default (Load treats it as missing).",
			args:   args{path: ""},
			want:   "version: 1\n",
		},
		"Existing": {
			reason: "View on an existing file should print its parsed contents.",
			args: args{
				preExisting: "version: 1\nfeatures:\n  enableBeta: true\n",
				path:        "/c.yaml",
			},
			want: "features:\n  enableBeta: true\nversion: 1\n",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			if tc.args.preExisting != "" {
				if err := afero.WriteFile(fs, string(tc.args.path), []byte(tc.args.preExisting), 0o600); err != nil {
					t.Fatalf("setting up pre-existing file: %v", err)
				}
			}

			buf := &bytes.Buffer{}
			kctx := &kong.Context{Kong: &kong.Kong{Stdout: buf, Stderr: io.Discard}}

			c := &viewCmd{fs: fs}
			if err := c.Run(kctx, tc.args.path); err != nil {
				t.Fatalf("Run returned error: %v", err)
			}
			if diff := cmp.Diff(tc.want, buf.String()); diff != "" {
				t.Errorf("\n%s\nRun output: -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}
