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
	"maps"
	"slices"
	"strconv"
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
	wrap := func(spec map[string]any) map[string]any {
		if spec == nil {
			return nil
		}
		return map[string]any{"spec": spec}
	}
	return objDiff(t, name, typ, wrap(current), wrap(desired))
}

// objDiff builds a diff as lineDiff does from each side's top-level fields.
func objDiff(t *testing.T, name string, typ dt.DiffType, current, desired map[string]any) *dt.ResourceDiff {
	t.Helper()

	text := func(fields map[string]any) (*unstructured.Unstructured, string) {
		if fields == nil {
			return nil, ""
		}
		u := obj(dbGVK, name, fields)
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
		// As GenerateDiff: only a non-equal diff carries Clean views.
		d.Current.Clean, d.Desired.Clean = cur, des
		d.LineDiffs = compare.GetLineDiff(curText, desText)
	}

	return d
}

// textDiff builds a resource diff of dbGVK from the YAML of each side,
// diffed by line, for YAML lineDiff cannot marshal.
func textDiff(typ dt.DiffType, current, desired string) *dt.ResourceDiff {
	return &dt.ResourceDiff{Gvk: dbGVK, ResourceName: "db", DiffType: typ, LineDiffs: compare.GetLineDiff(current, desired)}
}

// render renders r as -o diff does with plans: annotated, and with the
// Replaced hook set.
func render(t *testing.T, r *dt.XRReport, plans map[string]*plan.ResourcePlan, opts renderer.DiffOptions) string {
	t.Helper()

	var stdout bytes.Buffer
	opts.Stdout, opts.Stderr = &stdout, &bytes.Buffer{}
	var ds []*dt.ResourceDiff
	for _, g := range r.Groups {
		ds = slices.AppendSeq(ds, maps.Values(g.Diffs))
	}
	pp := held(plans, ds...)
	opts.Replaced = ReplacedBy(pp)
	if err := renderer.NewDiffRenderer(logging.NewNopLogger(), opts).RenderDiffs(AnnotateXRReport(r, pp).Groups, nil, nil); err != nil {
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
	keyedSecret := lineDiff(t, "db", dt.DiffTypeModified,
		fp(map[string]any{"tags": []any{map[string]any{"key": "k", "pw": "hunter2"}}}),
		fp(map[string]any{"tags": []any{map[string]any{"key": "k", "pw": "hunter3"}}}))
	addedSecret := lineDiff(t, "db", dt.DiffTypeAdded, nil, fp(map[string]any{"password": "hunter2", "size": int64(1)}))
	zones := lineDiff(t, "db", dt.DiffTypeModified,
		fp(map[string]any{"zones": []any{"hunter1", "hunter2"}}),
		fp(map[string]any{"zones": []any{"hunter1", "hunter3"}}))
	quoted := lineDiff(t, "db", dt.DiffTypeModified, fp(map[string]any{"a: b": "hunter2"}), fp(map[string]any{"a: b": "hunter3"}))
	equal := lineDiff(t, "db", dt.DiffTypeEqual, fp(map[string]any{"region": "us-east-1"}), fp(map[string]any{"region": "us-east-1"}))
	policy := textDiff(dt.DiffTypeModified,
		"spec:\n  forProvider:\n    policy: >\n      {\"version\": \"1\"}\n    tags:\n    - key: k\n      value: \"1\"\n",
		"spec:\n  forProvider:\n    policy: >\n      {\"version\": \"2\"}\n    tags:\n    - key: k\n      value: \"2\"\n")

	tagged := lineDiff(t, "db", dt.DiffTypeModified,
		fp(map[string]any{"policy": `{"version": "1"}`, "tags": []any{map[string]any{"key": "k", "value": "1"}}}),
		fp(map[string]any{"policy": `{"version": "2"}`, "tags": []any{map[string]any{"key": "k", "value": "2"}}}))

	cases := map[string]struct {
		reason string
		in     *dt.XRReport
		plans  map[string]*plan.ResourcePlan
		opts   renderer.DiffOptions
		want   string
	}{
		// The engine's semantic cleanup groups nearby changes, so lines between them print as -/+.
		"ModifiedReplaceAndUnknown": {
			reason: "A forcing field's + line is marked, the header and summary say replace, an equal computed field splits, and an absent one is added as nested levels.",
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
-     engineVersion: "5.7"
-     region: us-east-1
+     arn: (known after apply)
+     endpoint:
+       address: (known after apply)
+     engineVersion: "5.7"
+     region: us-west-2  # forces replacement
+ status:
+   atProvider:
+     id: (known after apply)
  
  
  

---

Summary: 1 modified (1 requires replacement)
`,
		},
		// Keys print in sorted order, so a computed id comes first in its item.
		"AddedCreate": {
			reason: "On a create, a + value the provider computes is rewritten, a field in a list item is added in it, and an absent forcing field marks its deepest ancestor's line.",
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
+     - id: (known after apply)
+       name: a
+       port: 1
+     region: us-west-2

---

Summary: 1 added
`,
		},
		// The engine's semantic cleanup groups nearby changes, so lines between them print as -/+.
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
-     engineVersion: "5.7"
-     region: us-east-1
+     arn: (known after apply)
+     engineVersion: "5.7"
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
+     password: (sensitive value, changed)
+     size: 2

---

Summary: 1 modified
`,
		},
		"SensitiveForcesReplace": {
			reason: "A redacted field of a keyed list item that forces replacement keeps the note.",
			in:     report(keyedSecret),
			plans: planFor(keyedSecret, plan.ActionReplace,
				plan.FieldChange{Path: `spec.forProvider.tags[key="k"].pw`, Actual: sensitive(), Planned: sensitive(), RequiresReplace: true},
			),
			opts: func() renderer.DiffOptions { o := noColor(); o.Compact, o.ContextLines = true, 0; return o }(),
			want: `~~~ Instance/db (requires replacement)
-       pw: (sensitive value)
+       pw: (sensitive value, changed)  # forces replacement

---

Summary: 1 modified (1 requires replacement)
`,
		},
		"SensitiveAdded": {
			reason: "A sensitive value in an added diff prints the plain marker, never the changed one.",
			in:     report(addedSecret),
			plans:  planFor(addedSecret, plan.ActionCreate, plan.FieldChange{Path: "spec.forProvider.password", Planned: sensitive()}),
			opts:   noColor(),
			want: `+++ Instance/db
+ apiVersion: rds.aws.upbound.io/v1beta1
+ kind: Instance
+ metadata:
+   name: db
+ spec:
+   forProvider:
+     password: (sensitive value)
+     size: 1

---

Summary: 1 added
`,
		},
		"SensitiveIndex": {
			reason: "An index path redacts every item on both sides, the changed marker only on the changed item.",
			in:     report(zones),
			plans:  planFor(zones, plan.ActionUpdate, plan.FieldChange{Path: "spec.forProvider.zones[1]", Actual: sensitive(), Planned: sensitive()}),
			opts:   func() renderer.DiffOptions { o := noColor(); o.Compact, o.ContextLines = true, 1; return o }(),
			want: `~~~ Instance/db
      - (sensitive value)
-     - (sensitive value)
+     - (sensitive value, changed)

---

Summary: 1 modified
`,
		},
		"SensitiveQuotedKey": {
			reason: "A sensitive key YAML must quote is redacted.",
			in:     report(quoted),
			plans:  planFor(quoted, plan.ActionUpdate, plan.FieldChange{Path: "spec.forProvider[a: b]", Actual: sensitive(), Planned: sensitive()}),
			opts:   func() renderer.DiffOptions { o := noColor(); o.Compact, o.ContextLines = true, 0; return o }(),
			want: `~~~ Instance/db
-     'a: b': (sensitive value)
+     'a: b': (sensitive value, changed)

---

Summary: 1 modified
`,
		},
		"SensitiveThenUnknown": {
			reason: "A value sensitive before and computed after prints the marker, then known after apply.",
			in:     report(secret),
			plans:  planFor(secret, plan.ActionUpdate, plan.FieldChange{Path: "spec.forProvider.password", Actual: sensitive(), Planned: unknown()}),
			opts:   func() renderer.DiffOptions { o := noColor(); o.Compact, o.ContextLines = true, 0; return o }(),
			want: `~~~ Instance/db
-     password: (sensitive value)
-     size: 1
+     password: (known after apply)
+     size: 2

---

Summary: 1 modified
`,
		},
		"CompactHidden": {
			reason: "Compact mode keeps a hidden body, with the header and summary, so the change still shows.",
			in:     report(policy),
			plans:  planFor(policy, plan.ActionUpdate, plan.FieldChange{Path: "spec.forProvider.policy", Actual: sensitive(), Planned: sensitive()}),
			opts:   func() renderer.DiffOptions { o := noColor(); o.Compact, o.ContextLines = true, 0; return o }(),
			want: `~~~ Instance/db
+ # diff hidden: it holds sensitive values that can't be redacted

---

Summary: 1 modified
`,
		},
		"Unplaceable": {
			reason: "An unknown inside a string is not set, and a field of a list item keyed by a field is marked.",
			in:     report(tagged),
			plans: planFor(tagged, plan.ActionReplace,
				plan.FieldChange{Path: `spec.forProvider.tags[key="k"].value`, Actual: str("1"), Planned: str("2"), RequiresReplace: true},
				plan.FieldChange{Path: "spec.forProvider.policy.version", Planned: unknown()},
			),
			opts: func() renderer.DiffOptions { o := noColor(); o.Compact, o.ContextLines = true, 0; return o }(),
			want: `~~~ Instance/db (requires replacement)
-     policy: '{"version": "1"}'
+     policy: '{"version": "2"}'
...
-       value: "1"
+       value: "2"  # forces replacement

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

func TestAnnotateSecret(t *testing.T) {
	gvk := schema.GroupVersionKind{Version: "v1", Kind: "Secret"}
	sec := func(data, stringData map[string]any) map[string]any {
		m := map[string]any{"data": data}
		if stringData != nil {
			m["stringData"] = stringData
		}
		return m
	}
	pem := map[string]any{"tls.crt": "PEMSECRET"}
	old := sec(map[string]any{"pw": "OLDSECRET", "same": "SAMESECRET"}, pem)
	nu := sec(map[string]any{"pw": "NEWSECRET", "same": "SAMESECRET"}, pem)
	cases := map[string]struct {
		typ      dt.DiffType
		cur, des map[string]any
		// want are lines the output must hold, after trimming spaces.
		want []string
	}{
		"Modified": {dt.DiffTypeModified, old, nu, []string{"- pw: (sensitive value)", "+ pw: (sensitive value, changed)", "same: (sensitive value)"}},
		"Added":    {dt.DiffTypeAdded, nil, nu, []string{"+ pw: (sensitive value)"}},
		"Removed":  {dt.DiffTypeRemoved, old, nil, []string{"- pw: (sensitive value)"}},
		"StringDataChanged": {dt.DiffTypeModified,
			sec(map[string]any{"same": "SAMESECRET"}, map[string]any{"tok": "OLDSECRET"}),
			sec(map[string]any{"same": "SAMESECRET"}, map[string]any{"tok": "NEWSECRET"}),
			[]string{"- tok: (sensitive value)", "+ tok: (sensitive value, changed)"}},
		"DockerConfig": {dt.DiffTypeAdded, nil,
			sec(map[string]any{".dockerconfigjson": "NEWSECRET"}, nil),
			[]string{"+ .dockerconfigjson: (sensitive value)"}},
		// A key that extends another, and one ending in a dot.
		"DottedKeys": {dt.DiffTypeModified,
			sec(map[string]any{"ca": "SAMESECRET", "ca.crt": "OLDSECRET", "a.": "PEMSECRET"}, nil),
			sec(map[string]any{"ca": "SAMESECRET", "ca.crt": "NEWSECRET", "a.": "PEMSECRET"}, nil),
			[]string{"- ca.crt: (sensitive value)", "+ ca.crt: (sensitive value, changed)", "ca: (sensitive value)", "a.: (sensitive value)"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			d := objDiff(t, "creds", tc.typ, tc.cur, tc.des)
			d.Gvk = gvk
			for _, u := range []*unstructured.Unstructured{d.Current.Clean, d.Desired.Clean} {
				if u != nil {
					u.SetGroupVersionKind(gvk)
				}
			}
			// No plans: a Secret is redacted without them.
			got := render(t, report(d), nil, noColor())
			if strings.Contains(got, diffHidden) {
				t.Errorf("render(): want redacted, not hidden:\n%s", got)
			}
			var lines []string
			for l := range strings.SplitSeq(got, "\n") {
				lines = append(lines, strings.Join(strings.Fields(l), " "))
			}
			for _, w := range tc.want {
				if !slices.Contains(lines, w) {
					t.Errorf("render(): want line %q:\n%s", w, got)
				}
			}
			for _, secret := range []string{"OLDSECRET", "NEWSECRET", "SAMESECRET", "PEMSECRET"} {
				if strings.Contains(got, secret) {
					t.Errorf("render(): output contains %q:\n%s", secret, got)
				}
			}
		})
	}
}

// TestAnnotateFailClosed pins that a removed diff honours its plan's
// sensitive paths, and that a Secret without the views to find its keys in
// has its body hidden.
func TestAnnotateFailClosed(t *testing.T) {
	removed := lineDiff(t, "db", dt.DiffTypeRemoved, map[string]any{"pw": "OLDSECRET"}, nil)
	secret := textDiff(dt.DiffTypeModified, "data:\n  pw: OLDSECRET\n", "data:\n  pw: NEWSECRET\n")
	secret.Gvk = schema.GroupVersionKind{Version: "v1", Kind: "Secret"}
	cases := map[string]struct {
		in    *dt.ResourceDiff
		plans map[string]*plan.ResourcePlan
		want  string
	}{
		"RemovedSensitive": {removed, planFor(removed, plan.ActionDelete, plan.FieldChange{Path: "spec.pw", Actual: sensitive()}), "pw: (sensitive value)"},
		"SecretWithoutViews": {secret, nil, diffHidden},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := render(t, report(tc.in), tc.plans, noColor())
			if !strings.Contains(got, tc.want) || strings.Contains(got, "SECRET") {
				t.Errorf("render(): want %q and no secret:\n%s", tc.want, got)
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

	if ReplacedBy(nil) != nil || ReplacedBy(&ProviderPlans{}) != nil {
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

	gotXR, gotComp := AnnotateXRReport(xr, held(plans, mod, eq)), AnnotateCompReport(comp, held(plans, mod, eq))

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
	opts.Stdout, opts.Stderr, opts.Replaced = &stdout, &bytes.Buffer{}, ReplacedBy(held(plans, eq))
	log := logging.NewNopLogger()
	r := renderer.NewDefaultCompDiffRenderer(log, renderer.NewDiffRenderer(log, opts), opts)
	if err := r.RenderCompDiff(AnnotateCompReport(comp, held(plans, eq))); err != nil {
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

// TestAnnotateDiffDriftReplaceNoFields pins that a kept-equal resource the
// provider would replace, without reporting a field, still renders.
func TestAnnotateDiffDriftReplaceNoFields(t *testing.T) {
	eq := lineDiff(t, "db", dt.DiffTypeEqual, map[string]any{"region": "a"}, map[string]any{"region": "a"})
	got := annotateDiff(eq, planFor(eq, plan.ActionReplace)[eq.GetDiffKey()])
	if got.DiffType != dt.DiffTypeModified {
		t.Fatalf("annotateDiff(...).DiffType = %q, want modified", got.DiffType)
	}
	if text := flatten(got.LineDiffs)[0].text; text != "# replaced by the provider; no field reported" {
		t.Errorf("annotateDiff(...) line = %q, want the replace comment", text)
	}
	if same := annotateDiff(eq, planFor(eq, plan.ActionUpdate)[eq.GetDiffKey()]); same != eq {
		t.Error("annotateDiff(...) with an update and no fields changed the diff, want it returned as is")
	}
}

// TestAnnotateOnlyItsRendering pins that a provider plan annotates only a
// diff that renders its resource as the planned diff did, and that a drifted
// equal diff it annotated is still found replaced.
func TestAnnotateOnlyItsRendering(t *testing.T) {
	fp := func(m map[string]any) map[string]any { return map[string]any{"forProvider": m} }
	planned := lineDiff(t, "db", dt.DiffTypeModified, fp(map[string]any{"region": "a"}), fp(map[string]any{"region": "b"}))
	other := lineDiff(t, "db", dt.DiffTypeModified, fp(map[string]any{"region": "a"}), fp(map[string]any{"region": "c"}))
	// The engine compares renderings by their Clean views.
	for _, d := range []*dt.ResourceDiff{planned, other} {
		d.Current.Clean, d.Desired.Clean = d.Current.Raw, d.Desired.Raw
	}
	region := plan.FieldChange{Path: "spec.forProvider.region", Actual: str("a"), Planned: str("b"), RequiresReplace: true}
	plans := held(planFor(planned, plan.ActionReplace, region), planned)

	if got := annotateDiffsOf(t, other, plans); got != other {
		t.Errorf("annotateDiffs(other): want a differing rendering left unannotated, got %+v", got)
	}
	if ReplacedBy(plans)(other) {
		t.Error("ReplacedBy(plans)(other) = true, want false for a differing rendering")
	}
	if got := annotateDiffsOf(t, planned, plans); got == planned {
		t.Error("annotateDiffs(planned): want the planned rendering annotated")
	}
	if !ReplacedBy(plans)(planned) {
		t.Error("ReplacedBy(plans)(planned) = false, want true")
	}

	eq := lineDiff(t, "eq", dt.DiffTypeEqual, fp(map[string]any{"region": "a"}), fp(map[string]any{"region": "a"}))
	drift := held(planFor(eq, plan.ActionReplace, region), eq)
	if got := annotateDiffsOf(t, eq, drift); got.DiffType != dt.DiffTypeModified || !ReplacedBy(drift)(got) {
		t.Errorf("drifted equal diff: want modified and replaced, got %q replaced=%t", got.DiffType, ReplacedBy(drift)(got))
	}
}

func annotateDiffsOf(t *testing.T, d *dt.ResourceDiff, plans *ProviderPlans) *dt.ResourceDiff {
	t.Helper()
	out, _ := annotateDiffs(map[string]*dt.ResourceDiff{"k": d}, plans)
	return out["k"]
}

// TestAnnotatePlacement pins where each -o diff marker lands: unknowns are
// set in the desired object, and replacements placed by a marshal probe of
// it. The blank context lines some diffs end with are the renderer's, with or
// without annotation.
func TestAnnotatePlacement(t *testing.T) {
	const fp = "spec.forProvider."
	unk := func(p string) plan.FieldChange { return plan.FieldChange{Path: fp + p, Planned: unknown()} }
	sens := func(p string) plan.FieldChange {
		return plan.FieldChange{Path: fp + p, Actual: sensitive(), Planned: sensitive()}
	}
	repl := func(p string) plan.FieldChange {
		return plan.FieldChange{Path: fp + p, Actual: str("x"), Planned: str("y"), RequiresReplace: true}
	}
	// The unchanged last line keeps the diff free of trailing blank lines.
	body := func(s string) string { return "spec:\n  forProvider:\n" + s + "  deletionPolicy: Delete\n" }
	fpm := func(m map[string]any) map[string]any { return map[string]any{"forProvider": m} }
	spec := func(m map[string]any) map[string]any { m["deletionPolicy"] = "Delete"; return m }
	ld := func(cur, des map[string]any) *dt.ResourceDiff {
		return lineDiff(t, "db", dt.DiffTypeModified, spec(cur), spec(des))
	}

	cases := map[string]struct {
		reason  string
		in      *dt.ResourceDiff
		changes []plan.FieldChange
		want    string
	}{
		"NestedMaps": {
			reason: "Fields of nested maps are marked, and an absent one is added as nested levels.",
			in: ld(fpm(map[string]any{"db": map[string]any{"settings": map[string]any{"password": "a", "size": int64(1), "tier": "a"}}}),
				fpm(map[string]any{"db": map[string]any{"settings": map[string]any{"password": "b", "size": int64(2), "tier": "b"}}})),
			changes: []plan.FieldChange{unk("db.settings.size"), repl("db.settings.tier"), unk("db.settings.extra.id")},
			want: `~~~ Instance/db (requires replacement)
  apiVersion: rds.aws.upbound.io/v1beta1
  kind: Instance
  metadata:
    name: db
  spec:
    deletionPolicy: Delete
    forProvider:
      db:
        settings:
-         password: a
-         size: 1
-         tier: a
+         extra:
+           id: (known after apply)
+         password: b
+         size: (known after apply)
+         tier: b  # forces replacement

---

Summary: 1 modified (1 requires replacement)
`,
		},
		"Lists": {
			reason: "Items of a list of lists and scalar items are marked by position.",
			in: ld(fpm(map[string]any{"matrix": []any{[]any{"a", "b"}, []any{"c"}}, "zones": []any{"z1", "z2"}}),
				fpm(map[string]any{"matrix": []any{[]any{"a", "x"}, []any{"d"}}, "zones": []any{"z1", "z3"}})),
			changes: []plan.FieldChange{repl("matrix[0][1]"), unk("matrix[1][0]")},
			want: `~~~ Instance/db (requires replacement)
  apiVersion: rds.aws.upbound.io/v1beta1
  kind: Instance
  metadata:
    name: db
  spec:
    deletionPolicy: Delete
    forProvider:
      matrix:
      - - a
-       - b
-     - - c
+       - x  # forces replacement
+     - - (known after apply)
      zones:
      - z1
-     - z2
+     - z3

---

Summary: 1 modified (1 requires replacement)
`,
		},
		"KeyedList": {
			reason: "A list item keyed by a field is the item holding that key on each side, at whatever position; an absent key gets nothing.",
			in: ld(fpm(map[string]any{"tags": []any{map[string]any{"key": "a", "value": "1"}, map[string]any{"key": "b", "value": "2"}}}),
				fpm(map[string]any{"tags": []any{map[string]any{"key": "b", "value": "3"}}})),
			changes: []plan.FieldChange{repl(`tags[key="b"].value`), unk(`tags[key="b"].id`), unk(`tags[key="c"].id`)},
			want: `~~~ Instance/db (requires replacement)
  apiVersion: rds.aws.upbound.io/v1beta1
  kind: Instance
  metadata:
    name: db
  spec:
    deletionPolicy: Delete
    forProvider:
      tags:
-     - key: a
-       value: "1"
-     - key: b
-       value: "2"
+     - id: (known after apply)
+       key: b
+       value: "3"  # forces replacement

---

Summary: 1 modified (1 requires replacement)
`,
		},
		"KeyedListShifted": {
			reason: "A keyed item that moves position is marked by its key.",
			in: ld(fpm(map[string]any{"tags": []any{map[string]any{"key": "b", "value": "SAME", "size": "SAME"}}}),
				fpm(map[string]any{"tags": []any{map[string]any{"key": "a", "value": "other"}, map[string]any{"key": "b", "value": "SAME", "size": "SAME"}}})),
			changes: []plan.FieldChange{unk(`tags[key="b"].size`)},
			want: `~~~ Instance/db (requires replacement)
  apiVersion: rds.aws.upbound.io/v1beta1
  kind: Instance
  metadata:
    name: db
  spec:
    deletionPolicy: Delete
    forProvider:
      tags:
-     - key: b
-       size: SAME
+     - key: a
+       value: other
+     - key: b
+       size: (known after apply)
        value: SAME
  
  

---

Summary: 1 modified (1 requires replacement)
`,
		},
		"Scalars": {
			reason: "A multi-line literal is marked on its first line, and a value set unknown is replaced whole.",
			in: ld(fpm(map[string]any{"cert": "a\nb\n", "name": "x: y", "note": "first"}),
				fpm(map[string]any{"cert": "a\nc\n", "name": "x: z", "note": "second"})),
			changes: []plan.FieldChange{unk("name"), repl("note")},
			want: `~~~ Instance/db (requires replacement)
  apiVersion: rds.aws.upbound.io/v1beta1
  kind: Instance
  metadata:
    name: db
  spec:
    deletionPolicy: Delete
    forProvider:
      cert: |
        a
-       b
-     name: 'x: y'
-     note: first
+       c
+     name: (known after apply)
+     note: second  # forces replacement

---

Summary: 1 modified (1 requires replacement)
`,
		},
		"LiteralJSON": {
			reason: "An unknown inside a JSON string is not set; a replace inside it marks the string's first changed line.",
			in: ld(fpm(map[string]any{"policy": "{\n  \"Statement\": {\n    \"Effect\": \"Allow\",\n    \"Sid\": \"a\"\n  },\n  \"Version\": \"1\"\n}\n"}),
				fpm(map[string]any{"policy": "{\n  \"Statement\": {\n    \"Effect\": \"Deny\",\n    \"Sid\": \"b\"\n  },\n  \"Version\": \"1\"\n}\n"})),
			changes: []plan.FieldChange{repl("policy.Statement.Sid"), unk("policy.Version")},
			want: `~~~ Instance/db (requires replacement)
  apiVersion: rds.aws.upbound.io/v1beta1
  kind: Instance
  metadata:
    name: db
  spec:
    deletionPolicy: Delete
    forProvider:
      policy: |
        {
          "Statement": {
-           "Effect": "Allow",
-           "Sid": "a"
+           "Effect": "Deny",  # forces replacement
+           "Sid": "b"
          },
          "Version": "1"
        }

---

Summary: 1 modified (1 requires replacement)
`,
		},
		"QuotedKeys": {
			reason: "Keys YAML quotes or escapes, and keys holding . or /, are matched unquoted.",
			in: objDiff(t, "db", dt.DiffTypeModified,
				map[string]any{"metadata": map[string]any{"annotations": map[string]any{"example.org/a.b": "x"}}, "spec": spec(fpm(map[string]any{"on": "a", "a: b": "c", "*": "d", "it's": "e"}))},
				map[string]any{"metadata": map[string]any{"annotations": map[string]any{"example.org/a.b": "y"}}, "spec": spec(fpm(map[string]any{"on": "b", "a: b": "d", "*": "e", "it's": "f"}))}),
			changes: []plan.FieldChange{
				{Path: "metadata.annotations[example.org/a.b]", Actual: str("x"), Planned: str("y"), RequiresReplace: true},
				unk("on"),
				{Path: "spec.forProvider[*]", Actual: str("d"), Planned: str("e"), RequiresReplace: true},
				unk("it's"),
			},
			want: `~~~ Instance/db (requires replacement)
  apiVersion: rds.aws.upbound.io/v1beta1
  kind: Instance
  metadata:
    annotations:
-     example.org/a.b: x
+     example.org/a.b: "y"  # forces replacement
    name: db
  spec:
    deletionPolicy: Delete
    forProvider:
-     '*': d
-     'a: b': c
-     it's: e
-     "on": a
+     '*': e  # forces replacement
+     'a: b': d
+     it's: (known after apply)
+     "on": (known after apply)

---

Summary: 1 modified (1 requires replacement)
`,
		},
		"Added": {
			reason: "On a create, + lines are marked: a keyed item's field and an absent field under a list item; a field inside a literal is not set.",
			in: lineDiff(t, "db", dt.DiffTypeAdded, nil,
				spec(fpm(map[string]any{"policy": "version: \"1\"\nsecret: s\n", "tags": []any{map[string]any{"key": "k", "value": "v"}}}))),
			changes: []plan.FieldChange{unk("policy.version"), repl(`tags[key="k"].value`), unk(`tags[key="k"].id`)},
			want: `+++ Instance/db (requires replacement)
+ apiVersion: rds.aws.upbound.io/v1beta1
+ kind: Instance
+ metadata:
+   name: db
+ spec:
+   deletionPolicy: Delete
+   forProvider:
+     policy: |
+       version: "1"
+       secret: s
+     tags:
+     - id: (known after apply)
+       key: k
+       value: v  # forces replacement

---

Summary: 1 added (1 requires replacement)
`,
		},
		"AddedSharedAncestor": {
			reason: "On a create, two replacements whose nearest ancestor is the same mark its line once, not the next field.",
			in:      lineDiff(t, "db", dt.DiffTypeAdded, nil, spec(fpm(map[string]any{"arn": "a", "region": "r"}))),
			changes: []plan.FieldChange{repl("x"), repl("y")},
			want: `+++ Instance/db (requires replacement)
+ apiVersion: rds.aws.upbound.io/v1beta1
+ kind: Instance
+ metadata:
+   name: db
+ spec:
+   deletionPolicy: Delete
+   forProvider:  # forces replacement
+     arn: a
+     region: r

---

Summary: 1 added (1 requires replacement)
`,
		},
		"KeyedListSwap": {
			reason: "Two keyed items that trade places, one holding a sensitive value, are redacted by their own key.",
			in: lineDiff(t, "db", dt.DiffTypeModified,
				map[string]any{"forProvider": map[string]any{"tags": []any{map[string]any{"key": "b", "value": "SAME"}, map[string]any{"key": "a", "value": "other"}}}},
				map[string]any{"forProvider": map[string]any{"tags": []any{map[string]any{"key": "a", "value": "other"}, map[string]any{"key": "b", "value": "SAME"}}}}),
			changes: []plan.FieldChange{sens(`tags[key="b"].value`)},
			want: `~~~ Instance/db (requires replacement)
  apiVersion: rds.aws.upbound.io/v1beta1
  kind: Instance
  metadata:
    name: db
  spec:
    forProvider:
      tags:
-     - key: b
-       value: (sensitive value)
      - key: a
        value: other
+     - key: b
+       value: (sensitive value)

---

Summary: 1 modified (1 requires replacement)
`,
		},
		"Unparseable": {
			reason: "A diff without objects to redact a sensitive change in has its body hidden, its header and summary kept.",
			in: textDiff(dt.DiffTypeModified,
				body("    password: a\n    size: 1\n"),
				body("    password: b\n    size: [\n")),
			changes: []plan.FieldChange{sens("password"), unk("size"), repl("password")},
			want: `~~~ Instance/db (requires replacement)
+ # diff hidden: it holds sensitive values that can't be redacted

---

Summary: 1 modified (1 requires replacement)
`,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := render(t, report(tc.in), planFor(tc.in, plan.ActionReplace, tc.changes...), noColor())
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("\n%s\nrender(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestAnnotateYAMLLineBreaks(t *testing.T) {
	sens := func(p string) plan.FieldChange {
		return plan.FieldChange{Path: p, Actual: sensitive(), Planned: sensitive()}
	}
	cases := map[string]struct {
		reason        string
		current, want map[string]any
		path          string
	}{
		"QuotedBeforeSensitive": {
			reason:  "A U+2028 in a quoted value doesn't shift a later sensitive field off its line.",
			current: map[string]any{"a": "x\u2028y", "pw": "OLDSECRET"},
			want:    map[string]any{"a": "x\u2028y", "pw": "NEWSECRET"},
			path:    "spec.pw",
		},
		"LiteralBeforeSensitive": {
			reason:  "A U+2028 in an earlier literal block doesn't shift a later sensitive field off its line.",
			current: map[string]any{"a": "l1\nx\u2028y\n", "pw": "OLDSECRET"},
			want:    map[string]any{"a": "l1\nx\u2028y\n", "pw": "NEWSECRET"},
			path:    "spec.pw",
		},
		"SiblingInLiteralMap": {
			reason:  "A U+2028 in a sibling inside a literal map doesn't shift the sensitive field off its line.",
			current: map[string]any{"cfg": "a: \"x\u2028y\"\npw: OLDSECRET\n"},
			want:    map[string]any{"cfg": "a: \"x\u2028y\"\npw: NEWSECRET\n"},
			path:    "spec.cfg.pw",
		},
		"LiteralLineOfBreaks": {
			reason:  "A literal block with a line of only U+2028 runes neither panics nor leaks.",
			current: map[string]any{"cfg": "\u2028\u2028\npw: OLDSECRET\n"},
			want:    map[string]any{"cfg": "\u2028\u2028\npw: NEWSECRET\n"},
			path:    "spec.cfg.pw",
		},
		"SensitiveListAfterKeptBlank": {
			reason:  "A kept blank line of a list item's literal stays in the item, so the sensitive list's later items drop with it.",
			current: map[string]any{"a": "\n", "pw": []any{"\n", "0OLDSECRET"}},
			want:    map[string]any{"a": "\n", "pw": []any{"\n", "0NEWSECRET"}},
			path:    "spec.pw",
		},
		"SensitiveListAfterLineOfBreaks": {
			reason:  "A list item whose literal is a line of U+2028 runes isn't a trailing blank, so the sensitive list's later items drop with it.",
			current: map[string]any{"pw": []any{"\u2028\u2028\n", "OLDSECRET"}},
			want:    map[string]any{"pw": []any{"\u2028\u2028\n", "NEWSECRET"}},
			path:    "spec.pw",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			d := lineDiff(t, "db", dt.DiffTypeModified, tc.current, tc.want)
			got := render(t, report(d), planFor(d, plan.ActionUpdate, sens(tc.path)), noColor())
			for _, secret := range []string{"OLDSECRET", "NEWSECRET"} {
				if strings.Contains(got, secret) {
					t.Errorf("\n%s\nrender(): output contains %q:\n%s", tc.reason, secret, got)
				}
			}
		})
	}
}

func TestAnnotateUnparsedSensitive(t *testing.T) {
	sens := func(p string) plan.FieldChange {
		return plan.FieldChange{Path: p, Actual: sensitive(), Planned: sensitive()}
	}
	marshal := func(spec map[string]any) string {
		b, err := sigsyaml.Marshal(map[string]any{"spec": spec})
		if err != nil {
			t.Fatalf("Marshal(): %v", err)
		}
		return string(b)
	}
	cases := map[string]struct {
		reason string
		in     *dt.ResourceDiff
		path   string
		// planned replaces the Planned side of the change when set.
		planned *plan.Value
		// hidden is whether the body is hidden rather than redacted; a diff
		// without objects (textDiff) is always hidden.
		hidden bool
	}{
		"SensitiveThenUnknown": {
			reason:  "A value sensitive before and computed after is still redacted.",
			in:      lineDiff(t, "db", dt.DiffTypeModified, map[string]any{"pw": "OLDSECRET"}, map[string]any{"pw": "NEWSECRET"}),
			path:    "spec.pw",
			planned: unknown(),
		},
		"QuotedKey": {
			reason: "A key starting and ending with a quote is matched by its trimmed path.",
			in:     lineDiff(t, "db", dt.DiffTypeModified, map[string]any{"'pw'": "OLDSECRET"}, map[string]any{"'pw'": "NEWSECRET"}),
			path:   "spec['pw']",
		},
		"LineSeparatorBeforeSensitive": {
			reason: "Text ending in U+2028 before a sensitive sibling doesn't hide it from redaction.",
			in: lineDiff(t, "db", dt.DiffTypeModified,
				map[string]any{"forProvider": map[string]any{"description": "line one\nends\u2028", "password": "OLDSECRET"}},
				map[string]any{"forProvider": map[string]any{"description": "line one\nends\u2028", "password": "NEWSECRET"}}),
			path: "spec.forProvider.password",
		},
		"KeptBlankBeforeSensitive": {
			reason: "A sibling of a newline and a space before a sensitive field doesn't hide it from redaction.",
			in:     lineDiff(t, "db", dt.DiffTypeModified, map[string]any{"a": "\n ", "pw": "OLDSECRET"}, map[string]any{"a": "\n ", "pw": "NEWSECRET"}),
			path:   "spec.pw",
		},
		"LongKey": {
			reason: "A key long enough that YAML writes its value on a later line is redacted.",
			in: lineDiff(t, "db", dt.DiffTypeModified,
				map[string]any{strings.Repeat("k", 140): "OLDSECRET"}, map[string]any{strings.Repeat("k", 140): "NEWSECRET"}),
			path: "spec." + strings.Repeat("k", 140),
		},
		"ValueBelowKeyInLiteral": {
			reason: "A field inside a literal whose value is below its key redacts the whole literal.",
			in: lineDiff(t, "db", dt.DiffTypeModified,
				map[string]any{"c": "pw:\n  OLDSECRET\nq: 1\n"}, map[string]any{"c": "pw:\n  NEWSECRET\nq: 1\n"}),
			path: "spec.c.pw",
		},
		"EqualLineNewSideUnparsed": {
			reason: "An unchanged sensitive line is masked when only the old side parses.",
			in: textDiff(dt.DiffTypeModified,
				marshal(map[string]any{"pw": "SAMESECRET", "size": 1}),
				marshal(map[string]any{"pw": "SAMESECRET"})+"  size: [\n"),
			path: "spec.pw",
		},
		"ChangedLineNewSideUnparsed": {
			reason: "The unparsed side's own sensitive line is masked.",
			in: textDiff(dt.DiffTypeModified,
				marshal(map[string]any{"pw": "OLDSECRET", "size": 1}),
				marshal(map[string]any{"pw": "NEWSECRET"})+"  size: [\n"),
			path: "spec.pw",
		},
		"AddedUnparsed": {
			reason: "An added diff whose only side doesn't parse is masked.",
			in:     textDiff(dt.DiffTypeAdded, "", marshal(map[string]any{"pw": "NEWSECRET"})+"  size: [\n"),
			path:   "spec.pw",
		},
		"LiteralJSON": {
			reason: "A literal block holding JSON is redacted whole.",
			in: lineDiff(t, "db", dt.DiffTypeModified,
				map[string]any{"cfg": "\u2028\u2028\n{\"pw\": \"OLDSECRET\"}\n"},
				map[string]any{"cfg": "\u2028\u2028\n{\"pw\": \"NEWSECRET\"}\n"}),
			path: "spec.cfg.pw",
		},
		"LiteralNotAMap": {
			reason: "A literal block whose content isn't a map is masked whole.",
			in: lineDiff(t, "db", dt.DiffTypeModified,
				map[string]any{"cfg": "zz\n{\"pw\": \"OLDSECRET\"}\n"},
				map[string]any{"cfg": "zz\n{\"pw\": \"NEWSECRET\"}\n"}),
			path: "spec.cfg.pw",
		},
		"ListItemUnparsed": {
			reason: "A list item or continuation line without a key on an unparsed side is masked.",
			in: textDiff(dt.DiffTypeModified,
				marshal(map[string]any{"pw": []any{"OLDSECRET"}, "size": 1}),
				marshal(map[string]any{"pw": []any{"NEWSECRET"}})+"  size: [\n"),
			path: "spec.pw",
		},
		"SensitiveMap": {
			reason: "A sensitive map is one masked line on each side; its children are dropped.",
			in: lineDiff(t, "db", dt.DiffTypeModified,
				map[string]any{"pw": map[string]any{"a": "OLDSECRET", "same": "SAMESECRET"}},
				map[string]any{"pw": map[string]any{"a": "NEWSECRET", "same": "SAMESECRET"}}),
			path: "spec.pw",
		},
		"SensitiveList": {
			reason: "A sensitive list is one masked line on each side; its items are dropped.",
			in: lineDiff(t, "db", dt.DiffTypeModified,
				map[string]any{"pw": []any{"SAMESECRET", map[string]any{"k": "OLDSECRET"}}},
				map[string]any{"pw": []any{"SAMESECRET", map[string]any{"k": "NEWSECRET"}}}),
			path: "spec.pw",
		},
		"SensitiveJSONObjectInLiteral": {
			reason: "A sensitive JSON object inside a literal block is masked with its content dropped.",
			in: lineDiff(t, "db", dt.DiffTypeModified,
				map[string]any{"policy": "{\n  \"Statement\": {\n    \"a\": \"OLDSECRET\",\n    \"b\": \"SAMESECRET\"\n  },\n  \"Version\": \"1\"\n}\n"},
				map[string]any{"policy": "{\n  \"Statement\": {\n    \"a\": \"NEWSECRET\",\n    \"b\": \"SAMESECRET\"\n  },\n  \"Version\": \"1\"\n}\n"}),
			path: "spec.policy.Statement",
		},
		"MapToLiteral": {
			reason: "A sensitive field changing from a map to a literal drops both sides' content.",
			in: lineDiff(t, "db", dt.DiffTypeModified,
				map[string]any{"pw": map[string]any{"SAMESECRET": "x"}},
				map[string]any{"pw": "SAMESECRET: x\nNEWSECRET: y\n"}),
			path: "spec.pw",
		},
		"DuplicateKeyedItems": {
			reason: "Every list item a keyed sensitive path matches is redacted, not only the first.",
			in: lineDiff(t, "db", dt.DiffTypeModified,
				map[string]any{"tags": []any{map[string]any{"key": "b", "value": "OLDSECRET"}, map[string]any{"key": "b", "value": "OLD2SECRET"}}},
				map[string]any{"tags": []any{map[string]any{"key": "b", "value": "NEWSECRET"}, map[string]any{"key": "b", "value": "NEW2SECRET"}}}),
			path: `spec.tags[key="b"].value`,
		},
		"FlowMap": {
			reason: "A sensitive map written in flow style is masked on both sides.",
			in: textDiff(dt.DiffTypeModified,
				marshal(map[string]any{"size": 1})+"  pw: {a: OLDSECRET, b: SAMESECRET}\n",
				marshal(map[string]any{"size": 1})+"  pw: {a: NEWSECRET, b: SAMESECRET}\n"),
			path: "spec.pw",
		},
		"FlowList": {
			reason: "A sensitive list written in flow style is masked on both sides.",
			in: textDiff(dt.DiffTypeModified,
				marshal(map[string]any{"size": 1})+"  pw: [OLDSECRET, SAMESECRET]\n",
				marshal(map[string]any{"size": 1})+"  pw: [NEWSECRET, SAMESECRET]\n"),
			path: "spec.pw",
		},
		"FlowFieldInMap": {
			reason: "A sensitive field inside a flow map is masked on both sides.",
			in: textDiff(dt.DiffTypeModified,
				marshal(map[string]any{"size": 1})+"  pw: {a: OLDSECRET, b: SAMESECRET}\n",
				marshal(map[string]any{"size": 1})+"  pw: {a: NEWSECRET, b: SAMESECRET}\n"),
			path: "spec.pw.a",
		},
		"FlowListItem": {
			reason: "A sensitive item of a flow list is masked on both sides.",
			in: textDiff(dt.DiffTypeModified,
				marshal(map[string]any{"size": 1})+"  pw: [OLDSECRET, x]\n",
				marshal(map[string]any{"size": 1})+"  pw: [NEWSECRET, x]\n"),
			path: "spec.pw[0]",
		},
		"JSONInLiteralFlowValue": {
			reason: "A sensitive object on one line of a JSON literal is masked on both sides.",
			in: lineDiff(t, "db", dt.DiffTypeModified,
				map[string]any{"cfg": "{\n  \"pw\": {\"a\": \"OLDSECRET\"},\n  \"l\": [\"x\"]\n}\n"},
				map[string]any{"cfg": "{\n  \"pw\": {\"a\": \"NEWSECRET\"},\n  \"l\": [\"x\"]\n}\n"}),
			path: "spec.cfg.pw",
		},
		"JSONInLiteralFlowArray": {
			reason: "A sensitive array on one line of a JSON literal is masked on both sides.",
			in: lineDiff(t, "db", dt.DiffTypeModified,
				map[string]any{"cfg": "{\n  \"pw\": [\"OLDSECRET\", \"SAMESECRET\"],\n  \"x\": 1\n}\n"},
				map[string]any{"cfg": "{\n  \"pw\": [\"NEWSECRET\", \"SAMESECRET\"],\n  \"x\": 1\n}\n"}),
			path: "spec.cfg.pw",
		},
		"JSONInLiteralFieldInFlow": {
			reason: "A sensitive field sharing its JSON line with a sibling is masked on both sides.",
			in: lineDiff(t, "db", dt.DiffTypeModified,
				map[string]any{"cfg": "{\n  \"o\": {\"pw\": \"OLDSECRET\", \"b\": 1}\n}\n"},
				map[string]any{"cfg": "{\n  \"o\": {\"pw\": \"NEWSECRET\", \"b\": 1}\n}\n"}),
			path: "spec.cfg.o.pw",
		},
		"DuplicateKeyedWholeItems": {
			reason: "A sensitive keyed item is masked whole, for every item it matches.",
			in: lineDiff(t, "db", dt.DiffTypeModified,
				map[string]any{"tags": []any{map[string]any{"key": "b", "value": "OLDSECRET"}, map[string]any{"key": "b", "value": "OLD2SECRET"}}},
				map[string]any{"tags": []any{map[string]any{"key": "b", "value": "NEWSECRET"}, map[string]any{"key": "b", "value": "NEW2SECRET"}}}),
			path: `spec.tags[key="b"]`,
		},
		"DottedOldNestedNew": {
			reason: "A dotted key on the old side and nesting on the new are each redacted.",
			in: lineDiff(t, "db", dt.DiffTypeModified,
				map[string]any{"tags": map[string]any{"a.b": "OLDSECRET"}},
				map[string]any{"tags": map[string]any{"a": map[string]any{"b": "NEWSECRET"}}}),
			path: "spec.tags.a.b",
		},
		"DottedAndNestedSameSide": {
			reason: "A path matching a dotted key and nesting on one side hides the body.",
			in: lineDiff(t, "db", dt.DiffTypeModified,
				map[string]any{"tags": map[string]any{"a.b": "OLDSECRET", "a": map[string]any{"b": "OLD2SECRET"}}},
				map[string]any{"tags": map[string]any{"a.b": "NEWSECRET", "a": map[string]any{"b": "NEW2SECRET"}}}),
			path:   "spec.tags.a.b",
			hidden: true,
		},
		"KeyedOnMap": {
			reason: "A keyed item on a map hides the body.",
			in: lineDiff(t, "db", dt.DiffTypeModified,
				map[string]any{"tags": map[string]any{"b": "OLDSECRET"}}, map[string]any{"tags": map[string]any{"b": "NEWSECRET"}}),
			path:   `spec.tags[key="b"]`,
			hidden: true,
		},
		"IndexHash": {
			reason: "A set element path whose hash index is past the end redacts the field in every item.",
			in: lineDiff(t, "db", dt.DiffTypeModified,
				map[string]any{"rule": []any{map[string]any{"name": "a", "pw": "OLDSECRET"}}},
				map[string]any{"rule": []any{map[string]any{"name": "a", "pw": "NEWSECRET"}}}),
			path: "spec.rule[2876543210].pw",
		},
		"IndexSmallHash": {
			reason: "A set element path whose hash index names another item redacts the field in every item.",
			in: lineDiff(t, "db", dt.DiffTypeModified,
				map[string]any{"rule": []any{map[string]any{"pw": "SAMESECRET"}, map[string]any{"pw": "OLDSECRET"}}},
				map[string]any{"rule": []any{map[string]any{"pw": "SAMESECRET"}, map[string]any{"pw": "NEWSECRET"}}}),
			path: "spec.rule[0].pw",
		},
		"BracketKey": {
			reason: "A map key holding brackets hides the body.",
			in: lineDiff(t, "db", dt.DiffTypeModified,
				map[string]any{"tags": map[string]any{"foo[bar]": "OLDSECRET"}}, map[string]any{"tags": map[string]any{"foo[bar]": "NEWSECRET"}}),
			path:   "spec.tags.foo[bar]",
			hidden: true,
		},
		"KeyedOnScalarList": {
			reason: "A keyed item on a list of scalars hides the body.",
			in: lineDiff(t, "db", dt.DiffTypeModified,
				map[string]any{"tags": []any{"OLDSECRET"}}, map[string]any{"tags": []any{"NEWSECRET"}}),
			path:   `spec.tags[key="b"]`,
			hidden: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ch := sens(tc.path)
			if tc.planned != nil {
				ch.Planned = tc.planned
			}
			got := render(t, report(tc.in), planFor(tc.in, plan.ActionUpdate, ch), noColor())
			hidden := tc.hidden || tc.in.Desired.Clean == nil
			if diff := cmp.Diff(hidden, strings.Contains(got, diffHidden)); diff != "" {
				t.Errorf("\n%s\nrender(): -want hidden, +got:\n%s\n%s", tc.reason, diff, got)
			}
			if !hidden && !strings.Contains(got, "(sensitive value") {
				t.Errorf("\n%s\nrender(): no redaction marker:\n%s", tc.reason, got)
			}
			for _, secret := range []string{"SAMESECRET", "OLDSECRET", "NEWSECRET", "OLD2SECRET", "NEW2SECRET"} {
				if strings.Contains(got, secret) {
					t.Errorf("\n%s\nrender(): output contains %q:\n%s", tc.reason, secret, got)
				}
			}
		})
	}
}

// FuzzAnnotateSensitive plants a secret under a Sensitive field, beside a
// fuzzed sibling, and checks -o diff never prints it.
func FuzzAnnotateSensitive(f *testing.F) {
	// Seeds from TestAnnotateYAMLLineBreaks and TestAnnotateUnparsedSensitive.
	for _, s := range []string{"x y", "l1\nx y\n", "a: \"x y\"\n", "  \n", "zz\n", "{\"pw\": 1}", "  size: [\n", "[a, b]", ""} {
		for shape := range uint8(5) {
			f.Add(s, "v", shape)
		}
	}
	const secret = "PLANTEDSECRET"
	f.Fuzz(func(t *testing.T, sibling, value string, shape uint8) {
		side := func(plant string) map[string]any {
			v := value + plant
			var pw any = v
			switch shape % 5 {
			case 1:
				pw = map[string]any{"a": v, sibling: "x"}
			case 2:
				pw = []any{sibling, v}
			case 3:
				// A literal holding JSON or YAML, the field inside it.
				b, err := json.MarshalIndent(map[string]any{"pw": v, "x": sibling}, "", "  ")
				if err != nil {
					t.Skip()
				}
				return map[string]any{"cfg": string(b) + "\n", "z": sibling}
			case 4:
				return map[string]any{"cfg": sibling + "\npw: " + strconv.Quote(v) + "\n"}
			}
			return map[string]any{"a": sibling, "pw": pw}
		}
		path := "spec.pw"
		if shape%5 >= 3 {
			path = "spec.cfg.pw"
		}
		cur, des := side("OLD"+secret), side("NEW"+secret)
		for _, spec := range []map[string]any{cur, des} {
			if _, err := sigsyaml.Marshal(spec); err != nil {
				t.Skip("not renderable as YAML:", err)
			}
		}
		d := lineDiff(t, "db", dt.DiffTypeModified, cur, des)
		ch := plan.FieldChange{Path: path, Actual: sensitive(), Planned: sensitive()}
		if got := render(t, report(d), planFor(d, plan.ActionUpdate, ch), noColor()); strings.Contains(got, secret) {
			t.Errorf("render(): output contains the planted secret:\n%s", got)
		}
	})
}
