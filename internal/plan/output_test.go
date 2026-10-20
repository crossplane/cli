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
	"bytes"
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func testPlans() []*ResourcePlan {
	return []*ResourcePlan{
		{
			APIVersion: "ec2.aws.upbound.io/v1beta1", Kind: "VPC", Name: "my-vpc",
			Action: ActionCreate,
			Changes: []FieldChange{
				{Path: "spec.forProvider.cidrBlock", Planned: &Value{Value: "10.0.0.0/16"}},
				{Path: "spec.forProvider.region", Planned: &Value{Value: "us-west-1"}},
			},
		},
		{
			APIVersion: "rds.aws.upbound.io/v1beta1", Kind: "Instance", Name: "prod-db",
			Action: ActionReplace,
			Changes: []FieldChange{{
				Path:            "spec.forProvider.engineVersion",
				Actual:          &Value{Value: "14.7"},
				Planned:         &Value{Value: "15.4"},
				RequiresReplace: true,
			}},
			ReplacePaths: []string{"spec.forProvider.engineVersion"},
		},
		{
			APIVersion: "kms.aws.upbound.io/v1beta1", Kind: "Key", Name: "my-key",
			Action: ActionNoOp,
			// A change with no value on either side renders nothing, not even
			// its parent keys.
			Changes: []FieldChange{{Path: "spec.forProvider.tags.cost-center"}},
		},
		{
			APIVersion: "s3.aws.upbound.io/v1beta2", Kind: "Bucket", Name: "my-bucket",
			Action:      ActionUpdate,
			Approximate: true, FallbackReason: "no diff server for Bucket.s3.aws.upbound.io",
			Changes: []FieldChange{{
				Path:    "spec.forProvider.forceDestroy",
				Actual:  &Value{Value: false},
				Planned: &Value{Value: true},
			}},
		},
		{
			APIVersion: "ec2.aws.upbound.io/v1beta1", Kind: "Subnet", Name: "my-subnet",
			Error: "rpc error: code = Internal desc = boom",
		},
	}
}

func TestRenderPlan(t *testing.T) {
	want := `[+] VPC.ec2.aws.upbound.io my-vpc (create)
    spec:
      forProvider:
        + cidrBlock: "10.0.0.0/16"
        + region: "us-west-1"

[-/+] Instance.rds.aws.upbound.io prod-db (replace)
    spec:
      forProvider:
        ~ engineVersion: "14.7" -> "15.4"  # forces replacement

[=] Key.kms.aws.upbound.io my-key (no-op)

[?] Bucket.s3.aws.upbound.io my-bucket (update)
    (approximate: no diff server for Bucket.s3.aws.upbound.io)
    spec:
      forProvider:
        ~ forceDestroy: false -> true

[!] Subnet.ec2.aws.upbound.io my-subnet (error)
    cannot plan: rpc error: code = Internal desc = boom

Plan: 1 to add, 1 to change, 0 to destroy, 1 requires replacement.

Warning: 1 resource has fields that force replacement: engineVersion.
Crossplane providers never destroy and recreate to apply a change.
Applying this leaves the resource unsynced until it is replaced manually.

Warning: 1 result is approximate, computed client-side without a provider diff
server: no replacement detection, no computed values.

Warning: 1 resource could not be planned.
`

	var buf bytes.Buffer
	r, err := NewRenderer(FormatPlan)
	if err != nil {
		t.Fatalf("NewRenderer(plan): unexpected error: %v", err)
	}
	if err := r.Render(&buf, NewResult(testPlans())); err != nil {
		t.Fatalf("Render(plan): unexpected error: %v", err)
	}
	if diff := cmp.Diff(want, buf.String()); diff != "" {
		t.Errorf("Render(plan): -want, +got:\n%s", diff)
	}
}

func TestRenderPlanReplaceWithoutReportedCause(t *testing.T) {
	// A diff server can decide on a replacement without flagging any reported
	// change as the cause (the forcing change can be a collection size or an
	// unresolved Secret-backed parameter, which it does not report as fields).
	// The warning must say so instead of listing nothing. A forcing change
	// with no value on either side still renders, so the cause is never
	// silently dropped from the tree.
	plans := []*ResourcePlan{{
		APIVersion: "containerservice.azure.upbound.io/v1beta2", Kind: "KubernetesCluster", Name: "my-aks",
		Action: ActionReplace,
		Changes: []FieldChange{
			{Path: "spec.forProvider.windowsProfile.adminPassword", Actual: &Value{Sensitive: true}, Planned: &Value{Unresolved: true}},
			{Path: "spec.forProvider.defaultNodePool.kubeletConfig", RequiresReplace: true},
		},
		ReplacePaths: []string{"spec.forProvider.defaultNodePool.kubeletConfig"},
	}, {
		APIVersion: "containerservice.azure.upbound.io/v1beta2", Kind: "KubernetesCluster", Name: "other-aks",
		Action: ActionReplace,
		Changes: []FieldChange{
			{Path: "spec.forProvider.windowsProfile.adminPassword", Actual: &Value{Sensitive: true}, Planned: &Value{Unresolved: true}},
		},
	}}

	want := `[-/+] KubernetesCluster.containerservice.azure.upbound.io my-aks (replace)
    spec:
      forProvider:
        defaultNodePool:
          ! kubeletConfig: (value not reported)  # forces replacement
        windowsProfile:
          ~ adminPassword: (sensitive value) -> (unresolved reference)

[-/+] KubernetesCluster.containerservice.azure.upbound.io other-aks (replace)
    spec:
      forProvider:
        windowsProfile:
          ~ adminPassword: (sensitive value) -> (unresolved reference)

Plan: 0 to add, 0 to change, 0 to destroy, 2 require replacement.

Warning: 1 resource has fields that force replacement: kubeletConfig.
Warning: 1 resource requires replacement, but the provider did not report which field forces it.
Crossplane providers never destroy and recreate to apply a change.
Applying this leaves the resource unsynced until it is replaced manually.
`

	var buf bytes.Buffer
	r, _ := NewRenderer(FormatPlan)
	if err := r.Render(&buf, NewResult(plans)); err != nil {
		t.Fatalf("Render(plan): unexpected error: %v", err)
	}
	if diff := cmp.Diff(want, buf.String()); diff != "" {
		t.Errorf("Render(plan): -want, +got:\n%s", diff)
	}

	// With no replace path reported anywhere, the warning says the provider
	// did not name the forcing field rather than listing an empty set.
	var buf2 bytes.Buffer
	if err := r.Render(&buf2, NewResult(plans[1:])); err != nil {
		t.Fatalf("Render(plan): unexpected error: %v", err)
	}
	wantWarning := "Warning: 1 resource requires replacement, but the provider did not report which field forces it.\n"
	if !bytes.Contains(buf2.Bytes(), []byte(wantWarning)) {
		t.Errorf("Render(plan): want output to contain %q, got:\n%s", wantWarning, buf2.String())
	}
}

func TestNewRendererUnknownFormat(t *testing.T) {
	if _, err := NewRenderer("diff"); err == nil {
		t.Errorf("NewRenderer(diff): want an error for an unsupported format")
	}
}

func TestRenderJSON(t *testing.T) {
	var buf bytes.Buffer
	r, err := NewRenderer(FormatJSON)
	if err != nil {
		t.Fatalf("NewRenderer(json): unexpected error: %v", err)
	}
	if err := r.Render(&buf, NewResult(testPlans())); err != nil {
		t.Fatalf("Render(json): unexpected error: %v", err)
	}

	got := Result{}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("Render(json): output is not valid JSON: %v", err)
	}

	want := Summary{Create: 1, Update: 1, Replace: 1, NoOp: 1, Approximate: 1, Errored: 1}
	if diff := cmp.Diff(want, got.Summary); diff != "" {
		t.Errorf("Render(json): -want summary, +got:\n%s", diff)
	}
	if len(got.Plans) != 5 {
		t.Errorf("Render(json): want 5 plans, got %d", len(got.Plans))
	}
	if got.FormatVersion != ResultFormatVersion {
		t.Errorf("Render(json): want formatVersion %q, got %q", ResultFormatVersion, got.FormatVersion)
	}
	if !got.Errored {
		t.Errorf("Render(json): want errored true with a failed plan in the set")
	}
}

func TestSummarize(t *testing.T) {
	got := Summarize(testPlans())
	want := Summary{Create: 1, Update: 1, Replace: 1, NoOp: 1, Approximate: 1, Errored: 1}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Summarize(...): -want, +got:\n%s", diff)
	}
}

func TestRenderPlanGrouped(t *testing.T) {
	xr := ResourceRef{APIVersion: "example.org/v1", Kind: "XNetwork", Name: "prod-network"}
	pinned := ResourceRef{APIVersion: "example.org/v1", Kind: "XNetwork", Name: "staging-network"}
	r := Result{
		Summary: Summary{Update: 1, Destroy: 1, NoOp: 1, Deferred: 1},
		Composites: []CompositeResult{
			{Ref: xr, Status: CompositeChanged},
			{Ref: pinned, Status: CompositeDeferred, Reason: "manual_policy", Detail: "compositionRevisionRef: net-6"},
		},
		Plans: []*ResourcePlan{
			// The XR's own no-op plan is not printed.
			{APIVersion: xr.APIVersion, Kind: xr.Kind, Name: xr.Name, Action: ActionNoOp, Composite: &xr},
			{
				APIVersion: "ec2.aws.upbound.io/v1beta1", Kind: "SecurityGroupRule", Name: "prod-network-ssh",
				Action: ActionUpdate, Composite: &xr,
				Changes: []FieldChange{{Path: "spec.forProvider.fromPort", Actual: &Value{Value: 22}, Planned: &Value{Value: 2222}}},
			},
			{
				APIVersion: "ec2.aws.upbound.io/v1beta1", Kind: "Route", Name: "prod-network-default",
				Action: ActionDelete, Composite: &xr,
				Changes: FieldChanges(map[string]any{"spec": map[string]any{"region": "us-west-1"}}, nil),
			},
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
	r2, _ := NewRenderer(FormatPlan)
	if err := r2.Render(&buf, r); err != nil {
		t.Fatalf("Render(plan): unexpected error: %v", err)
	}
	if diff := cmp.Diff(want, buf.String()); diff != "" {
		t.Errorf("Render(plan): -want, +got:\n%s", diff)
	}
}

func TestRenderJSONProjectFields(t *testing.T) {
	xr := ResourceRef{APIVersion: "example.org/v1", Kind: "XNetwork", Name: "prod-network"}
	in := Result{
		FormatVersion: ResultFormatVersion,
		Errored:       true,
		Summary:       Summary{Destroy: 1, Deferred: 1},
		Composites:    []CompositeResult{{Ref: xr, Status: CompositeDeferred, Reason: "manual_policy"}},
		Compositions: []CompositionResult{{
			Ref:            ResourceRef{APIVersion: "apiextensions.crossplane.io/v1", Kind: "Composition", Name: "net"},
			RevisionImpact: &RevisionImpact{ChangeScope: "spec", CreatesRevision: true, RepointedComposites: 2, PredictedRevision: "net-abc1234"},
		}},
		Plans: []*ResourcePlan{{
			APIVersion: "v1", Kind: "ConfigMap", Name: "cm", Action: ActionDelete, Composite: &xr,
			Changes: []FieldChange{{Path: "data.k", Actual: &Value{Value: "v"}}},
			DryRun:  &Verification{SkipReason: "forbidden", Detail: "no create permission"},
		}},
		Errors: []Error{{
			Resource: &xr, Message: "invalid",
			ValidationFailures: []ValidationFailure{{
				APIVersion: "v1", Kind: "ConfigMap", Name: "cm", Status: "invalid",
				Errors: []FieldValidationError{{Type: "schema", Field: "data", Message: "bad"}},
			}},
		}},
		Warnings: []Warning{{Message: "heads up", Context: map[string]string{"k": "v"}}},
	}
	var buf bytes.Buffer
	r, _ := NewRenderer(FormatJSON)
	if err := r.Render(&buf, in); err != nil {
		t.Fatalf("Render(json): unexpected error: %v", err)
	}
	got := Result{}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("Render(json): output is not valid JSON: %v", err)
	}
	if diff := cmp.Diff(in, got); diff != "" {
		t.Errorf("Render(json): -want, +got:\n%s", diff)
	}
}

func TestRenderJSONOmitsProjectFields(t *testing.T) {
	var buf bytes.Buffer
	r, _ := NewRenderer(FormatJSON)
	if err := r.Render(&buf, NewResult(testPlans())); err != nil {
		t.Fatalf("Render(json): unexpected error: %v", err)
	}
	for _, k := range []string{`"composites"`, `"compositions"`, `"errors"`, `"warnings"`, `"deferred"`, `"composite"`, `"dryRun"`} {
		if bytes.Contains(buf.Bytes(), []byte(k)) {
			t.Errorf("Render(json): resource simulate output should omit %s", k)
		}
	}
}
