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
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/crossplane/cli/v2/internal/plan"
)

func TestRenderPlanGrouped(t *testing.T) {
	xr := plan.ResourceRef{APIVersion: "example.org/v1", Kind: "XNetwork", Name: "prod-network"}
	pinned := plan.ResourceRef{APIVersion: "example.org/v1", Kind: "XNetwork", Name: "staging-network"}
	r := ProjectResult{
		Result: plan.Result{
			Summary: plan.Summary{Update: 1, Destroy: 1, NoOp: 1},
			Plans: []*plan.ResourcePlan{
				// The XR's own no-op plan is not printed.
				{APIVersion: xr.APIVersion, Kind: xr.Kind, Name: xr.Name, Action: plan.ActionNoOp, Composite: &xr},
				{
					APIVersion: "ec2.aws.upbound.io/v1beta1", Kind: "SecurityGroupRule", Name: "prod-network-ssh",
					Action: plan.ActionUpdate, Composite: &xr,
					Changes: []plan.FieldChange{{Path: "spec.forProvider.fromPort", Actual: &plan.Value{Value: 22}, Planned: &plan.Value{Value: 2222}}},
				},
				{
					APIVersion: "ec2.aws.upbound.io/v1beta1", Kind: "Route", Name: "prod-network-default",
					Action: plan.ActionDelete, Composite: &xr,
					Changes: plan.FieldChanges(map[string]any{"spec": map[string]any{"region": "us-west-1"}}, nil),
				},
			},
		},
		Summary: ProjectSummary{Summary: plan.Summary{Update: 1, Destroy: 1, NoOp: 1}, Deferred: 1},
		Composites: []CompositeResult{
			{Ref: xr, Status: CompositeChanged},
			{Ref: pinned, Status: CompositeDeferred, Reason: "manualPolicy", Detail: "compositionRevisionRef: net-6"},
		},
	}
	want := `XR: XNetwork.example.org prod-network
  [~] SecurityGroupRule.ec2.aws.upbound.io prod-network-ssh (update)
      spec:
        forProvider:
          ~ fromPort: 22 -> 2222

  [-] Route.ec2.aws.upbound.io prod-network-default (delete)

XR: XNetwork.example.org staging-network (deferred)
    Pinned to a composition revision (compositionRevisionRef: net-6); apply changes nothing until the
    revision advances. Rerun with --include-pinned to preview that moment.

Plan: 0 to add, 1 to change, 1 to destroy.
1 composite resource deferred (pinned to a composition revision).
`
	var buf bytes.Buffer
	r2, _ := NewRenderer(plan.FormatPlan)
	if err := r2.Render(&buf, r); err != nil {
		t.Fatalf("Render(plan): unexpected error: %v", err)
	}
	if diff := cmp.Diff(want, buf.String()); diff != "" {
		t.Errorf("Render(plan): -want, +got:\n%s", diff)
	}
}

func TestRenderPlanCompositeDetail(t *testing.T) {
	xr := plan.ResourceRef{APIVersion: "example.org/v1", Kind: "XNetwork", Name: "prod-network"}
	r := ProjectResult{
		Result:     plan.Result{},
		Composites: []CompositeResult{{Ref: xr, Status: CompositeUnchanged, Pinned: true, Detail: "rendered against pinned revision net-6 with the project's functions"}},
	}
	want := `XR: XNetwork.example.org prod-network
    rendered against pinned revision net-6 with the project's functions

Plan: 0 to add, 0 to change, 0 to destroy.
`
	var buf bytes.Buffer
	r2, _ := NewRenderer(plan.FormatPlan)
	if err := r2.Render(&buf, r); err != nil {
		t.Fatalf("Render(plan): unexpected error: %v", err)
	}
	if diff := cmp.Diff(want, buf.String()); diff != "" {
		t.Errorf("Render(plan): -want, +got:\n%s", diff)
	}
}

func TestRenderPlanPinnedHeader(t *testing.T) {
	xr := plan.ResourceRef{APIVersion: "example.org/v1", Kind: "XNetwork", Name: "prod-network"}
	var buf bytes.Buffer
	r, _ := NewRenderer(plan.FormatPlan)
	if err := r.Render(&buf, ProjectResult{Composites: []CompositeResult{{Ref: xr, Status: CompositeUnchanged, Pinned: true}}}); err != nil {
		t.Fatalf("Render(plan): unexpected error: %v", err)
	}
	if want := "XR: XNetwork.example.org prod-network (pinned)\n"; !strings.HasPrefix(buf.String(), want) {
		t.Errorf("Render(plan): want prefix %q, got %q", want, buf.String())
	}
}

func TestRenderPlanCompositions(t *testing.T) {
	comp := plan.ResourceRef{APIVersion: "apiextensions.crossplane.io/v1", Kind: "Composition", Name: "net"}
	same := plan.ResourceRef{APIVersion: comp.APIVersion, Kind: comp.Kind, Name: "same"}
	unknown := plan.ResourceRef{APIVersion: comp.APIVersion, Kind: comp.Kind, Name: "unknown"}
	xr := plan.ResourceRef{APIVersion: "example.org/v1", Kind: "XNetwork", Name: "prod-network"}
	r := ProjectResult{
		Result: plan.Result{
			Summary: plan.Summary{Update: 2, NoOp: 2},
			Plans: []*plan.ResourcePlan{
				{
					APIVersion: comp.APIVersion, Kind: comp.Kind, Name: comp.Name, Action: plan.ActionUpdate,
					Changes: []plan.FieldChange{{Path: "spec.mode", Actual: &plan.Value{Value: "Resources"}, Planned: &plan.Value{Value: "Pipeline"}}},
				},
				{APIVersion: same.APIVersion, Kind: same.Kind, Name: same.Name, Action: plan.ActionNoOp},
				{APIVersion: unknown.APIVersion, Kind: unknown.Kind, Name: unknown.Name, Action: plan.ActionNoOp},
				{
					APIVersion: "ec2.aws.upbound.io/v1beta1", Kind: "VPC", Name: "prod-network",
					Action: plan.ActionUpdate, Composite: &xr,
					Changes: []plan.FieldChange{{Path: "spec.forProvider.region", Actual: &plan.Value{Value: "us-west-1"}, Planned: &plan.Value{Value: "us-east-1"}}},
				},
			},
		},
		Summary:    ProjectSummary{Summary: plan.Summary{Update: 2, NoOp: 2}},
		Composites: []CompositeResult{{Ref: xr, Status: CompositeChanged}},
		Compositions: []CompositionResult{
			{Ref: comp, RevisionImpact: &RevisionImpact{ChangeScope: "spec", CreatesRevision: true, RepointedComposites: 1}},
			{Ref: same, RevisionImpact: &RevisionImpact{ChangeScope: "none"}, ImpactAnalysisSkipped: true},
			// No determined impact prints no line.
			{Ref: unknown},
		},
		Notes: []string{"Cloud-layer plan not run."},
	}
	want := `[~] Composition.apiextensions.crossplane.io net (update)
    Applying this creates a new CompositionRevision, which 1 composite would adopt.
    spec:
      ~ mode: "Resources" -> "Pipeline"

[=] Composition.apiextensions.crossplane.io same (no-op)
    Applying this creates no new CompositionRevision. Impact analysis skipped.

[=] Composition.apiextensions.crossplane.io unknown (no-op)

XR: XNetwork.example.org prod-network
  [~] VPC.ec2.aws.upbound.io prod-network (update)
      spec:
        forProvider:
          ~ region: "us-west-1" -> "us-east-1"

Plan: 0 to add, 2 to change, 0 to destroy.

Note: Cloud-layer plan not run.
`
	var buf bytes.Buffer
	pr, _ := NewRenderer(plan.FormatPlan)
	if err := pr.Render(&buf, r); err != nil {
		t.Fatalf("Render(plan): unexpected error: %v", err)
	}
	if diff := cmp.Diff(want, buf.String()); diff != "" {
		t.Errorf("Render(plan): -want, +got:\n%s", diff)
	}
}

func TestRenderJSONProjectFields(t *testing.T) {
	xr := plan.ResourceRef{APIVersion: "example.org/v1", Kind: "XNetwork", Name: "prod-network"}
	// The embedded Result.Summary is left zero: the outer Summary shadows it,
	// so it is neither written nor read back.
	in := ProjectResult{
		Summary:    ProjectSummary{Summary: plan.Summary{Destroy: 1}, Deferred: 1},
		Composites: []CompositeResult{{Ref: xr, Status: CompositeDeferred, Reason: "manualPolicy"}},
		Compositions: []CompositionResult{{
			Ref:            plan.ResourceRef{APIVersion: "apiextensions.crossplane.io/v1", Kind: "Composition", Name: "net"},
			RevisionImpact: &RevisionImpact{ChangeScope: "spec", CreatesRevision: true, RepointedComposites: 2, PredictedRevision: "net-abc1234"},
		}},
		Errors: []Error{{
			Ref: &xr, Message: "invalid",
			ValidationFailures: []ValidationFailure{{
				APIVersion: "v1", Kind: "ConfigMap", Name: "cm", Status: "invalid",
				Errors: []FieldValidationError{{Type: "schema", Field: "data", Message: "bad"}},
			}},
		}},
		Warnings: []Warning{{Message: "heads up", Context: map[string]string{"k": "v"}}},
		Notes:    []string{"a note"},
	}
	in.Result = plan.Result{
		FormatVersion: plan.ResultFormatVersion,
		Errored:       true,
		Plans: []*plan.ResourcePlan{{
			APIVersion: "v1", Kind: "ConfigMap", Name: "cm", Action: plan.ActionDelete, Composite: &xr,
			Changes: []plan.FieldChange{{Path: "data.k", Actual: &plan.Value{Value: "v"}}},
			DryRun:  &plan.Verification{SkipReason: "forbidden", Detail: "no create permission"},
		}},
	}
	var buf bytes.Buffer
	r, _ := NewRenderer(plan.FormatJSON)
	if err := r.Render(&buf, in); err != nil {
		t.Fatalf("Render(json): unexpected error: %v", err)
	}
	got := ProjectResult{}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("Render(json): output is not valid JSON: %v", err)
	}
	if diff := cmp.Diff(in, got); diff != "" {
		t.Errorf("Render(json): -want, +got:\n%s", diff)
	}
}

func TestNewProjectResult(t *testing.T) {
	xr := plan.ResourceRef{APIVersion: "example.org/v1", Kind: "XNetwork", Name: "prod-network"}
	pinned := plan.ResourceRef{APIVersion: "example.org/v1", Kind: "XNetwork", Name: "staging-network"}
	plans := []*plan.ResourcePlan{{APIVersion: "v1", Kind: "ConfigMap", Name: "cm", Action: plan.ActionCreate, Composite: &xr}}
	composites := []CompositeResult{
		{Ref: xr, Status: CompositeChanged},
		{Ref: pinned, Status: CompositeDeferred, Reason: CompositeReasonManualPolicy, Pinned: true},
		// Pinned but simulated (--include-pinned) is not deferred.
		{Ref: pinned, Status: CompositeUnchanged, Pinned: true},
	}

	type want struct {
		summary ProjectSummary
		errored bool
	}
	cases := map[string]struct {
		reason string
		errs   []Error
		want   want
	}{
		"CountsDeferred": {
			reason: "Summary.Deferred should count only deferred composites, and match the embedded plan summary.",
			want:   want{summary: ProjectSummary{Summary: plan.Summary{Create: 1}, Deferred: 1}},
		},
		"ErrorsMarkErrored": {
			reason: "A run-level error should mark the result errored even when every plan succeeded.",
			errs:   []Error{{Ref: &xr, Message: "boom"}},
			want:   want{summary: ProjectSummary{Summary: plan.Summary{Create: 1}, Deferred: 1}, errored: true},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := NewProjectResult(plans, composites, tc.errs)
			if diff := cmp.Diff(tc.want, want{summary: got.Summary, errored: got.Errored}, cmp.AllowUnexported(want{})); diff != "" {
				t.Errorf("\n%s\nNewProjectResult(...): -want, +got:\n%s", tc.reason, diff)
			}
			if got.FormatVersion != plan.ResultFormatVersion {
				t.Errorf("\n%s\nNewProjectResult(...).FormatVersion: want %q, got %q", tc.reason, plan.ResultFormatVersion, got.FormatVersion)
			}
		})
	}
}
