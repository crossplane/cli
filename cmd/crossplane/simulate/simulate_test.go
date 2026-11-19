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

package simulate

import (
	"bytes"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/google/go-cmp/cmp"

	"github.com/crossplane/cli/v2/internal/exitcode"
	"github.com/crossplane/cli/v2/internal/health"
	"github.com/crossplane/cli/v2/internal/plan"
)

func TestExitStatus(t *testing.T) {
	failing := &health.Health{State: health.Failing}

	type args struct {
		plans []*plan.ResourcePlan
	}
	cases := map[string]struct {
		reason string
		args   args
		want   int
	}{
		"NoChanges": {
			reason: "Plans that change nothing should exit 0.",
			args:   args{plans: []*plan.ResourcePlan{{Action: plan.ActionNoOp}}},
			want:   0,
		},
		"Changes": {
			reason: "A plan that would change a resource should exit 3.",
			args:   args{plans: []*plan.ResourcePlan{{Action: plan.ActionNoOp}, {Action: plan.ActionUpdate}}},
			want:   3,
		},
		"ErroredOnly": {
			reason: "A plan that errored should exit 1.",
			args:   args{plans: []*plan.ResourcePlan{{Action: plan.ActionUpdate, Error: "boom"}}},
			want:   1,
		},
		"ErroredWithChanges": {
			reason: "An errored plan should exit 1, not 3, even when other plans have changes.",
			args:   args{plans: []*plan.ResourcePlan{{Action: plan.ActionUpdate}, {Error: "boom"}}},
			want:   1,
		},
		"FailingNoChanges": {
			reason: "A failing resource with no changes should exit 4.",
			args:   args{plans: []*plan.ResourcePlan{{Action: plan.ActionNoOp, Health: failing}}},
			want:   4,
		},
		"FailingWithChanges": {
			reason: "Changes and a failing resource should exit 7, 3 and 4 combined.",
			args:   args{plans: []*plan.ResourcePlan{{Action: plan.ActionUpdate}, {Action: plan.ActionNoOp, Health: failing}}},
			want:   7,
		},
		"NotReadyAndPaused": {
			reason: "Not ready and paused resources should not set exit bit 4.",
			args: args{plans: []*plan.ResourcePlan{
				{Action: plan.ActionNoOp, Health: &health.Health{State: health.NotReady}},
				{Action: plan.ActionNoOp, Health: &health.Health{State: health.Paused}},
			}},
			want: 0,
		},
		"ErroredWithFailing": {
			reason: "An errored plan should exit 1, not 4 or 7, even when a resource is failing.",
			args:   args{plans: []*plan.ResourcePlan{{Action: plan.ActionUpdate, Health: failing}, {Error: "boom"}}},
			want:   1,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// Exit as main does: a silent ExitError with its code, any
			// other error with 1.
			got := 0
			err := exitStatus(tc.args.plans)
			if code, ok := exitcode.SilentExit(err); ok {
				got = code
			} else if err != nil {
				got = 1
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("\n%s\nexitStatus(...): -want exit code, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestHelp(t *testing.T) {
	type args struct {
		want string
	}
	cases := map[string]struct {
		reason string
		args   args
		want   bool
	}{
		"Detail": {
			reason: "Help should include the command description.",
			args:   args{want: "-f` (`--filename`)"},
			want:   true,
		},
		"Examples": {
			reason: "Help should include the examples section.",
			args:   args{want: "## Examples"},
			want:   true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := strings.Contains((&Cmd{}).Help(), tc.args.want)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("\n%s\nHelp() contains %q: -want, +got:\n%s", tc.reason, tc.args.want, diff)
			}
		})
	}
}

func TestRendererAndReport(t *testing.T) {
	// A failing resource whose message is too long for one line: plan output
	// cuts off its tail unless --full-messages is set.
	plans := []*plan.ResourcePlan{{
		APIVersion: "v1", Kind: "ConfigMap", Name: "cm", Action: plan.ActionNoOp,
		Health: &health.Health{State: health.Failing, Synced: &health.Condition{Status: "False", Reason: "ReconcileError", Message: strings.Repeat("x ", 200) + "tail"}},
	}}

	type args struct {
		args []string
	}
	type want struct {
		stderr     string
		fullOutput bool
		code       int
	}
	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"PlanCut": {
			reason: "Plan output cuts the message to one line and exits 4 for the failing resource.",
			args:   args{args: []string{"-f", "x.yaml"}},
			want:   want{code: 4},
		},
		"PlanFullMessages": {
			reason: "--full-messages reaches the plan renderer, which prints the message in full.",
			args:   args{args: []string{"-f", "x.yaml", "--full-messages"}},
			want:   want{fullOutput: true, code: 4},
		},
		"SetWithJSON": {
			reason: "json always carries messages in full, so --full-messages is ignored with a warning.",
			args:   args{args: []string{"-f", "x.yaml", "--full-messages", "-o", "json"}},
			want:   want{stderr: "Warning: ignoring --full-messages; -o json always prints messages in full.\n", fullOutput: true, code: 4},
		},
		"SetWithYAML": {
			reason: "yaml always carries messages in full, so --full-messages is ignored with a warning.",
			args:   args{args: []string{"-f", "x.yaml", "--full-messages", "-o", "yaml"}},
			want:   want{stderr: "Warning: ignoring --full-messages; -o yaml always prints messages in full.\n", fullOutput: true, code: 4},
		},
		"UnsetWithJSON": {
			reason: "--full-messages left at its default isn't warned about.",
			args:   args{args: []string{"-f", "x.yaml", "-o", "json"}},
			want:   want{fullOutput: true, code: 4},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var cli struct {
				Simulate Cmd `cmd:""`
			}
			var stdout, stderr bytes.Buffer
			parser, err := kong.New(&cli, kong.Vars{"project_file": "crossplane-project.yaml"}, kong.Exit(func(int) {}), kong.Writers(&stdout, &stderr))
			if err != nil {
				t.Fatalf("kong.New(...): %v", err)
			}
			k, err := parser.Parse(append([]string{"simulate"}, tc.args.args...))
			if err != nil {
				t.Fatalf("Parse(%v): %v", tc.args.args, err)
			}

			r, err := cli.Simulate.renderer(k)
			if err != nil {
				t.Fatalf("\n%s\nrenderer(...): unexpected error: %v", tc.reason, err)
			}
			err = report(k, r, plans)
			code := 0
			if c, ok := exitcode.SilentExit(err); ok {
				code = c
			} else if err != nil {
				code = 1
			}
			got := want{stderr: stderr.String(), fullOutput: strings.Contains(stdout.String(), "tail"), code: code}
			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(want{})); diff != "" {
				t.Errorf("\n%s\nrenderer(...) and report(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}
