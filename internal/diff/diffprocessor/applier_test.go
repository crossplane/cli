package diffprocessor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	gcmp "github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	un "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	"github.com/crossplane/cli/v2/internal/diff/compare"
	tu "github.com/crossplane/cli/v2/internal/diff/testutils"
	"github.com/crossplane/cli/v2/internal/plan"
)

func TestWithApplierWarning(t *testing.T) {
	live := func(entries ...metav1.ManagedFieldsEntry) *un.Unstructured {
		u := &un.Unstructured{Object: map[string]any{}}
		u.SetManagedFields(entries)
		return u
	}
	apply := metav1.ManagedFieldsOperationApply
	update := metav1.ManagedFieldsOperationUpdate

	type want struct {
		context    map[string]string
		imperative bool
		noWarnings bool
	}
	cases := map[string]struct {
		reason string
		live   *un.Unstructured
		want   want
	}{
		"OneApplier": {
			reason: "A resolved applier gives no warning.",
			live:   live(metav1.ManagedFieldsEntry{Manager: "argocd-controller", Operation: apply}),
			want:   want{noWarnings: true},
		},
		"OnlyImperativeWriter": {
			reason: "With no candidate applier, applying removes nothing whoever applies, so there is no warning.",
			live:   live(metav1.ManagedFieldsEntry{Manager: "kubectl-create", Operation: update}),
			want:   want{noWarnings: true},
		},
		"TwoAppliers": {
			reason: "Two candidate appliers warn, naming both, and no imperative writer.",
			live: live(
				metav1.ManagedFieldsEntry{Manager: "platform-operator", Operation: apply},
				metav1.ManagedFieldsEntry{Manager: "argocd-controller", Operation: apply},
			),
			want: want{context: map[string]string{"resource": "XR/x", "candidates": "argocd-controller, platform-operator"}},
		},
		"ApplierAndImperativeWriter": {
			reason: "An imperative writer beside the only applier warns, naming both.",
			live: live(
				metav1.ManagedFieldsEntry{Manager: "kubectl-create", Operation: update},
				metav1.ManagedFieldsEntry{Manager: "platform-operator", Operation: apply},
			),
			want: want{
				context:    map[string]string{"resource": "XR/x", "candidates": "platform-operator", "imperativeWriters": "kubectl-create"},
				imperative: true,
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p := &DefaultDiffProcessor{config: ProcessorConfig{Advisories: NewAdvisoryCollector(nil)}}
			p.withApplier(withUserSupplied(t.Context()), tc.live, nil, "XR/x")

			got := p.config.Advisories.Advisories()
			if tc.want.noWarnings {
				if len(got) != 0 {
					t.Errorf("%s\nwithApplier(...): want no warnings, got %v", tc.reason, got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("%s\nwithApplier(...): want 1 warning, got %v", tc.reason, got)
			}
			if diff := gcmp.Diff(tc.want.context, got[0].Context); diff != "" {
				t.Errorf("%s\nwithApplier(...) warning context: -want, +got:\n%s", tc.reason, diff)
			}
			if mentions := strings.Contains(got[0].Message, "imperative writers"); mentions != tc.want.imperative {
				t.Errorf("%s\nwithApplier(...) warning %q: mentions imperative writers = %t, want %t", tc.reason, got[0].Message, mentions, tc.want.imperative)
			}
		})
	}
}

func TestCalculateDiffClientSideApply(t *testing.T) {
	xr := func(annotations map[string]string, spec map[string]any) *un.Unstructured {
		u := &un.Unstructured{Object: map[string]any{
			"apiVersion": "example.org/v1",
			"kind":       "XR",
			"metadata":   map[string]any{"name": "x", "namespace": "default"},
			"spec":       spec,
		}}
		u.SetAnnotations(annotations)
		return u
	}
	lastApplied := map[string]string{
		plan.LastAppliedConfigAnnotation: `{"apiVersion":"example.org/v1","kind":"XR","metadata":{"name":"x","namespace":"default"},"spec":{"size":"large","team":"platform"}}`,
	}
	desired := xr(nil, map[string]any{"size": "large"})
	// As kubectl serializes it: with metadata.annotations even when empty.
	desiredApplied := `{"apiVersion":"example.org/v1","kind":"XR","metadata":{"annotations":{},"name":"x","namespace":"default"},"spec":{"size":"large"}}`

	type want struct {
		// patch is the merge patch sent, decoded.
		patch map[string]any
	}
	cases := map[string]struct {
		reason string
		live   *un.Unstructured
		want   want
	}{
		"DroppedFieldInAnnotationIsRemoved": {
			reason: "A field the last-applied annotation has and the manifest drops is nulled, and the annotation set to the manifest.",
			live:   xr(lastApplied, map[string]any{"size": "large", "team": "platform", "region": "eu"}),
			want: want{patch: map[string]any{
				"metadata": map[string]any{"annotations": map[string]any{plan.LastAppliedConfigAnnotation: desiredApplied}},
				"spec":     map[string]any{"team": nil},
			}},
		},
		"NoAnnotationRemovesNothing": {
			reason: "With no annotation there is no original, so no field is nulled: only the annotation is added.",
			live:   xr(map[string]string{"other": "kept"}, map[string]any{"size": "large", "team": "platform"}),
			want: want{patch: map[string]any{
				"metadata": map[string]any{"annotations": map[string]any{plan.LastAppliedConfigAnnotation: desiredApplied}},
			}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var sent []byte
			apply := tu.NewMockApplyClient().
				WithDryRunApply(func(context.Context, *un.Unstructured, string) (*un.Unstructured, error) {
					return nil, errors.New("client-side apply must not server-side apply")
				}).
				WithDryRunPatch(func(_ context.Context, _ *un.Unstructured, patch []byte) (*un.Unstructured, error) {
					sent = patch
					return tc.live, nil
				}).Build()
			advisories := NewAdvisoryCollector(nil)
			calculator := NewDiffCalculator(apply, tu.NewMockAccessChecker().Build(), tu.NewMockResourceTreeClient().Build(),
				NewResourceManager(tu.NewMockResourceClient().WithResourcesExist(tc.live).Build(), tu.NewMockDefinitionClient().Build(),
					tu.NewMockResourceTreeClient().Build(), tu.TestLogger(t, false), advisories),
				tu.TestLogger(t, false), compare.Options{}, DryRunOnAll, &tu.MockDefaulter{}, nil)

			p := &DefaultDiffProcessor{config: ProcessorConfig{Advisories: advisories, Applier: plan.ApplierFlags{ServerSide: new(false)}}}
			ctx := p.withApplier(withUserSupplied(t.Context()), tc.live, nil, "XR/x")

			if _, err := calculator.CalculateDiff(ctx, nil, desired); err != nil {
				t.Fatalf("%s\nCalculateDiff(...): unexpected error: %v", tc.reason, err)
			}

			var got map[string]any
			if err := json.Unmarshal(sent, &got); err != nil {
				t.Fatalf("%s\nCalculateDiff(...) sent patch %q: %v", tc.reason, sent, err)
			}
			if diff := gcmp.Diff(tc.want.patch, got); diff != "" {
				t.Errorf("%s\nCalculateDiff(...) merge patch: -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}
