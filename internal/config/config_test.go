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
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/spf13/afero"
)

func TestLoad(t *testing.T) {
	type args struct {
		fs   afero.Fs
		path string
	}

	type want struct {
		cfg *Config
		err error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"EmptyPath": {
			reason: "An empty path should yield a zero Config without error.",
			args: args{
				fs:   afero.NewMemMapFs(),
				path: "",
			},
			want: want{
				cfg: &Config{Version: version1},
			},
		},
		"MissingFile": {
			reason: "A path that does not exist should yield a zero Config without error.",
			args: args{
				fs:   afero.NewMemMapFs(),
				path: "/nope.yaml",
			},
			want: want{
				cfg: &Config{Version: version1},
			},
		},
		"Valid": {
			reason: "A valid config file should be parsed into a Config.",
			args: args{
				fs: func() afero.Fs {
					fs := afero.NewMemMapFs()
					_ = afero.WriteFile(fs, "/config.yaml", []byte("version: 1\nfeatures:\n  disableBeta: true\n  enableAlpha: false\n"), 0o600)
					return fs
				}(),
				path: "/config.yaml",
			},
			want: want{
				cfg: &Config{
					Version:  1,
					Features: Features{DisableBeta: true},
				},
			},
		},
		"UnsupportedVersion": {
			reason: "A config file with an unsupported version should return an error.",
			args: args{
				fs: func() afero.Fs {
					fs := afero.NewMemMapFs()
					_ = afero.WriteFile(fs, "/config.yaml", []byte("version: 2\n"), 0o600)
					return fs
				}(),
				path: "/config.yaml",
			},
			want: want{
				err: cmpopts.AnyError,
			},
		},
		"MissingVersion": {
			reason: "A config file without a version should return an error.",
			args: args{
				fs: func() afero.Fs {
					fs := afero.NewMemMapFs()
					_ = afero.WriteFile(fs, "/config.yaml", []byte("features:\n  enableAlpha: true\n"), 0o600)
					return fs
				}(),
				path: "/config.yaml",
			},
			want: want{
				err: cmpopts.AnyError,
			},
		},
		"InvalidYAML": {
			reason: "A malformed YAML file should return an error.",
			args: args{
				fs: func() afero.Fs {
					fs := afero.NewMemMapFs()
					_ = afero.WriteFile(fs, "/config.yaml", []byte("version: : :\n"), 0o600)
					return fs
				}(),
				path: "/config.yaml",
			},
			want: want{
				err: cmpopts.AnyError,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := Load(tc.args.fs, tc.args.path)
			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nLoad(...): -want err, +got err:\n%s", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.cfg, got); diff != "" {
				t.Errorf("\n%s\nLoad(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestSave(t *testing.T) {
	type args struct {
		fs   afero.Fs
		path string
		cfg  *Config
	}

	type want struct {
		// loaded is the Config that should be returned when Load reads the file
		// back. nil means we don't check (e.g. error cases).
		loaded *Config
		// mode is the expected file mode after the save. 0 means we don't check.
		mode os.FileMode
		err  error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"RoundTrip": {
			reason: "Saving a valid Config should produce a file that Load reads back as the same Config.",
			args: args{
				fs:   afero.NewMemMapFs(),
				path: "/c.yaml",
				cfg:  &Config{Version: 1, Features: Features{DisableBeta: true}},
			},
			want: want{
				loaded: &Config{Version: 1, Features: Features{DisableBeta: true}},
				mode:   0o600,
			},
		},
		"CreatesParentDir": {
			reason: "Save should create missing parent directories.",
			args: args{
				fs:   afero.NewMemMapFs(),
				path: "/a/b/c/config.yaml",
				cfg:  &Config{Version: 1},
			},
			want: want{
				loaded: &Config{Version: 1},
				mode:   0o600,
			},
		},
		"OverwritesExisting": {
			reason: "Save should overwrite an existing file.",
			args: args{
				fs: func() afero.Fs {
					fs := afero.NewMemMapFs()
					_ = afero.WriteFile(fs, "/c.yaml", []byte("version: 1\nfeatures:\n  enableAlpha: true\n"), 0o600)
					return fs
				}(),
				path: "/c.yaml",
				cfg:  &Config{Version: 1, Features: Features{DisableBeta: true}},
			},
			want: want{
				loaded: &Config{Version: 1, Features: Features{DisableBeta: true}},
				mode:   0o600,
			},
		},
		"DefaultsVersion": {
			reason: "Saving a Config with Version 0 should default the on-disk version to 1.",
			args: args{
				fs:   afero.NewMemMapFs(),
				path: "/c.yaml",
				cfg:  &Config{},
			},
			want: want{
				loaded: &Config{Version: 1},
				mode:   0o600,
			},
		},
		"EmptyPath": {
			reason: "Saving to an empty path should return an error.",
			args: args{
				fs:   afero.NewMemMapFs(),
				path: "",
				cfg:  &Config{Version: 1},
			},
			want: want{
				err: cmpopts.AnyError,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := Save(tc.args.fs, tc.args.path, tc.args.cfg)
			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nSave(...): -want err, +got err:\n%s", tc.reason, diff)
			}
			if tc.want.err != nil {
				return
			}

			got, err := Load(tc.args.fs, tc.args.path)
			if err != nil {
				t.Fatalf("Load after Save returned error: %v", err)
			}
			if diff := cmp.Diff(tc.want.loaded, got); diff != "" {
				t.Errorf("\n%s\nLoad after Save: -want, +got:\n%s", tc.reason, diff)
			}

			if tc.want.mode != 0 {
				info, err := tc.args.fs.Stat(tc.args.path)
				if err != nil {
					t.Fatalf("Stat after Save returned error: %v", err)
				}
				if info.Mode().Perm() != tc.want.mode {
					t.Errorf("file mode: want %o, got %o", tc.want.mode, info.Mode().Perm())
				}
			}
		})
	}
}

func TestResolvePath(t *testing.T) {
	type args struct {
		flag string
		env  string
		xdg  string
		home string
	}

	cases := map[string]struct {
		reason string
		args   args
		want   string
	}{
		"FlagWins": {
			reason: "When the flag is set, it should be returned regardless of env or default.",
			args:   args{flag: "/flag.yaml", env: "/env.yaml", xdg: "/x", home: "/h"},
			want:   "/flag.yaml",
		},
		"EnvOverDefault": {
			reason: "With no flag, the env var should be returned over the default.",
			args:   args{env: "/env.yaml", xdg: "/x", home: "/h"},
			want:   "/env.yaml",
		},
		"XDGDefault": {
			reason: "With no flag and no env, XDG_CONFIG_HOME should drive the default.",
			args:   args{xdg: "/x", home: "/h"},
			want:   "/x/crossplane/config.yaml",
		},
		"HomeFallback": {
			reason: "With no flag, no env, and no XDG, $HOME/.config should drive the default.",
			args:   args{home: "/h"},
			want:   "/h/.config/crossplane/config.yaml",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("CROSSPLANE_CONFIG", tc.args.env)
			t.Setenv("XDG_CONFIG_HOME", tc.args.xdg)
			t.Setenv("HOME", tc.args.home)
			got := ResolvePath(tc.args.flag)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("\n%s\nResolvePath(%q): -want, +got:\n%s", tc.reason, tc.args.flag, diff)
			}
		})
	}
}
