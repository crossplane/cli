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

// ListsNeedListType reports arrays with no explicit x-kubernetes-list-type,
// so that merge behaviour under server-side apply is a deliberate choice.
// atomic, set and map all pass.
type ListsNeedListType struct{}

// Meta implements xrdlint.SchemaRule.
func (ListsNeedListType) Meta() xrdlint.Meta {
	return xrdlint.Meta{
		ID:              "xrd/lists-need-list-type",
		Description:     "Every array declares an explicit x-kubernetes-list-type.",
		DefaultSeverity: xrdlint.SeverityError,
	}
}

// Check implements xrdlint.SchemaRule.
func (ListsNeedListType) Check(ctx *xrdlint.Context, xrd *xrdlint.XRD) error {
	return eachProperty(xrd, func(p xrdlint.Property) {
		if p.Schema.Type == "array" && p.Schema.XListType == nil {
			ctx.Report(p, "array has no x-kubernetes-list-type",
				xrdlint.WithFix("set x-kubernetes-list-type to atomic, set or map"))
		}
	})
}
