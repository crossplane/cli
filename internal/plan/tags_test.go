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
	"maps"
	"testing"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func externalTags(extra map[string]any) map[string]any {
	m := map[string]any{
		"crossplane-kind":           "bucket.s3.aws.upbound.io",
		"crossplane-name":           "my-bucket",
		"crossplane-providerconfig": "default",
	}
	maps.Copy(m, extra)
	return m
}

func TestMergeExternalTags(t *testing.T) {
	cases := map[string]struct {
		reason  string
		desired map[string]any // spec of the desired resource
		live    map[string]any // spec of the live resource
		want    map[string]any // expected desired spec after the merge
	}{
		"AddsTagsToUntaggedManifest": {
			reason: "A manifest without tags gains exactly the external tags the initializer guarantees, not the user's other live tags.",
			desired: map[string]any{"forProvider": map[string]any{
				"region": "eu-central-1",
			}},
			live: map[string]any{"forProvider": map[string]any{
				"region": "eu-central-1",
				"tags":   externalTags(map[string]any{"team": "platform"}),
			}},
			want: map[string]any{"forProvider": map[string]any{
				"region": "eu-central-1",
				"tags":   externalTags(nil),
			}},
		},
		"KeepsUserTags": {
			reason: "A manifest with its own tags keeps them and gains the external ones.",
			desired: map[string]any{"forProvider": map[string]any{
				"tags": map[string]any{"env": "prod"},
			}},
			live: map[string]any{"forProvider": map[string]any{
				"tags": externalTags(nil),
			}},
			want: map[string]any{"forProvider": map[string]any{
				"tags": externalTags(map[string]any{"env": "prod"}),
			}},
		},
		"LiveValuesWin": {
			reason: "The initializer overwrites external tag values, so the live values win over manifest ones.",
			desired: map[string]any{"forProvider": map[string]any{
				"tags": map[string]any{"crossplane-name": "wrong"},
			}},
			live: map[string]any{"forProvider": map[string]any{
				"tags": externalTags(nil),
			}},
			want: map[string]any{"forProvider": map[string]any{
				"tags": externalTags(nil),
			}},
		},
		"IgnoresMapsWithoutFullSignature": {
			reason: "A live map carrying only some external tag keys is not the initializer's work and stays untouched.",
			desired: map[string]any{"forProvider": map[string]any{
				"region": "eu-central-1",
			}},
			live: map[string]any{"forProvider": map[string]any{
				"tags": map[string]any{"crossplane-kind": "something"},
			}},
			want: map[string]any{"forProvider": map[string]any{
				"region": "eu-central-1",
			}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			desired := mr(tc.desired)
			MergeExternalTags(desired, mr(tc.live))

			got, _, _ := unstructured.NestedMap(desired.Object, "spec")
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("\n%s\nMergeExternalTags(...): -want desired spec, +got:\n%s", tc.reason, diff)
			}
		})
	}

	t.Run("NoLiveResourceIsANoOp", func(t *testing.T) {
		desired := mr(map[string]any{"forProvider": map[string]any{"region": "eu-central-1"}})
		MergeExternalTags(desired, nil)
		if _, ok, _ := unstructured.NestedMap(desired.Object, "spec", "forProvider", "tags"); ok {
			t.Errorf("MergeExternalTags(desired, nil): a create must not invent tags")
		}
	})

	t.Run("FallbackReportsNoTagRemoval", func(t *testing.T) {
		desired := mr(map[string]any{"forProvider": map[string]any{"region": "eu-central-1"}})
		live := mr(map[string]any{"forProvider": map[string]any{
			"region": "eu-central-1",
			"tags":   externalTags(nil),
		}})

		MergeExternalTags(desired, live)
		got := Fallback(desired, live, "test")

		if got.Action != ActionNoOp || len(got.Changes) != 0 {
			t.Errorf("Fallback after MergeExternalTags: want a clean no-op, got action %q with changes %+v", got.Action, got.Changes)
		}
	})
}
