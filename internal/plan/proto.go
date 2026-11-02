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
	diffv1alpha1 "github.com/crossplane/upjet/v2/proto/diff/v1alpha1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

// violationDiffComputationNotSupported is the PreconditionFailure violation
// type a diff server attaches to a FAILED_PRECONDITION status when it serves
// a resource's API group but cannot compute a diff for it. It is defined in
// upjet's pkg/diffserver/internal/plan but not exported.
const violationDiffComputationNotSupported = "DIFF_COMPUTATION_NOT_SUPPORTED"

// FromResponse converts a diff server's PlanResponse into the ResourcePlan
// for the given desired resource.
func FromResponse(desired *unstructured.Unstructured, rsp *diffv1alpha1.PlanResponse) (*ResourcePlan, error) {
	p := NewResourcePlan(desired)

	switch rsp.GetAction() {
	case diffv1alpha1.Action_ACTION_NO_OP:
		p.Action = ActionNoOp
	case diffv1alpha1.Action_ACTION_CREATE:
		p.Action = ActionCreate
	case diffv1alpha1.Action_ACTION_UPDATE:
		p.Action = ActionUpdate
	case diffv1alpha1.Action_ACTION_REPLACE:
		p.Action = ActionReplace
	case diffv1alpha1.Action_ACTION_UNSPECIFIED:
		return nil, errors.New("diff server returned an unspecified action")
	default:
		return nil, errors.Errorf("diff server returned an unknown action %q", rsp.GetAction())
	}

	for _, c := range rsp.GetChanges() {
		fc := FieldChange{
			Path:            c.GetField(),
			Actual:          fromFieldValue(c.GetActual()),
			Planned:         fromFieldValue(c.GetPlanned()),
			RequiresReplace: c.GetRequiresReplace(),
			Origin:          fromOrigin(c.GetOrigin()),
		}
		p.Changes = append(p.Changes, fc)
		if fc.RequiresReplace {
			p.ReplacePaths = append(p.ReplacePaths, fc.Path)
		}
	}

	// A diff server can answer update while reporting no field changes: the
	// Terraform diff the action is decided on can consist entirely of flatmap
	// count keys ("tags.%", "serviceEndpoints.#"), which carry a collection's
	// size rather than a field and are never reported as changes. An update
	// that changes no field is a no-op to the user, so report it as one. A
	// replace is left alone: it warns about a forced replacement even when the
	// field that forces it is not reported.
	if p.Action == ActionUpdate && !hasVisibleChange(p.Changes) {
		p.Action = ActionNoOp
	}

	return p, nil
}

// hasVisibleChange reports whether any change carries a value on either side.
// A change with neither has nothing to say about the field it names.
func hasVisibleChange(changes []FieldChange) bool {
	for _, c := range changes {
		if c.Actual != nil || c.Planned != nil {
			return true
		}
	}
	return false
}

func fromFieldValue(fv *diffv1alpha1.FieldValue) *Value {
	if fv == nil {
		return nil
	}
	switch k := fv.GetKind().(type) {
	case *diffv1alpha1.FieldValue_Value:
		return &Value{Value: k.Value.AsInterface()}
	case *diffv1alpha1.FieldValue_Absence:
		switch k.Absence {
		case diffv1alpha1.Absence_ABSENCE_UNKNOWN:
			return &Value{Unknown: true}
		case diffv1alpha1.Absence_ABSENCE_SENSITIVE:
			return &Value{Sensitive: true}
		case diffv1alpha1.Absence_ABSENCE_UNRESOLVED:
			return &Value{Unresolved: true}
		case diffv1alpha1.Absence_ABSENCE_UNSPECIFIED:
			return nil
		}
	}
	return nil
}

func fromOrigin(o diffv1alpha1.Origin) Origin {
	switch o {
	case diffv1alpha1.Origin_ORIGIN_DESIRED_STATE:
		return OriginDesired
	case diffv1alpha1.Origin_ORIGIN_PROVIDER:
		return OriginProvider
	case diffv1alpha1.Origin_ORIGIN_UNSPECIFIED:
		return ""
	}
	return ""
}

// IsNotSupported reports whether err is a diff server saying it serves this
// resource's API group but cannot compute a diff for it. Callers fall back to
// the approximate client-side diff for such resources.
func IsNotSupported(err error) bool {
	s, ok := status.FromError(err)
	if !ok || s.Code() != codes.FailedPrecondition {
		return false
	}
	for _, d := range s.Details() {
		pf, ok := d.(*errdetails.PreconditionFailure)
		if !ok {
			continue
		}
		for _, v := range pf.GetViolations() {
			if v.GetType() == violationDiffComputationNotSupported {
				return true
			}
		}
	}
	return false
}
