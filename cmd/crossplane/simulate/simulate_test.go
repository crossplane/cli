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
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/crossplane/cli/v2/internal/plan"
	isimulate "github.com/crossplane/cli/v2/internal/simulate"
)

func TestExitStatus(t *testing.T) {
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
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// Exit as main does: a silent ExitError with its code, any
			// other error with 1.
			got := 0
			err := exitStatus(tc.args.plans)
			if code, ok := isimulate.SilentExit(err); ok {
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
