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
	"errors"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	diffv1alpha1 "github.com/crossplane/upjet/v2/proto/diff/v1alpha1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/crossplane/cli/v2/internal/diff/diffprocessor"
	dt "github.com/crossplane/cli/v2/internal/diff/result"
	"github.com/crossplane/cli/v2/internal/plan"
)

// funcPlanClient answers Plan per resource name.
type funcPlanClient struct {
	fakePlanClient
	fail map[string]bool
	// ctxErr is the error of the context the last Plan call ran under.
	ctxErr error
}

func (f *funcPlanClient) Plan(ctx context.Context, in *diffv1alpha1.PlanRequest, _ ...grpc.CallOption) (*diffv1alpha1.PlanResponse, error) {
	f.ctxErr = ctx.Err()
	if f.fail[in.GetDesiredResource().GetFields()["metadata"].GetStructValue().GetFields()["name"].GetStringValue()] {
		return nil, status.Error(codes.Internal, "boom")
	}
	return f.rsp, nil
}

func added(r *unstructured.Unstructured) *dt.ResourceDiff {
	d := &dt.ResourceDiff{Gvk: vpcGVK, DiffType: dt.DiffTypeAdded}
	d.Desired.Raw = r
	return d
}

func TestXRPlanner(t *testing.T) {
	s := runtime.NewScheme()
	s.AddKnownTypeWithName(vpcGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(vpcGVK.GroupVersion().WithKind("VPCList"), &unstructured.UnstructuredList{})
	rm := meta.NewDefaultRESTMapper([]schema.GroupVersion{vpcGVK.GroupVersion()})
	rm.Add(vpcGVK, meta.RESTScopeRoot)

	pc := &funcPlanClient{
		fakePlanClient: fakePlanClient{rsp: &diffv1alpha1.PlanResponse{Action: diffv1alpha1.Action_ACTION_CREATE}},
		fail:           map[string]bool{"bad-a": true, "bad-b": true},
	}
	planner := NewPlanner(fake.NewClientBuilder().WithScheme(s).Build(), rm, "default", &fakeRouter{client: pc}, time.Second)

	removed := added(vpc("gone", "10.0.0.0/16"))
	removed.DiffType = dt.DiffTypeRemoved
	nilDesired := &dt.ResourceDiff{Gvk: vpcGVK, DiffType: dt.DiffTypeAdded}

	cases := map[string]struct {
		diffs       map[string]*dt.ResourceDiff
		ensureErr   error
		wantErr     string
		wantPlans   []string
		wantEnsured []string
	}{
		"AllOK": {
			diffs:       map[string]*dt.ResourceDiff{"a": added(vpc("a", "10.0.0.0/16")), "b": added(vpc("b", "10.0.0.0/16"))},
			wantPlans:   []string{"a", "b"},
			wantEnsured: []string{"a", "b"},
		},
		"OnePlanErrors": {
			diffs:       map[string]*dt.ResourceDiff{"a": added(vpc("a", "10.0.0.0/16")), "bad-a": added(vpc("bad-a", "10.0.0.0/16"))},
			wantErr:     "cannot plan 1 resources with their diff servers: VPC/bad-a: ",
			wantEnsured: []string{"a", "bad-a"},
		},
		"ErrorsAreSorted": {
			diffs:       map[string]*dt.ResourceDiff{"bad-b": added(vpc("bad-b", "10.0.0.0/16")), "bad-a": added(vpc("bad-a", "10.0.0.0/16"))},
			wantErr:     "cannot plan 2 resources",
			wantEnsured: []string{"bad-a", "bad-b"},
		},
		"EnsureError": {
			diffs:       map[string]*dt.ResourceDiff{"a": added(vpc("a", "10.0.0.0/16"))},
			ensureErr:   errors.New("pull failed"),
			wantErr:     "pull failed",
			wantEnsured: []string{"a"},
		},
		"SkipsRemovedAndNilDesired": {
			diffs:       map[string]*dt.ResourceDiff{"r": removed, "n": nilDesired, "a": added(vpc("a", "10.0.0.0/16"))},
			wantPlans:   []string{"a"},
			wantEnsured: []string{"a"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var ensured []string
			var ensureCtxs []context.Context
			ensure := func(ctx context.Context, rs []*unstructured.Unstructured) error {
				ensureCtxs = append(ensureCtxs, ctx)
				for _, r := range rs {
					ensured = append(ensured, r.GetName())
				}
				return tc.ensureErr
			}
			// An earlier XR's plans stay whatever this XR does.
			x := NewXRPlanner(planner, nil, ensure)
			x.plans["earlier"] = &plan.ResourcePlan{Name: "earlier"}

			err := x.AfterXRDiff(context.Background(), tc.diffs)

			if tc.wantErr == "" && err != nil {
				t.Fatalf("AfterXRDiff(...): unexpected error %v", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("AfterXRDiff(...): want error containing %q, got %v", tc.wantErr, err)
			}
			if name == "ErrorsAreSorted" {
				a, b := strings.Index(err.Error(), "VPC/bad-a: "), strings.Index(err.Error(), "VPC/bad-b: ")
				if a < 0 || b < 0 || a >= b {
					t.Errorf("AfterXRDiff(...): want bad-a before bad-b, got %v", err)
				}
			}
			sort.Strings(ensured)
			if !slices.Equal(ensured, tc.wantEnsured) && len(ensured)+len(tc.wantEnsured) > 0 {
				t.Errorf("ensure got %v, want %v", ensured, tc.wantEnsured)
			}
			if len(tc.wantEnsured) > 0 && (len(ensureCtxs) == 0 || ensureCtxs[0].Err() != nil) {
				t.Errorf("ensure ctx missing or ended: %v", ensureCtxs)
			}
			got := x.ProviderPlans().Plans
			if len(got) != len(tc.wantPlans)+1 || got["earlier"] == nil {
				t.Errorf("Plans() has %d entries, want %v plus the earlier XR's", len(got), tc.wantPlans)
			}
			for _, k := range tc.wantPlans {
				if got[k] == nil {
					t.Errorf("Plans()[%q] missing", k)
				}
			}
		})
	}

	t.Run("EnsureRunsOutsideXRDeadline", func(t *testing.T) {
		// ensure outlasts the XR's budget; the budget is paused meanwhile, so the plans still run.
		ensure := func(ctx context.Context, _ []*unstructured.Unstructured) error {
			time.Sleep(150 * time.Millisecond)
			return ctx.Err()
		}
		xrCtx, cancel := diffprocessor.NewXRContext(context.Background(), 50*time.Millisecond)
		defer cancel()

		x := NewXRPlanner(planner, nil, ensure)
		err := x.AfterXRDiff(xrCtx, map[string]*dt.ResourceDiff{"a": added(vpc("a", "10.0.0.0/16"))})
		if err != nil {
			t.Errorf("AfterXRDiff(...) = %v, want nil", err)
		}
		if xrCtx.Err() != nil {
			t.Errorf("XR ctx ended during ensure: %v", xrCtx.Err())
		}
		if pc.ctxErr != nil {
			t.Errorf("plan client saw ctx error %v, want a live context", pc.ctxErr)
		}
		if len(x.ProviderPlans().Plans) != 1 {
			t.Errorf("Plans() = %v, want the one plan", x.ProviderPlans().Plans)
		}
	})

	t.Run("FailedPlanStillRouted", func(t *testing.T) {
		x := NewXRPlanner(planner, nil, nil)
		err := x.AfterXRDiff(context.Background(), map[string]*dt.ResourceDiff{"bad-a": added(vpc("bad-a", "10.0.0.0/16"))})
		if err == nil {
			t.Fatal("AfterXRDiff(...) = nil, want the plan error")
		}
		if x.Routed() != 1 || len(x.ProviderPlans().Plans) != 0 {
			t.Errorf("Routed() = %d, Plans() = %v; want 1 routed and no merged plans", x.Routed(), x.ProviderPlans().Plans)
		}
	})

	t.Run("EmptyPlansNonNil", func(t *testing.T) {
		if NewXRPlanner(planner, nil, nil).ProviderPlans().Plans == nil {
			t.Errorf("Plans() = nil, want an empty non-nil map")
		}
	})
}

// countPlanClient counts Plan calls and fails the resource names in fail.
type countPlanClient struct {
	funcPlanClient
	calls int
}

func (c *countPlanClient) Plan(ctx context.Context, in *diffv1alpha1.PlanRequest, opts ...grpc.CallOption) (*diffv1alpha1.PlanResponse, error) {
	c.calls++
	return c.funcPlanClient.Plan(ctx, in, opts...)
}

// owned returns an added diff of a VPC controlled by the XNetwork xr.
func owned(xr, cidr string) *dt.ResourceDiff {
	r := vpc("shared", cidr)
	r.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "example.org/v1", Kind: "XNetwork", Name: xr, Controller: new(true)}})
	d := added(r)
	// The engine's Clean view has no ownerReferences (cleanupForDiff), so
	// two XRs rendering a child alike give equal Clean views.
	d.Desired.Clean = r.DeepCopy()
	d.Desired.Clean.SetOwnerReferences(nil)
	return d
}

func TestXRPlannerRepeatedKey(t *testing.T) {
	s := runtime.NewScheme()
	s.AddKnownTypeWithName(vpcGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(vpcGVK.GroupVersion().WithKind("VPCList"), &unstructured.UnstructuredList{})
	rm := meta.NewDefaultRESTMapper([]schema.GroupVersion{vpcGVK.GroupVersion()})
	rm.Add(vpcGVK, meta.RESTScopeRoot)
	newPlanner := func(fail bool) (*XRPlanner, *countPlanClient) {
		pc := &countPlanClient{funcPlanClient: funcPlanClient{fakePlanClient: fakePlanClient{rsp: &diffv1alpha1.PlanResponse{Action: diffv1alpha1.Action_ACTION_CREATE}}}}
		x := NewXRPlanner(NewPlanner(fake.NewClientBuilder().WithScheme(s).Build(), rm, "default", &fakeRouter{client: pc}, time.Second), nil, nil)
		if fail {
			pc.fail = map[string]bool{"shared": true}
		}
		return x, pc
	}

	t.Run("IdenticalRepeatSkipped", func(t *testing.T) {
		x, pc := newPlanner(false)
		if err := x.AfterXRDiff(context.Background(), map[string]*dt.ResourceDiff{"k": owned("a", "10.0.0.0/16")}); err != nil {
			t.Fatalf("AfterXRDiff(a): %v", err)
		}
		// Rendered by another XR, but identically once cleaned.
		b := owned("b", "10.0.0.0/16")
		if err := x.AfterXRDiff(context.Background(), map[string]*dt.ResourceDiff{"k": b}); err != nil {
			t.Fatalf("AfterXRDiff(b): %v", err)
		}
		if pc.calls != 1 || x.Routed() != 1 || len(x.ProviderPlans().Plans) != 1 {
			t.Errorf("calls = %d, Routed() = %d, Plans() = %v; want 1 call, 1 routed, 1 plan", pc.calls, x.Routed(), x.ProviderPlans().Plans)
		}
	})

	t.Run("DifferentRenderingNotPlanned", func(t *testing.T) {
		x, pc := newPlanner(false)
		a, b := owned("a", "10.0.0.0/16"), owned("b", "10.1.0.0/16")
		k := a.GetDiffKey()
		if err := x.AfterXRDiff(context.Background(), map[string]*dt.ResourceDiff{k: a}); err != nil {
			t.Fatalf("AfterXRDiff(a): %v", err)
		}
		// The engine's overlap check fails the run for b; the planner only
		// keeps a's plan from being shown with b's rendering.
		if err := x.AfterXRDiff(context.Background(), map[string]*dt.ResourceDiff{k: b}); err != nil {
			t.Fatalf("AfterXRDiff(b) = %v, want nil", err)
		}
		if pc.calls != 1 || x.Routed() != 1 {
			t.Errorf("calls = %d, Routed() = %d; want b not planned", pc.calls, x.Routed())
		}
		got := x.ProviderPlans()
		if got.Held[k] != a {
			t.Errorf("Held[k] = %v, want a's diff", got.Held[k])
		}
		if got.For(a) == nil || got.For(a) != got.Plans[k] {
			t.Errorf("For(a) = %v, want the plan of a's rendering", got.For(a))
		}
		if p := got.For(b); p != nil {
			t.Errorf("For(b) = %v, want no plan for a differing rendering", p)
		}
	})

	t.Run("RepeatNotReplanned", func(t *testing.T) {
		x, pc := newPlanner(false)
		if err := x.AfterXRDiff(context.Background(), map[string]*dt.ResourceDiff{"k": owned("a", "10.0.0.0/16")}); err != nil {
			t.Fatalf("AfterXRDiff(a): %v", err)
		}
		// A plan of "shared" now fails, but the held key is not planned again.
		pc.fail = map[string]bool{"shared": true}
		if err := x.AfterXRDiff(context.Background(), map[string]*dt.ResourceDiff{"k": owned("a", "10.0.0.0/16")}); err != nil {
			t.Errorf("AfterXRDiff(repeat) = %v, want nil", err)
		}
		if pc.calls != 1 {
			t.Errorf("calls = %d, want 1", pc.calls)
		}
	})

	t.Run("FailedPlanNotHeld", func(t *testing.T) {
		x, pc := newPlanner(true)
		if err := x.AfterXRDiff(context.Background(), map[string]*dt.ResourceDiff{"k": owned("a", "10.0.0.0/16")}); err == nil {
			t.Fatal("AfterXRDiff(a) = nil, want the plan error")
		}
		pc.fail = nil
		if err := x.AfterXRDiff(context.Background(), map[string]*dt.ResourceDiff{"k": owned("b", "10.1.0.0/16")}); err != nil {
			t.Errorf("AfterXRDiff(b) = %v, want b planned since a merged nothing", err)
		}
	})
}
