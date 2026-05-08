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

package main

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestConfigFlag(t *testing.T) {
	type want struct {
		path string
		err  error
	}

	cases := map[string]struct {
		reason string
		args   []string
		want   want
	}{
		"Empty": {
			reason: "No args should yield no path and no error.",
			args:   nil,
		},
		"NoConfigFlag": {
			reason: "Argv without --config should yield no path and no error.",
			args:   []string{"version"},
		},
		"EqualsForm": {
			reason: "--config=PATH should return PATH.",
			args:   []string{"--config=/tmp/config.yaml"},
			want:   want{path: "/tmp/config.yaml"},
		},
		"SpaceForm": {
			reason: "--config PATH should return PATH from the next argv.",
			args:   []string{"--config", "/tmp/config.yaml"},
			want:   want{path: "/tmp/config.yaml"},
		},
		"EmptyEquals": {
			reason: "--config= is an explicitly empty value and should return an error.",
			args:   []string{"--config="},
			want:   want{err: cmpopts.AnyError},
		},
		"MissingValue": {
			reason: "Trailing --config with no following argv should return an error.",
			args:   []string{"--config"},
			want:   want{err: cmpopts.AnyError},
		},
		"FirstWins": {
			reason: "If --config appears more than once, the first occurrence wins.",
			args:   []string{"--config=/first.yaml", "--config=/second.yaml"},
			want:   want{path: "/first.yaml"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := configFlag(tc.args)
			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nconfigFlag(%v): -want err, +got err:\n%s", tc.reason, tc.args, diff)
			}
			if diff := cmp.Diff(tc.want.path, got); diff != "" {
				t.Errorf("\n%s\nconfigFlag(%v): -want, +got:\n%s", tc.reason, tc.args, diff)
			}
		})
	}
}
