package tree

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	crossplanemeta "github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"

	"github.com/crossplane/cli/v2/pkg/resource"
)

func TestResumeCmd_applyAnnotation(t *testing.T) {
	errBoom := errors.New("boom")
	paused := crossplanemeta.AnnotationKeyReconciliationPaused
	requestedAt := crossplanemeta.AnnotationKeyReconcileRequestedAt

	// The reconcile-requested-at annotation contains the current time, so we
	// replace it with this placeholder after checking it is a valid timestamp.
	const timestamp = "<timestamp>"

	type want struct {
		updated map[string]map[string]string
		err     error
	}

	tests := map[string]struct {
		reason    string
		resources []*resource.Resource
		failOn    string
		want      want
	}{
		"NilAnnotations": {
			reason:    "Should request a reconcile on a resource without annotations",
			resources: []*resource.Resource{newTestResource("root", nil)},
			want: want{
				updated: map[string]map[string]string{
					"root": {requestedAt: timestamp},
				},
			},
		},
		"Paused": {
			reason:    "Should remove the paused annotation, request a reconcile and keep other annotations",
			resources: []*resource.Resource{newTestResource("root", map[string]string{"foo": "bar", paused: "true"})},
			want: want{
				updated: map[string]map[string]string{
					"root": {"foo": "bar", requestedAt: timestamp},
				},
			},
		},
		"Children": {
			reason: "Should remove the paused annotation and request a reconcile for all resources in the tree",
			resources: []*resource.Resource{
				newTestResource("root", map[string]string{paused: "true"},
					newTestResource("child", map[string]string{paused: "true", "foo": "bar"},
						newTestResource("grandchild", map[string]string{paused: "true"}))),
			},
			want: want{
				updated: map[string]map[string]string{
					"root":       {requestedAt: timestamp},
					"child":      {"foo": "bar", requestedAt: timestamp},
					"grandchild": {requestedAt: timestamp},
				},
			},
		},
		"UpdateError": {
			reason:    "Should return an error if updating a resource fails",
			resources: []*resource.Resource{newTestResource("root", map[string]string{paused: "true"})},
			failOn:    "root",
			want: want{
				updated: map[string]map[string]string{},
				err:     errors.Wrapf(errBoom, errFmtUpdateResource, "Example", "root", ""),
			},
		},
		"ChildUpdateError": {
			reason: "Should return an error if updating a child resource fails",
			resources: []*resource.Resource{
				newTestResource("root", map[string]string{paused: "true"},
					newTestResource("child", map[string]string{paused: "true"})),
			},
			failOn: "child",
			want: want{
				updated: map[string]map[string]string{
					"root": {requestedAt: timestamp},
				},
				err: errors.Wrapf(errBoom, errFmtUpdateResource, "Example", "child", ""),
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			updated := map[string]map[string]string{}
			c := &ResumeCmd{}

			err := c.applyAnnotation(context.Background(), recordingClient(updated, tc.failOn, errBoom), tc.resources)
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\napplyAnnotation(...): -want error, +got error:\n%s", tc.reason, diff)
			}

			for obj, annotations := range updated {
				v, ok := annotations[requestedAt]
				if !ok {
					t.Errorf("\n%s\napplyAnnotation(...): %q is missing the %s annotation", tc.reason, obj, requestedAt)
					continue
				}
				if _, err := time.Parse(time.RFC3339, v); err != nil {
					t.Errorf("\n%s\napplyAnnotation(...): %s annotation of %q is not a valid RFC3339 timestamp: %v", tc.reason, requestedAt, obj, err)
				}
				annotations[requestedAt] = timestamp
			}

			if diff := cmp.Diff(tc.want.updated, updated); diff != "" {
				t.Errorf("\n%s\napplyAnnotation(...): -want updated, +got updated:\n%s", tc.reason, diff)
			}
		})
	}
}
