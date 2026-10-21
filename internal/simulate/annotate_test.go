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
	"maps"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/sergi/go-diff/diffmatchpatch"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	sigsyaml "sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	"github.com/crossplane/cli/v2/internal/diff/compare"
	"github.com/crossplane/cli/v2/internal/diff/renderer"
	dt "github.com/crossplane/cli/v2/internal/diff/result"
	"github.com/crossplane/cli/v2/internal/plan"
)

var dbGVK = schema.GroupVersionKind{Group: "rds.aws.upbound.io", Version: "v1beta1", Kind: "Instance"}

// lineDiff builds a resource diff of dbGVK as the engine does: the YAML of
// each side, diffed by line. A nil side is absent.
func lineDiff(t *testing.T, name string, typ dt.DiffType, current, desired map[string]any) *dt.ResourceDiff {
	t.Helper()

	text := func(spec map[string]any) (*unstructured.Unstructured, string) {
		if spec == nil {
			return nil, ""
		}
		u := obj(dbGVK, name, map[string]any{"spec": spec})
		b, err := sigsyaml.Marshal(u.Object)
		if err != nil {
			t.Fatalf("Marshal(): %v", err)
		}
		return u, string(b)
	}
	cur, curText := text(current)
	des, desText := text(desired)

	d := &dt.ResourceDiff{
		Gvk: dbGVK, ResourceName: name, DiffType: typ,
		Current: dt.ResourceViews{Raw: cur}, Desired: dt.ResourceViews{Raw: des},
	}
	if typ != dt.DiffTypeEqual {
		d.LineDiffs = compare.GetLineDiff(curText, desText)
	}

	return d
}

// render renders r as -o diff does with plans: annotated, and with the
// Replaced hook set.
func render(t *testing.T, r *dt.XRReport, plans map[string]*plan.ResourcePlan, opts renderer.DiffOptions) string {
	t.Helper()

	var stdout bytes.Buffer
	opts.Stdout, opts.Stderr = &stdout, &bytes.Buffer{}
	opts.Replaced = ReplacedBy(plans)
	if err := renderer.NewDiffRenderer(logging.NewNopLogger(), opts).RenderDiffs(AnnotateXRReport(r, plans).Groups, nil, nil); err != nil {
		t.Fatalf("RenderDiffs(): %v", err)
	}

	return stdout.String()
}

func noColor() renderer.DiffOptions {
	o := renderer.DefaultDiffOptions()
	o.UseColors = false
	return o
}

func str(s string) *plan.Value { return &plan.Value{Value: s} }
func unknown() *plan.Value     { return &plan.Value{Unknown: true} }
func sensitive() *plan.Value   { return &plan.Value{Sensitive: true} }
func report(ds ...*dt.ResourceDiff) *dt.XRReport {
	return &dt.XRReport{Groups: []dt.XRDiffGroup{{XR: xrRef, Diffs: diffs(ds...)}}}
}

func planFor(d *dt.ResourceDiff, a plan.Action, changes ...plan.FieldChange) map[string]*plan.ResourcePlan {
	return map[string]*plan.ResourcePlan{d.GetDiffKey(): {
		APIVersion: d.Gvk.GroupVersion().String(), Kind: d.Gvk.Kind, Name: d.ResourceName, Action: a, Changes: changes,
	}}
}

func TestAnnotateXRReport(t *testing.T) {
	fp := func(m map[string]any) map[string]any { return map[string]any{"forProvider": m} }

	modified := lineDiff(t, "db", dt.DiffTypeModified,
		fp(map[string]any{"region": "us-east-1", "engineVersion": "5.7", "arn": "arn:old"}),
		fp(map[string]any{"region": "us-west-2", "engineVersion": "5.7", "arn": "arn:old"}))
	added := lineDiff(t, "db", dt.DiffTypeAdded, nil,
		fp(map[string]any{"region": "us-west-2", "arn": "", "ports": []any{map[string]any{"name": "a", "port": int64(1)}}}))
	secret := lineDiff(t, "db", dt.DiffTypeModified,
		fp(map[string]any{"password": "hunter2", "size": int64(1)}),
		fp(map[string]any{"password": "hunter3", "size": int64(2)}))
	equal := lineDiff(t, "db", dt.DiffTypeEqual, fp(map[string]any{"region": "us-east-1"}), fp(map[string]any{"region": "us-east-1"}))
	policy := lineDiff(t, "db", dt.DiffTypeModified,
		fp(map[string]any{"policy": "a\nb\n", "tags": []any{map[string]any{"key": "k", "value": "1"}}}),
		fp(map[string]any{"policy": "a\nc\n", "tags": []any{map[string]any{"key": "k", "value": "2"}}}))

	cases := map[string]struct {
		reason string
		in     *dt.XRReport
		plans  map[string]*plan.ResourcePlan
		opts   renderer.DiffOptions
		want   string
	}{
		"ModifiedReplaceAndUnknown": {
			reason: "A forcing field's + line is marked, the header and summary say replace, an equal computed field splits, and an absent one is added under its deepest ancestor.",
			in:     report(modified),
			plans: planFor(modified, plan.ActionReplace,
				plan.FieldChange{Path: "spec.forProvider.region", Actual: str("us-east-1"), Planned: str("us-west-2"), RequiresReplace: true},
				plan.FieldChange{Path: "spec.forProvider.arn", Actual: str("arn:old"), Planned: unknown()},
				plan.FieldChange{Path: "spec.forProvider.endpoint.address", Planned: unknown()},
				plan.FieldChange{Path: "status.atProvider.id", Planned: unknown()},
			),
			opts: noColor(),
			want: `~~~ Instance/db (requires replacement)
  apiVersion: rds.aws.upbound.io/v1beta1
  kind: Instance
  metadata:
    name: db
  spec:
    forProvider:
-     arn: arn:old
+     arn: (known after apply)
      engineVersion: "5.7"
-     region: us-east-1
+     region: us-west-2  # forces replacement
+     endpoint.address: (known after apply)
+ status.atProvider.id: (known after apply)

---

Summary: 1 modified (1 requires replacement)
`,
		},
		"AddedCreate": {
			reason: "On a create, a + value the provider computes is rewritten, a field in a list item is added under it, and an absent forcing field marks its deepest ancestor's line.",
			in:     report(added),
			plans: planFor(added, plan.ActionCreate,
				plan.FieldChange{Path: "spec.forProvider.arn", Planned: unknown()},
				plan.FieldChange{Path: "spec.forProvider.ports[0].id", Planned: unknown()},
				plan.FieldChange{Path: "spec.forProvider.subnetIds", Planned: str("x"), RequiresReplace: true},
			),
			opts: noColor(),
			want: `+++ Instance/db
+ apiVersion: rds.aws.upbound.io/v1beta1
+ kind: Instance
+ metadata:
+   name: db
+ spec:
+   forProvider:  # forces replacement
+     arn: (known after apply)
+     ports:
+     - name: a
+       port: 1
+       id: (known after apply)
+     region: us-west-2

---

Summary: 1 added
`,
		},
		"Compact": {
			reason: "Compact mode sees the annotated lines as ordinary changes, with its context around them.",
			in:     report(modified),
			plans: planFor(modified, plan.ActionUpdate,
				plan.FieldChange{Path: "spec.forProvider.arn", Actual: str("arn:old"), Planned: unknown()},
			),
			opts: func() renderer.DiffOptions { o := noColor(); o.Compact, o.ContextLines = true, 1; return o }(),
			want: `~~~ Instance/db
    forProvider:
-     arn: arn:old
+     arn: (known after apply)
      engineVersion: "5.7"
-     region: us-east-1
+     region: us-west-2

---

Summary: 1 modified
`,
		},
		"Colors": {
			reason: "With colors on, annotated lines are colored as the side they are on.",
			in:     report(modified),
			plans: planFor(modified, plan.ActionReplace,
				plan.FieldChange{Path: "spec.forProvider.region", Actual: str("us-east-1"), Planned: str("us-west-2"), RequiresReplace: true},
			),
			opts: func() renderer.DiffOptions {
				o := renderer.DefaultDiffOptions()
				o.Compact, o.ContextLines = true, 0
				return o
			}(),
			want: "~~~ Instance/db (requires replacement)\n" +
				dt.ColorRed + "-     region: us-east-1" + dt.ColorReset + "\n" +
				dt.ColorGreen + "+     region: us-west-2  # forces replacement" + dt.ColorReset + "\n" +
				"\n---\n\nSummary: 1 modified (1 requires replacement)\n",
		},
		"Sensitive": {
			reason: "A value the provider marks sensitive is never printed, on either side.",
			in:     report(secret),
			plans: planFor(secret, plan.ActionUpdate,
				plan.FieldChange{Path: "spec.forProvider.password", Actual: sensitive(), Planned: sensitive()},
			),
			opts: func() renderer.DiffOptions { o := noColor(); o.Compact, o.ContextLines = true, 0; return o }(),
			want: `~~~ Instance/db
-     password: (sensitive value)
-     size: 1
+     password: (sensitive value)
+     size: 2

---

Summary: 1 modified
`,
		},
		"Unplaceable": {
			reason: "A path the walker cannot place (a list keyed by a field, a field under a scalar) gets no inline annotation; the header and summary still say replace.",
			in:     report(policy),
			plans: planFor(policy, plan.ActionReplace,
				plan.FieldChange{Path: `spec.forProvider.tags[key="k"].value`, Actual: str("1"), Planned: str("2"), RequiresReplace: true},
				plan.FieldChange{Path: "spec.forProvider.policy.version", Planned: unknown()},
			),
			opts: func() renderer.DiffOptions { o := noColor(); o.Compact, o.ContextLines = true, 0; return o }(),
			want: `~~~ Instance/db (requires replacement)
-       b
+       c
...
-       value: "1"
+       value: "2"

---

Summary: 1 modified (1 requires replacement)
`,
		},
		"Drift": {
			reason: "An equal diff the provider would change renders as a modified block of its changes, marked as elsewhere.",
			in:     report(equal),
			plans: planFor(equal, plan.ActionReplace,
				plan.FieldChange{Path: "spec.forProvider.region", Actual: str("us-east-1"), Planned: str("us-west-2"), RequiresReplace: true},
				plan.FieldChange{Path: "spec.forProvider.arn", Actual: str("arn:old"), Planned: unknown()},
				plan.FieldChange{Path: "spec.forProvider.password", Actual: sensitive(), Planned: sensitive()},
				plan.FieldChange{Path: "spec.forProvider.zone", Actual: str("a")},
			),
			opts: noColor(),
			want: `~~~ Instance/db (requires replacement)
- spec.forProvider.arn: "arn:old"
+ spec.forProvider.arn: (known after apply)
- spec.forProvider.password: (sensitive value)
+ spec.forProvider.password: (sensitive value)
- spec.forProvider.region: "us-east-1"
+ spec.forProvider.region: "us-west-2"  # forces replacement
- spec.forProvider.zone: "a"

---

Summary: 1 modified (1 requires replacement)
`,
		},
		"DriftNoOp": {
			reason: "An equal diff the provider would not change stays equal and renders nothing.",
			in:     report(equal),
			plans:  planFor(equal, plan.ActionNoOp, plan.FieldChange{Path: "spec.forProvider.region", Actual: str("a"), Planned: str("a")}),
			opts:   noColor(),
			want:   "",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := render(t, tc.in, tc.plans, tc.opts)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("\n%s\nrender(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

// TestAnnotateIdentity pins that no provider plan, or one that cannot
// annotate, renders -o diff exactly as it renders without annotation.
func TestAnnotateIdentity(t *testing.T) {
	fp := func(m map[string]any) map[string]any { return map[string]any{"forProvider": m} }
	d := lineDiff(t, "db", dt.DiffTypeModified, fp(map[string]any{"region": "a"}), fp(map[string]any{"region": "b"}))
	r := report(d)

	var want bytes.Buffer
	opts := noColor()
	opts.Stdout, opts.Stderr = &want, &bytes.Buffer{}
	if err := renderer.NewDiffRenderer(logging.NewNopLogger(), opts).RenderDiffs(r.Groups, nil, nil); err != nil {
		t.Fatalf("RenderDiffs(): %v", err)
	}

	region := plan.FieldChange{Path: "spec.forProvider.region", Actual: str("a"), Planned: unknown(), RequiresReplace: true}
	cases := map[string]map[string]*plan.ResourcePlan{
		"NilPlans":    nil,
		"EmptyPlans":  {},
		"OtherKey":    {"other": {Action: plan.ActionReplace, Changes: []plan.FieldChange{region}}},
		"Approximate": {d.GetDiffKey(): {Action: plan.ActionUpdate, Approximate: true, Changes: []plan.FieldChange{region}}},
		"Error":       {d.GetDiffKey(): {Error: "boom", Changes: []plan.FieldChange{region}}},
	}
	for name, plans := range cases {
		t.Run(name, func(t *testing.T) {
			if diff := cmp.Diff(want.String(), render(t, r, plans, noColor())); diff != "" {
				t.Errorf("render(...): -want, +got:\n%s", diff)
			}
		})
	}

	if ReplacedBy(nil) != nil || ReplacedBy(map[string]*plan.ResourcePlan{}) != nil {
		t.Error("ReplacedBy(empty): want nil, so -o diff takes its unannotated path")
	}
}

// TestAnnotateDoesNotMutate pins that annotation works on copies only.
func TestAnnotateDoesNotMutate(t *testing.T) {
	fp := func(m map[string]any) map[string]any { return map[string]any{"forProvider": m} }
	mod := lineDiff(t, "db", dt.DiffTypeModified, fp(map[string]any{"region": "a"}), fp(map[string]any{"region": "b"}))
	eq := lineDiff(t, "eq", dt.DiffTypeEqual, fp(map[string]any{"region": "a"}), fp(map[string]any{"region": "a"}))
	plans := planFor(mod, plan.ActionReplace,
		plan.FieldChange{Path: "spec.forProvider.region", Actual: str("a"), Planned: str("b"), RequiresReplace: true},
		plan.FieldChange{Path: "spec.forProvider.id", Planned: unknown()})
	maps.Copy(plans, planFor(eq, plan.ActionUpdate, plan.FieldChange{Path: "spec.forProvider.region", Actual: str("a"), Planned: str("b")}))

	xr := report(mod, eq)
	comp := &dt.CompDiffOutput{Compositions: []dt.CompositionDiff{{
		Name:              "net",
		AffectedResources: dt.AffectedResourcesSummary{Total: 1, Unchanged: 1},
		ImpactAnalysis:    []dt.XRImpact{{ObjectReference: xrRef, Status: dt.XRStatusUnchanged, Diffs: diffs(eq)}},
	}}}
	wantXR, wantComp := cloneXR(xr), cloneComp(comp)

	gotXR, gotComp := AnnotateXRReport(xr, plans), AnnotateCompReport(comp, plans)

	if diff := cmp.Diff(wantXR, xr); diff != "" {
		t.Errorf("AnnotateXRReport(...) mutated its input: -want, +got:\n%s", diff)
	}
	if diff := cmp.Diff(wantComp, comp); diff != "" {
		t.Errorf("AnnotateCompReport(...) mutated its input: -want, +got:\n%s", diff)
	}

	// The copies are annotated.
	if got := gotXR.Groups[0].Diffs[eq.GetDiffKey()].DiffType; got != dt.DiffTypeModified {
		t.Errorf("AnnotateXRReport(...): drifted diff type %q, want %q", got, dt.DiffTypeModified)
	}
	c := gotComp.Compositions[0]
	if c.ImpactAnalysis[0].Status != dt.XRStatusChanged || c.AffectedResources.Unchanged != 0 || c.AffectedResources.WithChanges != 1 {
		t.Errorf("AnnotateCompReport(...): drifted composite %q with %+v, want changed and counted", c.ImpactAnalysis[0].Status, c.AffectedResources)
	}
}

// TestAnnotateCompReport pins that a composite the provider finds drift
// under renders as changed, with its drift in the impact analysis.
func TestAnnotateCompReport(t *testing.T) {
	fp := func(m map[string]any) map[string]any { return map[string]any{"forProvider": m} }
	eq := lineDiff(t, "db", dt.DiffTypeEqual, fp(map[string]any{"region": "a"}), fp(map[string]any{"region": "a"}))
	plans := planFor(eq, plan.ActionUpdate, plan.FieldChange{Path: "spec.forProvider.region", Actual: str("a"), Planned: str("b")})
	comp := &dt.CompDiffOutput{Compositions: []dt.CompositionDiff{{
		Name:              "net",
		AffectedResources: dt.AffectedResourcesSummary{Total: 1, Unchanged: 1},
		ImpactAnalysis:    []dt.XRImpact{{ObjectReference: xrRef, Status: dt.XRStatusUnchanged, Diffs: diffs(eq)}},
	}}}

	var stdout bytes.Buffer
	opts := noColor()
	opts.Stdout, opts.Stderr, opts.Replaced = &stdout, &bytes.Buffer{}, ReplacedBy(plans)
	log := logging.NewNopLogger()
	r := renderer.NewDefaultCompDiffRenderer(log, renderer.NewDiffRenderer(log, opts), opts)
	if err := r.RenderCompDiff(AnnotateCompReport(comp, plans)); err != nil {
		t.Fatalf("RenderCompDiff(): %v", err)
	}

	for _, want := range []string{
		"XNetwork/prod (cluster-scoped)",
		"1 resource with changes",
		"~~~ Instance/db\n- spec.forProvider.region: \"a\"\n+ spec.forProvider.region: \"b\"\n",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("RenderCompDiff(...): want %q in:\n%s", want, stdout.String())
		}
	}
}

func cloneXR(r *dt.XRReport) *dt.XRReport {
	out := *r
	out.Groups = nil
	for _, g := range r.Groups {
		g.Diffs = cloneDiffs(g.Diffs)
		out.Groups = append(out.Groups, g)
	}
	return &out
}

func cloneComp(o *dt.CompDiffOutput) *dt.CompDiffOutput {
	out := *o
	out.Compositions = nil
	for _, c := range o.Compositions {
		impacts := make([]dt.XRImpact, 0, len(c.ImpactAnalysis))
		for _, i := range c.ImpactAnalysis {
			i.Diffs = cloneDiffs(i.Diffs)
			impacts = append(impacts, i)
		}
		c.ImpactAnalysis = impacts
		out.Compositions = append(out.Compositions, c)
	}
	return &out
}

func cloneDiffs(m map[string]*dt.ResourceDiff) map[string]*dt.ResourceDiff {
	out := map[string]*dt.ResourceDiff{}
	for k, d := range m {
		c := *d
		c.LineDiffs = append([]diffmatchpatch.Diff(nil), d.LineDiffs...)
		out[k] = &c
	}
	return out
}
