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

// Rule IDs the linter reports itself. They cannot be configured, disabled or
// suppressed inline, except lint/unused-disable, which is always a warning.
const (
	// RuleInvalidXRD reports an XRD that is invalid or not structural. The
	// remaining rules are skipped for that XRD.
	RuleInvalidXRD = "lint/invalid-xrd"
	// RuleError reports a rule that failed to run. It always fails the run.
	RuleError = "lint/rule-error"
	// RuleUnusedDisable reports a disable comment that suppressed nothing.
	RuleUnusedDisable = "lint/unused-disable"
)

// A Finding is one problem reported by a rule.
type Finding struct {
	Rule     string   `json:"rule"`
	Severity Severity `json:"severity"`
	File     string   `json:"file,omitempty"`
	Line     int      `json:"line,omitempty"`
	Column   int      `json:"column,omitempty"`
	XRD      string   `json:"xrd,omitempty"`
	Version  string   `json:"version,omitempty"`
	Path     string   `json:"path,omitempty"`
	Message  string   `json:"message"`
	DocsURL  string   `json:"docsURL,omitempty"`
	Fix      string   `json:"fix,omitempty"`
}

// Summary counts findings by severity.
type Summary struct {
	Errors   int `json:"errors"`
	Warnings int `json:"warnings"`
}

// Result is the outcome of a lint run.
type Result struct {
	Summary  Summary   `json:"summary"`
	Findings []Finding `json:"findings"`
}

// Failed reports whether the result should fail the run: there is at least
// one error, or there are more warnings than maxWarnings. A negative
// maxWarnings means warnings are unlimited.
func (r *Result) Failed(maxWarnings int) bool {
	if r.Summary.Errors > 0 {
		return true
	}
	return maxWarnings >= 0 && r.Summary.Warnings > maxWarnings
}
