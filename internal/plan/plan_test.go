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

package plan

import (
	"context"
	"testing"

	diffv1alpha1 "github.com/crossplane/upjet/v2/proto/diff/v1alpha1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

// fakePlanClient implements diffv1alpha1.PlanServiceClient in-process,
// capturing the request and returning a canned response or error.
type fakePlanClient struct {
	rsp *diffv1alpha1.PlanResponse
	err error
	got *diffv1alpha1.PlanRequest
}

func (f *fakePlanClient) Plan(_ context.Context, in *diffv1alpha1.PlanRequest, _ ...grpc.CallOption) (*diffv1alpha1.PlanResponse, error) {
	f.got = in
	return f.rsp, f.err
}

func TestPlan(t *testing.T) {
	desired := mr(map[string]any{"forProvider": map[string]any{"cidrBlock": "10.1.0.0/16"}})
	actual := mr(map[string]any{"forProvider": map[string]any{"cidrBlock": "10.0.0.0/16"}})
	secret, err := structpb.NewStruct(map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata":   map[string]any{"name": "creds", "namespace": "default"},
	})
	if err != nil {
		t.Fatalf("cannot convert secret: %v", err)
	}

	t.Run("RequestCarriesAllState", func(t *testing.T) {
		c := &fakePlanClient{rsp: &diffv1alpha1.PlanResponse{
			Action: diffv1alpha1.Action_ACTION_UPDATE,
			Changes: []*diffv1alpha1.FieldChange{{
				Field:   "spec.forProvider.cidrBlock",
				Actual:  value("10.0.0.0/16"),
				Planned: value("10.1.0.0/16"),
			}},
		}}

		got, err := Plan(context.Background(), c, desired, actual, []*structpb.Struct{secret})
		if err != nil {
			t.Fatalf("Plan(...): unexpected error: %v", err)
		}
		if got.Action != ActionUpdate {
			t.Errorf("Plan(...): want action %q, got %q", ActionUpdate, got.Action)
		}

		if c.got.GetDesiredResource().GetFields()["kind"].GetStringValue() != "VPC" {
			t.Errorf("Plan(...): desired resource not carried in request")
		}
		if c.got.GetActualResource() == nil {
			t.Errorf("Plan(...): actual resource not carried in request")
		}
		if got.Before != actual || got.After != desired {
			t.Errorf("Plan(...): want the plan to carry actual as before and desired as after")
		}
		if len(c.got.GetKubernetesObjectStore()) != 1 {
			t.Errorf("Plan(...): want 1 object store entry, got %d", len(c.got.GetKubernetesObjectStore()))
		}
	})

	t.Run("NoActualMeansCreate", func(t *testing.T) {
		c := &fakePlanClient{rsp: &diffv1alpha1.PlanResponse{Action: diffv1alpha1.Action_ACTION_CREATE}}

		got, err := Plan(context.Background(), c, desired, nil, nil)
		if err != nil {
			t.Fatalf("Plan(...): unexpected error: %v", err)
		}
		if got.Action != ActionCreate {
			t.Errorf("Plan(...): want action %q, got %q", ActionCreate, got.Action)
		}
		if c.got.GetActualResource() != nil {
			t.Errorf("Plan(...): actual resource should be unset for a create")
		}
		if got.Before != nil || got.After != desired {
			t.Errorf("Plan(...): want a create to carry no before and desired as after")
		}
	})

	t.Run("ServerErrorsPassThrough", func(t *testing.T) {
		c := &fakePlanClient{err: status.Error(codes.NotFound, "not served here")}

		_, err := Plan(context.Background(), c, desired, nil, nil)
		if status.Code(err) != codes.NotFound {
			t.Errorf("Plan(...): want NotFound to pass through, got %v", err)
		}
	})
}
