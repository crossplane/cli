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
	"strings"

	un "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	dt "github.com/crossplane/cli/v2/internal/diff/result"
	"github.com/crossplane/cli/v2/internal/plan"
)

// warningContextResource is the context key of an advisory that names the resource it is about.
const warningContextResource = "resource"

// resolveApplier returns the applier of the composite resource or claim resourceID names, whose
// cluster copy is live, when it is user-supplied: a top-level input PerformDiff was handed
// (userSupplied), with a cluster copy to apply to; otherwise nil. The composite resources
// DiffSingleResource is handed come from the cluster, so they are not user-supplied. A nested XR
// (depth above 0) is composed, so it takes no applier, even when its parent does, whether or not its parent exists yet. When the applier cannot be told and
// live records one, a warning names the appliers and imperative writers it records: the diff then may
// not show fields the manifest drops.
func (p *DefaultDiffProcessor) resolveApplier(live *un.Unstructured, depth int, resourceID string, userSupplied bool) *plan.Applier {
	if depth > 0 || !userSupplied || live == nil {
		return nil
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

	return &a
}
