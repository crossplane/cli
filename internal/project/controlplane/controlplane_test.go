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

package controlplane

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/cli/v2/internal/project/certs"
)

func TestCrossplaneHelmValues(t *testing.T) {
	defaults := func() map[string]any {
		return map[string]any{
			"args": []any{"--enable-dependency-version-upgrades"},
			"registryCaBundleConfig": map[string]any{
				"name": "ca",
				"key":  certs.SecretKeyCACert,
			},
			"rbac": map[string]any{
				"clusterAdmin": true,
			},
		}
	}

	type args struct {
		defaultMRAP bool
		extraValues string
		extraFlags  []string
	}
	type want struct {
		values func() map[string]any
		err    bool
	}

	tcs := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"Defaults": {
			reason: "With no extra configuration the default values should be returned.",
			args:   args{defaultMRAP: true},
			want:   want{values: defaults},
		},
		"NoDefaultMRAP": {
			reason: "Disabling the default MRAP should null out the default activations.",
			args:   args{defaultMRAP: false},
			want: want{values: func() map[string]any {
				v := defaults()
				v["provider"] = map[string]any{"defaultActivations": nil}
				return v
			}},
		},
		"ExtraFlags": {
			reason: "Extra flags should be appended to the default args.",
			args: args{
				defaultMRAP: true,
				extraFlags:  []string{"--debug", "--enable-realtime-compositions"},
			},
			want: want{values: func() map[string]any {
				v := defaults()
				v["args"] = []any{"--enable-dependency-version-upgrades", "--debug", "--enable-realtime-compositions"}
				return v
			}},
		},
		"ExtraValuesDeepMerge": {
			reason: "Extra values should be deep-merged over the defaults, overriding them where they conflict.",
			args: args{
				defaultMRAP: false,
				extraValues: `
rbac:
  clusterAdmin: false
provider:
  defaultActivations: ["*.example.org"]
replicas: 2
`,
			},
			want: want{values: func() map[string]any {
				v := defaults()
				v["rbac"] = map[string]any{"clusterAdmin": false}
				v["provider"] = map[string]any{"defaultActivations": []any{"*.example.org"}}
				v["replicas"] = float64(2)
				return v
			}},
		},
		"ExtraValuesReplaceArgs": {
			reason: "Args in extra values should replace the default args, and extra flags should be appended to them.",
			args: args{
				defaultMRAP: true,
				extraValues: `args: ["--debug"]`,
				extraFlags:  []string{"--poll-interval=1m"},
			},
			want: want{values: func() map[string]any {
				v := defaults()
				v["args"] = []any{"--debug", "--poll-interval=1m"}
				return v
			}},
		},
		"ExtraValuesNullArgs": {
			reason: "Extra flags should still be added when extra values null out the default args.",
			args: args{
				defaultMRAP: true,
				extraValues: `args: null`,
				extraFlags:  []string{"--debug"},
			},
			want: want{values: func() map[string]any {
				v := defaults()
				v["args"] = []any{"--debug"}
				return v
			}},
		},
		"ExtraValuesInvalidArgs": {
			reason: "Extra flags cannot be added when extra values set args to a non-list.",
			args: args{
				defaultMRAP: true,
				extraValues: `args: "--debug"`,
				extraFlags:  []string{"--poll-interval=1m"},
			},
			want: want{err: true},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			var extra map[string]any
			if tc.args.extraValues != "" {
				if err := yaml.Unmarshal([]byte(tc.args.extraValues), &extra); err != nil {
					t.Fatal(err)
				}
			}

			got, err := crossplaneHelmValues("ca", true, tc.args.defaultMRAP, extra, tc.args.extraFlags)
			if tc.want.err {
				if err == nil {
					t.Errorf("\n%s\ncrossplaneHelmValues(...): expected error, got nil", tc.reason)
				}
				return
			}
			if err != nil {
				t.Fatalf("\n%s\ncrossplaneHelmValues(...): unexpected error: %v", tc.reason, err)
			}
			if diff := cmp.Diff(tc.want.values(), got); diff != "" {
				t.Errorf("\n%s\ncrossplaneHelmValues(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}
