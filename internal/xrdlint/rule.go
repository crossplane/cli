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

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

// Meta describes a rule.
type Meta struct {
	// ID names the rule, as <prefix>/<name> in lower-case kebab case, for
	// example xrd/no-bools.
	ID string

	// Description is a one-line summary of what the rule checks.
	Description string

	// DocsURL links to the rule's documentation. It is copied into each
	// finding.
	DocsURL string

	// DefaultSeverity applies when no config overrides it.
	DefaultSeverity Severity

	// Options is the schema the rule's options are validated against, or nil
	// if the rule takes none.
	Options *extv1.JSONSchemaProps
}

// A SchemaRule checks one XRD at a time.
type SchemaRule interface {
	// Meta describes the rule.
	Meta() Meta

	// Check reports findings on the XRD through ctx. It returns an error only
	// if the rule cannot run; the runner then reports lint/rule-error, which
	// fails the run whatever the rule's severity.
	Check(ctx *Context, xrd *XRD) error
}

// A ReportOption modifies a finding as it is reported.
type ReportOption func(f *Finding)

// WithFix attaches a suggested fix, as text, to a finding.
func WithFix(fix string) ReportOption {
	return func(f *Finding) {
		f.Fix = fix
	}
}

// reported is a finding together with the YAML nodes used to suppress it.
type reported struct {
	finding Finding
	anchors anchors
}

// Context is passed to a rule while it checks one XRD.
type Context struct {
	meta     Meta
	severity Severity
	options  []byte
	xrd      *XRD
	reported []reported
}

// Options decodes the rule's configured options into the supplied value. It
// leaves the value unchanged if no options are configured, so a rule sets its
// defaults before calling it.
func (c *Context) Options(into any) error {
	if len(c.options) == 0 {
		return nil
	}
	return errors.Wrapf(json.Unmarshal(c.options, into), "cannot decode options for rule %q", c.meta.ID)
}

// Report records a finding on the supplied property.
func (c *Context) Report(p Property, message string, opts ...ReportOption) {
	line, col := p.position()
	f := Finding{
		Rule:     c.meta.ID,
		Severity: c.severity,
		File:     c.xrd.File,
		Line:     line,
		Column:   col,
		XRD:      c.xrd.Name,
		Version:  p.Version,
		Path:     p.Path,
		Message:  message,
		DocsURL:  c.meta.DocsURL,
	}
	for _, o := range opts {
		o(&f)
	}
	c.reported = append(c.reported, reported{finding: f, anchors: p.anchors})
}
