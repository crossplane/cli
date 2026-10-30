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

package xrdlint

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

// Format names a Renderer.
type Format string

// Formats.
const (
	// FormatText renders findings grouped by file and XRD, for people.
	FormatText Format = "text"
	// FormatJSON renders the Result as JSON.
	FormatJSON Format = "json"
	// FormatYAML renders the Result as YAML.
	FormatYAML Format = "yaml"
)

// A Renderer writes a Result.
type Renderer interface {
	Render(res *Result, w io.Writer) error
}

// RendererFor returns the Renderer for a format. The empty format is text.
func RendererFor(f Format) (Renderer, error) {
	switch f {
	case FormatText, "":
		return textRenderer{}, nil
	case FormatJSON:
		return jsonRenderer{}, nil
	case FormatYAML:
		return yamlRenderer{}, nil
	default:
		return nil, errors.Errorf("unknown output format: %q", f)
	}
}

// structured returns res with a non-nil findings list, so that an empty
// result renders as [] rather than null.
func structured(res *Result) *Result {
	out := *res
	if out.Findings == nil {
		out.Findings = []Finding{}
	}
	return &out
}

type jsonRenderer struct{}

func (jsonRenderer) Render(res *Result, w io.Writer) error {
	out, err := json.MarshalIndent(structured(res), "", "  ")
	if err != nil {
		return errors.Wrap(err, "cannot marshal lint result as JSON")
	}
	_, err = fmt.Fprintln(w, string(out))
	return errors.Wrap(err, "cannot write lint result")
}

type yamlRenderer struct{}

func (yamlRenderer) Render(res *Result, w io.Writer) error {
	out, err := yaml.Marshal(structured(res))
	if err != nil {
		return errors.Wrap(err, "cannot marshal lint result as YAML")
	}
	_, err = w.Write(out)
	return errors.Wrap(err, "cannot write lint result")
}

// textRenderer groups findings by file, then by XRD and version, in the order
// they first appear, and aligns each group's columns.
type textRenderer struct{}

func (textRenderer) Render(res *Result, w io.Writer) error {
	b := &strings.Builder{}
	var tw *tabwriter.Writer
	file, group := "", ""
	first := true

	flush := func() {
		if tw != nil {
			_ = tw.Flush()
		}
	}

	for _, f := range res.Findings {
		if first || f.File != file {
			flush()
			if !first {
				b.WriteString("\n")
			}
			fmt.Fprintln(b, f.File)
			file, group, first = f.File, "", false
		}
		g := strings.TrimSpace(f.XRD + " " + f.Version)
		if tw == nil || g != group {
			flush()
			fmt.Fprintf(b, "  %s\n", g)
			group = g
			tw = tabwriter.NewWriter(b, 0, 0, 2, ' ', 0)
		}
		msg := f.Message
		if f.Path != "" {
			msg = f.Path + ": " + msg
		}
		_, _ = fmt.Fprintf(tw, "    %d:%d\t%s\t%s\t%s\n", f.Line, f.Column, f.Severity, msg, f.Rule)
	}
	flush()
	if len(res.Findings) > 0 {
		b.WriteString("\n")
	}
	fmt.Fprintf(b, "%s, %s\n", plural(res.Summary.Errors, "error"), plural(res.Summary.Warnings, "warning"))

	_, err := io.WriteString(w, b.String())
	return errors.Wrap(err, "cannot write lint result")
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
