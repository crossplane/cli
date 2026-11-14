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

package xrd

import (
	"bytes"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/afero"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/cli/v2/internal/xrdlint"
)

// lintFixtures holds the inputs the lint tests share: example XRDs in xrds/,
// lint configs in configs/, and a small directory tree in tree/. Any
// combination of XRDs and config is one more row in TestLint.
const lintFixtures = "testdata/lint"

// lintWorkDir is the working directory each case runs in.
const lintWorkDir = "/work"

// lintFinding is the part of a finding the tests compare. Column and docs URL
// are left out.
type lintFinding struct {
	Rule     string
	Severity xrdlint.Severity
	File     string
	Line     int
	XRD      string
	Version  string
	Path     string
	Message  string
	Fix      string
}

// lintWant is the expected result of one run. An empty err means the run
// passes. When the case sets an output format, output is compared byte for
// byte; otherwise the run uses -o yaml and summary and findings are compared.
type lintWant struct {
	err      string
	summary  xrdlint.Summary
	findings []lintFinding
	output   string
}

// boolFindings are the findings xrd/no-bools reports on xrds/bools.yaml.
func boolFindings(sev xrdlint.Severity) []lintFinding {
	return []lintFinding{
		{Rule: "xrd/no-bools", Severity: sev, File: "/xrds/bools.yaml", Line: 22, XRD: "xqueues.example.org", Version: "v1alpha1", Path: "spec.fifo", Message: "prefer an enum to a boolean"},
		{Rule: "xrd/no-bools", Severity: sev, File: "/xrds/bools.yaml", Line: 27, XRD: "xqueues.example.org", Version: "v1alpha1", Path: "spec.encryption.enabled", Message: "prefer an enum to a boolean"},
		{Rule: "xrd/no-bools", Severity: sev, File: "/xrds/bools.yaml", Line: 32, XRD: "xqueues.example.org", Version: "v1alpha1", Path: "status.ready", Message: "prefer an enum to a boolean"},
	}
}

// listFindings are the findings xrd/lists-need-list-type reports on
// xrds/lists-missing.yaml.
func listFindings(sev xrdlint.Severity) []lintFinding {
	return []lintFinding{
		{Rule: "xrd/lists-need-list-type", Severity: sev, File: "/xrds/lists-missing.yaml", Line: 22, XRD: "xclusters.example.org", Version: "v1alpha1", Path: "spec.zones", Message: "array has no x-kubernetes-list-type", Fix: "set x-kubernetes-list-type to atomic, set or map"},
		{Rule: "xrd/lists-need-list-type", Severity: sev, File: "/xrds/lists-missing.yaml", Line: 36, XRD: "xclusters.example.org", Version: "v1alpha1", Path: "spec.nodePools[*].taints", Message: "array has no x-kubernetes-list-type", Fix: "set x-kubernetes-list-type to atomic, set or map"},
		{Rule: "xrd/lists-need-list-type", Severity: sev, File: "/xrds/lists-missing.yaml", Line: 42, XRD: "xclusters.example.org", Version: "v1alpha1", Path: "spec.labels.*", Message: "array has no x-kubernetes-list-type", Fix: "set x-kubernetes-list-type to atomic, set or map"},
		{Rule: "xrd/lists-need-list-type", Severity: sev, File: "/xrds/lists-missing.yaml", Line: 49, XRD: "xclusters.example.org", Version: "v1alpha1", Path: "status.addresses", Message: "array has no x-kubernetes-list-type", Fix: "set x-kubernetes-list-type to atomic, set or map"},
	}
}

func concat(sets ...[]lintFinding) []lintFinding {
	var out []lintFinding
	for _, s := range sets {
		out = append(out, s...)
	}
	return out
}

func TestLint(t *testing.T) {
	cases := map[string]struct {
		reason string
		// inputs are paths under testdata/lint, or "-" for stdin.
		inputs []string
		// stdin is a file under testdata/lint passed on stdin.
		stdin string
		// config is a file under testdata/lint placed as .crossplane-lint.yaml
		// in the working directory, where the command discovers it.
		config string
		// configAbove places config in the parent of the working directory
		// instead.
		configAbove bool
		// configFlag is a file under testdata/lint passed with --lint-config.
		configFlag string
		// maxWarnings is the --max-warnings value. Default: -1.
		maxWarnings *int
		// output is an output format whose output is compared byte for byte.
		output string
		want   lintWant
	}{
		"budget/errors-first": {
			reason:      "Errors fail the run whatever the budget, and the error says so.",
			inputs:      []string{"xrds/lists-missing.yaml", "xrds/bools.yaml"},
			maxWarnings: new(0),
			want: lintWant{
				err:      "lint found 4 errors",
				summary:  xrdlint.Summary{Errors: 4, Warnings: 3},
				findings: concat(listFindings(xrdlint.SeverityError), boolFindings(xrdlint.SeverityWarn)),
			},
		},
		"budget/over": {
			reason:      "Warnings over the budget fail the run.",
			inputs:      []string{"xrds/bools.yaml"},
			maxWarnings: new(2),
			want: lintWant{
				err:      "3 warnings exceed the maximum of 2",
				summary:  xrdlint.Summary{Errors: 0, Warnings: 3},
				findings: boolFindings(xrdlint.SeverityWarn),
			},
		},
		"budget/unlimited": {
			reason: "With no budget, warnings never fail the run.",
			inputs: []string{"xrds/bools.yaml"},
			want: lintWant{
				summary:  xrdlint.Summary{Errors: 0, Warnings: 3},
				findings: boolFindings(xrdlint.SeverityWarn),
			},
		},
		"budget/within": {
			reason:      "Warnings up to the budget pass.",
			inputs:      []string{"xrds/bools.yaml"},
			maxWarnings: new(3),
			want: lintWant{
				summary:  xrdlint.Summary{Errors: 0, Warnings: 3},
				findings: boolFindings(xrdlint.SeverityWarn),
			},
		},
		"budget/zero": {
			reason:      "A zero budget fails on any warning.",
			inputs:      []string{"xrds/bools.yaml"},
			maxWarnings: new(0),
			want: lintWant{
				err:      "3 warnings exceed the maximum of 0",
				summary:  xrdlint.Summary{Errors: 0, Warnings: 3},
				findings: boolFindings(xrdlint.SeverityWarn),
			},
		},
		"budget/zero-clean": {
			reason:      "A zero budget passes with no warnings.",
			inputs:      []string{"xrds/clean.yaml"},
			maxWarnings: new(0),
		},
		"config/custom-rules": {
			reason: "Custom CEL rules are not implemented yet, and say so rather than being ignored.",
			inputs: []string{"xrds/bools.yaml"},
			config: "configs/custom-rules.yaml",
			want: lintWant{
				err: "invalid lint config \"/work/.crossplane-lint.yaml\": line 4: customRules are not supported yet",
			},
		},
		"config/discovery/nested": {
			reason:      "A config in a parent of the working directory is discovered.",
			inputs:      []string{"xrds/bools.yaml"},
			config:      "configs/no-bools-error.yaml",
			configAbove: true,
			want: lintWant{
				err:      "lint found 3 errors",
				summary:  xrdlint.Summary{Errors: 3, Warnings: 0},
				findings: boolFindings(xrdlint.SeverityError),
			},
		},
		"config/duplicate-rule": {
			reason: "The same rule cannot be configured twice in one file.",
			inputs: []string{"xrds/bools.yaml"},
			config: "configs/duplicate-rule.yaml",
			want: lintWant{
				err: "invalid lint config \"/work/.crossplane-lint.yaml\": cannot parse lint config: yaml: unmarshal errors:\n  line 5: mapping key \"xrd/no-bools\" already defined at line 4",
			},
		},
		"config/duplicate-object-key": {
			reason: "A rule object cannot set the same key twice, so a later severity cannot silently win.",
			inputs: []string{"xrds/bools.yaml"},
			config: "configs/duplicate-object-key.yaml",
			want: lintWant{
				err: "invalid lint config \"/work/.crossplane-lint.yaml\": cannot parse lint config: line 4: duplicate key \"severity\" in rule object",
			},
		},
		"config/null-rule": {
			reason: "A rule with no value is a config error rather than silently turning the rule off.",
			inputs: []string{"xrds/bools.yaml"},
			config: "configs/null-rule.yaml",
			want: lintWant{
				err: "invalid lint config \"/work/.crossplane-lint.yaml\": rule \"xrd/no-bools\" has no value: set a severity (off, warn or error) or {severity: ..., options: {...}}",
			},
		},
		"config/empty-file": {
			reason: "An empty config file applies the defaults.",
			inputs: []string{"xrds/bools.yaml"},
			config: "configs/empty.yaml",
			want: lintWant{
				summary:  xrdlint.Summary{Errors: 0, Warnings: 3},
				findings: boolFindings(xrdlint.SeverityWarn),
			},
		},
		"config/flag-missing": {
			reason:     "A --lint-config file that does not exist is an error.",
			inputs:     []string{"xrds/bools.yaml"},
			configFlag: "configs/does-not-exist.yaml",
			want: lintWant{
				err: "cannot read lint config \"/configs/does-not-exist.yaml\": open /configs/does-not-exist.yaml: file does not exist",
			},
		},
		"config/flag/override": {
			reason:     "--lint-config replaces the discovered config.",
			inputs:     []string{"xrds/bools.yaml"},
			config:     "configs/no-bools-error.yaml",
			configFlag: "configs/no-bools-off.yaml",
		},
		"config/header-only": {
			reason: "A config with only its header applies the defaults.",
			inputs: []string{"xrds/bools.yaml"},
			config: "configs/header-only.yaml",
			want: lintWant{
				summary:  xrdlint.Summary{Errors: 0, Warnings: 3},
				findings: boolFindings(xrdlint.SeverityWarn),
			},
		},
		"config/invalid-yaml": {
			reason: "A config that is not YAML is a config error.",
			inputs: []string{"xrds/bools.yaml"},
			config: "configs/invalid-yaml.yaml",
			want: lintWant{
				err: "invalid lint config \"/work/.crossplane-lint.yaml\": cannot parse lint config: yaml: line 3: did not find expected node content",
			},
		},
		"config/list-form-block": {
			reason: "A rule value written as a block sequence is a config error.",
			inputs: []string{"xrds/bools.yaml"},
			config: "configs/list-form-block.yaml",
			want: lintWant{
				err: "invalid lint config \"/work/.crossplane-lint.yaml\": cannot parse lint config: line 5: the list form is not supported; use a severity or {severity: ..., options: {...}}",
			},
		},
		"config/list-form-flow": {
			reason: "ESLint's list form is a config error.",
			inputs: []string{"xrds/bools.yaml"},
			config: "configs/list-form-flow.yaml",
			want: lintWant{
				err: "invalid lint config \"/work/.crossplane-lint.yaml\": cannot parse lint config: line 4: the list form is not supported; use a severity or {severity: ..., options: {...}}",
			},
		},
		"config/missing-header": {
			reason: "A non-empty config must carry its header.",
			inputs: []string{"xrds/bools.yaml"},
			config: "configs/missing-header.yaml",
			want: lintWant{
				err: "invalid lint config \"/work/.crossplane-lint.yaml\": lint config apiVersion must be \"lint.crossplane.io/v1alpha1\", got \"\"",
			},
		},
		"config/missing-severity": {
			reason: "A rule object must set a severity.",
			inputs: []string{"xrds/bools.yaml"},
			config: "configs/missing-severity.yaml",
			want: lintWant{
				err: "invalid lint config \"/work/.crossplane-lint.yaml\": cannot parse lint config: line 4: severity is required",
			},
		},
		"config/multiple-documents": {
			reason: "A config file holds exactly one document.",
			inputs: []string{"xrds/bools.yaml"},
			config: "configs/multiple-documents.yaml",
			want: lintWant{
				err: "invalid lint config \"/work/.crossplane-lint.yaml\": a lint config must be exactly one YAML document",
			},
		},
		"config/options-not-accepted": {
			reason: "Options for a rule that takes none are a config error.",
			inputs: []string{"xrds/bools.yaml"},
			config: "configs/options-not-accepted.yaml",
			want: lintWant{
				err: "invalid lint config \"/work/.crossplane-lint.yaml\": rule \"xrd/no-bools\" takes no options",
			},
		},
		"config/options-not-object": {
			reason: "Options must be an object.",
			inputs: []string{"xrds/bools.yaml"},
			config: "configs/options-not-object.yaml",
			want: lintWant{
				err: "invalid lint config \"/work/.crossplane-lint.yaml\": cannot parse lint config: line 4: options must be an object",
			},
		},
		"config/reserved-rule": {
			reason: "The linter's own rules cannot be configured.",
			inputs: []string{"xrds/bools.yaml"},
			config: "configs/reserved-rule.yaml",
			want: lintWant{
				err: "invalid lint config \"/work/.crossplane-lint.yaml\": unknown rule \"lint/rule-error\"",
			},
		},
		"config/unknown-object-key": {
			reason: "A rule object accepts only severity and options.",
			inputs: []string{"xrds/bools.yaml"},
			config: "configs/unknown-object-key.yaml",
			want: lintWant{
				err: "invalid lint config \"/work/.crossplane-lint.yaml\": cannot parse lint config: line 4: unknown key \"level\"; a rule object has only severity and options",
			},
		},
		"config/unknown-rule": {
			reason: "A rules entry that names no known rule is a config error, so typos surface.",
			inputs: []string{"xrds/bools.yaml"},
			config: "configs/unknown-rule.yaml",
			want: lintWant{
				err: "invalid lint config \"/work/.crossplane-lint.yaml\": unknown rule \"xrd/no-boools\"",
			},
		},
		"config/unknown-severity": {
			reason: "Only off, warn and error are severities.",
			inputs: []string{"xrds/bools.yaml"},
			config: "configs/unknown-severity.yaml",
			want: lintWant{
				err: "invalid lint config \"/work/.crossplane-lint.yaml\": cannot parse lint config: line 4: unknown severity \"fatal\": must be one of off, warn or error",
			},
		},
		"config/unknown-top-level-key": {
			reason: "An unknown top-level key is a config error.",
			inputs: []string{"xrds/bools.yaml"},
			config: "configs/unknown-top-level-key.yaml",
			want: lintWant{
				err: "invalid lint config \"/work/.crossplane-lint.yaml\": cannot parse lint config: yaml: unmarshal errors:\n  line 3: field rulez not found in type xrdlint.Config",
			},
		},
		"config/wrong-api-version": {
			reason: "The header must name the lint config API version.",
			inputs: []string{"xrds/bools.yaml"},
			config: "configs/wrong-api-version.yaml",
			want: lintWant{
				err: "invalid lint config \"/work/.crossplane-lint.yaml\": lint config apiVersion must be \"lint.crossplane.io/v1alpha1\", got \"lint.crossplane.io/v1\"",
			},
		},
		"config/wrong-kind": {
			reason: "The header must name the LintConfig kind.",
			inputs: []string{"xrds/bools.yaml"},
			config: "configs/wrong-kind.yaml",
			want: lintWant{
				err: "invalid lint config \"/work/.crossplane-lint.yaml\": lint config kind must be \"LintConfig\", got \"Config\"",
			},
		},
		"inputs/directory": {
			reason: "A directory is searched recursively for .yaml and .yml files, in lexical order.",
			inputs: []string{"tree"},
			want: lintWant{
				err:     "lint found 4 errors",
				summary: xrdlint.Summary{Errors: 4, Warnings: 3},
				findings: []lintFinding{
					{Rule: "xrd/lists-need-list-type", Severity: "error", File: "/tree/cluster/definition.yml", Line: 22, XRD: "xclusters.example.org", Version: "v1alpha1", Path: "spec.zones", Message: "array has no x-kubernetes-list-type", Fix: "set x-kubernetes-list-type to atomic, set or map"},
					{Rule: "xrd/lists-need-list-type", Severity: "error", File: "/tree/cluster/definition.yml", Line: 36, XRD: "xclusters.example.org", Version: "v1alpha1", Path: "spec.nodePools[*].taints", Message: "array has no x-kubernetes-list-type", Fix: "set x-kubernetes-list-type to atomic, set or map"},
					{Rule: "xrd/lists-need-list-type", Severity: "error", File: "/tree/cluster/definition.yml", Line: 42, XRD: "xclusters.example.org", Version: "v1alpha1", Path: "spec.labels.*", Message: "array has no x-kubernetes-list-type", Fix: "set x-kubernetes-list-type to atomic, set or map"},
					{Rule: "xrd/lists-need-list-type", Severity: "error", File: "/tree/cluster/definition.yml", Line: 49, XRD: "xclusters.example.org", Version: "v1alpha1", Path: "status.addresses", Message: "array has no x-kubernetes-list-type", Fix: "set x-kubernetes-list-type to atomic, set or map"},
					{Rule: "xrd/no-bools", Severity: "warn", File: "/tree/queue/definition.yaml", Line: 22, XRD: "xqueues.example.org", Version: "v1alpha1", Path: "spec.fifo", Message: "prefer an enum to a boolean"},
					{Rule: "xrd/no-bools", Severity: "warn", File: "/tree/queue/definition.yaml", Line: 27, XRD: "xqueues.example.org", Version: "v1alpha1", Path: "spec.encryption.enabled", Message: "prefer an enum to a boolean"},
					{Rule: "xrd/no-bools", Severity: "warn", File: "/tree/queue/definition.yaml", Line: 32, XRD: "xqueues.example.org", Version: "v1alpha1", Path: "status.ready", Message: "prefer an enum to a boolean"},
				},
			},
		},
		"inputs/invalid-yaml": {
			reason: "A file that is not valid YAML fails the run.",
			inputs: []string{"xrds/invalid-yaml.yaml"},
			want: lintWant{
				err: "cannot parse /xrds/invalid-yaml.yaml: yaml: line 2: did not find expected node content",
			},
		},
		"inputs/missing-file": {
			reason: "An input that cannot be read fails the run.",
			inputs: []string{"xrds/does-not-exist.yaml"},
			want: lintWant{
				err: "cannot read \"/xrds/does-not-exist.yaml\": open /xrds/does-not-exist.yaml: file does not exist",
			},
		},
		"inputs/multi-document": {
			reason: "Every XRD in a multi-document file is linted, with line numbers from the whole file.",
			inputs: []string{"xrds/multi-document.yaml"},
			want: lintWant{
				err:     "lint found 4 errors",
				summary: xrdlint.Summary{Errors: 4, Warnings: 3},
				findings: []lintFinding{
					{Rule: "xrd/no-bools", Severity: "warn", File: "/xrds/multi-document.yaml", Line: 27, XRD: "xqueues.example.org", Version: "v1alpha1", Path: "spec.fifo", Message: "prefer an enum to a boolean"},
					{Rule: "xrd/no-bools", Severity: "warn", File: "/xrds/multi-document.yaml", Line: 32, XRD: "xqueues.example.org", Version: "v1alpha1", Path: "spec.encryption.enabled", Message: "prefer an enum to a boolean"},
					{Rule: "xrd/no-bools", Severity: "warn", File: "/xrds/multi-document.yaml", Line: 37, XRD: "xqueues.example.org", Version: "v1alpha1", Path: "status.ready", Message: "prefer an enum to a boolean"},
					{Rule: "xrd/lists-need-list-type", Severity: "error", File: "/xrds/multi-document.yaml", Line: 66, XRD: "xclusters.example.org", Version: "v1alpha1", Path: "spec.zones", Message: "array has no x-kubernetes-list-type", Fix: "set x-kubernetes-list-type to atomic, set or map"},
					{Rule: "xrd/lists-need-list-type", Severity: "error", File: "/xrds/multi-document.yaml", Line: 80, XRD: "xclusters.example.org", Version: "v1alpha1", Path: "spec.nodePools[*].taints", Message: "array has no x-kubernetes-list-type", Fix: "set x-kubernetes-list-type to atomic, set or map"},
					{Rule: "xrd/lists-need-list-type", Severity: "error", File: "/xrds/multi-document.yaml", Line: 86, XRD: "xclusters.example.org", Version: "v1alpha1", Path: "spec.labels.*", Message: "array has no x-kubernetes-list-type", Fix: "set x-kubernetes-list-type to atomic, set or map"},
					{Rule: "xrd/lists-need-list-type", Severity: "error", File: "/xrds/multi-document.yaml", Line: 93, XRD: "xclusters.example.org", Version: "v1alpha1", Path: "status.addresses", Message: "array has no x-kubernetes-list-type", Fix: "set x-kubernetes-list-type to atomic, set or map"},
				},
			},
		},
		"inputs/no-xrds": {
			reason: "Inputs that hold no XRDs fail the run, so a wrong path does not pass silently. Kinds and groups that are not XRDs are skipped.",
			inputs: []string{"xrds/not-xrds.yaml"},
			want: lintWant{
				err: "no XRDs found in the supplied paths",
			},
		},
		"inputs/several-paths": {
			reason: "Several paths are linted in the order given.",
			inputs: []string{"xrds/lists-missing.yaml", "xrds/bools.yaml"},
			want: lintWant{
				err:      "lint found 4 errors",
				summary:  xrdlint.Summary{Errors: 4, Warnings: 3},
				findings: concat(listFindings(xrdlint.SeverityError), boolFindings(xrdlint.SeverityWarn)),
			},
		},
		"inputs/stdin": {
			reason: "The path - reads an XRD from stdin.",
			inputs: []string{"-"},
			stdin:  "xrds/bools.yaml",
			want: lintWant{
				summary: xrdlint.Summary{Errors: 0, Warnings: 3},
				findings: []lintFinding{
					{Rule: "xrd/no-bools", Severity: "warn", File: "<stdin>", Line: 22, XRD: "xqueues.example.org", Version: "v1alpha1", Path: "spec.fifo", Message: "prefer an enum to a boolean"},
					{Rule: "xrd/no-bools", Severity: "warn", File: "<stdin>", Line: 27, XRD: "xqueues.example.org", Version: "v1alpha1", Path: "spec.encryption.enabled", Message: "prefer an enum to a boolean"},
					{Rule: "xrd/no-bools", Severity: "warn", File: "<stdin>", Line: 32, XRD: "xqueues.example.org", Version: "v1alpha1", Path: "status.ready", Message: "prefer an enum to a boolean"},
				},
			},
		},
		"inputs/v1-xrd": {
			reason: "A v1 XRD that offers a claim is linted like a v2 XRD.",
			inputs: []string{"xrds/v1-legacy.yaml"},
			want: lintWant{
				summary: xrdlint.Summary{Errors: 0, Warnings: 1},
				findings: []lintFinding{
					{Rule: "xrd/no-bools", Severity: "warn", File: "/xrds/v1-legacy.yaml", Line: 25, XRD: "xpostgres.example.org", Version: "v1alpha1", Path: "spec.highAvailability", Message: "prefer an enum to a boolean"},
				},
			},
		},
		"invalid/alongside-valid": {
			reason: "An invalid XRD does not stop the other XRDs from being linted.",
			inputs: []string{"xrds/untyped.yaml", "xrds/bools.yaml"},
			want: lintWant{
				err:      "lint found 1 error",
				summary:  xrdlint.Summary{Errors: 1, Warnings: 3},
				findings: concat([]lintFinding{{Rule: "lint/invalid-xrd", Severity: "error", File: "/xrds/untyped.yaml", Line: 2, XRD: "xfirewalls.example.org", Message: "composite CRD derived from XRD \"xfirewalls.example.org\" has an unusable schema: version \"v1alpha1\": properties[spec].properties[acl].type: Required value: must not be empty for specified object fields"}}, boolFindings(xrdlint.SeverityWarn)),
			},
		},
		"invalid/untyped-field": {
			reason: "An XRD that is not structural is reported as lint/invalid-xrd and no other rule runs on it.",
			inputs: []string{"xrds/untyped.yaml"},
			want: lintWant{
				err:     "lint found 1 error",
				summary: xrdlint.Summary{Errors: 1, Warnings: 0},
				findings: []lintFinding{
					{Rule: "lint/invalid-xrd", Severity: "error", File: "/xrds/untyped.yaml", Line: 2, XRD: "xfirewalls.example.org", Message: "composite CRD derived from XRD \"xfirewalls.example.org\" has an unusable schema: version \"v1alpha1\": properties[spec].properties[acl].type: Required value: must not be empty for specified object fields"},
				},
			},
		},
		"output/json": {
			reason: "JSON output carries the summary and every field of each finding.",
			inputs: []string{"xrds/lists-missing.yaml"},
			output: "json",
			want: lintWant{
				err: "lint found 4 errors",
				output: `{
  "summary": {
    "errors": 4,
    "warnings": 0
  },
  "findings": [
    {
      "rule": "xrd/lists-need-list-type",
      "severity": "error",
      "file": "/xrds/lists-missing.yaml",
      "line": 22,
      "column": 15,
      "xrd": "xclusters.example.org",
      "version": "v1alpha1",
      "path": "spec.zones",
      "message": "array has no x-kubernetes-list-type",
      "fix": "set x-kubernetes-list-type to atomic, set or map"
    },
    {
      "rule": "xrd/lists-need-list-type",
      "severity": "error",
      "file": "/xrds/lists-missing.yaml",
      "line": 36,
      "column": 21,
      "xrd": "xclusters.example.org",
      "version": "v1alpha1",
      "path": "spec.nodePools[*].taints",
      "message": "array has no x-kubernetes-list-type",
      "fix": "set x-kubernetes-list-type to atomic, set or map"
    },
    {
      "rule": "xrd/lists-need-list-type",
      "severity": "error",
      "file": "/xrds/lists-missing.yaml",
      "line": 42,
      "column": 17,
      "xrd": "xclusters.example.org",
      "version": "v1alpha1",
      "path": "spec.labels.*",
      "message": "array has no x-kubernetes-list-type",
      "fix": "set x-kubernetes-list-type to atomic, set or map"
    },
    {
      "rule": "xrd/lists-need-list-type",
      "severity": "error",
      "file": "/xrds/lists-missing.yaml",
      "line": 49,
      "column": 15,
      "xrd": "xclusters.example.org",
      "version": "v1alpha1",
      "path": "status.addresses",
      "message": "array has no x-kubernetes-list-type",
      "fix": "set x-kubernetes-list-type to atomic, set or map"
    }
  ]
}
`,
			},
		},
		"output/json-clean": {
			reason: "JSON output for a clean run has an empty findings list.",
			inputs: []string{"xrds/clean.yaml"},
			output: "json",
			want: lintWant{
				output: `{
  "summary": {
    "errors": 0,
    "warnings": 0
  },
  "findings": []
}
`,
			},
		},
		"output/text": {
			reason: "Text output groups findings by file, then by XRD and version, and aligns them.",
			inputs: []string{"xrds/bools.yaml", "xrds/lists-missing.yaml", "xrds/untyped.yaml"},
			output: "text",
			want: lintWant{
				err: "lint found 5 errors",
				output: `/xrds/bools.yaml
  xqueues.example.org v1alpha1
    22:15  warn  spec.fifo: prefer an enum to a boolean                xrd/no-bools
    27:19  warn  spec.encryption.enabled: prefer an enum to a boolean  xrd/no-bools
    32:15  warn  status.ready: prefer an enum to a boolean             xrd/no-bools

/xrds/lists-missing.yaml
  xclusters.example.org v1alpha1
    22:15  error  spec.zones: array has no x-kubernetes-list-type                xrd/lists-need-list-type
    36:21  error  spec.nodePools[*].taints: array has no x-kubernetes-list-type  xrd/lists-need-list-type
    42:17  error  spec.labels.*: array has no x-kubernetes-list-type             xrd/lists-need-list-type
    49:15  error  status.addresses: array has no x-kubernetes-list-type          xrd/lists-need-list-type

/xrds/untyped.yaml
  xfirewalls.example.org
    2:1  error  composite CRD derived from XRD "xfirewalls.example.org" has an unusable schema: version "v1alpha1": properties[spec].properties[acl].type: Required value: must not be empty for specified object fields  lint/invalid-xrd

5 errors, 3 warnings
`,
			},
		},
		"output/text-clean": {
			reason: "Text output for a clean run is just the summary.",
			inputs: []string{"xrds/clean.yaml"},
			output: "text",
			want: lintWant{
				output: `0 errors, 0 warnings
`,
			},
		},
		"output/unknown": {
			reason: "An unknown output format is an error.",
			inputs: []string{"xrds/clean.yaml"},
			output: "xml",
			want: lintWant{
				err: "unknown output format: \"xml\"",
			},
		},
		"output/yaml": {
			reason: "YAML output carries the summary and every field of each finding.",
			inputs: []string{"xrds/bools.yaml"},
			output: "yaml",
			want: lintWant{
				output: `findings:
- column: 15
  file: /xrds/bools.yaml
  line: 22
  message: prefer an enum to a boolean
  path: spec.fifo
  rule: xrd/no-bools
  severity: warn
  version: v1alpha1
  xrd: xqueues.example.org
- column: 19
  file: /xrds/bools.yaml
  line: 27
  message: prefer an enum to a boolean
  path: spec.encryption.enabled
  rule: xrd/no-bools
  severity: warn
  version: v1alpha1
  xrd: xqueues.example.org
- column: 15
  file: /xrds/bools.yaml
  line: 32
  message: prefer an enum to a boolean
  path: status.ready
  rule: xrd/no-bools
  severity: warn
  version: v1alpha1
  xrd: xqueues.example.org
summary:
  errors: 0
  warnings: 3
`,
			},
		},
		"rules/defaults-clean": {
			reason: "An XRD that follows every built-in rule passes with no findings.",
			inputs: []string{"xrds/clean.yaml"},
		},
		"rules/lists-need-list-type/fail": {
			reason: "Arrays without x-kubernetes-list-type are errors, in spec, status, array items and map values.",
			inputs: []string{"xrds/lists-missing.yaml"},
			want: lintWant{
				err:      "lint found 4 errors",
				summary:  xrdlint.Summary{Errors: 4, Warnings: 0},
				findings: listFindings(xrdlint.SeverityError),
			},
		},
		"rules/lists-need-list-type/pass": {
			reason: "atomic, set and map list types all pass.",
			inputs: []string{"xrds/clean.yaml"},
		},
		"rules/lists-need-list-type/warn": {
			reason: "Lowered to warn with the object form, the same XRD passes with warnings.",
			inputs: []string{"xrds/lists-missing.yaml"},
			config: "configs/lists-need-list-type-warn.yaml",
			want: lintWant{
				summary:  xrdlint.Summary{Errors: 0, Warnings: 4},
				findings: listFindings(xrdlint.SeverityWarn),
			},
		},
		"rules/no-bools/error": {
			reason: "Raised to error, the same XRD fails the run.",
			inputs: []string{"xrds/bools.yaml"},
			config: "configs/no-bools-error.yaml",
			want: lintWant{
				err:      "lint found 3 errors",
				summary:  xrdlint.Summary{Errors: 3, Warnings: 0},
				findings: boolFindings(xrdlint.SeverityError),
			},
		},
		"rules/no-bools/fail": {
			reason: "Booleans warn by default, in spec, nested objects and status, and the run passes.",
			inputs: []string{"xrds/bools.yaml"},
			want: lintWant{
				summary:  xrdlint.Summary{Errors: 0, Warnings: 3},
				findings: boolFindings(xrdlint.SeverityWarn),
			},
		},
		"rules/no-bools/off": {
			reason: "Turned off, the same XRD passes with no findings.",
			inputs: []string{"xrds/bools.yaml"},
			config: "configs/no-bools-off.yaml",
		},
		"rules/no-bools/pass": {
			reason: "String enums pass.",
			inputs: []string{"xrds/clean.yaml"},
		},
		"rules/served-versions-only": {
			reason: "Rules check every served version, and skip versions that are not served.",
			inputs: []string{"xrds/multi-version.yaml"},
			want: lintWant{
				summary: xrdlint.Summary{Errors: 0, Warnings: 2},
				findings: []lintFinding{
					{Rule: "xrd/no-bools", Severity: "warn", File: "/xrds/multi-version.yaml", Line: 22, XRD: "xcaches.example.org", Version: "v1alpha1", Path: "spec.public", Message: "prefer an enum to a boolean"},
					{Rule: "xrd/no-bools", Severity: "warn", File: "/xrds/multi-version.yaml", Line: 34, XRD: "xcaches.example.org", Version: "v1beta1", Path: "spec.public", Message: "prefer an enum to a boolean"},
				},
			},
		},
		"rules/status-fields-optional/fail": {
			reason: "Required lists under status, and status itself required, are errors by default.",
			inputs: []string{"xrds/status-required.yaml"},
			want: lintWant{
				err:     "lint found 3 errors",
				summary: xrdlint.Summary{Errors: 3, Warnings: 0},
				findings: []lintFinding{
					{Rule: "xrd/status-fields-optional", Severity: "error", File: "/xrds/status-required.yaml", Line: 26, XRD: "xdatabases.example.org", Version: "v1alpha1", Path: "status", Message: "status fields must be optional; remove required: [endpoint]"},
					{Rule: "xrd/status-fields-optional", Severity: "error", File: "/xrds/status-required.yaml", Line: 26, XRD: "xdatabases.example.org", Version: "v1alpha1", Path: "status", Message: "status must not be required"},
					{Rule: "xrd/status-fields-optional", Severity: "error", File: "/xrds/status-required.yaml", Line: 35, XRD: "xdatabases.example.org", Version: "v1alpha1", Path: "status.replicas[*]", Message: "status fields must be optional; remove required: [host, port]"},
				},
			},
		},
		"rules/status-fields-optional/off": {
			reason: "Turned off, the same XRD passes with no findings.",
			inputs: []string{"xrds/status-required.yaml"},
			config: "configs/status-fields-optional-off.yaml",
		},
		"rules/status-fields-optional/pass": {
			reason: "Required fields under spec are fine.",
			inputs: []string{"xrds/clean.yaml"},
		},
		"rules/status-fields-optional/warn": {
			reason: "Lowered to warn, the same XRD reports warnings and passes.",
			inputs: []string{"xrds/status-required.yaml"},
			config: "configs/status-fields-optional-warn.yaml",
			want: lintWant{
				summary: xrdlint.Summary{Errors: 0, Warnings: 3},
				findings: []lintFinding{
					{Rule: "xrd/status-fields-optional", Severity: "warn", File: "/xrds/status-required.yaml", Line: 26, XRD: "xdatabases.example.org", Version: "v1alpha1", Path: "status", Message: "status fields must be optional; remove required: [endpoint]"},
					{Rule: "xrd/status-fields-optional", Severity: "warn", File: "/xrds/status-required.yaml", Line: 26, XRD: "xdatabases.example.org", Version: "v1alpha1", Path: "status", Message: "status must not be required"},
					{Rule: "xrd/status-fields-optional", Severity: "warn", File: "/xrds/status-required.yaml", Line: 35, XRD: "xdatabases.example.org", Version: "v1alpha1", Path: "status.replicas[*]", Message: "status fields must be optional; remove required: [host, port]"},
				},
			},
		},
		"rules/status-no-value-validation/fail": {
			reason: "Every value-validation keyword under status is reported, listed per field; spec is untouched.",
			inputs: []string{"xrds/status-validation.yaml"},
			want: lintWant{
				err:     "lint found 6 errors",
				summary: xrdlint.Summary{Errors: 6, Warnings: 0},
				findings: []lintFinding{
					{Rule: "xrd/status-no-value-validation", Severity: "error", File: "/xrds/status-validation.yaml", Line: 26, XRD: "xbuckets.example.org", Version: "v1alpha1", Path: "status", Message: "status field must not set minProperties"},
					{Rule: "xrd/status-no-value-validation", Severity: "error", File: "/xrds/status-validation.yaml", Line: 30, XRD: "xbuckets.example.org", Version: "v1alpha1", Path: "status.arn", Message: "status field must not set pattern"},
					{Rule: "xrd/status-no-value-validation", Severity: "error", File: "/xrds/status-validation.yaml", Line: 33, XRD: "xbuckets.example.org", Version: "v1alpha1", Path: "status.objects", Message: "status field must not set minimum, maximum, multipleOf"},
					{Rule: "xrd/status-no-value-validation", Severity: "error", File: "/xrds/status-validation.yaml", Line: 38, XRD: "xbuckets.example.org", Version: "v1alpha1", Path: "status.phase", Message: "status field must not set minLength, maxLength, enum"},
					{Rule: "xrd/status-no-value-validation", Severity: "error", File: "/xrds/status-validation.yaml", Line: 43, XRD: "xbuckets.example.org", Version: "v1alpha1", Path: "status.regions", Message: "status field must not set minItems, maxItems, uniqueItems"},
					{Rule: "xrd/status-no-value-validation", Severity: "error", File: "/xrds/status-validation.yaml", Line: 49, XRD: "xbuckets.example.org", Version: "v1alpha1", Path: "status.regions[*]", Message: "status field must not set maxLength"},
				},
			},
		},
		"rules/status-no-value-validation/off": {
			reason: "Turned off, the same XRD passes with no findings.",
			inputs: []string{"xrds/status-validation.yaml"},
			config: "configs/status-no-value-validation-off.yaml",
		},
		"rules/status-no-value-validation/pass": {
			reason: "Types and descriptions under status are fine.",
			inputs: []string{"xrds/clean.yaml"},
		},
		"suppress/document-root": {
			reason: "A comment at the top of the document covers the whole XRD.",
			inputs: []string{"xrds/suppress-document-root.yaml"},
		},
		"suppress/invalid-xrd": {
			reason: "lint/invalid-xrd cannot be suppressed.",
			inputs: []string{"xrds/suppress-invalid-xrd.yaml"},
			want: lintWant{
				err:     "lint found 1 error",
				summary: xrdlint.Summary{Errors: 1, Warnings: 0},
				findings: []lintFinding{
					{Rule: "lint/invalid-xrd", Severity: "error", File: "/xrds/suppress-invalid-xrd.yaml", Line: 2, XRD: "xfirewalls.example.org", Message: "composite CRD derived from XRD \"xfirewalls.example.org\" has an unusable schema: version \"v1alpha1\": properties[spec].properties[acl].type: Required value: must not be empty for specified object fields"},
				},
			},
		},
		"suppress/look-alike": {
			reason: "A comment that only resembles the directive is ignored.",
			inputs: []string{"xrds/suppress-look-alike.yaml"},
			want: lintWant{
				summary: xrdlint.Summary{Errors: 0, Warnings: 1},
				findings: []lintFinding{
					{Rule: "xrd/no-bools", Severity: "warn", File: "/xrds/suppress-look-alike.yaml", Line: 22, XRD: "xqueues.example.org", Version: "v1alpha1", Path: "spec.fifo", Message: "prefer an enum to a boolean"},
				},
			},
		},
		"suppress/no-rules": {
			reason: "A comment that names no rules is reported.",
			inputs: []string{"xrds/suppress-no-rules.yaml"},
			want: lintWant{
				summary: xrdlint.Summary{Errors: 0, Warnings: 2},
				findings: []lintFinding{
					{Rule: "lint/unused-disable", Severity: "warn", File: "/xrds/suppress-no-rules.yaml", Line: 21, XRD: "xqueues.example.org", Message: "disable comment names no rules"},
					{Rule: "xrd/no-bools", Severity: "warn", File: "/xrds/suppress-no-rules.yaml", Line: 22, XRD: "xqueues.example.org", Version: "v1alpha1", Path: "spec.fifo", Message: "prefer an enum to a boolean"},
				},
			},
		},
		"suppress/node-and-children": {
			reason: "A disable comment covers the next node and everything beneath it.",
			inputs: []string{"xrds/suppress-node-and-children.yaml"},
			want: lintWant{
				summary: xrdlint.Summary{Errors: 0, Warnings: 1},
				findings: []lintFinding{
					{Rule: "xrd/no-bools", Severity: "warn", File: "/xrds/suppress-node-and-children.yaml", Line: 32, XRD: "xqueues.example.org", Version: "v1alpha1", Path: "status.ready", Message: "prefer an enum to a boolean"},
				},
			},
		},
		"suppress/other-rule-still-reported": {
			reason: "A comment suppresses only the rules it names.",
			inputs: []string{"xrds/suppress-other-rule-still-reported.yaml"},
			want: lintWant{
				err:     "lint found 1 error",
				summary: xrdlint.Summary{Errors: 1, Warnings: 0},
				findings: []lintFinding{
					{Rule: "xrd/lists-need-list-type", Severity: "error", File: "/xrds/suppress-other-rule-still-reported.yaml", Line: 22, XRD: "xqueues.example.org", Version: "v1alpha1", Path: "spec.zones", Message: "array has no x-kubernetes-list-type", Fix: "set x-kubernetes-list-type to atomic, set or map"},
				},
			},
		},
		"suppress/reserved-rule": {
			reason: "The linter's own findings cannot be suppressed, and saying so is reported.",
			inputs: []string{"xrds/suppress-reserved-rule.yaml"},
			want: lintWant{
				summary: xrdlint.Summary{Errors: 0, Warnings: 1},
				findings: []lintFinding{
					{Rule: "lint/unused-disable", Severity: "warn", File: "/xrds/suppress-reserved-rule.yaml", Line: 21, XRD: "xqueues.example.org", Message: "lint/rule-error cannot be suppressed"},
				},
			},
		},
		"suppress/several-rules": {
			reason: "One comment can name several rules; each rule it names but does not use is reported.",
			inputs: []string{"xrds/suppress-several-rules.yaml"},
			want: lintWant{
				err:     "lint found 1 error",
				summary: xrdlint.Summary{Errors: 1, Warnings: 2},
				findings: []lintFinding{
					{Rule: "lint/unused-disable", Severity: "warn", File: "/xrds/suppress-several-rules.yaml", Line: 21, XRD: "xqueues.example.org", Message: "disable comment for xrd/lists-need-list-type suppressed nothing"},
					{Rule: "lint/unused-disable", Severity: "warn", File: "/xrds/suppress-several-rules.yaml", Line: 21, XRD: "xqueues.example.org", Message: "disable comment for xrd/no-floats suppressed nothing"},
					{Rule: "xrd/lists-need-list-type", Severity: "error", File: "/xrds/suppress-several-rules.yaml", Line: 24, XRD: "xqueues.example.org", Version: "v1alpha1", Path: "spec.zones", Message: "array has no x-kubernetes-list-type", Fix: "set x-kubernetes-list-type to atomic, set or map"},
				},
			},
		},
		"suppress/unused": {
			reason: "A comment that suppresses nothing is reported as lint/unused-disable.",
			inputs: []string{"xrds/suppress-unused.yaml"},
			want: lintWant{
				summary: xrdlint.Summary{Errors: 0, Warnings: 1},
				findings: []lintFinding{
					{Rule: "lint/unused-disable", Severity: "warn", File: "/xrds/suppress-unused.yaml", Line: 21, XRD: "xqueues.example.org", Message: "disable comment for xrd/no-bools suppressed nothing"},
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fs := lintFs(t)
			wd := lintWorkDir
			if tc.configAbove {
				wd = path.Join(lintWorkDir, "nested")
			}
			if err := fs.MkdirAll(wd, 0o755); err != nil {
				t.Fatal(err)
			}
			if tc.config != "" {
				data, err := afero.ReadFile(fs, "/"+tc.config)
				if err != nil {
					t.Fatal(err)
				}
				if err := afero.WriteFile(fs, path.Join(lintWorkDir, xrdlint.ConfigFileName), data, 0o644); err != nil {
					t.Fatal(err)
				}
			}

			c := lintCmd{fs: fs, wd: wd, Output: tc.output, MaxWarnings: -1, stdin: strings.NewReader("")}
			if c.Output == "" {
				c.Output = string(xrdlint.FormatYAML)
			}
			if tc.maxWarnings != nil {
				c.MaxWarnings = *tc.maxWarnings
			}
			if tc.configFlag != "" {
				c.Config = "/" + tc.configFlag
			}
			for _, in := range tc.inputs {
				if in != "-" {
					in = "/" + in
				}
				c.Paths = append(c.Paths, in)
			}
			if tc.stdin != "" {
				data, err := afero.ReadFile(fs, "/"+tc.stdin)
				if err != nil {
					t.Fatal(err)
				}
				c.stdin = bytes.NewReader(data)
			}

			k := newKongContext(t)
			err := c.Run(k)
			out := k.Stdout.(*bytes.Buffer).String()

			got := lintWant{}
			if err != nil {
				got.err = err.Error()
			}
			switch {
			case tc.output != "":
				got.output = out
			case out != "":
				res := &xrdlint.Result{}
				if err := yaml.Unmarshal([]byte(out), res); err != nil {
					t.Fatalf("cannot decode yaml output: %v\n%s", err, out)
				}
				got.summary = res.Summary
				for _, f := range res.Findings {
					got.findings = append(got.findings, lintFinding{
						Rule: f.Rule, Severity: f.Severity, File: f.File, Line: f.Line, XRD: f.XRD,
						Version: f.Version, Path: f.Path, Message: f.Message, Fix: f.Fix,
					})
				}
			}

			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(lintWant{})); diff != "" {
				t.Errorf("\n%s\nxrd lint: -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

// lintFs returns an in-memory copy of testdata/lint rooted at /, so that paths
// and error messages are the same on every machine, and so that config
// discovery never reaches a .crossplane-lint.yaml outside the fixtures.
func lintFs(t *testing.T) afero.Fs {
	t.Helper()
	mem := afero.NewMemMapFs()
	err := filepath.WalkDir(lintFixtures, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(lintFixtures, p)
		if err != nil {
			return err
		}
		return afero.WriteFile(mem, "/"+filepath.ToSlash(rel), data, 0o644)
	})
	if err != nil {
		t.Fatalf("cannot load lint fixtures: %v", err)
	}
	return mem
}
