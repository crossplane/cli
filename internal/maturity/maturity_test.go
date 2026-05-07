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

package maturity

import (
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/google/go-cmp/cmp"
)

type stubRun struct{}

func (stubRun) Run() error { return nil }

type testCLI struct {
	GA struct {
		stubRun
	} `cmd:"" help:"GA help."`
	Beta struct {
		stubRun
	} `cmd:"" help:"Beta help." maturity:"beta"`
	Alpha struct {
		stubRun
	} `cmd:"" help:"Alpha help." maturity:"alpha"`
	Group struct {
		Inner struct {
			stubRun
		} `cmd:"" help:"Inner help."`
	} `cmd:"" help:"Group help." maturity:"alpha"`
}

func findNode(app *kong.Application, path string) *kong.Node {
	parts := strings.Split(path, "/")
	cur := app.Node
	for _, name := range parts {
		var next *kong.Node
		for _, c := range cur.Children {
			if c.Name == name {
				next = c
				break
			}
		}
		if next == nil {
			return nil
		}
		cur = next
	}
	return cur
}

func TestApply(t *testing.T) {
	type nodeWant struct {
		hidden     bool
		helpHas    []string
		helpHasNot []string
	}

	type args struct {
		enabled map[Level]bool
	}

	type want struct {
		nodes map[string]nodeWant
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"OnlyGAEnabled": {
			reason: "With only GA enabled, beta and alpha commands are hidden; an alpha-tagged group hides its children too.",
			args: args{
				enabled: map[Level]bool{},
			},
			want: want{
				nodes: map[string]nodeWant{
					"ga":          {hidden: false},
					"beta":        {hidden: true},
					"alpha":       {hidden: true},
					"group":       {hidden: true},
					"group/inner": {hidden: true, helpHas: []string{"ALPHA"}},
				},
			},
		},
		"BetaEnabled": {
			reason: "Enabling beta unhides beta commands but leaves alpha hidden.",
			args: args{
				enabled: map[Level]bool{LevelBeta: true},
			},
			want: want{
				nodes: map[string]nodeWant{
					"beta":  {hidden: false},
					"alpha": {hidden: true},
				},
			},
		},
		"AllEnabledShowsBanners": {
			reason: "With all levels enabled, non-GA nodes still get a banner prepended to their help; GA nodes do not.",
			args: args{
				enabled: map[Level]bool{LevelAlpha: true, LevelBeta: true},
			},
			want: want{
				nodes: map[string]nodeWant{
					"ga":    {hidden: false, helpHasNot: []string{"ALPHA", "BETA"}},
					"beta":  {hidden: false, helpHas: []string{"BETA", "Beta help."}},
					"alpha": {hidden: false, helpHas: []string{"ALPHA", "Alpha help."}},
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p, err := kong.New(&testCLI{}, kong.Name("test"), kong.Exit(func(int) {}))
			if err != nil {
				t.Fatalf("kong.New: %v", err)
			}

			Apply(p.Model, tc.args.enabled)

			for path, nw := range tc.want.nodes {
				n := findNode(p.Model, path)
				if n == nil {
					t.Errorf("\n%s\nApply(...): node %q not found in model", tc.reason, path)
					continue
				}
				if diff := cmp.Diff(nw.hidden, n.Hidden); diff != "" {
					t.Errorf("\n%s\nApply(...): node %q Hidden -want, +got:\n%s", tc.reason, path, diff)
				}
				for _, sub := range nw.helpHas {
					if !strings.Contains(n.Help, sub) {
						t.Errorf("\n%s\nApply(...): node %q help should contain %q, got: %q", tc.reason, path, sub, n.Help)
					}
				}
				for _, sub := range nw.helpHasNot {
					if strings.Contains(n.Help, sub) {
						t.Errorf("\n%s\nApply(...): node %q help should not contain %q, got: %q", tc.reason, path, sub, n.Help)
					}
				}
			}
		})
	}
}
