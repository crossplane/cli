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

package validate

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
)

// testCRD is a minimal CRD whose schema requires spec.replicas as an integer.
var testCRD = &extv1.CustomResourceDefinition{
	TypeMeta: metav1.TypeMeta{
		APIVersion: "apiextensions.k8s.io/v1",
		Kind:       "CustomResourceDefinition",
	},
	ObjectMeta: metav1.ObjectMeta{Name: "test"},
	Spec: extv1.CustomResourceDefinitionSpec{
		Group: "test.org",
		Names: extv1.CustomResourceDefinitionNames{
			Kind:     "Test",
			ListKind: "TestList",
			Plural:   "tests",
			Singular: "test",
		},
		Scope: "Cluster",
		Versions: []extv1.CustomResourceDefinitionVersion{{
			Name:    "v1alpha1",
			Served:  true,
			Storage: true,
			Schema: &extv1.CustomResourceValidation{
				OpenAPIV3Schema: &extv1.JSONSchemaProps{
					Type: "object",
					Properties: map[string]extv1.JSONSchemaProps{
						"spec": {
							Type: "object",
							Properties: map[string]extv1.JSONSchemaProps{
								"replicas": {Type: "integer"},
							},
							Required: []string{"replicas"},
						},
					},
				},
			},
		}},
	},
}

// testCRDWithCEL requires minReplicas <= replicas <= maxReplicas.
var testCRDWithCEL = &extv1.CustomResourceDefinition{
	TypeMeta: metav1.TypeMeta{
		APIVersion: "apiextensions.k8s.io/v1",
		Kind:       "CustomResourceDefinition",
	},
	ObjectMeta: metav1.ObjectMeta{Name: "test-cel"},
	Spec: extv1.CustomResourceDefinitionSpec{
		Group: "test.org",
		Names: extv1.CustomResourceDefinitionNames{
			Kind: "TestCEL", ListKind: "TestCELList", Plural: "testcels", Singular: "testcel",
		},
		Scope: "Cluster",
		Versions: []extv1.CustomResourceDefinitionVersion{{
			Name: "v1alpha1", Served: true, Storage: true,
			Schema: &extv1.CustomResourceValidation{
				OpenAPIV3Schema: &extv1.JSONSchemaProps{
					Type: "object",
					Properties: map[string]extv1.JSONSchemaProps{
						"spec": {
							Type: "object",
							XValidations: extv1.ValidationRules{{
								Rule:    "self.minReplicas <= self.replicas && self.replicas <= self.maxReplicas",
								Message: "replicas should be in between minReplicas and maxReplicas",
							}},
							Properties: map[string]extv1.JSONSchemaProps{
								"replicas":    {Type: "integer"},
								"minReplicas": {Type: "integer"},
								"maxReplicas": {Type: "integer"},
							},
							Required: []string{"replicas", "minReplicas", "maxReplicas"},
						},
					},
				},
			},
		}},
	},
}

// testCRDNoMatchingVersion is a CRD that shares group+kind with testCRD but
// only declares v1beta1. When used BEFORE testCRD in the crds slice,
// applyDefaults matches it first and fails because v1alpha1 is missing.
var testCRDNoMatchingVersion = &extv1.CustomResourceDefinition{
	TypeMeta: metav1.TypeMeta{
		APIVersion: "apiextensions.k8s.io/v1",
		Kind:       "CustomResourceDefinition",
	},
	ObjectMeta: metav1.ObjectMeta{Name: "test-other-version"},
	Spec: extv1.CustomResourceDefinitionSpec{
		Group: "test.org",
		Names: extv1.CustomResourceDefinitionNames{
			Kind: "Test", ListKind: "TestList", Plural: "tests", Singular: "test",
		},
		Scope: "Cluster",
		Versions: []extv1.CustomResourceDefinitionVersion{{
			Name: "v1beta1", Served: true, Storage: true,
			Schema: &extv1.CustomResourceValidation{
				OpenAPIV3Schema: &extv1.JSONSchemaProps{
					Type: "object",
					Properties: map[string]extv1.JSONSchemaProps{
						"spec": {
							Type: "object",
							Properties: map[string]extv1.JSONSchemaProps{
								"replicas": {Type: "integer"},
							},
							Required: []string{"replicas"},
						},
					},
				},
			},
		}},
	},
}

func TestSchemaValidate(t *testing.T) {
	validResource := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "test.org/v1alpha1",
		"kind":       "Test",
		"metadata":   map[string]any{"name": "test"},
		"spec":       map[string]any{"replicas": int64(1)},
	}}
	invalidSchemaResource := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "test.org/v1alpha1",
		"kind":       "Test",
		"metadata":   map[string]any{"name": "bad-type"},
		"spec":       map[string]any{"replicas": "not-an-int"},
	}}
	invalidCELResource := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "test.org/v1alpha1",
		"kind":       "TestCEL",
		"metadata":   map[string]any{"name": "bad-cel"},
		"spec": map[string]any{
			"replicas": int64(50), "minReplicas": int64(3), "maxReplicas": int64(10),
		},
	}}
	missingSchemaResource := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "other.org/v1",
		"kind":       "Unknown",
		"metadata":   map[string]any{"name": "no-crd"},
	}}
	unknownFieldResource := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "test.org/v1alpha1",
		"kind":       "Test",
		"metadata":   map[string]any{"name": "extra"},
		"spec": map[string]any{
			"replicas": int64(1), "unknownField": "surprise",
		},
	}}
	defaultingFailureResource := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "test.org/v1alpha1",
		"kind":       "Test",
		"metadata":   map[string]any{"name": "def-fail"},
		"spec":       map[string]any{"replicas": int64(1)},
	}}

	type args struct {
		resources []*unstructured.Unstructured
		crds      []*extv1.CustomResourceDefinition
	}
	// expect declares everything we assert about a single resource's result:
	// its Status and the exact set of FieldValidationErrors. Message and
	// Value are compared with cmpopts.IgnoreFields below, since k8s
	// validation libraries phrase those strings differently across versions.
	type expect struct {
		status ValidationStatus
		errors []FieldValidationError
	}
	type want struct {
		summary ValidationSummary
		perRes  []expect
		wantErr bool
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"Valid": {
			reason: "A resource matching its CRD schema should be marked Valid with no errors.",
			args: args{
				resources: []*unstructured.Unstructured{validResource},
				crds:      []*extv1.CustomResourceDefinition{testCRD},
			},
			want: want{
				summary: ValidationSummary{Total: 1, Valid: 1},
				perRes:  []expect{{status: ValidationStatusValid}},
			},
		},
		"InvalidSchema": {
			reason: "A resource violating OpenAPI schema should be Invalid with a schema-type field error on spec.replicas.",
			args: args{
				resources: []*unstructured.Unstructured{invalidSchemaResource},
				crds:      []*extv1.CustomResourceDefinition{testCRD},
			},
			want: want{
				summary: ValidationSummary{Total: 1, Invalid: 1},
				perRes: []expect{{
					status: ValidationStatusInvalid,
					errors: []FieldValidationError{
						{Type: FieldErrorTypeSchema, Field: "spec.replicas"},
					},
				}},
			},
		},
		"InvalidCEL": {
			reason: "A resource violating a CEL rule should be Invalid with a cel-type field error.",
			args: args{
				resources: []*unstructured.Unstructured{invalidCELResource},
				crds:      []*extv1.CustomResourceDefinition{testCRDWithCEL},
			},
			want: want{
				summary: ValidationSummary{Total: 1, Invalid: 1},
				perRes: []expect{{
					status: ValidationStatusInvalid,
					errors: []FieldValidationError{
						// Field is the CEL rule's location, "spec" in this fixture.
						{Type: FieldErrorTypeCEL, Field: "spec"},
					},
				}},
			},
		},
		"MissingSchema": {
			reason: "A resource whose GVK has no matching CRD should be MissingSchema with no errors.",
			args: args{
				resources: []*unstructured.Unstructured{missingSchemaResource},
				crds:      []*extv1.CustomResourceDefinition{testCRD},
			},
			want: want{
				summary: ValidationSummary{Total: 1, MissingSchemas: 1},
				perRes:  []expect{{status: ValidationStatusMissingSchema}},
			},
		},
		"UnknownField": {
			reason: "A resource with a field not declared in the schema should surface an unknownField error on the offending field path.",
			args: args{
				resources: []*unstructured.Unstructured{unknownFieldResource},
				crds:      []*extv1.CustomResourceDefinition{testCRD},
			},
			want: want{
				summary: ValidationSummary{Total: 1, Invalid: 1},
				perRes: []expect{{
					status: ValidationStatusInvalid,
					errors: []FieldValidationError{
						{Type: FieldErrorTypeUnknownField, Field: "spec.unknownField"},
					},
				}},
			},
		},
		"DefaultingFailureOnly": {
			reason: "A defaulting-only failure is a warning: status DefaultingFailed, summary counts as Valid (matches historical SchemaValidation semantics).",
			args: args{
				resources: []*unstructured.Unstructured{defaultingFailureResource},
				// testCRDNoMatchingVersion matches group+kind but only has v1beta1;
				// applyDefaults picks it first and fails before the v1alpha1 schema (from testCRD) is consulted.
				crds: []*extv1.CustomResourceDefinition{testCRDNoMatchingVersion, testCRD},
			},
			want: want{
				summary: ValidationSummary{Total: 1, Valid: 1},
				perRes: []expect{{
					status: ValidationStatusDefaultingFailed,
					errors: []FieldValidationError{
						{Type: FieldErrorTypeDefaulting},
					},
				}},
			},
		},
		"DefaultingFailureWithSchemaError": {
			reason: "Schema validation must still run when defaulting fails; both errors must be reported in order, and the resource must be Invalid (not DefaultingFailed).",
			args: args{
				// Same multi-CRD trick as DefaultingFailureOnly to force a defaulting failure,
				// but the resource has a wrong-typed replicas value so schema validation also fails.
				// Regression guard: a prior version of this code did `continue` after a defaulting
				// failure, silently hiding the schema error.
				resources: []*unstructured.Unstructured{invalidSchemaResource},
				crds:      []*extv1.CustomResourceDefinition{testCRDNoMatchingVersion, testCRD},
			},
			want: want{
				summary: ValidationSummary{Total: 1, Invalid: 1},
				perRes: []expect{{
					status: ValidationStatusInvalid,
					errors: []FieldValidationError{
						// Defaulting comes first because the implementation
						// records it before running schema/CEL checks.
						{Type: FieldErrorTypeDefaulting},
						{Type: FieldErrorTypeSchema, Field: "spec.replicas"},
					},
				}},
			},
		},
		"Empty": {
			reason: "Empty inputs should return a zero-total result without error.",
			args:   args{},
			want: want{
				summary: ValidationSummary{Total: 0},
				perRes:  nil,
			},
		},
		"MixedOrder": {
			reason: "Resources are returned in input order with their respective statuses.",
			args: args{
				resources: []*unstructured.Unstructured{validResource, invalidSchemaResource, missingSchemaResource},
				crds:      []*extv1.CustomResourceDefinition{testCRD},
			},
			want: want{
				summary: ValidationSummary{Total: 3, Valid: 1, Invalid: 1, MissingSchemas: 1},
				perRes: []expect{
					{status: ValidationStatusValid},
					{
						status: ValidationStatusInvalid,
						errors: []FieldValidationError{
							{Type: FieldErrorTypeSchema, Field: "spec.replicas"},
						},
					},
					{status: ValidationStatusMissingSchema},
				},
			},
		},
	}

	// Message and Value text comes from k8s validator libraries and changes
	// across versions; we assert on Type and Field instead.
	ignoreErrTextFields := cmpopts.IgnoreFields(FieldValidationError{}, "Message", "Value")

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			result, err := SchemaValidate(t.Context(), tc.args.resources, tc.args.crds)
			if (err != nil) != tc.want.wantErr {
				t.Fatalf("%s\nSchemaValidate() err = %v, wantErr = %v", tc.reason, err, tc.want.wantErr)
			}
			if tc.want.wantErr {
				return
			}
			if diff := cmp.Diff(tc.want.summary, result.Summary); diff != "" {
				t.Errorf("%s\nSummary mismatch (-want +got):\n%s", tc.reason, diff)
			}
			if got, want := len(result.Resources), len(tc.want.perRes); got != want {
				t.Fatalf("%s\nlen(Resources) = %d, want %d", tc.reason, got, want)
			}
			for i, r := range result.Resources {
				exp := tc.want.perRes[i]
				if r.Status != exp.status {
					t.Errorf("%s\nResources[%d].Status = %q, want %q", tc.reason, i, r.Status, exp.status)
				}
				// Treat a nil-valued exp.errors as "expect no errors". Use a
				// concrete empty slice instead of nil for the comparison so
				// cmp doesn't trip on slice-nilness when the validator
				// returns `nil`.
				wantErrs := exp.errors
				if wantErrs == nil {
					wantErrs = []FieldValidationError{}
				}
				gotErrs := r.Errors
				if gotErrs == nil {
					gotErrs = []FieldValidationError{}
				}
				if diff := cmp.Diff(wantErrs, gotErrs, ignoreErrTextFields); diff != "" {
					t.Errorf("%s\nResources[%d].Errors mismatch (-want +got):\n%s", tc.reason, i, diff)
				}
			}
		})
	}
}

func TestResultError(t *testing.T) {
	cases := map[string]struct {
		reason                string
		summary               ValidationSummary
		errorOnMissingSchemas bool
		wantErr               error
	}{
		"Clean": {
			reason:  "No invalid or missing schemas should return nil.",
			summary: ValidationSummary{Total: 1, Valid: 1},
		},
		"InvalidPresent": {
			reason:  "Invalid > 0 should return the invalid-resources error regardless of the flag.",
			summary: ValidationSummary{Total: 1, Invalid: 1},
			wantErr: errors.New("could not validate all resources"),
		},
		"MissingIgnored": {
			reason:  "MissingSchemas should be ignored when errorOnMissingSchemas is false.",
			summary: ValidationSummary{Total: 1, MissingSchemas: 1},
		},
		"MissingWithFlag": {
			reason:                "MissingSchemas with errorOnMissingSchemas should return the missing-schemas error.",
			summary:               ValidationSummary{Total: 1, MissingSchemas: 1},
			errorOnMissingSchemas: true,
			wantErr:               errors.New("could not validate all resources, schema(s) missing"),
		},
		"InvalidAndMissing": {
			reason:                "Invalid takes precedence over MissingSchemas when both are present.",
			summary:               ValidationSummary{Total: 2, Invalid: 1, MissingSchemas: 1},
			errorOnMissingSchemas: true,
			wantErr:               errors.New("could not validate all resources"),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := ResultError(&ValidationResult{Summary: tc.summary}, tc.errorOnMissingSchemas)
			if diff := cmp.Diff(tc.wantErr, got, test.EquateErrors()); diff != "" {
				t.Errorf("%s\nResultError(): -want err, +got err:\n%s", tc.reason, diff)
			}
		})
	}
}
