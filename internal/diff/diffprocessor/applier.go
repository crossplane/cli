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

package diffprocessor

import (
	"context"
	"strings"

	un "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	dt "github.com/crossplane/cli/v2/internal/diff/result"
	"github.com/crossplane/cli/v2/internal/plan"
)

// warningContextResource is the context key of an advisory that names the resource it is about.
const warningContextResource = "resource"

// userSuppliedKey marks a context whose top-level composite resources and claims the user supplied
// as input, so they are dry-run applied as whoever applies them. PerformDiff sets it; the composite
// resources DiffSingleResource is handed come from the cluster, so it does not.
type userSuppliedKey struct{}

// applierKey carries the plan.Applier of the user-supplied composite resource or claim being diffed,
// for CalculateDiff's XR call. It is absent for composed resources' diffs, nested XRs included.
type applierKey struct{}

// withUserSupplied marks ctx's top-level composite resources and claims as the user's input.
func withUserSupplied(ctx context.Context) context.Context {
	return context.WithValue(ctx, userSuppliedKey{}, true)
}

// applierFrom returns the applier withApplier put on ctx, if any.
func applierFrom(ctx context.Context) (plan.Applier, bool) {
	a, ok := ctx.Value(applierKey{}).(*plan.Applier)
	if !ok || a == nil {
		return plan.Applier{}, false
	}

	return *a, true
}

// withApplier returns ctx carrying the applier of the composite resource or claim resourceID names,
// whose cluster copy is live, when it is user-supplied: a top-level input of a context
// withUserSupplied marked, with a cluster copy to apply to. A nested XR (depth above 0) is composed, so
// its context carries no applier, even when its parent's does, whether or not its parent exists yet. When the applier cannot be told and
// live records one, a warning names the appliers and imperative writers it records: the diff then may
// not show fields the manifest drops.
func (p *DefaultDiffProcessor) withApplier(ctx context.Context, live *un.Unstructured, depth int, resourceID string) context.Context {
	supplied, _ := ctx.Value(userSuppliedKey{}).(bool)
	if depth > 0 || !supplied || live == nil {
		return context.WithValue(ctx, applierKey{}, (*plan.Applier)(nil))
	}

	a := plan.ResolveApplier(live, p.config.Applier)
	if a.Mode == plan.ApplyUndetermined && a.HasCandidates() {
		msg := "Cannot tell which field manager applies this resource"
		wctx := map[string]string{
			warningContextResource: resourceID,
			"candidates":           a.DescribeCandidates(),
		}
		if ns := live.GetNamespace(); ns != "" {
			wctx[ctxNamespace] = ns
		}
		if len(a.ImperativeWriters) > 0 {
			msg += ", since imperative writers (managers that updated it without applying, such as kubectl create or edit, or a controller) wrote it too"
			wctx["imperativeWriters"] = strings.Join(a.ImperativeWriters, ", ")
		}

		p.config.Advisories.Raise(dt.OutputWarning{
			Message: msg + ", so fields its manifest drops may not be shown as removed; " +
				"name the applier with --field-manager, and --server-side or --server-side=false",
			Context: wctx,
		})
	}

	return context.WithValue(ctx, applierKey{}, &a)
}
