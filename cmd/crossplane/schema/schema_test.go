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

package schema

import (
	"bytes"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	"github.com/crossplane/cli/v2/internal/config"
	"github.com/crossplane/cli/v2/internal/terminal"
)

const projectYAML = `apiVersion: dev.crossplane.io/v1alpha1
kind: Project
metadata:
  name: test
spec:
  repository: registry.example.com/test
  schemas:
    languages: [json]
`

// newParser returns a kong parser for the schema commands that binds what the
// commands' Run methods ask for.
func newParser(t *testing.T, stdout *bytes.Buffer) *kong.Kong {
	t.Helper()
	var c Cmd
	parser, err := kong.New(&c,
		kong.Vars{"project_file": "crossplane-project.yaml"},
		kong.Bind(&config.Config{}),
		kong.BindTo(logging.NewNopLogger(), (*logging.Logger)(nil)),
		kong.BindTo(terminal.NewSpinnerPrinter(stdout, false), (*terminal.SpinnerPrinter)(nil)),
		kong.Writers(stdout, stdout),
	)
	if err != nil {
		t.Fatalf("kong.New(): %v", err)
	}
	return parser
}

func TestParse(t *testing.T) {
	t.Parallel()

	type args struct {
		cmd []string
	}
	type want struct {
		command string
		refresh bool
		err     error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"Update":            {reason: "update has no required arguments.", args: args{cmd: []string{"update"}}, want: want{command: "update"}},
		"UpdateRefresh":     {reason: "--refresh asks for fresh resolution.", args: args{cmd: []string{"update", "--refresh"}}, want: want{command: "update", refresh: true}},
		"Generate":          {reason: "generate takes the same source flags as update.", args: args{cmd: []string{"generate", "--cache-dir", "/tmp/x"}}, want: want{command: "generate"}},
		"Status":            {reason: "status is offline, so it takes only the project file.", args: args{cmd: []string{"status", "-f", "p.yaml"}}, want: want{command: "status"}},
		"StatusNoCacheFlag": {reason: "status must not accept source flags.", args: args{cmd: []string{"status", "--cache-dir", "/tmp/x"}}, want: want{err: cmpopts.AnyError}},
		"Clean":             {reason: "clean is offline too.", args: args{cmd: []string{"clean"}}, want: want{command: "clean"}},
		"RefreshOnlyUpdate": {reason: "--refresh belongs to update only.", args: args{cmd: []string{"generate", "--refresh"}}, want: want{err: cmpopts.AnyError}},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ctx, err := newParser(t, &bytes.Buffer{}).Parse(tc.args.cmd)
			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("%s\nParse(%v): -want err, +got err:\n%s", tc.reason, tc.args.cmd, diff)
			}
			if err != nil {
				return
			}
			got := want{command: ctx.Command()}
			if u, ok := reflect.TypeAssert[*updateCmd](ctx.Selected().Target.Addr()); ok {
				got.refresh = u.Refresh
			}
			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(want{})); diff != "" {
				t.Errorf("%s\nParse(%v): -want, +got:\n%s", tc.reason, tc.args.cmd, diff)
			}
		})
	}
}

func TestOfflineCommands(t *testing.T) {
	t.Parallel()

	type args struct {
		cmd []string
		// files are written under the project directory before running.
		files map[string]string
		// cache is whether the command takes a package cache directory.
		cache bool
	}
	type want struct {
		stdout string
		err    error
		// errMsg is a substring the error message must contain.
		errMsg string
		// schemaFiles is whether the schemas directory contains files afterwards.
		schemaFiles bool
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"StatusWithoutLock": {
			reason: "A project that never generated schemas has no drift.",
			args:   args{cmd: []string{"status"}},
			want:   want{stdout: "Schemas match the schema lock"},
		},
		"StatusReportsDriftedFile": {
			reason: "A file recorded in the lock but gone from disk is reported, and the command fails.",
			args: args{cmd: []string{"status"}, files: map[string]string{
				"schemas/.lock.json": `{"lockVersion":1,"sources":{"fs://apis":{"sourceHash":"h","flags":{"goRuntimeObjects":false},"files":[{"path":"json/a.json","sha256":"x"}]}}}`,
			}},
			want: want{stdout: "fs://apis: json/a.json (missing)", err: cmpopts.AnyError, errMsg: "1 generated files differ", schemaFiles: true},
		},
		"GenerateWithoutLock": {
			reason: "Generate only works from a lock, and says how to create one.",
			args:   args{cmd: []string{"generate"}, cache: true},
			want:   want{err: cmpopts.AnyError, errMsg: "no schema lock; run `crossplane schema update`"},
		},
		"CleanRemovesSchemas": {
			reason: "Clean removes the generated files and the lock.",
			args:   args{cmd: []string{"clean"}, files: map[string]string{"schemas/.lock.json": "{}", "schemas/json/a.json": "{}"}},
			want:   want{stdout: "Schemas cleaned"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			files := map[string]string{"crossplane-project.yaml": projectYAML}
			maps.Copy(files, tc.args.files)
			for p, c := range files {
				full := filepath.Join(dir, p)
				if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(full, []byte(c), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			cmd := append(slices.Clone(tc.args.cmd), "-f", filepath.Join(dir, "crossplane-project.yaml"))
			if tc.args.cache {
				cmd = append(cmd, "--cache-dir", filepath.Join(dir, "cache"))
			}

			var stdout bytes.Buffer
			parser := newParser(t, &stdout)
			ctx, err := parser.Parse(cmd)
			if err != nil {
				t.Fatalf("%s\nParse: %v", tc.reason, err)
			}
			err = ctx.Run()

			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("%s\nRun(): -want err, +got err:\n%s", tc.reason, diff)
			}
			if tc.want.errMsg != "" && (err == nil || !strings.Contains(err.Error(), tc.want.errMsg)) {
				t.Errorf("%s\nRun(): err = %v, want error containing %q", tc.reason, err, tc.want.errMsg)
			}
			if !strings.Contains(stdout.String(), tc.want.stdout) {
				t.Errorf("%s\nstdout = %q, want it to contain %q", tc.reason, stdout.String(), tc.want.stdout)
			}
			entries, _ := os.ReadDir(filepath.Join(dir, "schemas"))
			if diff := cmp.Diff(tc.want.schemaFiles, len(entries) > 0); diff != "" {
				t.Errorf("%s\nschema files left behind (-want +got):\n%s", tc.reason, diff)
			}
		})
	}
}
