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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	"github.com/crossplane/cli/v2/internal/diff/diffprocessor"
	dt "github.com/crossplane/cli/v2/internal/diff/result"
	"github.com/crossplane/cli/v2/internal/plan"
)

// EnsureServers starts diff servers for the given provider images if they
// are not running yet. It is called outside the XR's deadline.
type EnsureServers func(ctx context.Context, resources []*unstructured.Unstructured) error

// XRPlanner plans each composite resource's composed resources with their
// provider diff servers as the engine finishes rendering it, so an XR whose
// plans fail is reported as an error, whole, like a failed render.
//
// It keeps one plan per diff key, with the diff it planned. A later XR that
// renders a held key the same way is not planned again; one that renders it
// differently fails with a conflict error, since no one plan is correct for
// both. Plans merge in the AfterXRDiff hook, before schema validation
// verdicts, so an XR that validation later fails has already contributed its
// plans.
//
// It is not safe for concurrent use; the engine calls AfterXRDiff
// sequentially.
type XRPlanner struct {
	planner *Planner
	keep    func(*unstructured.Unstructured) bool
	ensure  EnsureServers

	plans map[string]*plan.ResourcePlan
	// held holds the diff each plan was planned from.
	held   map[string]*dt.ResourceDiff
	routed int
	// considered counts the resources AfterXRDiff was asked to route.
	considered int
}

// NewXRPlanner returns an XRPlanner that plans with p. keep and ensure may be nil.
func NewXRPlanner(p *Planner, keep func(*unstructured.Unstructured) bool, ensure EnsureServers) *XRPlanner {
	return &XRPlanner{planner: p, keep: keep, ensure: ensure, plans: map[string]*plan.ResourcePlan{}, held: map[string]*dt.ResourceDiff{}}
}

// AfterXRDiff is a diffprocessor.WithAfterXRDiff hook. It fails the XR if any
// of its resources' plans errors, or if it renders a resource an earlier XR
// rendered differently, and merges none of its plans then.
func (x *XRPlanner) AfterXRDiff(ctx context.Context, diffs map[string]*dt.ResourceDiff) error {
	fresh := map[string]*dt.ResourceDiff{}
	var conflicts []string
	for key, d := range diffs {
		if d.DiffType == dt.DiffTypeRemoved || d.Desired.Raw == nil {
			continue
		}
		x.considered++
		held, ok := x.held[key]
		switch {
		case !ok:
			fresh[key] = d
		case !diffprocessor.SameRendering(held, d):
			conflicts = append(conflicts, conflict(key, held, d))
		}
	}
	if len(conflicts) > 0 {
		sort.Strings(conflicts)
		return errors.New(strings.Join(conflicts, "; "))
	}

	if x.ensure != nil {
		resources := make([]*unstructured.Unstructured, 0, len(fresh))
		for _, d := range fresh {
			resources = append(resources, d.Desired.Raw)
		}
		// Server start and image pulls are run-wide work, not the XR's.
		err := diffprocessor.OutsideXRDeadline(ctx, func(run context.Context) error { return x.ensure(run, resources) })
		if errors.Is(err, diffprocessor.ErrXRTimedOut) {
			return err
		}
		if err != nil {
			return errors.Wrap(err, "cannot start provider diff servers")
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

// conflict describes why the later diff d of key cannot share held's plan,
// naming the XRs that control each rendering when they say.
func conflict(key string, held, d *dt.ResourceDiff) string {
	return fmt.Sprintf("cannot plan resource %q: %s renders it differently from %s, which rendered it first, so no single "+
		"provider plan is correct for both — simulate them separately", key, controller(d, "this input"), controller(held, "an earlier input"))
}

// controller names the XR controlling d's rendered resource, or fallback.
func controller(d *dt.ResourceDiff, fallback string) string {
	if ref := metav1.GetControllerOf(d.Desired.Raw); ref != nil {
		return ref.Kind + "/" + ref.Name
	}
	return fallback
}

// Plans returns a copy of the plans collected so far, never nil.
func (x *XRPlanner) Plans() map[string]*plan.ResourcePlan {
	return maps.Clone(x.plans)
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
