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

package render

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"
	"sigs.k8s.io/yaml"

	pkgvalidate "github.com/crossplane/cli/v2/cmd/crossplane/pkg/validate"
)

// fixture returns a ValidationResult covering a valid, an invalid, and a
// missing-schema resource in that order.
func fixture() *pkgvalidate.ValidationResult {
	return &pkgvalidate.ValidationResult{
		Summary: pkgvalidate.ValidationSummary{Total: 3, Valid: 1, Invalid: 1, MissingSchemas: 1},
		Resources: []pkgvalidate.ResourceValidationResult{
			{
				APIVersion: "test.org/v1alpha1", Kind: "Test", Name: "ok",
				Status: pkgvalidate.ValidationStatusValid,
			},
			{
				APIVersion: "test.org/v1alpha1", Kind: "Test", Name: "bad",
				Status: pkgvalidate.ValidationStatusInvalid,
				Errors: []pkgvalidate.FieldValidationError{
					{
						Type:    pkgvalidate.FieldErrorTypeSchema,
						Field:   "spec.replicas",
						Message: `spec.replicas: Invalid value: "string": spec.replicas in body must be of type integer: "string"`,
						Value:   "string",
					},
				},
			},
			{
				APIVersion: "other.org/v1", Kind: "Unknown", Name: "missing",
				Status: pkgvalidate.ValidationStatusMissingSchema,
			},
		},
	}
}

const expectedTextWithSuccess = `[✓] test.org/v1alpha1, Kind=Test, ok validated successfully
[x] schema validation error test.org/v1alpha1, Kind=Test, bad : spec.replicas: Invalid value: "string": spec.replicas in body must be of type integer: "string"
[!] could not find CRD/XRD for: other.org/v1, Kind=Unknown
Total 3 resources: 1 missing schemas, 1 success cases, 1 failure cases
`

const expectedTextSkipSuccess = `[x] schema validation error test.org/v1alpha1, Kind=Test, bad : spec.replicas: Invalid value: "string": spec.replicas in body must be of type integer: "string"
[!] could not find CRD/XRD for: other.org/v1, Kind=Unknown
Total 3 resources: 1 missing schemas, 1 success cases, 1 failure cases
`

func TestRenderValidationResult_Text(t *testing.T) {
	cases := map[string]struct {
		format   OutputFormat
		opts     RenderOptions
		expected string
	}{
		"TextWithSuccess": {format: OutputFormatText, expected: expectedTextWithSuccess},
		"TextSkipSuccess": {format: OutputFormatText, opts: RenderOptions{SkipSuccessResults: true}, expected: expectedTextSkipSuccess},
		"TextEmptyFormat": {format: OutputFormat(""), expected: expectedTextWithSuccess},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := RenderValidationResult(fixture(), tc.format, &buf, tc.opts); err != nil {
				t.Fatalf("RenderValidationResult() unexpected error: %v", err)
			}
			if got := buf.String(); got != tc.expected {
				t.Errorf("text output mismatch\n--- want ---\n%s\n--- got ---\n%s", tc.expected, got)
			}
		})
	}
}

// defaultingFixture covers the DefaultingFailed status (warning-only) and an
// Invalid resource that has both a defaulting error and a schema error,
// exercising the per-error prefix selection in renderText.
func defaultingFixture() *pkgvalidate.ValidationResult {
	return &pkgvalidate.ValidationResult{
		// Summary: defaulting-only resource counts as Valid; the mixed-error
		// resource counts as Invalid.
		Summary: pkgvalidate.ValidationSummary{Total: 2, Valid: 1, Invalid: 1},
		Resources: []pkgvalidate.ResourceValidationResult{
			{
				APIVersion: "test.org/v1alpha1", Kind: "Test", Name: "warn-only",
				Status: pkgvalidate.ValidationStatusDefaultingFailed,
				Errors: []pkgvalidate.FieldValidationError{{
					Type:    pkgvalidate.FieldErrorTypeDefaulting,
					Message: "no schema found for version v1alpha1 in CRD test-other-version",
				}},
			},
			{
				APIVersion: "test.org/v1alpha1", Kind: "Test", Name: "mixed",
				Status: pkgvalidate.ValidationStatusInvalid,
				Errors: []pkgvalidate.FieldValidationError{
					{
						Type:    pkgvalidate.FieldErrorTypeDefaulting,
						Message: "no schema found for version v1alpha1 in CRD test-other-version",
					},
					{
						Type:    pkgvalidate.FieldErrorTypeSchema,
						Field:   "spec.replicas",
						Message: `spec.replicas: Invalid value: "string": spec.replicas in body must be of type integer: "string"`,
						Value:   "string",
					},
				},
			},
		},
	}
}

const expectedTextDefaulting = `[!] failed to apply defaults for test.org/v1alpha1, Kind=Test, warn-only: no schema found for version v1alpha1 in CRD test-other-version
[!] failed to apply defaults for test.org/v1alpha1, Kind=Test, mixed: no schema found for version v1alpha1 in CRD test-other-version
[x] schema validation error test.org/v1alpha1, Kind=Test, mixed : spec.replicas: Invalid value: "string": spec.replicas in body must be of type integer: "string"
Total 2 resources: 0 missing schemas, 1 success cases, 1 failure cases
`

func TestRenderValidationResult_TextDefaulting(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderValidationResult(defaultingFixture(), OutputFormatText, &buf, RenderOptions{}); err != nil {
		t.Fatalf("RenderValidationResult() unexpected error: %v", err)
	}
	if got := buf.String(); got != expectedTextDefaulting {
		t.Errorf("text output mismatch\n--- want ---\n%s\n--- got ---\n%s", expectedTextDefaulting, got)
	}
}

func TestRenderValidationResult_JSON(t *testing.T) {
	in := fixture()
	var buf bytes.Buffer
	if err := RenderValidationResult(in, OutputFormatJSON, &buf, RenderOptions{}); err != nil {
		t.Fatalf("RenderValidationResult(JSON) err = %v", err)
	}
	var got pkgvalidate.ValidationResult
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("json.Unmarshal() err = %v; output was:\n%s", err, buf.String())
	}
	if diff := cmp.Diff(*in, got); diff != "" {
		t.Errorf("JSON round-trip mismatch (-want +got):\n%s", diff)
	}
}

func TestRenderValidationResult_YAML(t *testing.T) {
	in := fixture()
	var buf bytes.Buffer
	if err := RenderValidationResult(in, OutputFormatYAML, &buf, RenderOptions{}); err != nil {
		t.Fatalf("RenderValidationResult(YAML) err = %v", err)
	}
	var got pkgvalidate.ValidationResult
	if err := yaml.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("yaml.Unmarshal() err = %v; output was:\n%s", err, buf.String())
	}
	if diff := cmp.Diff(*in, got); diff != "" {
		t.Errorf("YAML round-trip mismatch (-want +got):\n%s", diff)
	}
}

func TestRenderValidationResult_Unknown(t *testing.T) {
	var buf bytes.Buffer
	err := RenderValidationResult(fixture(), OutputFormat("bogus"), &buf, RenderOptions{})
	if err == nil {
		t.Fatal("RenderValidationResult(bogus) = nil; want non-nil error")
	}
	if buf.Len() != 0 {
		t.Errorf("Unknown format wrote %d bytes; want 0 (content: %q)", buf.Len(), buf.String())
	}
}
