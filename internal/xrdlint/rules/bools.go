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
	"github.com/crossplane/cli/v2/internal/xrdlint"
)

// NoBools reports boolean fields. An enum can grow a third value later; a
// boolean cannot.
type NoBools struct{}

// Meta implements xrdlint.SchemaRule.
func (NoBools) Meta() xrdlint.Meta {
	return xrdlint.Meta{
		ID:              "xrd/no-bools",
		Description:     "No type: boolean; prefer an enum.",
		DefaultSeverity: xrdlint.SeverityWarn,
	}
}

// Check implements xrdlint.SchemaRule.
func (NoBools) Check(ctx *xrdlint.Context, xrd *xrdlint.XRD) error {
	return eachProperty(xrd, func(p xrdlint.Property) {
		if p.Schema.Type == "boolean" {
			ctx.Report(p, "prefer an enum to a boolean")
		}
	})
}
