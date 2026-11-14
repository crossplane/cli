package tree

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	crossplanemeta "github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"

	"github.com/crossplane/cli/v2/pkg/resource"
)

func TestSuspendCmd_applyAnnotation(t *testing.T) {
	errBoom := errors.New("boom")
	paused := crossplanemeta.AnnotationKeyReconciliationPaused

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
			reason:    "Should add the paused annotation to a resource without annotations",
			resources: []*resource.Resource{newTestResource("root", nil)},
			want: want{
				updated: map[string]map[string]string{
					"root": {paused: "true"},
				},
			},
		},
		"ExistingAnnotations": {
			reason:    "Should add the paused annotation and keep existing annotations",
			resources: []*resource.Resource{newTestResource("root", map[string]string{"foo": "bar"})},
			want: want{
				updated: map[string]map[string]string{
					"root": {"foo": "bar", paused: "true"},
				},
			},
		},
		"Children": {
			reason: "Should add the paused annotation to all resources in the tree",
			resources: []*resource.Resource{
				newTestResource("root", nil,
					newTestResource("child", nil,
						newTestResource("grandchild", nil))),
			},
			want: want{
				updated: map[string]map[string]string{
					"root":       {paused: "true"},
					"child":      {paused: "true"},
					"grandchild": {paused: "true"},
				},
			},
		},
		"UpdateError": {
			reason:    "Should return an error if updating a resource fails",
			resources: []*resource.Resource{newTestResource("root", nil)},
			failOn:    "root",
			want: want{
				updated: map[string]map[string]string{},
				err:     errors.Wrapf(errBoom, errFmtUpdateResource, "Example", "root", ""),
			},
		},
		"ChildUpdateError": {
			reason: "Should return an error if updating a child resource fails",
			resources: []*resource.Resource{
				newTestResource("root", nil, newTestResource("child", nil)),
			},
			failOn: "child",
			want: want{
				updated: map[string]map[string]string{
					"root": {paused: "true"},
				},
				err: errors.Wrapf(errBoom, errFmtUpdateResource, "Example", "child", ""),
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			updated := map[string]map[string]string{}
			c := &SuspendCmd{}

			err := c.applyAnnotation(context.Background(), recordingClient(updated, tc.failOn, errBoom), tc.resources)
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\napplyAnnotation(...): -want error, +got error:\n%s", tc.reason, diff)
			}

			if diff := cmp.Diff(tc.want.updated, updated); diff != "" {
				t.Errorf("\n%s\napplyAnnotation(...): -want updated, +got updated:\n%s", tc.reason, diff)
			}
		})
	}
}
