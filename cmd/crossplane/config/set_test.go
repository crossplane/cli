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
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/spf13/afero"

	cfgpkg "github.com/crossplane/cli/v2/internal/config"
)

func TestSetRun(t *testing.T) {
	type args struct {
		// preExisting, if non-empty, is written to the path before Run.
		preExisting string
		path        ConfigPath
		key         string
		value       string
	}

	type want struct {
		// loaded is the Config that should be returned by Load after Run.
		// nil means we don't check (e.g. error cases).
		loaded *cfgpkg.Config
		err    error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"CreatesFile": {
			reason: "Setting a key when the file is missing should create it with version 1.",
			args: args{
				path:  "/c.yaml",
				key:   "features.disableBeta",
				value: "true",
			},
			want: want{
				loaded: &cfgpkg.Config{Version: 1, Features: cfgpkg.Features{DisableBeta: true}},
			},
		},
		"UpdatesExisting": {
			reason: "Setting a key should preserve other keys in the file.",
			args: args{
				preExisting: "version: 1\nfeatures:\n  enableAlpha: true\n",
				path:        "/c.yaml",
				key:         "features.disableBeta",
				value:       "true",
			},
			want: want{
				loaded: &cfgpkg.Config{Version: 1, Features: cfgpkg.Features{EnableAlpha: true, DisableBeta: true}},
			},
		},
		"UnsetExisting": {
			reason: "Setting a key to false should clear it without affecting other keys.",
			args: args{
				preExisting: "version: 1\nfeatures:\n  enableAlpha: true\n  disableBeta: true\n",
				path:        "/c.yaml",
				key:         "features.disableBeta",
				value:       "false",
			},
			want: want{
				loaded: &cfgpkg.Config{Version: 1, Features: cfgpkg.Features{EnableAlpha: true}},
			},
		},
		"UnknownKey": {
			reason: "An unknown key should return an error.",
			args: args{
				path:  "/c.yaml",
				key:   "features.bogus",
				value: "true",
			},
			want: want{
				err: cmpopts.AnyError,
			},
		},
		"InvalidBool": {
			reason: "An invalid bool value should return an error.",
			args: args{
				path:  "/c.yaml",
				key:   "features.enableAlpha",
				value: "yes",
			},
			want: want{
				err: cmpopts.AnyError,
			},
		},
		"EmptyPath": {
			reason: "An empty resolved path should return an error.",
			args: args{
				path:  "",
				key:   "features.enableAlpha",
				value: "true",
			},
			want: want{
				err: cmpopts.AnyError,
			},
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

			c := &setCmd{Key: tc.args.key, Value: tc.args.value, fs: fs}
			err := c.Run(tc.args.path)
			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nRun(): -want err, +got err:\n%s", tc.reason, diff)
			}
			if tc.want.err != nil {
				return
			}

			got, err := cfgpkg.Load(fs, string(tc.args.path))
			if err != nil {
				t.Fatalf("Load after Run returned error: %v", err)
			}
			if diff := cmp.Diff(tc.want.loaded, got); diff != "" {
				t.Errorf("\n%s\nLoad after Run: -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}
