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

package rules

import (
	"fmt"
	"strings"

	"github.com/crossplane/cli/v2/internal/xrdlint"
)

// StatusFieldsOptional reports required fields under status. Status is written
// in pieces, by Crossplane and by functions, and a required field can cause
// those writes to be rejected.
type StatusFieldsOptional struct{}

// Meta implements xrdlint.SchemaRule.
func (StatusFieldsOptional) Meta() xrdlint.Meta {
	return xrdlint.Meta{
		ID:              "xrd/status-fields-optional",
		Description:     "No field under status is required.",
		DefaultSeverity: xrdlint.SeverityError,
	}
}

// Check implements xrdlint.SchemaRule.
func (StatusFieldsOptional) Check(ctx *xrdlint.Context, xrd *xrdlint.XRD) error {
	return eachProperty(xrd, func(p xrdlint.Property) {
		if !p.InStatus {
			return
		}
		if p.Path == "status" && p.Required {
			ctx.Report(p, "status must not be required")
		}
		if len(p.Schema.Required) > 0 {
			ctx.Report(p, fmt.Sprintf("status fields must be optional; remove required: [%s]", strings.Join(p.Schema.Required, ", ")))
		}
	})
}

// StatusNoValueValidation reports value validation under status. Status
// records what was observed, and an observed value should never be rejected.
// Types are fine.
type StatusNoValueValidation struct{}

// Meta implements xrdlint.SchemaRule.
func (StatusNoValueValidation) Meta() xrdlint.Meta {
	return xrdlint.Meta{
		ID:              "xrd/status-no-value-validation",
		Description:     "No minimum, maximum, pattern, enum, length or item limits under status.",
		DefaultSeverity: xrdlint.SeverityError,
	}
}

// Check implements xrdlint.SchemaRule.
func (StatusNoValueValidation) Check(ctx *xrdlint.Context, xrd *xrdlint.XRD) error {
	return eachProperty(xrd, func(p xrdlint.Property) {
		if !p.InStatus {
			return
		}
		s := p.Schema
		var set []string
		add := func(present bool, keyword string) {
			if present {
				set = append(set, keyword)
			}
		}
		add(s.Minimum != nil, "minimum")
		add(s.Maximum != nil, "maximum")
		add(s.MultipleOf != nil, "multipleOf")
		add(s.MinLength != nil, "minLength")
		add(s.MaxLength != nil, "maxLength")
		add(s.Pattern != "", "pattern")
		add(len(s.Enum) > 0, "enum")
		add(s.MinItems != nil, "minItems")
		add(s.MaxItems != nil, "maxItems")
		add(s.UniqueItems, "uniqueItems")
		add(s.MinProperties != nil, "minProperties")
		add(s.MaxProperties != nil, "maxProperties")
		if len(set) > 0 {
			ctx.Report(p, "status field must not set "+strings.Join(set, ", "))
		}
	})
}
