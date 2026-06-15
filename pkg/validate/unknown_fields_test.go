/*
Copyright 2024 The Crossplane Authors.

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

package validate

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
)

func TestValidateUnknownFields(t *testing.T) {
	type args struct {
		mr  map[string]any
		sch *schema.Structural
	}

	type want struct {
		errs field.ErrorList
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"UnknownFieldPresent": {
			reason: "Should detect unknown fields in the resource and return an error",
			args: args{
				mr: map[string]any{
					"apiVersion": "test.org/v1alpha1",
					"kind":       "Test",
					"metadata": map[string]any{
						"name": "test-instance",
					},
					"spec": map[string]any{
						"replicas":     3,
						"unknownField": "should fail",
					},
				},
				sch: &schema.Structural{
					Properties: map[string]schema.Structural{
						"spec": {
							Properties: map[string]schema.Structural{
								"replicas": {
									Generic: schema.Generic{Type: "integer"},
								},
							},
						},
					},
				},
			},
			want: want{
				errs: field.ErrorList{
					field.Invalid(field.NewPath("spec.unknownField"), "unknownField", `unknown field: "unknownField"`),
				},
			},
		},
		"UnknownFieldNotPresent": {
			reason: "Should not return an error when no unknown fields are present",
			args: args{
				mr: map[string]any{
					"apiVersion": "test.org/v1alpha1",
					"kind":       "Test",
					"metadata": map[string]any{
						"name": "test-instance",
					},
					"spec": map[string]any{
						"replicas": 3,
					},
				},
				sch: &schema.Structural{
					Properties: map[string]schema.Structural{
						"spec": {
							Properties: map[string]schema.Structural{
								"replicas": {
									Generic: schema.Generic{Type: "integer"},
								},
							},
						},
					},
				},
			},
			want: want{
				errs: field.ErrorList{},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			errs := validateUnknownFields(tc.args.mr, tc.args.sch)
			if diff := cmp.Diff(tc.want.errs, errs, test.EquateErrors()); diff != "" {
				t.Errorf("%s\nvalidateUnknownFields(...): -want errs, +got errs:\n%s", tc.reason, diff)
			}
		})
	}
}
