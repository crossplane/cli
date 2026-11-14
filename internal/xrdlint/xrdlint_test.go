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
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/afero"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

// The behaviour of the linter is tested by the fixture suite that drives
// `crossplane xrd lint`, in cmd/crossplane/xrd/testdata/lint. The tests here
// cover only what a fixture file cannot express: rules that fail or panic,
// option schemas (no built-in rule takes options yet), and the Go API's own
// validation of rule IDs.

// fakeRule is a SchemaRule whose behaviour is set by the test.
type fakeRule struct {
	meta  Meta
	check func(ctx *Context, xrd *XRD) error
}

func (r fakeRule) Meta() Meta { return r.meta }

func (r fakeRule) Check(ctx *Context, xrd *XRD) error {
	if r.check == nil {
		return nil
	}
	return r.check(ctx, xrd)
}

// reportBools reports every boolean property.
func reportBools(ctx *Context, xrd *XRD) error {
	return xrd.Walk(func(p Property) error {
		if p.Schema.Type == "boolean" {
			ctx.Report(p, "prefer an enum")
		}
		return nil
	})
}

func loadFixture(t *testing.T) []*XRD {
	t.Helper()
	xrds, err := Load(afero.NewOsFs(), nil, []string{"testdata/xrd.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	return xrds
}

// TestRuleError covers lint/rule-error. No built-in rule fails or panics, so
// the failing rules are defined here.
func TestRuleError(t *testing.T) {
	type got struct {
		Rule     string
		Severity Severity
		Path     string
		Message  string
	}

	cases := map[string]struct {
		reason string
		broken SchemaRule
		want   []got
	}{
		"ReturnsError": {
			reason: "A warn rule that returns an error is reported as an error-severity lint/rule-error, and the other rules still report.",
			broken: fakeRule{
				meta:  Meta{ID: "xrd/broken", DefaultSeverity: SeverityWarn},
				check: func(*Context, *XRD) error { return errors.New("boom") },
			},
			want: []got{
				{Rule: RuleError, Severity: SeverityError, Message: "rule xrd/broken failed to run: boom"},
				{Rule: "xrd/bools", Severity: SeverityWarn, Path: "spec.public", Message: "prefer an enum"},
			},
		},
		"Panics": {
			reason: "A rule that panics is reported as lint/rule-error rather than crashing the run.",
			broken: fakeRule{
				meta:  Meta{ID: "xrd/broken", DefaultSeverity: SeverityWarn},
				check: func(*Context, *XRD) error { panic("oops") },
			},
			want: []got{
				{Rule: RuleError, Severity: SeverityError, Message: "rule xrd/broken failed to run: panic: oops"},
				{Rule: "xrd/bools", Severity: SeverityWarn, Path: "spec.public", Message: "prefer an enum"},
			},
		},
		"ReportsThenFails": {
			reason: "Findings a rule reported before failing are kept alongside its lint/rule-error.",
			broken: fakeRule{
				meta: Meta{ID: "xrd/broken", DefaultSeverity: SeverityWarn},
				check: func(ctx *Context, xrd *XRD) error {
					if err := reportBools(ctx, xrd); err != nil {
						return err
					}
					return errors.New("boom")
				},
			},
			want: []got{
				{Rule: RuleError, Severity: SeverityError, Message: "rule xrd/broken failed to run: boom"},
				{Rule: "xrd/bools", Severity: SeverityWarn, Path: "spec.public", Message: "prefer an enum"},
				{Rule: "xrd/broken", Severity: SeverityWarn, Path: "spec.public", Message: "prefer an enum"},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			reg, err := NewRegistry(
				fakeRule{meta: Meta{ID: "xrd/bools", DefaultSeverity: SeverityWarn}, check: reportBools},
				tc.broken,
			)
			if err != nil {
				t.Fatal(err)
			}
			settings, err := (*Config)(nil).Resolve(reg)
			if err != nil {
				t.Fatal(err)
			}

			res := NewRunner(reg, settings).Run(loadFixture(t))

			gotFindings := make([]got, 0, len(res.Findings))
			for _, f := range res.Findings {
				gotFindings = append(gotFindings, got{Rule: f.Rule, Severity: f.Severity, Path: f.Path, Message: f.Message})
			}
			if diff := cmp.Diff(tc.want, gotFindings); diff != "" {
				t.Errorf("\n%s\nRun(): -want, +got:\n%s", tc.reason, diff)
			}
			if !res.Failed(-1) {
				t.Errorf("\n%s\nFailed(-1): a lint/rule-error must fail the run", tc.reason)
			}
		})
	}
}

// TestResolveOptions covers option validation. No built-in rule takes
// options yet, so the rule that declares an options schema is defined here.
func TestResolveOptions(t *testing.T) {
	reg, err := NewRegistry(fakeRule{meta: Meta{
		ID:              "xrd/limited",
		DefaultSeverity: SeverityOff,
		Options: &extv1.JSONSchemaProps{
			Type: "object",
			Properties: map[string]extv1.JSONSchemaProps{
				"max": {Type: "integer", Minimum: new(1.0)},
			},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string]struct {
		reason  string
		options map[string]any
		want    []byte
		err     string
	}{
		"Valid": {
			reason:  "Options that satisfy the schema are passed to the rule as JSON.",
			options: map[string]any{"max": 5},
			want:    []byte(`{"max":5}`),
		},
		"WrongType": {
			reason:  "An option of the wrong type is a config error.",
			options: map[string]any{"max": "big"},
			err:     `invalid options for rule "xrd/limited"`,
		},
		"BelowMinimum": {
			reason:  "An option that breaks a schema constraint is a config error.",
			options: map[string]any{"max": 0},
			err:     `invalid options for rule "xrd/limited"`,
		},
		"UnknownOption": {
			reason:  "An option the schema does not declare is a config error.",
			options: map[string]any{"maxx": 3},
			err:     `unknown option "maxx"`,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := &Config{Rules: map[string]RuleConfig{"xrd/limited": {Severity: SeverityWarn, Options: tc.options}}}
			got, err := cfg.Resolve(reg)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("\n%s\nResolve(): want error containing %q, got %v", tc.reason, tc.err, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("\n%s\nResolve(): unexpected error: %v", tc.reason, err)
			}
			if diff := cmp.Diff(tc.want, got["xrd/limited"].Options); diff != "" {
				t.Errorf("\n%s\nResolve(): -want options, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

// TestNewRegistry covers the Go API's validation of the rules it is given.
// Built-in rules are always well formed, so malformed ones are defined here.
func TestNewRegistry(t *testing.T) {
	cases := map[string]struct {
		reason string
		rules  []SchemaRule
		ok     bool
	}{
		"Valid":           {reason: "Distinct, well-formed IDs register.", rules: []SchemaRule{fakeRule{meta: Meta{ID: "xrd/a"}}, fakeRule{meta: Meta{ID: "acme/b-c"}}}, ok: true},
		"Duplicate":       {reason: "Two rules cannot share an ID.", rules: []SchemaRule{fakeRule{meta: Meta{ID: "xrd/a"}}, fakeRule{meta: Meta{ID: "xrd/a"}}}},
		"NoPrefix":        {reason: "An ID needs a prefix.", rules: []SchemaRule{fakeRule{meta: Meta{ID: "a"}}}},
		"NotKebab":        {reason: "An ID is lower-case kebab case.", rules: []SchemaRule{fakeRule{meta: Meta{ID: "xrd/noBools"}}}},
		"ReservedLint":    {reason: "The lint/ prefix is reserved for the linter.", rules: []SchemaRule{fakeRule{meta: Meta{ID: "lint/x"}}}},
		"InvalidSeverity": {reason: "A default severity must be a known severity.", rules: []SchemaRule{fakeRule{meta: Meta{ID: "xrd/a", DefaultSeverity: "fatal"}}}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := NewRegistry(tc.rules...)
			if (err == nil) != tc.ok {
				t.Errorf("\n%s\nNewRegistry(): want ok=%t, got err=%v", tc.reason, tc.ok, err)
			}
		})
	}
}
