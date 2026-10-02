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

package docker

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/moby/moby/api/types/container"
)

func TestRunWithLabels(t *testing.T) {
	cases := map[string]struct {
		reason   string
		existing map[string]string
		labels   map[string]string
		want     map[string]string
	}{
		"NilLabels": {
			reason: "Labels should be set when the container config has none.",
			labels: map[string]string{"a": "1"},
			want:   map[string]string{"a": "1"},
		},
		"MergeLabels": {
			reason:   "Labels should be merged into existing labels.",
			existing: map[string]string{"a": "1"},
			labels:   map[string]string{"b": "2"},
			want:     map[string]string{"a": "1", "b": "2"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := &runContainerConfig{containerConfig: &container.Config{Labels: tc.existing}}
			RunWithLabels(tc.labels)(cfg)
			if diff := cmp.Diff(tc.want, cfg.containerConfig.Labels); diff != "" {
				t.Errorf("\n%s\nRunWithLabels(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}
