//go:build integration

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

package integration

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	authorizationv1 "k8s.io/api/authorization/v1"
	rbacv1 "k8s.io/api/rbac/v1"
)

func TestProbeAccess(t *testing.T) {
	type want struct {
		spec authorizationv1.SelfSubjectAccessReviewSpec
		ok   bool
	}

	cases := map[string]struct {
		reason string
		rules  []rbacv1.PolicyRule
		want   want
	}{
		"NoRules": {
			reason: "Rules granting nothing leave nothing to probe.",
			rules:  []rbacv1.PolicyRule{},
		},
		"SkipsIncompleteResourceRule": {
			reason: "A rule without an API group is skipped in favour of a later complete one.",
			rules: []rbacv1.PolicyRule{
				{Resources: []string{"pods"}, Verbs: []string{"get"}},
				{APIGroups: []string{"apps"}, Resources: []string{"deployments"}, Verbs: []string{"list"}},
			},
			want: want{ok: true, spec: authorizationv1.SelfSubjectAccessReviewSpec{
				ResourceAttributes: &authorizationv1.ResourceAttributes{Group: "apps", Resource: "deployments", Verb: "list"},
			}},
		},
		"PrefersResourceRuleOverEarlierNonResource": {
			reason: "A resource rule is probed even when a non-resource rule comes first.",
			rules: []rbacv1.PolicyRule{
				{NonResourceURLs: []string{"/healthz"}, Verbs: []string{"get"}},
				{APIGroups: []string{""}, Resources: []string{"configmaps"}, ResourceNames: []string{"cm"}, Verbs: []string{"get"}},
			},
			want: want{ok: true, spec: authorizationv1.SelfSubjectAccessReviewSpec{
				ResourceAttributes: &authorizationv1.ResourceAttributes{Group: "", Resource: "configmaps", Name: "cm", Verb: "get"},
			}},
		},
		"NonResourceOnly": {
			reason: "With no resource rule, the first non-resource rule is probed.",
			rules:  []rbacv1.PolicyRule{{NonResourceURLs: []string{"/metrics"}, Verbs: []string{"get"}}},
			want: want{ok: true, spec: authorizationv1.SelfSubjectAccessReviewSpec{
				NonResourceAttributes: &authorizationv1.NonResourceAttributes{Path: "/metrics", Verb: "get"},
			}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			spec, ok := probeAccess(tc.rules)
			if diff := cmp.Diff(tc.want, want{spec: spec, ok: ok}, cmp.AllowUnexported(want{})); diff != "" {
				t.Errorf("\n%s\nprobeAccess(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}
