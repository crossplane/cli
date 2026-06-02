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
	"encoding/json"
	"fmt"
	"io"

	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	pkgvalidate "github.com/crossplane/cli/v2/cmd/crossplane/pkg/validate"
)

const (
	errCannotMarshalJSON = "cannot marshal validation result as JSON"
	errCannotMarshalYAML = "cannot marshal validation result as YAML"
	errUnknownFormat     = "unknown output format"
)

// Renderer writes a *ValidationResult to an io.Writer in some specific
// encoding. New formats are added by implementing Renderer and registering
// a value under an OutputFormat in renderers below.
type Renderer interface {
	Render(result *pkgvalidate.ValidationResult, w io.Writer, opts RenderOptions) error
}

// OutputFormat names a Renderer.
type OutputFormat string

// OutputFormat values.
const (
	// OutputFormatText renders results in human-readable text format with
	// [x], [!], [✓] markers.
	OutputFormatText OutputFormat = "text"
	// OutputFormatJSON renders results as JSON.
	OutputFormatJSON OutputFormat = "json"
	// OutputFormatYAML renders results as YAML.
	OutputFormatYAML OutputFormat = "yaml"
)

// RenderOptions configures how a validation result is rendered.
type RenderOptions struct {
	// SkipSuccessResults suppresses per-resource success lines in text output.
	// It has no effect on JSON or YAML output, where success entries are
	// always part of the structured payload.
	SkipSuccessResults bool
}

// renderers is the polymorphic registry: each known OutputFormat names a
// Renderer value. Adding a new format means adding a type that implements
// Renderer and one entry below — nothing else in this package needs to
// change.
var renderers = map[OutputFormat]Renderer{
	OutputFormatText: textRenderer{},
	OutputFormatJSON: jsonRenderer{},
	OutputFormatYAML: yamlRenderer{},
}

// Renderer returns the Renderer registered for f, or an error if f is not a
// known format. The empty string is treated as text, so callers passing the
// zero value (e.g. struct defaults) get sensible behaviour.
func (f OutputFormat) Renderer() (Renderer, error) {
	if f == "" {
		f = OutputFormatText
	}
	r, ok := renderers[f]
	if !ok {
		return nil, errors.Errorf("%s: %q", errUnknownFormat, f)
	}
	return r, nil
}

// Render dispatches to the Renderer registered for f.
func (f OutputFormat) Render(result *pkgvalidate.ValidationResult, w io.Writer, opts RenderOptions) error {
	r, err := f.Renderer()
	if err != nil {
		return err
	}
	return r.Render(result, w, opts)
}

// RenderValidationResult writes the validation result to w in the requested
// format. It is a free-function shim around OutputFormat.Render kept for
// callers that prefer the procedural style.
func RenderValidationResult(result *pkgvalidate.ValidationResult, format OutputFormat, w io.Writer, opts RenderOptions) error {
	return format.Render(result, w, opts)
}

// jsonRenderer emits indented JSON with a trailing newline.
type jsonRenderer struct{}

// Render implements Renderer.
func (jsonRenderer) Render(result *pkgvalidate.ValidationResult, w io.Writer, _ RenderOptions) error {
	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return errors.Wrap(err, errCannotMarshalJSON)
	}
	_, err = fmt.Fprintln(w, string(out))
	return err
}

// yamlRenderer emits sigs.k8s.io/yaml output.
type yamlRenderer struct{}

// Render implements Renderer.
func (yamlRenderer) Render(result *pkgvalidate.ValidationResult, w io.Writer, _ RenderOptions) error {
	out, err := yaml.Marshal(result)
	if err != nil {
		return errors.Wrap(err, errCannotMarshalYAML)
	}
	_, err = fmt.Fprint(w, string(out))
	return err
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

// Render implements Renderer.
func (textRenderer) Render(result *pkgvalidate.ValidationResult, w io.Writer, opts RenderOptions) error {
	for _, r := range result.Resources {
		gvk := fmt.Sprintf("%s, Kind=%s", r.APIVersion, r.Kind)
		switch r.Status {
		case pkgvalidate.ValidationStatusMissingSchema:
			if _, err := fmt.Fprintf(w, "[!] could not find CRD/XRD for: %s\n", gvk); err != nil {
				return err
			}
		case pkgvalidate.ValidationStatusValid:
			if opts.SkipSuccessResults {
				continue
			}
			if _, err := fmt.Fprintf(w, "[✓] %s, %s validated successfully\n", gvk, r.Name); err != nil {
				return err
			}
		case pkgvalidate.ValidationStatusInvalid, pkgvalidate.ValidationStatusDefaultingFailed:
			for _, e := range r.Errors {
				if err := writeTextErrorLine(w, gvk, r.Name, e); err != nil {
					return err
				}
			}
		}
	}
	_, err := fmt.Fprintf(w, "Total %d resources: %d missing schemas, %d success cases, %d failure cases\n",
		result.Summary.Total, result.Summary.MissingSchemas, result.Summary.Valid, result.Summary.Invalid)
	return err
}

// writeTextErrorLine emits a single per-error line. Defaulting failures use
// the [!] warning prefix; schema, CEL, and unknown-field errors use [x].
// Kept private to the textRenderer because the per-error format is a
// detail of the text output, not part of the package's public surface.
func writeTextErrorLine(w io.Writer, gvk, name string, e pkgvalidate.FieldValidationError) error {
	switch e.Type {
	case pkgvalidate.FieldErrorTypeDefaulting:
		_, err := fmt.Fprintf(w, "[!] failed to apply defaults for %s, %s: %s\n", gvk, name, e.Message)
		return err
	case pkgvalidate.FieldErrorTypeCEL:
		_, err := fmt.Fprintf(w, "[x] CEL validation error %s, %s : %s\n", gvk, name, e.Message)
		return err
	default:
		_, err := fmt.Fprintf(w, "[x] schema validation error %s, %s : %s\n", gvk, name, e.Message)
		return err
	}
}
