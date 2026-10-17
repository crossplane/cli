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
	"testing"

	diffv1alpha1 "github.com/crossplane/upjet/v2/proto/diff/v1alpha1"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

func testDesired() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ec2.aws.upbound.io/v1beta1",
		"kind":       "VPC",
		"metadata":   map[string]any{"name": "my-vpc"},
	}}
}

func value(v any) *diffv1alpha1.FieldValue {
	pv, err := structpb.NewValue(v)
	if err != nil {
		panic(err)
	}
	return &diffv1alpha1.FieldValue{Kind: &diffv1alpha1.FieldValue_Value{Value: pv}}
}

func absence(a diffv1alpha1.Absence) *diffv1alpha1.FieldValue {
	return &diffv1alpha1.FieldValue{Kind: &diffv1alpha1.FieldValue_Absence{Absence: a}}
}

func TestFromResponse(t *testing.T) {
	type args struct {
		rsp *diffv1alpha1.PlanResponse
	}
	type want struct {
		plan *ResourcePlan
		err  error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"Update": {
			reason: "An update with a concrete value change should decode action, values, and origin.",
			args: args{rsp: &diffv1alpha1.PlanResponse{
				Action: diffv1alpha1.Action_ACTION_UPDATE,
				Changes: []*diffv1alpha1.FieldChange{{
					Field:   "spec.forProvider.cidrBlock",
					Actual:  value("10.0.0.0/16"),
					Planned: value("10.1.0.0/16"),
					Origin:  diffv1alpha1.Origin_ORIGIN_DESIRED_STATE,
				}},
			}},
			want: want{plan: &ResourcePlan{
				APIVersion: "ec2.aws.upbound.io/v1beta1",
				Kind:       "VPC",
				Name:       "my-vpc",
				Action:     ActionUpdate,
				Changes: []FieldChange{{
					Path:    "spec.forProvider.cidrBlock",
					Actual:  &Value{Value: "10.0.0.0/16"},
					Planned: &Value{Value: "10.1.0.0/16"},
					Origin:  OriginDesired,
				}},
			}},
		},
		"Replace": {
			reason: "A change that requires replacement should be recorded in the change and in ReplacePaths.",
			args: args{rsp: &diffv1alpha1.PlanResponse{
				Action: diffv1alpha1.Action_ACTION_REPLACE,
				Changes: []*diffv1alpha1.FieldChange{{
					Field:           "spec.forProvider.engineVersion",
					Actual:          value("14.7"),
					Planned:         value("15.4"),
					RequiresReplace: true,
				}},
			}},
			want: want{plan: &ResourcePlan{
				APIVersion: "ec2.aws.upbound.io/v1beta1",
				Kind:       "VPC",
				Name:       "my-vpc",
				Action:     ActionReplace,
				Changes: []FieldChange{{
					Path:            "spec.forProvider.engineVersion",
					Actual:          &Value{Value: "14.7"},
					Planned:         &Value{Value: "15.4"},
					RequiresReplace: true,
				}},
				ReplacePaths: []string{"spec.forProvider.engineVersion"},
			}},
		},
		"Absences": {
			reason: "Sensitive, unknown, and unresolved values should decode to their markers.",
			args: args{rsp: &diffv1alpha1.PlanResponse{
				Action: diffv1alpha1.Action_ACTION_CREATE,
				Changes: []*diffv1alpha1.FieldChange{
					{Field: "spec.forProvider.password", Planned: absence(diffv1alpha1.Absence_ABSENCE_SENSITIVE)},
					{Field: "status.atProvider.id", Planned: absence(diffv1alpha1.Absence_ABSENCE_UNKNOWN), Origin: diffv1alpha1.Origin_ORIGIN_PROVIDER},
					{Field: "spec.forProvider.vpcId", Planned: absence(diffv1alpha1.Absence_ABSENCE_UNRESOLVED)},
				},
			}},
			want: want{plan: &ResourcePlan{
				APIVersion: "ec2.aws.upbound.io/v1beta1",
				Kind:       "VPC",
				Name:       "my-vpc",
				Action:     ActionCreate,
				Changes: []FieldChange{
					{Path: "spec.forProvider.password", Planned: &Value{Sensitive: true}},
					{Path: "status.atProvider.id", Planned: &Value{Unknown: true}, Origin: OriginProvider},
					{Path: "spec.forProvider.vpcId", Planned: &Value{Unresolved: true}},
				},
			}},
		},
		"NoOp": {
			reason: "A no-op should decode with no changes.",
			args:   args{rsp: &diffv1alpha1.PlanResponse{Action: diffv1alpha1.Action_ACTION_NO_OP}},
			want: want{plan: &ResourcePlan{
				APIVersion: "ec2.aws.upbound.io/v1beta1",
				Kind:       "VPC",
				Name:       "my-vpc",
				Action:     ActionNoOp,
			}},
		},
		"UnspecifiedAction": {
			reason: "An unspecified action is a server bug and should error rather than guess.",
			args:   args{rsp: &diffv1alpha1.PlanResponse{Action: diffv1alpha1.Action_ACTION_UNSPECIFIED}},
			want:   want{err: errors.New("diff server returned an unspecified action")},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := FromResponse(testDesired(), tc.args.rsp)

			if tc.want.err != nil {
				if err == nil || err.Error() != tc.want.err.Error() {
					t.Errorf("\n%s\nFromResponse(...): want error %q, got %v", tc.reason, tc.want.err, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("\n%s\nFromResponse(...): unexpected error: %v", tc.reason, err)
			}
			if diff := cmp.Diff(tc.want.plan, got, cmpopts.IgnoreFields(ResourcePlan{}, "Desired")); diff != "" {
				t.Errorf("\n%s\nFromResponse(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestIsNotSupported(t *testing.T) {
	notSupported, err := status.New(codes.FailedPrecondition, "cannot compute diff").WithDetails(&errdetails.PreconditionFailure{
		Violations: []*errdetails.PreconditionFailure_Violation{{
			Type:    violationDiffComputationNotSupported,
			Subject: "ec2.aws.upbound.io/v1beta1, Kind=VPC",
		}},
	})
	if err != nil {
		t.Fatalf("cannot build status: %v", err)
	}

	otherPrecondition, err := status.New(codes.FailedPrecondition, "something else").WithDetails(&errdetails.PreconditionFailure{
		Violations: []*errdetails.PreconditionFailure_Violation{{Type: "SOMETHING_ELSE"}},
	})
	if err != nil {
		t.Fatalf("cannot build status: %v", err)
	}

	cases := map[string]struct {
		reason string
		err    error
		want   bool
	}{
		"NotSupported": {
			reason: "FAILED_PRECONDITION with the DIFF_COMPUTATION_NOT_SUPPORTED violation is the fallback signal.",
			err:    notSupported.Err(),
			want:   true,
		},
		"OtherViolation": {
			reason: "FAILED_PRECONDITION with another violation type is not the fallback signal.",
			err:    otherPrecondition.Err(),
			want:   false,
		},
		"PlainPrecondition": {
			reason: "FAILED_PRECONDITION without details is not the fallback signal.",
			err:    status.Error(codes.FailedPrecondition, "nope"),
			want:   false,
		},
		"OtherCode": {
			reason: "Other gRPC codes are real failures.",
			err:    status.Error(codes.Internal, "boom"),
			want:   false,
		},
		"NotAStatus": {
			reason: "A non-gRPC error is a real failure.",
			err:    errors.New("boom"),
			want:   false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := IsNotSupported(tc.err); got != tc.want {
				t.Errorf("\n%s\nIsNotSupported(...): want %t, got %t", tc.reason, tc.want, got)
			}
		})
	}
}
