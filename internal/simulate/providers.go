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
	"context"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	dt "github.com/crossplane/cli/v2/internal/diff/result"
	"github.com/crossplane/cli/v2/internal/plan"
)

// PlanDiffs plans the engine's resource diffs against the provider diff
// servers p routes to, keyed by diff key, for FromXRReport and FromCompReport
// to merge. Each key across sets is planned once.
//
// Removals are not planned: a provider plans no deletes, so the engine's
// stands. An equal diff is planned only when keep reports true for it (nil
// keeps none), which leaves out the XR's own diff the engine always stores. A
// kind no diff server serves is not planned either, so its engine plan stands
// and is never approximate; a kept equal diff of such a kind gets a no-op
// entry, which the merge drops.
func PlanDiffs(ctx context.Context, p *Planner, keep func(*unstructured.Unstructured) bool, sets ...map[string]*dt.ResourceDiff) map[string]*plan.ResourcePlan {
	plans := map[string]*plan.ResourcePlan{}

	// ponytail: sequential, as resource simulate's loop is; parallelize with
	// errgroup if large projects are slow.
	for _, diffs := range sets {
		for key, d := range diffs {
			if _, done := plans[key]; done || d.DiffType == dt.DiffTypeRemoved || d.Desired.Raw == nil {
				continue
			}
			if d.DiffType == dt.DiffTypeEqual && (keep == nil || !keep(d.Desired.Raw)) {
				continue
			}
			if !p.CanPlan(d.Gvk.GroupKind()) {
				if d.DiffType == dt.DiffTypeEqual {
					noop := plan.NewResourcePlan(d.Desired.Raw)
					noop.Action = plan.ActionNoOp
					plans[key] = noop
				}
				continue
			}

			// Copies, because PlanWithLive merges external tags into desired.
			// Desired.Raw is the server-side dry run's result, and Current.Raw
			// the live resource the engine matched, nil for a create.
			plans[key] = p.PlanWithLive(ctx, d.Desired.Raw.DeepCopy(), d.Current.Raw.DeepCopy())
		}
	}

	return plans
}

// Changed reports whether res plans any change: a plan that is not an error
// and creates, updates, replaces or deletes. With provider plans merged in it
// is the run's verdict, since a provider can find drift the cluster cannot.
func Changed(res ProjectResult) bool {
	for _, p := range res.Plans {
		if p.Error == "" && isChange(p.Action) {
			return true
		}
	}

	return false
}

func isChange(a plan.Action) bool {
	switch a {
	case plan.ActionCreate, plan.ActionUpdate, plan.ActionReplace, plan.ActionDelete:
		return true
	case plan.ActionNoOp:
	}

	return false
}
