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
	"testing"
	"time"

	diffv1alpha1 "github.com/crossplane/upjet/v2/proto/diff/v1alpha1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	dt "github.com/crossplane/cli/v2/internal/diff/result"
	"github.com/crossplane/cli/v2/internal/plan"
)

// vpcRouter routes only VPCs, to client.
type vpcRouter struct {
	client diffv1alpha1.PlanServiceClient
}

func (r *vpcRouter) Route(gk schema.GroupKind) (diffv1alpha1.PlanServiceClient, bool) {
	return r.client, gk == vpcGVK.GroupKind()
}

// countingPlanClient answers Plan with rsp or err, counting calls.
type countingPlanClient struct {
	rsp   *diffv1alpha1.PlanResponse
	err   error
	calls int
}

func (c *countingPlanClient) Plan(context.Context, *diffv1alpha1.PlanRequest, ...grpc.CallOption) (*diffv1alpha1.PlanResponse, error) {
	c.calls++
	return c.rsp, c.err
}

// vpcDiff is a VPC diff of type t whose dry-run result is desired.
func vpcDiff(t dt.DiffType, name string) *dt.ResourceDiff {
	d := &dt.ResourceDiff{Gvk: vpcGVK, ResourceName: name, DiffType: t}
	d.Desired.Raw = vpc(name, "10.1.0.0/16")
	if t != dt.DiffTypeAdded {
		d.Current.Raw = vpc(name, "10.0.0.0/16")
	}
	return d
}

func TestPlanDiffs(t *testing.T) {
	ctx := context.Background()

	s := runtime.NewScheme()
	s.AddKnownTypeWithName(vpcGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(vpcGVK.GroupVersion().WithKind("VPCList"), &unstructured.UnstructuredList{})
	rm := meta.NewDefaultRESTMapper([]schema.GroupVersion{vpcGVK.GroupVersion()})
	rm.Add(vpcGVK, meta.RESTScopeRoot)

	keepAll := func(*unstructured.Unstructured) bool { return true }
	// An update with no visible change is a no-op, so this one has a change.
	update := &diffv1alpha1.PlanResponse{
		Action:  diffv1alpha1.Action_ACTION_UPDATE,
		Changes: []*diffv1alpha1.FieldChange{{Field: "spec.forProvider.cidrBlock", Actual: fieldValue("10.0.0.0/16"), Planned: fieldValue("10.1.0.0/16")}},
	}

	cm := &dt.ResourceDiff{Gvk: cmGVK, ResourceName: "cm", DiffType: dt.DiffTypeModified}
	cm.Desired.Raw = obj(cmGVK, "cm", nil)
	cmEqual := &dt.ResourceDiff{Gvk: cmGVK, ResourceName: "cm-eq", DiffType: dt.DiffTypeEqual}
	cmEqual.Desired.Raw = obj(cmGVK, "cm-eq", nil)

	cases := map[string]struct {
		reason    string
		rsp       *diffv1alpha1.PlanResponse
		err       error
		keep      func(*unstructured.Unstructured) bool
		sets      []map[string]*dt.ResourceDiff
		want      map[string]plan.Action
		wantCalls int
		check     func(t *testing.T, got map[string]*plan.ResourcePlan)
	}{
		"Routed": {
			reason:    "A routed modified diff is planned by its diff server.",
			rsp:       update,
			sets:      []map[string]*dt.ResourceDiff{diffs(vpcDiff(dt.DiffTypeModified, "a"))},
			want:      map[string]plan.Action{vpcDiff(dt.DiffTypeModified, "a").GetDiffKey(): plan.ActionUpdate},
			wantCalls: 1,
		},
		"Unrouted": {
			reason: "A kind no diff server serves gets no entry, so its engine plan stands; a kept equal one gets a no-op entry.",
			keep:   keepAll,
			sets:   []map[string]*dt.ResourceDiff{diffs(cm, cmEqual)},
			want:   map[string]plan.Action{cmEqual.GetDiffKey(): plan.ActionNoOp},
		},
		"Removed": {
			reason: "A removal is not planned: providers plan no deletes.",
			rsp:    update,
			sets:   []map[string]*dt.ResourceDiff{diffs(vpcDiff(dt.DiffTypeRemoved, "a"))},
			want:   map[string]plan.Action{},
		},
		"EqualNotKept": {
			reason: "An equal diff keep declines, such as the XR's own, is not planned.",
			rsp:    update,
			sets:   []map[string]*dt.ResourceDiff{diffs(vpcDiff(dt.DiffTypeEqual, "a"))},
			want:   map[string]plan.Action{},
		},
		"KeptEqualNoOp": {
			reason:    "A kept equal diff is planned; the provider's no-op is returned for the merge to drop.",
			rsp:       &diffv1alpha1.PlanResponse{Action: diffv1alpha1.Action_ACTION_NO_OP},
			keep:      keepAll,
			sets:      []map[string]*dt.ResourceDiff{diffs(vpcDiff(dt.DiffTypeEqual, "a"))},
			want:      map[string]plan.Action{vpcDiff(dt.DiffTypeEqual, "a").GetDiffKey(): plan.ActionNoOp},
			wantCalls: 1,
		},
		"NotSupported": {
			reason:    "DIFF_COMPUTATION_NOT_SUPPORTED is an approximate plan, not an error.",
			err:       notSupportedErr(t),
			sets:      []map[string]*dt.ResourceDiff{diffs(vpcDiff(dt.DiffTypeModified, "a"))},
			want:      map[string]plan.Action{vpcDiff(dt.DiffTypeModified, "a").GetDiffKey(): plan.ActionUpdate},
			wantCalls: 1,
			check: func(t *testing.T, got map[string]*plan.ResourcePlan) {
				t.Helper()
				if p := got[vpcDiff(dt.DiffTypeModified, "a").GetDiffKey()]; !p.Approximate || p.Error != "" {
					t.Errorf("PlanDiffs(...): want an approximate plan, got %+v", p)
				}
			},
		},
		"Error": {
			reason:    "A diff server error is a per-resource error plan.",
			err:       status.Error(codes.Internal, "boom"),
			sets:      []map[string]*dt.ResourceDiff{diffs(vpcDiff(dt.DiffTypeAdded, "a"))},
			want:      map[string]plan.Action{vpcDiff(dt.DiffTypeAdded, "a").GetDiffKey(): ""},
			wantCalls: 1,
			check: func(t *testing.T, got map[string]*plan.ResourcePlan) {
				t.Helper()
				if p := got[vpcDiff(dt.DiffTypeAdded, "a").GetDiffKey()]; p.Error == "" {
					t.Errorf("PlanDiffs(...): want an error plan, got %+v", p)
				}
			},
		},
		"DedupeAcrossGroups": {
			reason: "One resource reached by two groups is planned once.",
			rsp:    update,
			sets: []map[string]*dt.ResourceDiff{
				diffs(vpcDiff(dt.DiffTypeModified, "a")),
				diffs(vpcDiff(dt.DiffTypeModified, "a"), vpcDiff(dt.DiffTypeModified, "b")),
			},
			want: map[string]plan.Action{
				vpcDiff(dt.DiffTypeModified, "a").GetDiffKey(): plan.ActionUpdate,
				vpcDiff(dt.DiffTypeModified, "b").GetDiffKey(): plan.ActionUpdate,
			},
			wantCalls: 2,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			pc := &countingPlanClient{rsp: tc.rsp, err: tc.err}
			kube := fake.NewClientBuilder().WithScheme(s).Build()
			p := NewPlanner(kube, rm, "default", &vpcRouter{client: pc}, time.Second)

			got := PlanDiffs(ctx, p, tc.keep, tc.sets...)

			actions := map[string]plan.Action{}
			for k, rp := range got {
				actions[k] = rp.Action
			}
			if len(actions) != len(tc.want) {
				t.Errorf("%s\nPlanDiffs(...): want %v, got %v", tc.reason, tc.want, actions)
			}
			for k, a := range tc.want {
				if g, ok := actions[k]; !ok || g != a {
					t.Errorf("%s\nPlanDiffs(...)[%q]: want %q, got %q (present %t)", tc.reason, k, a, g, ok)
				}
			}
			if pc.calls != tc.wantCalls {
				t.Errorf("%s\nPlanDiffs(...): want %d Plan calls, got %d", tc.reason, tc.wantCalls, pc.calls)
			}
			if tc.check != nil {
				tc.check(t, got)
			}
		})
	}
}

func TestChanged(t *testing.T) {
	cases := map[string]struct {
		plans []*plan.ResourcePlan
		want  bool
	}{
		"Empty":         {want: false},
		"NoOpOnly":      {plans: []*plan.ResourcePlan{{Action: plan.ActionNoOp}}, want: false},
		"ErrorOnly":     {plans: []*plan.ResourcePlan{{Action: plan.ActionUpdate, Error: "boom"}}, want: false},
		"Create":        {plans: []*plan.ResourcePlan{{Action: plan.ActionNoOp}, {Action: plan.ActionCreate}}, want: true},
		"Update":        {plans: []*plan.ResourcePlan{{Action: plan.ActionUpdate}}, want: true},
		"Replace":       {plans: []*plan.ResourcePlan{{Action: plan.ActionReplace}}, want: true},
		"Delete":        {plans: []*plan.ResourcePlan{{Action: plan.ActionDelete}}, want: true},
		"ApproximateUp": {plans: []*plan.ResourcePlan{{Action: plan.ActionUpdate, Approximate: true}}, want: true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := Changed(plan.Result{Plans: tc.plans}); got != tc.want {
				t.Errorf("Changed(...): want %t, got %t", tc.want, got)
			}
		})
	}
}
