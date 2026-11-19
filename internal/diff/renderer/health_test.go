package renderer

import (
	"bytes"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	un "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	dt "github.com/crossplane/cli/v2/internal/diff/result"
	tu "github.com/crossplane/cli/v2/internal/diff/testutils"
	"github.com/crossplane/cli/v2/internal/health"
)

// liveWith returns d with a live object carrying a Synced=False condition.
func liveWith(d *dt.ResourceDiff, msg string) *dt.ResourceDiff {
	d.Current.Raw = &un.Unstructured{Object: map[string]any{
		"apiVersion": d.Gvk.GroupVersion().String(), "kind": d.Gvk.Kind,
		"metadata": map[string]any{"name": d.ResourceName},
		"status": map[string]any{"conditions": []any{map[string]any{
			"type": "Synced", "status": "False", "reason": "ReconcileError", "message": msg,
			"lastTransitionTime": "2026-01-01T00:00:00Z",
		}}},
	}}

	return d
}

func TestRenderDiffsHealth(t *testing.T) {
	failing := &health.Health{State: health.Failing, Synced: &health.Condition{Status: "False", Reason: "ReconcileError", Message: "vpc does not exist"}}
	notReady := &health.Health{State: health.NotReady, Ready: &health.Condition{Status: "False", Reason: "Creating"}}
	ref := func(kind, name string) corev1.ObjectReference {
		return corev1.ObjectReference{APIVersion: "example.org/v1", Kind: kind, Name: name}
	}
	group := func(name string, xr *health.Health, composed []dt.ResourceHealth, diffs ...*dt.ResourceDiff) dt.XRDiffGroup {
		g := xrGroup("XNetwork", name, map[string]*dt.ResourceDiff{})
		for _, d := range diffs {
			g.Diffs[d.GetDiffKey()] = d
		}

		g.Health = dt.CompositeHealth{XR: xr, Composed: composed}

		return g
	}

	type args struct {
		groups []dt.XRDiffGroup
		full   bool
	}
	cases := map[string]struct {
		reason string
		args   args
		want   string
	}{
		"SingleXR": {
			reason: "A single XR prints its health before its diffs, marks a failing resource, lists the unhealthy resources without changes after them, nested by via, and the Health: line after the summary.",
			args: args{groups: []dt.XRDiffGroup{group("net", notReady, []dt.ResourceHealth{
				{ObjectReference: ref("Bucket", "b"), Health: failing},
				{ObjectReference: ref("RouteTable", "rt"), Health: failing},
				{ObjectReference: ref("Subnet", "sn"), Via: new(ref("XSubnet", "x")), Health: notReady},
				{ObjectReference: ref("Healthy", "h"), Health: &health.Health{State: health.Healthy}},
			}, liveWith(modifiedDiffFor("Bucket", "b"), "vpc does not exist"))}},
			want: `XR: XNetwork/net ⚠ not ready (Ready=False, Creating)

~~~ Bucket/b ⚠ failing (Synced=False, ReconcileError): vpc does not exist
- spec:
-   field: old
+ spec:
+   field: new

---

Unhealthy resources without changes:
  RouteTable/rt ⚠ failing (Synced=False, ReconcileError): vpc does not exist
  Subnet/sn (via XSubnet/x) ⚠ not ready (Ready=False, Creating)

Summary: 1 modified

Health: 1 of 1 XR unhealthy before apply; 2 resources failing (Synced=False), 1 not ready.
`,
		},
		"SingleXRNoChanges": {
			reason: "A single XR without changes still prints its unhealthy resources and the Health: line.",
			args: args{groups: []dt.XRDiffGroup{group("net", nil, []dt.ResourceHealth{
				{ObjectReference: ref("RouteTable", "rt"), Health: notReady},
			})}},
			want: `Unhealthy resources without changes:
  RouteTable/rt ⚠ not ready (Ready=False, Creating)

Health: 1 resource not ready.
`,
		},
		"Grouped": {
			reason: "Each XR section carries its health under its header and its unhealthy resources, changed or not, and the Health: line follows the footer.",
			args: args{groups: []dt.XRDiffGroup{
				group("a", failing, nil, modifiedDiffFor("Bucket", "b")),
				group("c", nil, []dt.ResourceHealth{{ObjectReference: ref("RouteTable", "rt"), Health: notReady}}),
			}},
			want: `=== XNetwork/a ===

⚠ failing (Synced=False, ReconcileError): vpc does not exist

~~~ Bucket/b
- spec:
-   field: old
+ spec:
+   field: new

---

Summary: 1 modified

=== XNetwork/c ===

No changes.

Unhealthy resources without changes:
  RouteTable/rt ⚠ not ready (Ready=False, Creating)

================================================================================
Total: 1 modified across 2 XRs (1 unchanged)
Health: 1 of 2 XRs unhealthy before apply; 1 resource not ready.
`,
		},
		"FullMessages": {
			reason: "With full messages, a message prints in full beneath its line.",
			args:   args{full: true, groups: []dt.XRDiffGroup{group("net", nil, nil, liveWith(modifiedDiffFor("Bucket", "b"), "line one\nline two"))}},
			want: `~~~ Bucket/b ⚠ failing (Synced=False, ReconcileError)
    line one
    line two
- spec:
-   field: old
+ spec:
+   field: new

---

Summary: 1 modified

Health: 1 resource failing (Synced=False).
`,
		},
		"XRMessageOnce": {
			reason: "An unhealthy XR's message prints once, on its XR: line, with full messages too, and its own diff entry carries only its tier; a composed resource keeps its whole marker.",
			args: args{full: true, groups: []dt.XRDiffGroup{group("net", failing, nil,
				liveWith(modifiedDiffFor("XNetwork", "net"), "vpc does not exist"),
				liveWith(modifiedDiffFor("Bucket", "b"), "boom"))}},
			want: `XR: XNetwork/net ⚠ failing (Synced=False, ReconcileError)
    vpc does not exist

~~~ Bucket/b ⚠ failing (Synced=False, ReconcileError)
    boom
- spec:
-   field: old
+ spec:
+   field: new

---
~~~ XNetwork/net ⚠ failing
- spec:
-   field: old
+ spec:
+   field: new

---

Summary: 2 modified

Health: 1 of 1 XR unhealthy before apply; 1 resource failing (Synced=False).
`,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer

			opts := DefaultDiffOptions()
			opts.UseColors = false
			opts.FullMessages = tc.args.full
			opts.Stdout = &buf
			opts.Stderr = &bytes.Buffer{}

			if err := NewDiffRenderer(tu.TestLogger(t, false), opts).RenderDiffs(tc.args.groups, nil, nil); err != nil {
				t.Fatalf("\n%s\nRenderDiffs(...): unexpected error: %v", tc.reason, err)
			}

			if diff := cmp.Diff(tc.want, buf.String()); diff != "" {
				t.Errorf("\n%s\nRenderDiffs(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestRenderCompDiffHealth(t *testing.T) {
	failing := &health.Health{State: health.Failing, Synced: &health.Condition{Status: "False", Reason: "ReconcileError", Message: "boom"}}
	notReady := &health.Health{State: health.NotReady, Ready: &health.Condition{Status: "False", Reason: "Creating"}}
	healthy := &health.Health{State: health.Healthy}
	rt := corev1.ObjectReference{APIVersion: "example.org/v1", Kind: "RouteTable", Name: "rt"}
	p := corev1.ObjectReference{APIVersion: "example.org/v1", Kind: "XBucket", Name: "p"}
	c := corev1.ObjectReference{APIVersion: "example.org/v1", Kind: "XBucket", Name: "c"}
	sn := corev1.ObjectReference{APIVersion: "example.org/v1", Kind: "Subnet", Name: "sn"}

	type args struct {
		impacts []dt.XRImpact
	}
	cases := map[string]struct {
		reason string
		args   args
		want   string
	}{
		"Unhealthy": {
			reason: "An unhealthy XR's affected line carries its health, each XR's unhealthy resources without changes follow the impact analysis under its name, and the Health: line ends the run.",
			args: args{impacts: []dt.XRImpact{
				{
					ObjectReference: corev1.ObjectReference{APIVersion: "example.org/v1", Kind: "XBucket", Namespace: "ns", Name: "a"},
					Status:          dt.XRStatusUnchanged,
					Health:          dt.CompositeHealth{XR: failing, Composed: []dt.ResourceHealth{{ObjectReference: rt, Health: notReady}}},
				},
				{
					ObjectReference: corev1.ObjectReference{APIVersion: "example.org/v1", Kind: "XBucket", Name: "b"},
					Status:          dt.XRStatusUnchanged,
				},
			}},
			want: `=== Composition Changes ===

No changes detected in composition xbuckets.example.org

=== Affected Composite Resources ===

  ✓ XBucket/a (namespace: ns) ⚠ failing (Synced=False, ReconcileError): boom
  ✓ XBucket/b (cluster-scoped)

Summary: 2 resources unchanged

=== Impact Analysis ===

All composite resources are up-to-date. No downstream resource changes detected.

XR: XBucket/ns/a
  Unhealthy resources without changes:
    RouteTable/rt ⚠ not ready (Ready=False, Creating)

Health: 1 of 2 XRs unhealthy before apply; 1 resource not ready.
`,
		},
		"NestedListsItsOwn": {
			reason: "A nested XR listed with health of its own lists what it composes; its parent lists neither, so each resource is listed once, as in plan output.",
			args: args{impacts: []dt.XRImpact{
				{
					ObjectReference: p,
					Status:          dt.XRStatusUnchanged,
					Health:          dt.CompositeHealth{XR: healthy, Composed: []dt.ResourceHealth{{ObjectReference: c, Health: notReady}, {ObjectReference: sn, Via: &c, Health: failing}}},
				},
				{
					ObjectReference: c,
					Status:          dt.XRStatusUnchanged,
					ComposedBy:      &p,
					Health:          dt.CompositeHealth{XR: notReady, Composed: []dt.ResourceHealth{{ObjectReference: sn, Health: failing}}},
				},
			}},
			want: `=== Composition Changes ===

No changes detected in composition xbuckets.example.org

=== Affected Composite Resources ===

  ✓ XBucket/p (cluster-scoped)
  ✓ XBucket/c (cluster-scoped) ⚠ not ready (Ready=False, Creating)

Summary: 2 resources unchanged

=== Impact Analysis ===

All composite resources are up-to-date. No downstream resource changes detected.

XR: XBucket/c
  Unhealthy resources without changes:
    Subnet/sn ⚠ failing (Synced=False, ReconcileError): boom

Health: 1 of 2 XRs unhealthy before apply; 1 resource failing (Synced=False).
`,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var stdout bytes.Buffer

			opts := DefaultDiffOptions()
			opts.Stdout = &stdout
			opts.Stderr = &bytes.Buffer{}
			opts.UseColors = false

			r := NewDefaultCompDiffRenderer(tu.TestLogger(t, false), NewDiffRenderer(tu.TestLogger(t, false), opts), opts)
			comp := dt.CompositionDiff{
				Name:              "xbuckets.example.org",
				AffectedResources: dt.AffectedResourcesSummary{Total: 2, Unchanged: 2},
				ImpactAnalysis:    tc.args.impacts,
			}

			if err := r.RenderCompDiff(&dt.CompDiffOutput{Compositions: []dt.CompositionDiff{comp}}); err != nil {
				t.Fatalf("\n%s\nRenderCompDiff(...): unexpected error: %v", tc.reason, err)
			}

			if diff := cmp.Diff(tc.want, stdout.String()); diff != "" {
				t.Errorf("\n%s\nRenderCompDiff(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}
