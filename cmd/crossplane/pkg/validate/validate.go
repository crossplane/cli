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
	"context"
	"fmt"

	ext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel"
	structuraldefaulting "k8s.io/apiextensions-apiserver/pkg/apiserver/schema/defaulting"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	runtimeschema "k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	celconfig "k8s.io/apiserver/pkg/apis/cel"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xcrd"
)

// SchemaValidate performs schema validation and returns structured results.
//
// This is the processing-only API: no I/O is performed, allowing programmatic
// consumers to inspect the result directly or hand it to a renderer. The
// returned error is non-nil only for setup failures (for example, a CRD that
// cannot be converted or compiled); per-resource validation failures are
// reported via ResourceValidationResult.Status and .Errors, not via the error.
func SchemaValidate(ctx context.Context, resources []*unstructured.Unstructured, crds []*extv1.CustomResourceDefinition) (*ValidationResult, error) { //nolint:gocognit // validation has many branches
	schemaValidators, structurals, err := newValidatorsAndStructurals(crds)
	if err != nil {
		return nil, errors.Wrap(err, "cannot create schema validators")
	}

	result := &ValidationResult{
		Resources: make([]ResourceValidationResult, 0, len(resources)),
	}

	for _, r := range resources {
		gvk := r.GetObjectKind().GroupVersionKind()
		rvr := ResourceValidationResult{
			APIVersion: gvk.GroupVersion().String(),
			Kind:       gvk.Kind,
			Name:       getResourceName(r),
			Namespace:  r.GetNamespace(),
		}

		sv, ok := schemaValidators[gvk]
		s := structurals[gvk]

		if !ok {
			rvr.Status = ValidationStatusMissingSchema
			result.Resources = append(result.Resources, rvr)
			continue
		}

		// A defaulting failure is recorded as a warning-class error and does
		// not abort validation: schema and CEL checks still run on the
		// (un-defaulted) resource so the user sees every problem at once,
		// matching the historical behavior of the SchemaValidation entry
		// point this package replaced.
		if err := applyDefaults(r, gvk, crds); err != nil {
			rvr.Errors = append(rvr.Errors, FieldValidationError{
				Type:    FieldErrorTypeDefaulting,
				Message: err.Error(),
			})
		}

		for _, v := range sv {
			for _, e := range validation.ValidateCustomResource(nil, r, *v) {
				rvr.Errors = append(rvr.Errors, fieldErrorToFieldValidationError(e, FieldErrorTypeSchema))
			}
			for _, e := range validateUnknownFields(r.UnstructuredContent(), s) {
				rvr.Errors = append(rvr.Errors, fieldErrorToFieldValidationError(e, FieldErrorTypeUnknownField))
			}

			celValidator := cel.NewValidator(s, true, celconfig.PerCallLimit)
			celErrs, _ := celValidator.Validate(ctx, nil, s, r.Object, nil, celconfig.PerCallLimit)
			for _, e := range celErrs {
				rvr.Errors = append(rvr.Errors, fieldErrorToFieldValidationError(e, FieldErrorTypeCEL))
			}
		}

		rvr.Status = statusFromErrors(rvr.Errors)
		result.Resources = append(result.Resources, rvr)
	}

	result.Summary = computeSummary(result.Resources)
	return result, nil
}

// ResultError returns an error summarizing the outcome of validation, or nil
// if validation succeeded. This preserves the historical error semantics of
// SchemaValidation for programmatic consumers who want a boolean pass/fail.
func ResultError(result *ValidationResult, errorOnMissingSchemas bool) error {
	if result.Summary.Invalid > 0 {
		return errors.New("could not validate all resources")
	}
	if errorOnMissingSchemas && result.Summary.MissingSchemas > 0 {
		return errors.New("could not validate all resources, schema(s) missing")
	}
	return nil
}

func newValidatorsAndStructurals(crds []*extv1.CustomResourceDefinition) (map[runtimeschema.GroupVersionKind][]*validation.SchemaValidator, map[runtimeschema.GroupVersionKind]*schema.Structural, error) {
	validators := map[runtimeschema.GroupVersionKind][]*validation.SchemaValidator{}
	structurals := map[runtimeschema.GroupVersionKind]*schema.Structural{}

	for i := range crds {
		internal := &ext.CustomResourceDefinition{}
		if err := extv1.Convert_v1_CustomResourceDefinition_To_apiextensions_CustomResourceDefinition(crds[i], internal, nil); err != nil {
			return nil, nil, err
		}

		// Top-level and per-version schemas are mutually exclusive.
		for _, ver := range internal.Spec.Versions {
			var (
				sv  validation.SchemaValidator
				err error
			)

			gvk := runtimeschema.GroupVersionKind{
				Group:   internal.Spec.Group,
				Version: ver.Name,
				Kind:    internal.Spec.Names.Kind,
			}

			var s *ext.JSONSchemaProps

			switch {
			case internal.Spec.Validation != nil:
				s = internal.Spec.Validation.OpenAPIV3Schema
			case ver.Schema != nil && ver.Schema.OpenAPIV3Schema != nil:
				s = ver.Schema.OpenAPIV3Schema
			default:
				// TODO log a warning here, it should never happen
				continue
			}

			sv, _, err = validation.NewSchemaValidator(s)
			if err != nil {
				return nil, nil, err
			}

			validators[gvk] = append(validators[gvk], &sv)

			structural, err := schema.NewStructural(s)
			if err != nil {
				return nil, nil, err
			}

			structurals[gvk] = structural
		}
	}

	return validators, structurals, nil
}

// statusFromErrors picks the right ValidationStatus given the errors a
// resource accumulated during validation. A real schema/CEL/unknown-field
// error trumps a defaulting failure: the schema error is the actionable
// problem, and DefaultingFailed is reserved for the case where defaulting
// was the only thing that went wrong.
func statusFromErrors(errs []FieldValidationError) ValidationStatus {
	if len(errs) == 0 {
		return ValidationStatusValid
	}
	onlyDefaulting := true
	for _, e := range errs {
		if e.Type != FieldErrorTypeDefaulting {
			onlyDefaulting = false
			break
		}
	}
	if onlyDefaulting {
		return ValidationStatusDefaultingFailed
	}
	return ValidationStatusInvalid
}

// fieldErrorToFieldValidationError converts a k8s field.Error into our structured type.
func fieldErrorToFieldValidationError(e *field.Error, errType string) FieldValidationError {
	out := FieldValidationError{
		Type:    errType,
		Field:   e.Field,
		Message: e.Error(),
	}
	if e.BadValue != nil {
		out.Value = e.BadValue
	}
	return out
}

// computeSummary calculates aggregate counts from per-resource results.
//
// DefaultingFailed counts toward Valid: a defaulting failure is a warning,
// not a failure, and ResultError must not surface it as an error. This
// mirrors the historical SchemaValidation semantics where defaulting errors
// produced a [!] line but did not increment the failure counter.
func computeSummary(results []ResourceValidationResult) ValidationSummary {
	s := ValidationSummary{Total: len(results)}
	for _, r := range results {
		switch r.Status {
		case ValidationStatusValid, ValidationStatusDefaultingFailed:
			s.Valid++
		case ValidationStatusInvalid:
			s.Invalid++
		case ValidationStatusMissingSchema:
			s.MissingSchemas++
		}
	}
	return s
}

func getResourceName(r *unstructured.Unstructured) string {
	if r.GetName() != "" {
		return r.GetName()
	}

	// fallback to composition resource name
	return r.GetAnnotations()[xcrd.AnnotationKeyCompositionResourceName]
}

// applyDefaults applies default values from the CRD schema to the unstructured
// resource.
func applyDefaults(resource *unstructured.Unstructured, gvk runtimeschema.GroupVersionKind, crds []*extv1.CustomResourceDefinition) error {
	var matchingCRD *extv1.CustomResourceDefinition

	for _, crd := range crds {
		if crd.Spec.Group == gvk.Group && crd.Spec.Names.Kind == gvk.Kind {
			matchingCRD = crd
			break
		}
	}

	if matchingCRD == nil {
		// no CRD found for applying defaults, skip defaulting
		return nil
	}

	var schemaProps *extv1.JSONSchemaProps

	for _, v := range matchingCRD.Spec.Versions {
		if v.Name == gvk.Version {
			if v.Schema != nil && v.Schema.OpenAPIV3Schema != nil {
				schemaProps = v.Schema.OpenAPIV3Schema
			}

			break
		}
	}

	if schemaProps == nil {
		return fmt.Errorf("no schema found for version %s in CRD %s", gvk.Version, matchingCRD.Name)
	}

	var apiExtSchema ext.JSONSchemaProps

	err := extv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(schemaProps, &apiExtSchema, nil)
	if err != nil {
		return fmt.Errorf("failed to convert schema: %w", err)
	}

	structural, err := schema.NewStructural(&apiExtSchema)
	if err != nil {
		return fmt.Errorf("failed to create structural schema: %w", err)
	}

	obj := resource.UnstructuredContent()
	structuraldefaulting.Default(obj, structural)

	return nil
}
