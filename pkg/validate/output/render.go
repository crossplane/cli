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

package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	pkgvalidate "github.com/crossplane/cli/v2/pkg/validate"
)

// Per-line markers used in text output. Lifted into named constants so
// the line formats below read as "<marker> <message>" rather than
// repeating the bracket literal at every call site.
const (
	markerSuccess = "[✓]" // resource passed validation
	markerWarning = "[!]" // operational issue (missing schema, defaulting failure)
	markerFail    = "[x]" // assertion failure (schema, CEL, or unknown-field error)
)

// Renderer writes a *ValidationResult to an io.Writer in some specific
// encoding. Implementations are obtained via RendererFor; the package
// exposes no other constructors so the supported set is fully closed.
type Renderer interface {
	Render(result *pkgvalidate.ValidationResult, w io.Writer, opts Options) error
}

// Options configures how a validation result is rendered.
type Options struct {
	// SkipSuccessResults suppresses per-resource success lines in text output.
	// It has no effect on JSON or YAML output, where success entries are
	// always part of the structured payload.
	SkipSuccessResults bool
}

// Format names a Renderer. It is a defined string type rather than
// a bare string so that call sites can use the symbolic constants below
// (FormatText, FormatJSON, FormatYAML) instead of
// embedding raw "text"/"json"/"yaml" literals, and so the compiler
// catches accidental cross-wiring of unrelated string flags.
type Format string

// Format values.
const (
	// FormatText renders results in human-readable text format with
	// [x], [!], [✓] markers.
	FormatText Format = "text"
	// FormatJSON renders results as JSON.
	FormatJSON Format = "json"
	// FormatYAML renders results as YAML.
	FormatYAML Format = "yaml"
)

// RendererFor returns the Renderer for the given format. The empty
// string is accepted as FormatText for ergonomics with
// zero-valued config; any other unrecognised value returns an error.
// This is the one and only boundary between a format identifier and
// the typed Renderer dependency that downstream code receives.
func RendererFor(format Format) (Renderer, error) {
	switch format {
	case FormatText, "":
		return textRenderer{}, nil
	case FormatJSON:
		return jsonRenderer{}, nil
	case FormatYAML:
		return yamlRenderer{}, nil
	default:
		return nil, errors.Errorf("unknown output format: %q", format)
	}
}

// jsonRenderer emits indented JSON with a trailing newline.
type jsonRenderer struct{}

// Render implements Renderer.
func (jsonRenderer) Render(result *pkgvalidate.ValidationResult, w io.Writer, _ Options) error {
	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return errors.Wrap(err, "cannot marshal validation result as JSON")
	}
	_, err = fmt.Fprintln(w, string(out))
	return errors.Wrap(err, "cannot write validation result")
}

// yamlRenderer emits sigs.k8s.io/yaml output.
type yamlRenderer struct{}

// Render implements Renderer.
func (yamlRenderer) Render(result *pkgvalidate.ValidationResult, w io.Writer, _ Options) error {
	out, err := yaml.Marshal(result)
	if err != nil {
		return errors.Wrap(err, "cannot marshal validation result as YAML")
	}
	_, err = fmt.Fprint(w, string(out))
	return errors.Wrap(err, "cannot write validation result")
}

// textRenderer emits the human-readable text format that the validate CLI
// has historically produced. Each resource emits zero or more lines
// depending on its status and accumulated errors:
//
//   - MissingSchema: one [!] "could not find CRD/XRD for ..." line.
//   - Valid:        one [✓] "validated successfully" line, suppressed when
//     opts.SkipSuccessResults is set.
//   - Invalid or DefaultingFailed: one line per FieldValidationError with
//     the prefix chosen by the error's Type — [!] for defaulting (a
//     warning), [x] for schema/CEL/unknown-field failures.
//
// A trailing summary line lists totals.
type textRenderer struct{}

// Render implements Renderer. The outer switch over ValidationStatus
// dispatches per-resource emission; the per-error switch over
// FieldErrorType lives in textErrorLine (a different enumeration
// covering a different concern, so it earns its own helper).
func (textRenderer) Render(result *pkgvalidate.ValidationResult, w io.Writer, opts Options) error {
	for _, r := range result.Resources {
		gvk := fmt.Sprintf("%s, Kind=%s", r.APIVersion, r.Kind)
		var line string
		switch r.Status {
		case pkgvalidate.ValidationStatusMissingSchema:
			line = fmt.Sprintf(markerWarning+" could not find CRD/XRD for: %s\n", gvk)
		case pkgvalidate.ValidationStatusValid:
			if opts.SkipSuccessResults {
				continue
			}
			line = fmt.Sprintf(markerSuccess+" %s, %s validated successfully\n", gvk, r.Name)
		case pkgvalidate.ValidationStatusInvalid, pkgvalidate.ValidationStatusDefaultingFailed:
			var sb strings.Builder
			for _, e := range r.Errors {
				sb.WriteString(textErrorLine(gvk, r.Name, e))
			}
			line = sb.String()
		}
		if _, err := fmt.Fprint(w, line); err != nil {
			return errors.Wrap(err, "cannot write validation result")
		}
	}
	_, err := fmt.Fprintf(w, "Total %d resources: %d missing schemas, %d success cases, %d failure cases\n",
		result.Summary.Total, result.Summary.MissingSchemas, result.Summary.Valid, result.Summary.Invalid)
	return errors.Wrap(err, "cannot write validation result")
}

// textErrorLine returns the rendered text for a single
// FieldValidationError. Defaulting failures use the warning marker;
// schema, CEL, and unknown-field errors use the error marker.
func textErrorLine(gvk, name string, e pkgvalidate.FieldValidationError) string {
	switch e.Type {
	case pkgvalidate.FieldErrorTypeDefaulting:
		return fmt.Sprintf(markerWarning+" failed to apply defaults for %s, %s: %s\n", gvk, name, e.Message)
	case pkgvalidate.FieldErrorTypeCEL:
		return fmt.Sprintf(markerFail+" CEL validation error %s, %s : %s\n", gvk, name, e.Message)
	case pkgvalidate.FieldErrorTypeSchema, pkgvalidate.FieldErrorTypeUnknownField:
		return fmt.Sprintf(markerFail+" schema validation error %s, %s : %s\n", gvk, name, e.Message)
	default:
		// Breadcrumb for an unhandled FieldErrorType added without updating this switch.
		return fmt.Sprintf(markerFail+" validation error %s, %s : %s\n", gvk, name, e.Message)
	}
}
