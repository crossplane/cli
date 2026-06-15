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

// ValidationResult contains the complete results of schema validation.
type ValidationResult struct {
	Summary   ValidationSummary          `json:"summary"`
	Resources []ResourceValidationResult `json:"resources"`
}

// ValidationSummary provides aggregate counts across all validated resources.
type ValidationSummary struct {
	Total          int `json:"total"`
	Valid          int `json:"valid"`
	Invalid        int `json:"invalid"`
	MissingSchemas int `json:"missingSchemas"`
}

// ResourceValidationResult contains validation results for a single resource.
type ResourceValidationResult struct {
	APIVersion string                 `json:"apiVersion"`
	Kind       string                 `json:"kind"`
	Name       string                 `json:"name"`
	Namespace  string                 `json:"namespace,omitempty"`
	Status     ValidationStatus       `json:"status"`
	Errors     []FieldValidationError `json:"errors,omitempty"`
}

// ValidationStatus indicates the validation result for a resource.
type ValidationStatus string

// ValidationStatus values.
const (
	// ValidationStatusValid indicates the resource passed all validation checks.
	ValidationStatusValid ValidationStatus = "valid"
	// ValidationStatusInvalid indicates the resource failed one or more validation checks.
	ValidationStatusInvalid ValidationStatus = "invalid"
	// ValidationStatusMissingSchema indicates no schema (CRD/XRD) was found for the resource.
	ValidationStatusMissingSchema ValidationStatus = "missingSchema"
	// ValidationStatusDefaultingFailed indicates defaults could not be applied to the resource.
	ValidationStatusDefaultingFailed ValidationStatus = "defaultingFailed"
)

// FieldValidationError represents a single field-level validation error.
type FieldValidationError struct {
	// Type categorizes the error.
	Type FieldErrorType `json:"type"`
	// Field is the path to the invalid field (e.g. "spec.forProvider.region").
	Field string `json:"field,omitempty"`
	// Message is a human-readable description of the error.
	Message string `json:"message"`
	// Value is the invalid value, if applicable.
	Value any `json:"value,omitempty"`
}

// FieldErrorType categorizes the kind of validation error a
// FieldValidationError describes. The supported set is closed; producers
// and consumers should use the named constants below.
type FieldErrorType string

// FieldErrorType values.
const (
	// FieldErrorTypeSchema indicates a schema validation error from OpenAPI validation.
	FieldErrorTypeSchema FieldErrorType = "schema"
	// FieldErrorTypeCEL indicates a CEL rule validation error.
	FieldErrorTypeCEL FieldErrorType = "cel"
	// FieldErrorTypeUnknownField indicates an unknown field was present in the resource.
	FieldErrorTypeUnknownField FieldErrorType = "unknownField"
	// FieldErrorTypeDefaulting indicates defaults could not be applied to the resource.
	FieldErrorTypeDefaulting FieldErrorType = "defaulting"
)
