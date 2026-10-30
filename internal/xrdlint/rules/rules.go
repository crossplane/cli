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

// Package rules holds the built-in xrdlint rules.
package rules

import (
	"github.com/crossplane/cli/v2/internal/xrdlint"
)

// Builtin returns every built-in rule.
func Builtin() []xrdlint.SchemaRule {
	return []xrdlint.SchemaRule{
		StatusFieldsOptional{},
		StatusNoValueValidation{},
		ListsNeedListType{},
		NoBools{},
	}
}

// eachProperty runs fn on every property of every served version.
func eachProperty(xrd *xrdlint.XRD, fn func(p xrdlint.Property)) error {
	return xrd.Walk(func(p xrdlint.Property) error {
		fn(p)
		return nil
	})
}
