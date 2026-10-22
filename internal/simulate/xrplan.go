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
	"sync"

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
// It keeps one plan per diff key: when two XRs produce the same key, the
// first XR's plan wins, so a composed resource rendered differently by two
// inputs shows the first XR's plan in both reports. Plans merge in the
// AfterXRDiff hook, before schema validation verdicts, so an XR that
// validation later fails has already contributed its plans.
//
// The engine runs XRs sequentially, so the mutex only guards Plans and
// Routed against readers; the Planner and the maps ensure and keep close over
// are not safe for concurrent AfterXRDiff calls.
type XRPlanner struct {
	planner *Planner
	keep    func(*unstructured.Unstructured) bool
	ensure  EnsureServers

	mu     sync.Mutex
	plans  map[string]*plan.ResourcePlan
	routed int
}

// NewXRPlanner returns an XRPlanner that plans with p. keep and ensure may be nil.
func NewXRPlanner(p *Planner, keep func(*unstructured.Unstructured) bool, ensure EnsureServers) *XRPlanner {
	return &XRPlanner{planner: p, keep: keep, ensure: ensure, plans: map[string]*plan.ResourcePlan{}}
}

// AfterXRDiff is a diffprocessor.WithAfterXRDiff hook. It fails the XR if any
// of its resources' plans errors, and merges none of them then.
func (x *XRPlanner) AfterXRDiff(ctx context.Context, diffs map[string]*dt.ResourceDiff) error {
	if x.ensure != nil {
		var resources []*unstructured.Unstructured
		for _, d := range diffs {
			if d.DiffType != dt.DiffTypeRemoved && d.Desired.Raw != nil {
				resources = append(resources, d.Desired.Raw)
			}
		}
		// Server start and image pulls are run-wide work, not the XR's.
		err := diffprocessor.OutsideXRDeadline(ctx, func(run context.Context) error { return x.ensure(run, resources) })
		if err != nil {
			return errors.Wrap(err, "cannot start provider diff servers")
		}
	}

	plans := PlanDiffs(ctx, x.planner, x.keep, diffs)

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

	x.mu.Lock()
	defer x.mu.Unlock()
	for key, p := range plans {
		if _, ok := x.plans[key]; !ok {
			x.plans[key] = p
		}
	}
	for key, d := range diffs {
		if _, ok := plans[key]; ok && x.planner.CanPlan(d.Gvk.GroupKind()) {
			x.routed++
		}
	}

	return nil
}

// Plans returns a copy of the plans collected so far, never nil.
func (x *XRPlanner) Plans() map[string]*plan.ResourcePlan {
	x.mu.Lock()
	defer x.mu.Unlock()

	return maps.Clone(x.plans)
}

// Routed returns how many collected plans went to a diff server. Plans that
// stand in for a kind no server serves are not counted.
func (x *XRPlanner) Routed() int {
	x.mu.Lock()
	defer x.mu.Unlock()

	return x.routed
}
