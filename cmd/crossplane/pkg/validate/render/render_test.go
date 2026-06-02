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
	"fmt"
	"strings"
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

// defaultingFixture covers the DefaultingFailed status (warning-only) and an
// Invalid resource that has both a defaulting error and a schema error,
// exercising the per-error prefix selection in renderText.
func defaultingFixture() *pkgvalidate.ValidationResult {
	return &pkgvalidate.ValidationResult{
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

// renderTextLines runs Render with the given format and options and returns
// the non-empty lines of the resulting output. It centralises the call so
// individual cases can focus on assertions.
func renderTextLines(t *testing.T, in *pkgvalidate.ValidationResult, format OutputFormat, opts RenderOptions) []string {
	t.Helper()
	var buf bytes.Buffer
	if err := format.Render(in, &buf, opts); err != nil {
		t.Fatalf("Render() unexpected error: %v", err)
	}
	raw := strings.TrimRight(buf.String(), "\n")
	if raw == "" {
		return nil
	}
	return strings.Split(raw, "\n")
}

// summaryLine builds the trailing summary line for the given result.
func summaryLine(r *pkgvalidate.ValidationResult) string {
	return fmt.Sprintf("Total %d resources: %d missing schemas, %d success cases, %d failure cases",
		r.Summary.Total, r.Summary.MissingSchemas, r.Summary.Valid, r.Summary.Invalid)
}

func TestRenderValidationResult_Text(t *testing.T) {
	cases := map[string]struct {
		in           *pkgvalidate.ValidationResult
		format       OutputFormat
		opts         RenderOptions
		wantLineSubs []string // every entry must appear as a substring of some output line, in order
	}{
		"WithSuccess": {
			in:     fixture(),
			format: OutputFormatText,
			wantLineSubs: []string{
				"[✓] test.org/v1alpha1, Kind=Test, ok",
				"[x] schema validation error test.org/v1alpha1, Kind=Test, bad",
				"[!] could not find CRD/XRD for: other.org/v1, Kind=Unknown",
				summaryLine(fixture()),
			},
		},
		"SkipSuccess": {
			in:     fixture(),
			format: OutputFormatText,
			opts:   RenderOptions{SkipSuccessResults: true},
			wantLineSubs: []string{
				"[x] schema validation error test.org/v1alpha1, Kind=Test, bad",
				"[!] could not find CRD/XRD for: other.org/v1, Kind=Unknown",
				summaryLine(fixture()),
			},
		},
		"EmptyFormatActsAsText": {
			in:     fixture(),
			format: OutputFormat(""),
			wantLineSubs: []string{
				"[✓] test.org/v1alpha1, Kind=Test, ok",
				"[x] schema validation error test.org/v1alpha1, Kind=Test, bad",
				"[!] could not find CRD/XRD for: other.org/v1, Kind=Unknown",
				summaryLine(fixture()),
			},
		},
		"DefaultingMixed": {
			in:     defaultingFixture(),
			format: OutputFormatText,
			wantLineSubs: []string{
				"[!] failed to apply defaults for test.org/v1alpha1, Kind=Test, warn-only",
				"[!] failed to apply defaults for test.org/v1alpha1, Kind=Test, mixed",
				"[x] schema validation error test.org/v1alpha1, Kind=Test, mixed",
				summaryLine(defaultingFixture()),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			lines := renderTextLines(t, tc.in, tc.format, tc.opts)
			if len(lines) != len(tc.wantLineSubs) {
				t.Fatalf("line count = %d, want %d\n--- got ---\n%s", len(lines), len(tc.wantLineSubs), strings.Join(lines, "\n"))
			}
			for i, sub := range tc.wantLineSubs {
				if !strings.Contains(lines[i], sub) {
					t.Errorf("line %d: expected substring %q, got %q", i, sub, lines[i])
				}
			}
		})
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
