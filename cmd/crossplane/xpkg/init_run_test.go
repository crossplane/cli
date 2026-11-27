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

package xpkg

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/kong"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/google/go-cmp/cmp"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
)

// writeFiles writes the given files (path relative to dir: content) to dir.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for p, content := range files {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// newTemplateRepo commits the given files to a new Git repository and returns
// its path.
func newTemplateRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	r, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	writeFiles(t, dir, files)
	if f, ok := files[initScript]; ok && f != "" {
		if err := os.Chmod(filepath.Join(dir, initScript), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	wt, err := r.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := wt.AddGlob("."); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Commit("init", &git.CommitOptions{Author: &object.Signature{Name: "test", Email: "test@example.org", When: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestInitCmdRun clones from local repositories over go-git's file transport,
// which runs git-upload-pack.
func TestInitCmdRun(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required to clone over the file transport")
	}

	type args struct {
		repo          map[string]string
		existing      map[string]string
		runInitScript bool
		directoryFile bool
	}
	type want struct {
		files  []string
		stdout []string
		// err builds the expected error from the per-test directory and
		// template paths.
		err func(dir, template string) error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"ClonesAndPrintsNotes": {
			reason: "The template should be cloned into the directory and its NOTES.txt printed.",
			args:   args{repo: map[string]string{"crossplane.yaml": "x", notes: "read me"}},
			want: want{
				files:  []string{"crossplane.yaml", notes},
				stdout: []string{`Initialized package "pkg"`, "read me"},
			},
		},
		"RunsInitScript": {
			reason: "With --run-init-script the template's init.sh should run with the package name and directory.",
			args: args{
				repo:          map[string]string{initScript: "#!/bin/sh\necho \"$1\" > \"$2/ran\"\n"},
				runInitScript: true,
			},
			want: want{files: []string{initScript, "ran"}},
		},
		"AllowsGitDir": {
			reason: "A directory containing only .git should be accepted.",
			args: args{
				repo:     map[string]string{"crossplane.yaml": "x"},
				existing: map[string]string{".git/HEAD": "ref"},
			},
			want: want{files: []string{"crossplane.yaml"}},
		},
		"NonEmptyDirectory": {
			reason: "A directory that already has files should be rejected before cloning.",
			args: args{
				repo:     map[string]string{"crossplane.yaml": "x"},
				existing: map[string]string{"stale.yaml": "x"},
			},
			want: want{err: func(dir, _ string) error {
				return errors.Errorf("directory %s is not empty, contains existing files/directories: %s", dir, "stale.yaml")
			}},
		},
		"DirectoryIsFile": {
			reason: "A --directory that is a file should be rejected.",
			args:   args{repo: map[string]string{"crossplane.yaml": "x"}, directoryFile: true},
			want:   want{err: func(dir, _ string) error { return errors.Errorf("path %s is not a directory", dir) }},
		},
		"CloneFails": {
			reason: "A template that isn't a repository should fail to clone.",
			want: want{err: func(_, template string) error {
				return errors.Wrapf(transport.ErrRepositoryNotFound, "failed to clone repository from %q", "file://"+template)
			}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			template := filepath.Join(t.TempDir(), "missing")
			if tc.args.repo != nil {
				template = newTemplateRepo(t, tc.args.repo)
			}

			// A directory that doesn't exist yet should be created.
			dir := filepath.Join(t.TempDir(), "new")
			writeFiles(t, dir, tc.args.existing)
			if tc.args.directoryFile {
				writeFiles(t, filepath.Dir(dir), map[string]string{"new": "x"})
			}

			stdout := &bytes.Buffer{}
			k := &kong.Context{Kong: &kong.Kong{Stdout: stdout, Stderr: stdout}}
			c := &initCmd{Name: "pkg", Template: "file://" + template, Directory: dir, RunInitScript: tc.args.runInitScript}

			err := c.Run(k, logging.NewNopLogger())
			var wantErr error
			if tc.want.err != nil {
				wantErr = tc.want.err(dir, template)
			}
			if diff := cmp.Diff(wantErr, err, test.EquateErrors()); diff != "" {
				t.Fatalf("\n%s\nRun(...): -want error, +got error:\n%s", tc.reason, diff)
			}
			if wantErr != nil {
				return
			}

			for _, f := range tc.want.files {
				if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
					t.Errorf("\n%s\nRun(...): want file %s in directory: %v", tc.reason, f, err)
				}
			}
			for _, s := range tc.want.stdout {
				if !strings.Contains(stdout.String(), s) {
					t.Errorf("\n%s\nRun(...): want stdout containing %q, got:\n%s", tc.reason, s, stdout)
				}
			}
			if tc.args.runInitScript {
				b, err := os.ReadFile(filepath.Join(dir, "ran"))
				if err != nil || strings.TrimSpace(string(b)) != "pkg" {
					t.Errorf("\n%s\nRun(...): want init.sh to record package name %q, got %q (%v)", tc.reason, "pkg", b, err)
				}
			}
		})
	}
}
