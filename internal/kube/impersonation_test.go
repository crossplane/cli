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

package kube

import (
	"testing"

	"github.com/alecthomas/kong"
	"github.com/google/go-cmp/cmp"
	"k8s.io/client-go/rest"
)

func TestApply(t *testing.T) {
	type args struct {
		flags ImpersonationFlags
		cfg   *rest.Config
	}
	type want struct {
		impersonate rest.ImpersonationConfig
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"Empty": {
			reason: "No flags set should leave Impersonate empty.",
			args:   args{flags: ImpersonationFlags{}, cfg: &rest.Config{}},
			want:   want{impersonate: rest.ImpersonationConfig{}},
		},
		"UserOnly": {
			reason: "Only --as sets UserName.",
			args:   args{flags: ImpersonationFlags{As: "jane@example.com"}, cfg: &rest.Config{}},
			want:   want{impersonate: rest.ImpersonationConfig{UserName: "jane@example.com"}},
		},
		"GroupsOnly": {
			reason: "Only --as-group sets Groups.",
			args:   args{flags: ImpersonationFlags{AsGroup: []string{"team-a", "team-b"}}, cfg: &rest.Config{}},
			want:   want{impersonate: rest.ImpersonationConfig{Groups: []string{"team-a", "team-b"}}},
		},
		"UIDOnly": {
			reason: "Only --as-uid sets UID.",
			args:   args{flags: ImpersonationFlags{AsUID: "1000"}, cfg: &rest.Config{}},
			want:   want{impersonate: rest.ImpersonationConfig{UID: "1000"}},
		},
		"All": {
			reason: "All flags set all fields.",
			args: args{
				flags: ImpersonationFlags{As: "system:serviceaccount:team-a:reader", AsGroup: []string{"team-a-admins"}, AsUID: "42"},
				cfg:   &rest.Config{},
			},
			want: want{impersonate: rest.ImpersonationConfig{UserName: "system:serviceaccount:team-a:reader", Groups: []string{"team-a-admins"}, UID: "42"}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tc.args.flags.Apply(tc.args.cfg)
			if diff := cmp.Diff(tc.want.impersonate, tc.args.cfg.Impersonate); diff != "" {
				t.Errorf("%s\nApply(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestApplyNilConfig(_ *testing.T) {
	// Must not panic on a nil config.
	ImpersonationFlags{As: "jane"}.Apply(nil)
}

func TestImpersonationFlagsParse(t *testing.T) {
	cases := map[string]struct {
		reason string
		args   []string
		want   ImpersonationFlags
	}{
		"Repeatable": {
			reason: "--as-group can be repeated to specify multiple groups.",
			args:   []string{"--as=jane", "--as-group=team-a", "--as-group=team-b", "--as-uid=42"},
			want:   ImpersonationFlags{As: "jane", AsGroup: []string{"team-a", "team-b"}, AsUID: "42"},
		},
		"NoCommaSplit": {
			reason: "sep:none means a comma is part of the group name, matching kubectl.",
			args:   []string{"--as-group=team-a,team-b"},
			want:   ImpersonationFlags{AsGroup: []string{"team-a,team-b"}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var cli struct {
				Impersonation ImpersonationFlags `embed:""`
			}
			p, err := kong.New(&cli)
			if err != nil {
				t.Fatalf("%s\nkong.New(): unexpected error: %v", tc.reason, err)
			}
			if _, err := p.Parse(tc.args); err != nil {
				t.Fatalf("%s\nParse(%v): unexpected error: %v", tc.reason, tc.args, err)
			}
			if diff := cmp.Diff(tc.want, cli.Impersonation); diff != "" {
				t.Errorf("%s\nParse(%v): -want, +got:\n%s", tc.reason, tc.args, diff)
			}
		})
	}
}
