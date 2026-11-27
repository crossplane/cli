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
	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	"github.com/crossplane/cli/v2/internal/exitcode"
)

// Exit statuses of crossplane xrd lint, besides 0 for a clean run and 1 for a
// run that could not do its job. They follow simulate's numbering where the
// meanings match.
const (
	// ExitCodeInvalidXRD means an input XRD is invalid or not structural
	// (lint/invalid-xrd). Like simulate's 2, the input failed schema
	// validation.
	ExitCodeInvalidXRD = 2

	// ExitCodeFindings means lint ran and found problems: at least one error
	// finding, or more warnings than the budget allows.
	ExitCodeFindings = 3
)

// ExitError returns what a lint command returns once the result has been
// rendered: nil for a clean run, an error (exit 1) if a rule failed to run,
// or a silent exitcode.ExitError with ExitCodeInvalidXRD or ExitCodeFindings.
// When several apply, the lowest non-zero code wins. A negative maxWarnings
// means warnings are unlimited.
func (r *Result) ExitError(maxWarnings int) error {
	ruleErrors, invalid := 0, 0
	for _, f := range r.Findings {
		switch f.Rule {
		case RuleError:
			ruleErrors++
		case RuleInvalidXRD:
			invalid++
		}
	}

	switch {
	case ruleErrors > 0:
		return errors.Errorf("%s failed to run; see %s in the report", plural(ruleErrors, "rule"), RuleError)
	case invalid > 0:
		return exitcode.ExitError{Code: ExitCodeInvalidXRD}
	case r.Failed(maxWarnings):
		return exitcode.ExitError{Code: ExitCodeFindings}
	}
	return nil
}
