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

package dependency

import (
	"testing"

	"github.com/alecthomas/kong"
	"github.com/google/go-cmp/cmp"

	"github.com/crossplane/cli/v2/internal/git"
)

func TestUpdateCacheGitAuth(t *testing.T) {
	type args struct {
		cmd []string
		env map[string]string
	}
	type want struct {
		auth git.AuthProvider
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"Defaults": {
			reason: "Without flags the username defaults to x-access-token and there is no token.",
			args:   args{cmd: []string{"update-cache"}},
			want:   want{auth: &git.HTTPSAuthProvider{Username: "x-access-token"}},
		},
		"Flags": {
			reason: "The git flags configure the auth provider.",
			args:   args{cmd: []string{"update-cache", "--git-token", "t", "--git-username", "u"}},
			want:   want{auth: &git.HTTPSAuthProvider{Username: "u", Password: "t"}},
		},
		"Env": {
			reason: "The git environment variables configure the auth provider.",
			args: args{
				cmd: []string{"update-cache"},
				env: map[string]string{"CROSSPLANE_GIT_TOKEN": "t", "CROSSPLANE_GIT_USERNAME": "u"},
			},
			want: want{auth: &git.HTTPSAuthProvider{Username: "u", Password: "t"}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var c Cmd
			parser, err := kong.New(&c, kong.Vars{"project_file": "crossplane-project.yaml"})
			if err != nil {
				t.Fatalf("kong.New(): %v", err)
			}
			for k, v := range tc.args.env {
				t.Setenv(k, v)
			}
			if _, err := parser.Parse(tc.args.cmd); err != nil {
				t.Fatalf("%s\nParse(%v): %v", tc.reason, tc.args.cmd, err)
			}
			if diff := cmp.Diff(tc.want.auth, c.UpdateCache.gitAuth()); diff != "" {
				t.Errorf("%s\ngitAuth(): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}
