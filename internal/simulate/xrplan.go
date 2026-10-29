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
	"fmt"
	"maps"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	"github.com/crossplane/cli/v2/internal/diff/diffprocessor"
	dt "github.com/crossplane/cli/v2/internal/diff/result"
	"github.com/crossplane/cli/v2/internal/plan"
)

// EnsureFunc starts diff servers for the given resources' providers if they
// are not running yet. It is called outside the XR's deadline.
type EnsureFunc func(ctx context.Context, resources []*unstructured.Unstructured) error

// XRPlanner plans each composite resource's composed resources with their
// provider diff servers as the engine finishes rendering it, so an XR whose
// plans fail is reported as an error, whole, like a failed render.
//
// It keeps one plan per diff key, with the diff it planned (see ProviderPlans). A
// later XR that renders a held key, the same way or not, is not planned
// again: one plan cannot be right for two renderings, so a report uses a
// plan only for a diff that matches its held one, and the engine's
// render-overlap check fails the run for the differing one. Plans merge in
// the AfterXRDiff hook, before schema validation verdicts, so an XR that
// validation later fails has already contributed its plans.
//
// It is not safe for concurrent use; the engine calls AfterXRDiff
// sequentially.
type XRPlanner struct {
	planner *Planner
	keep    func(*unstructured.Unstructured) bool
	ensure  EnsureFunc

	plans map[string]*plan.ResourcePlan
	// held holds the diff each plan was planned from.
	held   map[string]*dt.ResourceDiff
	routed int
	// considered counts the resources AfterXRDiff was asked to route.
	considered int
}

// NewXRPlanner returns an XRPlanner that plans with p. keep and ensure may be nil.
func NewXRPlanner(p *Planner, keep func(*unstructured.Unstructured) bool, ensure EnsureFunc) *XRPlanner {
	return &XRPlanner{planner: p, keep: keep, ensure: ensure, plans: map[string]*plan.ResourcePlan{}, held: map[string]*dt.ResourceDiff{}}
}

// AfterXRDiff is a diffprocessor.WithAfterXRDiff hook. It fails the XR if any
// of its resources' plans errors, and merges none of its plans then. A
// resource an earlier XR's plan is held for is not planned again.
func (x *XRPlanner) AfterXRDiff(ctx context.Context, diffs map[string]*dt.ResourceDiff) error {
	fresh := map[string]*dt.ResourceDiff{}
	for key, d := range diffs {
		if d.DiffType == dt.DiffTypeRemoved || d.Desired.Raw == nil {
			continue
		}
		x.considered++
		if _, ok := x.held[key]; !ok {
			fresh[key] = d
		}
	}

	if x.ensure != nil {
		resources := make([]*unstructured.Unstructured, 0, len(fresh))
		for _, d := range fresh {
			resources = append(resources, d.Desired.Raw)
		}
		// Server start and image pulls are run-wide work, not the XR's.
		err := diffprocessor.OutsideXRDeadline(ctx, func(run context.Context) error { return x.ensure(run, resources) })
		if err != nil {
			return err
		}
	}

	plans := PlanDiffs(ctx, x.planner, x.keep, fresh)
	// Count what reached a server even when its plan failed, so the run still knows servers served it.
	x.routed += len(plans)

	var failed []string
	for _, p := range plans {
		if p.Error != "" {
			failed = append(failed, fmt.Sprintf("%s/%s: %s", p.Kind, p.Name, p.Error))
		}
	}
	if len(failed) > 0 {
		sort.Strings(failed)
		return errors.Errorf("cannot plan %d resources with their diff servers: %s", len(failed), strings.Join(failed, "; "))
	}

	for key, p := range plans {
		x.plans[key] = p
		x.held[key] = fresh[key]
	}

	return nil
}

// ProviderPlans returns a copy of the plans collected so far, with the diff
// each was planned from. Its maps are never nil.
func (x *XRPlanner) ProviderPlans() *ProviderPlans {
	return &ProviderPlans{Plans: maps.Clone(x.plans), Held: maps.Clone(x.held)}
}

// Routed returns how many resources went to a diff server, failed plans included.
func (x *XRPlanner) Routed() int {
	return x.routed
}

// Considered returns how many resources the planner was asked to route, so a
// run can tell having no diff servers from having nothing to plan.
func (x *XRPlanner) Considered() int {
	return x.considered
}

// ProviderPlans are a run's provider plans by diff key, each with the diff it
// was planned from. A nil *ProviderPlans means no provider plan ran.
type ProviderPlans struct {
	Plans map[string]*plan.ResourcePlan
	// Held holds the diff each plan was planned from, by the same keys.
	Held map[string]*dt.ResourceDiff
}

// For returns d's provider plan: the plan for its diff key when d renders the
// resource as the diff it was planned from did (diffprocessor.SameRendering),
// or nil. So a plan of one XR's rendering is never shown with another XR's
// differing rendering of the same resource, which the engine's render-overlap
// check reports instead.
func (p *ProviderPlans) For(d *dt.ResourceDiff) *plan.ResourcePlan {
	if p == nil {
		return nil
	}
	key := d.GetDiffKey()
	if !diffprocessor.SameRendering(p.Held[key], d) {
		return nil
	}
	return p.Plans[key]
}
