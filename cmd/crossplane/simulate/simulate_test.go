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
	"errors"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/google/go-cmp/cmp"

	"github.com/crossplane/cli/v2/internal/plan"
)

func TestExitStatus(t *testing.T) {
	cases := map[string]struct {
		reason string
		plans  []*plan.ResourcePlan
		want   int
	}{
		"NoChanges": {
			reason: "Plans that change nothing should exit 0.",
			plans:  []*plan.ResourcePlan{{Action: plan.ActionNoOp}},
			want:   0,
		},
		"Changes": {
			reason: "A plan that would change a resource should exit 3.",
			plans:  []*plan.ResourcePlan{{Action: plan.ActionNoOp}, {Action: plan.ActionUpdate}},
			want:   3,
		},
		"ErroredOnly": {
			reason: "An errored plan isn't a change.",
			plans:  []*plan.ResourcePlan{{Action: plan.ActionUpdate, Error: "boom"}},
			want:   0,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := 0
			var ec kong.ExitCoder
			if err := exitStatus(tc.plans); errors.As(err, &ec) {
				if err.Error() != "" {
					t.Errorf("\n%s\nexitStatus(...): want a silent error, got %q", tc.reason, err)
				}
				got = ec.ExitCode()
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("\n%s\nexitStatus(...): -want exit code, +got:\n%s", tc.reason, diff)
			}
		})
	}
}
