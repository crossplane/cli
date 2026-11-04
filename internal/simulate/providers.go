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
// servers p routes to, keyed by diff key, for FromCompReport
// to merge.
//
// Removals are not planned: a provider plans no deletes, so the engine's
// stands. An equal diff is planned only when keep reports true for it (nil
// keeps none), which leaves out the XR's own diff the engine always stores. A
// kind no diff server serves is not planned either, so its engine plan stands
// and is never approximate.
func PlanDiffs(ctx context.Context, p *Planner, keep func(*unstructured.Unstructured) bool, diffs map[string]*dt.ResourceDiff) map[string]*plan.ResourcePlan {
	plans := map[string]*plan.ResourcePlan{}

	// Plans run sequentially, like resource simulate.
	// TODO(jcogilvie): plan the compositions in parallel with an errgroup.
	for key, d := range diffs {
		if d.DiffType == dt.DiffTypeRemoved || d.Desired.Raw == nil || !p.CanPlan(d.Gvk.GroupKind()) {
			continue
		}
		if d.DiffType == dt.DiffTypeEqual && (keep == nil || !keep(d.Desired.Raw)) {
			continue
		}

		// Copies, because PlanWithLive merges external tags into desired.
		// Desired.Raw is the server-side dry run's result, and Current.Raw
		// the live resource the engine matched, nil for a create.
		plans[key] = p.PlanWithLive(ctx, d.Desired.Raw.DeepCopy(), d.Current.Raw.DeepCopy())
	}

	return plans
}

func isChange(a plan.Action) bool {
	switch a {
	case plan.ActionCreate, plan.ActionUpdate, plan.ActionReplace, plan.ActionDelete:
		return true
	case plan.ActionNoOp:
	}

	return false
}
