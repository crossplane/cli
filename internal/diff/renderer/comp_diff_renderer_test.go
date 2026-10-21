package renderer

import (
	"bytes"
	"strings"
	"testing"

	gcmp "github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	dt "github.com/crossplane/cli/v2/internal/diff/result"
	tu "github.com/crossplane/cli/v2/internal/diff/testutils"
)

func TestDefaultCompDiffRenderer_RenderCompDiff(t *testing.T) {
	tests := map[string]struct {
		output   *dt.CompDiffOutput
		colorize bool
		minimize bool
		validate func(t *testing.T, result string)
	}{
		"EmptyCompositions": {
			output:   &dt.CompDiffOutput{Compositions: []dt.CompositionDiff{}},
			colorize: false,
			validate: func(t *testing.T, result string) {
				t.Helper()

				if result != "" {
					t.Errorf("Expected empty output, got: %q", result)
				}
			},
		},
		"NoChangesComposition": {
			output: &dt.CompDiffOutput{
				Compositions: []dt.CompositionDiff{{
					Name:              "test-comp",
					AffectedResources: dt.AffectedResourcesSummary{Total: 1, Unchanged: 1},
					ImpactAnalysis:    []dt.XRImpact{{ObjectReference: corev1.ObjectReference{APIVersion: "example.org/v1", Kind: "XResource", Name: "xr-1"}, Status: dt.XRStatusUnchanged}},
				}},
			},
			colorize: false,
			validate: func(t *testing.T, result string) {
				t.Helper()

				if !strings.Contains(result, "=== Composition Changes ===") {
					t.Error("Expected composition changes header")
				}

				if !strings.Contains(result, "No changes detected in composition test-comp") {
					t.Error("Expected no changes message")
				}

				if !strings.Contains(result, "=== Affected Composite Resources ===") {
					t.Error("Expected affected resources header")
				}
			},
		},
		// Discovery records the filtered composites in ImpactAnalysis but reports only their count.
		"FilteredByPolicy": {
			output: &dt.CompDiffOutput{
				Compositions: []dt.CompositionDiff{{
					Name:              "test-comp",
					AffectedResources: dt.AffectedResourcesSummary{Total: 1, FilteredByPolicy: 1},
					ImpactAnalysis: []dt.XRImpact{{
						ObjectReference: corev1.ObjectReference{APIVersion: "example.org/v1", Kind: "XR", Name: "manual-xr", Namespace: "ns"},
						Status:          dt.XRStatusFiltered,
						FilterReason:    dt.FilterReasonManualPolicy,
						FilterDetail:    "compositionRevisionRef: test-comp-abc1234",
					}},
				}},
			},
			colorize: false,
			validate: func(t *testing.T, result string) {
				t.Helper()

				if !strings.Contains(result, "All 1 XR(s) using composition test-comp have Manual update policy (use --include-manual to see them)") {
					t.Errorf("Expected Manual update policy message, got:\n%s", result)
				}

				if strings.Contains(result, "manual-xr") || strings.Contains(result, "=== Affected Composite Resources ===") {
					t.Errorf("Expected discovery to leave the filtered composite unnamed, got:\n%s", result)
				}
			},
		},
		// With evaluated composites beside a filtered one, discovery lists only the evaluated ones.
		"DiscoveryListsOnlyEvaluatedComposites": {
			output: &dt.CompDiffOutput{
				Compositions: []dt.CompositionDiff{{
					Name:              "test-comp",
					AffectedResources: dt.AffectedResourcesSummary{Total: 2, Unchanged: 1, FilteredBySelector: 1},
					ImpactAnalysis: []dt.XRImpact{
						{
							ObjectReference: corev1.ObjectReference{APIVersion: "example.org/v1", Kind: "XR", Name: "selector-xr", Namespace: "ns"},
							Status:          dt.XRStatusFiltered,
							FilterReason:    dt.FilterReasonRevisionSelectorMismatch,
						},
						{ObjectReference: corev1.ObjectReference{APIVersion: "example.org/v1", Kind: "XR", Name: "xr-1", Namespace: "ns"}, Status: dt.XRStatusUnchanged},
					},
				}},
			},
			colorize: false,
			validate: func(t *testing.T, result string) {
				t.Helper()

				if !strings.Contains(result, "XR/xr-1 (namespace: ns)") {
					t.Errorf("Expected the evaluated composite listed, got:\n%s", result)
				}

				if strings.Contains(result, "selector-xr") {
					t.Errorf("Expected discovery to leave the filtered composite unnamed, got:\n%s", result)
				}
			},
		},
		"FilteredBySelectorOnly": {
			output: &dt.CompDiffOutput{
				Compositions: []dt.CompositionDiff{{
					Name:              "test-comp",
					AffectedResources: dt.AffectedResourcesSummary{FilteredBySelector: 2},
					ImpactAnalysis:    []dt.XRImpact{},
				}},
			},
			colorize: false,
			validate: func(t *testing.T, result string) {
				t.Helper()

				if !strings.Contains(result, "compositionRevisionSelector") {
					t.Errorf("Expected compositionRevisionSelector message, got %q", result)
				}
			},
		},
		"FilteredByPolicyAndSelector": {
			output: &dt.CompDiffOutput{
				Compositions: []dt.CompositionDiff{{
					Name:              "test-comp",
					AffectedResources: dt.AffectedResourcesSummary{FilteredByPolicy: 1, FilteredBySelector: 2},
					ImpactAnalysis:    []dt.XRImpact{},
				}},
			},
			colorize: false,
			validate: func(t *testing.T, result string) {
				t.Helper()

				if !strings.Contains(result, "Manual update policy") || !strings.Contains(result, "compositionRevisionSelector") {
					t.Errorf("Expected both Manual-policy and selector messages, got %q", result)
				}
			},
		},
		"CompositionWithError": {
			output: &dt.CompDiffOutput{
				Compositions: []dt.CompositionDiff{{
					Name:  "error-comp",
					Error: &dt.OutputError{Message: "failed to fetch composition from cluster"},
					AffectedResources: dt.AffectedResourcesSummary{
						Total:       0,
						WithChanges: 0,
						Unchanged:   0,
						WithErrors:  0,
					},
					ImpactAnalysis: []dt.XRImpact{},
				}},
			},
			colorize: false,
			validate: func(t *testing.T, result string) {
				t.Helper()

				// Should show the error message
				if !strings.Contains(result, "Error processing composition error-comp") {
					t.Error("Expected error processing message")
				}

				if !strings.Contains(result, "failed to fetch composition from cluster") {
					t.Error("Expected error details")
				}

				// Should NOT show affected resources or impact analysis sections
				if strings.Contains(result, "=== Affected Composite Resources ===") {
					t.Error("Should not show affected resources header when composition has error")
				}

				if strings.Contains(result, "=== Impact Analysis ===") {
					t.Error("Should not show impact analysis header when composition has error")
				}
			},
		},
		"MinimizeChangedComposition": {
			// A changed composition collapses to a single marker line (no YAML body,
			// no per-composition Summary footer), while the impact sections remain.
			output: &dt.CompDiffOutput{
				Compositions: []dt.CompositionDiff{{
					Name:              "test-comp",
					CompositionDiff:   &dt.ResourceDiff{DiffType: dt.DiffTypeModified, ResourceName: "test-comp", Gvk: schema.GroupVersionKind{Group: "apiextensions.crossplane.io", Version: "v1", Kind: "Composition"}},
					AffectedResources: dt.AffectedResourcesSummary{Total: 1, Unchanged: 1},
					ImpactAnalysis:    []dt.XRImpact{{ObjectReference: corev1.ObjectReference{APIVersion: "example.org/v1", Kind: "XResource", Name: "xr-1"}, Status: dt.XRStatusUnchanged}},
				}},
			},
			colorize: false,
			minimize: true,
			validate: func(t *testing.T, result string) {
				t.Helper()

				if !strings.Contains(result, "=== Composition Changes ===") {
					t.Errorf("expected composition changes header, got: %q", result)
				}

				if !strings.Contains(result, "~~~ Composition/test-comp (minimized)") {
					t.Errorf("expected minimized marker line, got: %q", result)
				}

				if !strings.Contains(result, "=== Affected Composite Resources ===") {
					t.Errorf("expected affected resources section, got: %q", result)
				}

				if !strings.Contains(result, "=== Impact Analysis ===") {
					t.Errorf("expected impact analysis section, got: %q", result)
				}

				if strings.Contains(result, "Summary: 1 modified") {
					t.Errorf("expected no composition Summary footer when minimized, got: %q", result)
				}
			},
		},
		// Issue #478: which composites would not adopt the resulting revision is settled locally, with no
		// render, so a skipped impact analysis must still report it. What the skip withholds is the
		// changed/unchanged/errored summary — that verdict needed the renders it did not run.
		"SkippedAnalysisStillReportsFilteredComposites": {
			output: &dt.CompDiffOutput{
				Compositions: []dt.CompositionDiff{{
					Name:                  "test-comp",
					ImpactAnalysisSkipped: true,
					RevisionImpact:        dt.RevisionImpact{ChangeScope: "metadata", CreatesRevision: true, RepointedComposites: 1},
					AffectedResources:     dt.AffectedResourcesSummary{Total: 3, FilteredByPolicy: 1, FilteredByDeletion: 1},
					ImpactAnalysis: []dt.XRImpact{
						{
							ObjectReference: corev1.ObjectReference{APIVersion: "example.org/v1", Kind: "XR", Name: "my-manual-xr", Namespace: "default"},
							Status:          dt.XRStatusFiltered,
							FilterReason:    dt.FilterReasonManualPolicy,
						},
						{
							ObjectReference: corev1.ObjectReference{APIVersion: "example.org/v1", Kind: "XR", Name: "my-deleting-xr", Namespace: "default"},
							Status:          dt.XRStatusFiltered,
							FilterReason:    dt.FilterReasonDeleting,
							FilterDetail:    "deletionTimestamp: 2026-09-07T11:25:03Z",
						},
					},
				}},
			},
			colorize: false,
			validate: func(t *testing.T, result string) {
				t.Helper()

				// Discovery reports the breakdown, not the names.
				for _, unwanted := range []string{"my-manual-xr", "my-deleting-xr"} {
					if strings.Contains(result, unwanted) {
						t.Errorf("did not expect discovery to name %q, got:\n%s", unwanted, result)
					}
				}

				for _, want := range []string{
					"Impact analysis skipped",
					"2 of 3 XR(s) using composition test-comp would not adopt this revision",
					"1 with Manual update policy (use --include-manual to see them)",
					"1 being deleted",
				} {
					if !strings.Contains(result, want) {
						t.Errorf("expected %q in output, got:\n%s", want, result)
					}
				}

				// The kept composites went unevaluated, so no per-status verdict may be printed for them.
				for _, unwanted := range []string{"=== Affected Composite Resources ===", "=== Impact Analysis ==="} {
					if strings.Contains(result, unwanted) {
						t.Errorf("did not expect %q for a skipped composition, got:\n%s", unwanted, result)
					}
				}
			},
		},
		// In --resource mode the filtered composites reach ImpactAnalysis, so the skip path names them
		// individually with their reason and detail — the affordance issue #457 shipped, which the skip
		// used to discard.
		"SkippedAnalysisNamesFilteredCompositesInResourceMode": {
			output: &dt.CompDiffOutput{
				Compositions: []dt.CompositionDiff{{
					Name:                  "test-comp",
					Scoped:                true,
					ImpactAnalysisSkipped: true,
					RevisionImpact:        dt.RevisionImpact{ChangeScope: "metadata", CreatesRevision: true},
					AffectedResources:     dt.AffectedResourcesSummary{Total: 1, FilteredByDeletion: 1},
					ImpactAnalysis: []dt.XRImpact{{
						ObjectReference: corev1.ObjectReference{APIVersion: "example.org/v1", Kind: "XR", Name: "my-deleting-xr", Namespace: "default"},
						Status:          dt.XRStatusFiltered,
						FilterReason:    dt.FilterReasonDeleting,
						FilterDetail:    "deletionTimestamp: 2026-09-07T11:25:03Z",
					}},
				}},
			},
			colorize: false,
			validate: func(t *testing.T, result string) {
				t.Helper()

				for _, want := range []string{
					"Impact analysis skipped",
					"1 of 1 XR(s) using composition test-comp would not adopt this revision",
					"XR/my-deleting-xr",
					"filtered: being deleted (deletionTimestamp: 2026-09-07T11:25:03Z)",
				} {
					if !strings.Contains(result, want) {
						t.Errorf("expected %q in output, got:\n%s", want, result)
					}
				}
			},
		},
		// Nothing was filtered, so the skip note stands alone — no "0 of 0" line.
		"SkippedAnalysisWithNothingFilteredSaysNoMore": {
			output: &dt.CompDiffOutput{
				Compositions: []dt.CompositionDiff{{
					Name:                  "test-comp",
					ImpactAnalysisSkipped: true,
					RevisionImpact:        dt.RevisionImpact{ChangeScope: "none"},
					AffectedResources:     dt.AffectedResourcesSummary{Total: 2},
					ImpactAnalysis:        []dt.XRImpact{},
				}},
			},
			colorize: false,
			validate: func(t *testing.T, result string) {
				t.Helper()

				if !strings.Contains(result, "Impact analysis skipped") {
					t.Errorf("expected the skip note, got:\n%s", result)
				}

				if strings.Contains(result, "would not adopt this revision") {
					t.Errorf("did not expect a filtered-composites line when nothing was filtered, got:\n%s", result)
				}
			},
		},
		"MinimizeErrorStillSurfaces": {
			// A processing error must render in full even when minimized: only the
			// diff body is collapsed, never the error.
			output: &dt.CompDiffOutput{
				Compositions: []dt.CompositionDiff{{
					Name:           "error-comp",
					Error:          &dt.OutputError{Message: "boom"},
					ImpactAnalysis: []dt.XRImpact{},
				}},
			},
			colorize: false,
			minimize: true,
			validate: func(t *testing.T, result string) {
				t.Helper()

				if !strings.Contains(result, "Error processing composition error-comp") {
					t.Errorf("expected error to surface when minimized, got: %q", result)
				}
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			logger := tu.TestLogger(t, false)

			var buf bytes.Buffer

			opts := DefaultDiffOptions()
			opts.UseColors = tt.colorize
			opts.MinimizeComposition = tt.minimize
			opts.Stdout = &buf
			opts.Stderr = &bytes.Buffer{} // discard stderr

			diffRenderer := NewDiffRenderer(logger, opts)
			renderer := NewDefaultCompDiffRenderer(logger, diffRenderer, opts)

			err := renderer.RenderCompDiff(tt.output)
			if err != nil {
				t.Fatalf("RenderCompDiff() failed: %v", err)
			}

			tt.validate(t, buf.String())
		})
	}
}

// TestDefaultCompDiffRenderer_RenderCompDiff_TopLevelErrorsNotPrinted verifies
// that top-level errors in CompDiffOutput.Errors are written to neither stdout
// nor stderr: the processor returns them to the command, which prints them once.
func TestDefaultCompDiffRenderer_RenderCompDiff_TopLevelErrorsNotPrinted(t *testing.T) {
	output := &dt.CompDiffOutput{
		Compositions: []dt.CompositionDiff{},
		Errors: []dt.OutputError{
			{ResourceID: "xbuckets.example.org", Message: "failed to list XRs for composition"},
			{Message: "cluster connection lost"},
		},
	}

	logger := tu.TestLogger(t, false)

	var (
		stdout bytes.Buffer
		stderr bytes.Buffer
	)

	opts := DefaultDiffOptions()
	opts.UseColors = false
	opts.Stdout = &stdout
	opts.Stderr = &stderr

	diffRenderer := NewDiffRenderer(logger, opts)
	renderer := NewDefaultCompDiffRenderer(logger, diffRenderer, opts)

	if err := renderer.RenderCompDiff(output); err != nil {
		t.Fatalf("RenderCompDiff() failed: %v", err)
	}

	for _, e := range output.Errors {
		for name, out := range map[string]string{"stdout": stdout.String(), "stderr": stderr.String()} {
			if strings.Contains(out, e.Message) {
				t.Errorf("Expected %s not to contain error %q, got: %q", name, e.Message, out)
			}
		}
	}
}

func Test_formatXRStatusSummary(t *testing.T) {
	tests := map[string]struct {
		changed, unchanged, errors int
		want                       string
	}{
		"NoResources":     {0, 0, 0, ""},
		"OneChanged":      {1, 0, 0, "\nSummary: 1 resource with changes\n"},
		"MultipleChanged": {5, 0, 0, "\nSummary: 5 resources with changes\n"},
		"AllTypes":        {2, 3, 1, "\nSummary: 2 resources with changes, 3 resources unchanged, 1 resource with errors\n"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got := formatXRStatusSummary(tt.changed, tt.unchanged, tt.errors)
			if got != tt.want {
				t.Errorf("formatXRStatusSummary() = %q, want %q", got, tt.want)
			}
		})
	}
}

func Test_pluralize(t *testing.T) {
	tests := map[string]struct {
		count int
		want  string
	}{
		"Singular_NoSuffix":  {count: 1, want: ""},
		"Zero_HasSuffix":     {count: 0, want: "s"},
		"Plural_HasSuffix":   {count: 2, want: "s"},
		"Negative_HasSuffix": {count: -1, want: "s"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := pluralize(tt.count); got != tt.want {
				t.Errorf("pluralize(%d) = %q, want %q", tt.count, got, tt.want)
			}
		})
	}
}

func TestXRStatusFiltered_TextRenderer(t *testing.T) {
	comp := dt.CompositionDiff{
		Name:              "test-comp",
		AffectedResources: dt.AffectedResourcesSummary{Total: 3, FilteredByPolicy: 1, FilteredBySelector: 1, FilteredByDeletion: 1},
		ImpactAnalysis: []dt.XRImpact{
			{
				ObjectReference: corev1.ObjectReference{APIVersion: "example.org/v1", Kind: "XR", Name: "manual-xr", Namespace: "ns"},
				Status:          dt.XRStatusFiltered,
				FilterReason:    dt.FilterReasonManualPolicy,
				FilterDetail:    "compositionRevisionRef: test-comp-abc1234",
			},
			{
				ObjectReference: corev1.ObjectReference{APIVersion: "example.org/v1", Kind: "XR", Name: "selector-xr", Namespace: "ns"},
				Status:          dt.XRStatusFiltered,
				FilterReason:    dt.FilterReasonRevisionSelectorMismatch,
				FilterDetail:    "compositionRevisionSelector {version: 0.0.1} does not match composition labels {version: 0.0.2}",
			},
			{
				ObjectReference: corev1.ObjectReference{APIVersion: "example.org/v1", Kind: "XR", Name: "deleting-xr", Namespace: "ns"},
				Status:          dt.XRStatusFiltered,
				FilterReason:    dt.FilterReasonDeleting,
				FilterDetail:    "deletionTimestamp: 2026-09-07T11:25:03Z",
			},
		},
	}

	logger := tu.TestLogger(t, false)
	r := &DefaultCompDiffRenderer{logger: logger, opts: DefaultDiffOptions()}
	got := r.buildXRStatusList(comp.ImpactAnalysis)

	if !strings.Contains(got, "manual-xr") || !strings.Contains(got, "selector-xr") {
		t.Errorf("expected both XR names in output, got %q", got)
	}

	// Manual-policy exclusion must mention the policy and the escape hatch, and nothing more: the
	// revision it stays pinned to is data for structured output, not part of the human line.
	if !strings.Contains(got, "manual-xr (namespace: ns) — filtered: Manual update policy (use --include-manual to evaluate)\x1b[0m\n") {
		t.Errorf("expected the Manual-policy line unchanged, got %q", got)
	}

	if strings.Contains(got, "compositionRevisionRef") {
		t.Errorf("expected the Manual-policy line to omit its FilterDetail, got %q", got)
	}

	// Selector-mismatch exclusion must distinguish itself from policy and surface the hint.
	if !strings.Contains(strings.ToLower(got), "revision selector") {
		t.Errorf("expected 'revision selector' verbiage for selector mismatch, got %q", got)
	}

	if !strings.Contains(got, "does not match composition labels") {
		t.Errorf("expected the selector-mismatch fix hint in output, got %q", got)
	}

	// Deletion exclusion must say so and surface the timestamp, which is the diagnostic signal for
	// an XR that has been stuck terminating.
	if !strings.Contains(got, "being deleted") {
		t.Errorf("expected 'being deleted' verbiage for a deleting XR, got %q", got)
	}

	if !strings.Contains(got, "deletionTimestamp: 2026-09-07T11:25:03Z") {
		t.Errorf("expected the deletionTimestamp detail in output, got %q", got)
	}
}

// Test_allFilteredMessage covers the default-discovery "everything was filtered" line. Each
// single-reason case gets bespoke prose that names the remedy; mixed reasons enumerate the
// breakdown. The total must always be the sum of the per-reason counters, so a newly added reason
// cannot silently go unreported.
func Test_allFilteredMessage(t *testing.T) {
	tests := map[string]struct {
		summary dt.AffectedResourcesSummary
		want    string
	}{
		"PolicyOnly": {
			summary: dt.AffectedResourcesSummary{FilteredByPolicy: 2},
			want:    "All 2 XR(s) using composition test-comp have Manual update policy (use --include-manual to see them)",
		},
		"SelectorOnly": {
			summary: dt.AffectedResourcesSummary{FilteredBySelector: 3},
			want:    "All 3 XR(s) using composition test-comp have a compositionRevisionSelector that does not match the composition's labels, so they would not adopt this revision",
		},
		"DeletionOnly": {
			summary: dt.AffectedResourcesSummary{FilteredByDeletion: 1},
			want:    "All 1 XR(s) using composition test-comp are being deleted, so they would not adopt this revision",
		},
		"PolicyAndSelector": {
			summary: dt.AffectedResourcesSummary{FilteredByPolicy: 1, FilteredBySelector: 2},
			want:    "All 3 XR(s) using composition test-comp were filtered: 1 with Manual update policy (use --include-manual to see them), 2 with a compositionRevisionSelector that does not match the composition's labels",
		},
		"PolicyAndDeletion": {
			summary: dt.AffectedResourcesSummary{FilteredByPolicy: 1, FilteredByDeletion: 1},
			want:    "All 2 XR(s) using composition test-comp were filtered: 1 with Manual update policy (use --include-manual to see them), 1 being deleted",
		},
		"SelectorAndDeletion": {
			summary: dt.AffectedResourcesSummary{FilteredBySelector: 1, FilteredByDeletion: 2},
			want:    "All 3 XR(s) using composition test-comp were filtered: 1 with a compositionRevisionSelector that does not match the composition's labels, 2 being deleted",
		},
		"AllThreeReasons": {
			summary: dt.AffectedResourcesSummary{FilteredByPolicy: 1, FilteredBySelector: 2, FilteredByDeletion: 3},
			want:    "All 6 XR(s) using composition test-comp were filtered: 1 with Manual update policy (use --include-manual to see them), 2 with a compositionRevisionSelector that does not match the composition's labels, 3 being deleted",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if diff := gcmp.Diff(tt.want, allFilteredMessage("test-comp", tt.summary)); diff != "" {
				t.Errorf("allFilteredMessage() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestSkippedMessage pins the distinction the two skip reasons must keep. An identical composition
// creates no CompositionRevision and so genuinely cannot affect anything — a guarantee. A
// metadata-only change under --analyze-on=spec-change does create one and simply was not evaluated —
// an absence of information. Reporting the first for the second case would assert something the tool
// never established, which is the whole reason skippedMessage branches at all.
func TestSkippedMessage(t *testing.T) {
	tests := map[string]struct {
		impact dt.RevisionImpact
		// wantContains are substrings that must appear; wantOmits must not.
		wantContains []string
		wantOmits    []string
	}{
		"IdenticalCompositionMakesTheStrongClaim": {
			impact: dt.RevisionImpact{ChangeScope: "none", CreatesRevision: false},
			wantContains: []string{
				"identical to the cluster's",
				"creates no new CompositionRevision",
				"no composite resource could change",
				"--analyze-on=always",
			},
			// Must not hedge: for this case the tool really does know nothing can change.
			wantOmits: []string{"was not evaluated", "not predictable"},
		},
		"MetadataOnlyChangeMakesOnlyTheWeakClaim": {
			impact: dt.RevisionImpact{
				ChangeScope:           "metadata",
				CreatesRevision:       true,
				RepointedComposites:   2,
				PredictedRevisionName: "xbuckets.example.org-abc1234",
			},
			wantContains: []string{
				"still creates a new CompositionRevision",
				"2 composites would adopt",
				"whether that changes any rendered output was not evaluated",
				"--analyze-on=any-change",
			},
			wantOmits: []string{
				// Must never claim nothing could change, nor that the composition is identical.
				"no composite resource could change",
				"identical to the cluster's",
				// The hash suffix stays out of human output; see revisionImpactMessage.
				"abc1234",
			},
		},
		"SingleCompositeIsNotPluralized": {
			impact: dt.RevisionImpact{
				ChangeScope:         "metadata",
				CreatesRevision:     true,
				RepointedComposites: 1,
			},
			wantContains: []string{"1 composite would adopt"},
			wantOmits:    []string{"1 composites"},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got := skippedMessage(tt.impact)

			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("skippedMessage() missing %q, got:\n%s", want, got)
				}
			}

			for _, omit := range tt.wantOmits {
				if strings.Contains(got, omit) {
					t.Errorf("skippedMessage() must not contain %q, got:\n%s", omit, got)
				}
			}
		})
	}
}

// TestRevisionImpactMessage covers the line emitted when the composites *were* evaluated. It exists to
// make a downstream change interpretable — a composed resource templating on the revision name
// otherwise shows a diff with no visible cause — so the name has to be in it, and it must stay silent
// when there is no revision to name.
func TestRevisionImpactMessage(t *testing.T) {
	tests := map[string]struct {
		impact       dt.RevisionImpact
		want         string
		wantContains []string
		wantOmits    []string
	}{
		"NoRevisionSaysNothing": {
			impact: dt.RevisionImpact{ChangeScope: "none", CreatesRevision: false},
			want:   "",
		},
		"ReportsTheChurnWithoutTheHash": {
			impact: dt.RevisionImpact{
				ChangeScope:           "spec",
				CreatesRevision:       true,
				RepointedComposites:   3,
				PredictedRevisionName: "xbuckets.example.org-abc1234",
			},
			wantContains: []string{
				"creates a new CompositionRevision",
				"3 composites would adopt",
			},
			// The name is available on the impact but must stay out of human stdout, which would
			// otherwise churn on every composition edit. Structured output carries it instead.
			wantOmits: []string{"abc1234"},
		},
		"ZeroAdoptingCompositesSaysNothing": {
			// The line's only job is explaining a downstream diff that a template reading the revision
			// name would cause. Nothing adopts this revision, so there is no such diff, and the sections
			// around it already report the composition as changed.
			impact: dt.RevisionImpact{
				ChangeScope:           "spec",
				CreatesRevision:       true,
				PredictedRevisionName: "xbuckets.example.org-abc1234",
			},
			want: "",
		},
		"UnpredictableNameReadsIdentically": {
			// Nothing was seeded here, so a revision-observing change would go undetected — but that is
			// said on stderr as a warning, which is the channel for "this could not be checked". The
			// stdout line reports the same churn either way, so it needs no variant.
			impact:       dt.RevisionImpact{ChangeScope: "metadata", CreatesRevision: true, RepointedComposites: 1},
			wantContains: []string{"creates a new CompositionRevision", "1 composite would adopt"},
			wantOmits:    []string{"not predictable"},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got := revisionImpactMessage(tt.impact)

			if len(tt.wantContains) == 0 {
				if diff := gcmp.Diff(tt.want, got); diff != "" {
					t.Errorf("revisionImpactMessage() mismatch (-want +got):\n%s", diff)
				}

				return
			}

			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("revisionImpactMessage() missing %q, got:\n%s", want, got)
				}
			}

			for _, omit := range tt.wantOmits {
				if strings.Contains(got, omit) {
					t.Errorf("revisionImpactMessage() must not contain %q, got:\n%s", omit, got)
				}
			}
		})
	}
}

// TestRenderCompDiff_RevisionImpactLine checks the wiring, which the message unit tests above cannot:
// that the line reaches stdout for an evaluated composition, and that a skipped one reports the churn
// exactly once rather than twice (skippedMessage carries its own sentence, and both would be emitted if
// the early-continue were removed). It also pins that the hash never reaches stdout by either route.
func TestRenderCompDiff_RevisionImpactLine(t *testing.T) {
	impact := dt.RevisionImpact{
		ChangeScope:           "metadata",
		CreatesRevision:       true,
		RepointedComposites:   1,
		PredictedRevisionName: "xbuckets.example.org-abc1234",
	}

	tests := map[string]struct {
		comp dt.CompositionDiff
		// wantCount is how many times the churn is reported: "composite would adopt" appears in both
		// revisionImpactMessage and skippedMessage, so it counts either route.
		wantCount int
	}{
		"EvaluatedCompositionReportsChurnOnce": {
			comp: dt.CompositionDiff{
				Name:              "xbuckets.example.org",
				RevisionImpact:    impact,
				AffectedResources: dt.AffectedResourcesSummary{Total: 1, Unchanged: 1},
				ImpactAnalysis: []dt.XRImpact{{
					ObjectReference: corev1.ObjectReference{Kind: "XBucket", Name: "test-xr"},
					Status:          dt.XRStatusUnchanged,
				}},
			},
			wantCount: 1,
		},
		"SkippedCompositionReportsChurnOnce": {
			comp: dt.CompositionDiff{
				Name:                  "xbuckets.example.org",
				RevisionImpact:        impact,
				ImpactAnalysisSkipped: true,
			},
			wantCount: 1,
		},
		"UnchangedCompositionReportsChurnNever": {
			comp: dt.CompositionDiff{
				Name:           "xbuckets.example.org",
				RevisionImpact: dt.RevisionImpact{ChangeScope: "none", CreatesRevision: false},
			},
			wantCount: 0,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			opts := DefaultDiffOptions()
			opts.Stdout = &stdout
			opts.Stderr = &stderr
			opts.UseColors = false

			r := NewDefaultCompDiffRenderer(tu.TestLogger(t, false), NewDiffRenderer(tu.TestLogger(t, false), opts), opts)

			if err := r.RenderCompDiff(&dt.CompDiffOutput{Compositions: []dt.CompositionDiff{tt.comp}}); err != nil {
				t.Fatalf("RenderCompDiff() unexpected error: %v", err)
			}

			if got := strings.Count(stdout.String(), "composite would adopt"); got != tt.wantCount {
				t.Errorf("RenderCompDiff() reported the revision churn %d times, want %d; output:\n%s",
					got, tt.wantCount, stdout.String())
			}

			// The predicted name's hash suffix must never reach human stdout by any route; it lives in
			// structured output, where churning on a composition edit costs nobody anything.
			if strings.Contains(stdout.String(), "abc1234") {
				t.Errorf("RenderCompDiff() leaked the revision hash into human output:\n%s", stdout.String())
			}
		})
	}
}

func TestCompositionDiff_HasChanges_FilteredOnly(t *testing.T) {
	c := &dt.CompositionDiff{
		ImpactAnalysis: []dt.XRImpact{
			{Status: dt.XRStatusFiltered, FilterReason: dt.FilterReasonManualPolicy},
			{Status: dt.XRStatusFiltered, FilterReason: dt.FilterReasonRevisionSelectorMismatch},
			{Status: dt.XRStatusFiltered, FilterReason: dt.FilterReasonDeleting},
		},
	}
	if c.HasChanges() {
		t.Errorf("dt.CompositionDiff with only filtered impacts should not be HasChanges()")
	}
}
