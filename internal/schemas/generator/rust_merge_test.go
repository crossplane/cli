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

package generator

import (
	"strings"
	"testing"

	"github.com/spf13/afero"
)

func TestRustMergeGeneratedSchemas(t *testing.T) {
	t.Parallel()

	part := func(t *testing.T, files map[string]string) afero.Fs {
		t.Helper()
		fsys := afero.NewMemMapFs()
		for p, content := range files {
			if err := afero.WriteFile(fsys, p, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return fsys
	}

	cases := map[string]struct {
		reason  string
		parts   []map[string]string
		wantErr string
		// wantLib are lines src/lib.rs must contain.
		wantLib []string
	}{
		"ModuleTreesAreRebuilt": {
			reason: "Each source's lib.rs, mod.rs and Cargo.toml describe only that source, so they must be rebuilt rather than conflict.",
			parts: []map[string]string{
				{"models/Cargo.toml": "a", "models/src/lib.rs": "pub mod com;", "models/src/com/mod.rs": "a", "models/src/com/widget.rs": "W"},
				{"models/Cargo.toml": "b", "models/src/lib.rs": "pub mod io;", "models/src/io/mod.rs": "b", "models/src/io/bucket.rs": "B"},
			},
			wantLib: []string{"pub mod com;", "pub mod io;"},
		},
		"SharedModelsMustMatch": {
			reason: "A model two sources emit must be byte-identical.",
			parts: []map[string]string{
				{"models/src/io/objectmeta.rs": "one"},
				{"models/src/io/objectmeta.rs": "two"},
			},
			wantErr: "conflicting generated Rust file src/io/objectmeta.rs",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			parts := make([]afero.Fs, 0, len(tc.parts))
			for _, files := range tc.parts {
				parts = append(parts, part(t, files))
			}
			merged, err := rustGenerator{}.MergeGeneratedSchemas(parts)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("%s\nwant error containing %q, got %v", tc.reason, tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("%s\nMergeGeneratedSchemas(...): %v", tc.reason, err)
			}
			lib, err := afero.ReadFile(merged, "models/src/lib.rs")
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.wantLib {
				if !strings.Contains(string(lib), want) {
					t.Errorf("%s\nsrc/lib.rs lacks %q:\n%s", tc.reason, want, lib)
				}
			}
		})
	}
}
