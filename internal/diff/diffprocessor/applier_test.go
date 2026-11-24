package diffprocessor

import (
	"strings"
	"testing"

	gcmp "github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	un "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
