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
	"fmt"
	"sort"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	"github.com/crossplane/cli/v2/internal/crd"
)

// A Runner applies a registry's rules to XRDs.
type Runner struct {
	reg      *Registry
	settings map[string]Setting
}

// NewRunner returns a Runner that applies the rules in reg with the supplied
// settings, as returned by Config.Resolve.
func NewRunner(reg *Registry, settings map[string]Setting) *Runner {
	return &Runner{reg: reg, settings: settings}
}

// Run lints the XRDs. It never stops early: a rule that fails is reported as
// lint/rule-error and the other rules still run.
func (r *Runner) Run(xrds []*XRD) *Result {
	res := &Result{Findings: []Finding{}}
	for _, x := range xrds {
		fs := r.lint(x)
		sort.SliceStable(fs, func(i, j int) bool {
			if fs[i].Line != fs[j].Line {
				return fs[i].Line < fs[j].Line
			}
			if fs[i].Column != fs[j].Column {
				return fs[i].Column < fs[j].Column
			}
			if fs[i].Rule != fs[j].Rule {
				return fs[i].Rule < fs[j].Rule
			}
			return fs[i].Message < fs[j].Message
		})
		res.Findings = append(res.Findings, fs...)
	}
	for _, f := range res.Findings {
		switch f.Severity {
		case SeverityError:
			res.Summary.Errors++
		case SeverityWarn:
			res.Summary.Warnings++
		case SeverityOff:
		}
	}
	return res
}

func (r *Runner) lint(x *XRD) []Finding {
	if err := validate(x); err != nil {
		return []Finding{rootFinding(x, RuleInvalidXRD, err.Error())}
	}

	ds := collectDirectives(x.doc)
	var out []Finding
	for _, rule := range r.reg.All() {
		m := rule.Meta()
		s := r.settings[m.ID]
		if s.Severity == SeverityOff || s.Severity == "" {
			continue
		}

		ctx := &Context{meta: m, severity: s.Severity, options: s.Options, xrd: x}
		if err := check(rule, ctx, x); err != nil {
			out = append(out, rootFinding(x, RuleError, fmt.Sprintf("rule %s failed to run: %v", m.ID, err)))
		}
		for _, rep := range ctx.reported {
			if ds.suppress(rep.finding.Rule, rep.anchors) {
				continue
			}
			out = append(out, rep.finding)
		}
	}
	return append(out, ds.unused(x)...)
}

// check runs a rule, turning a panic into an error.
func check(rule SchemaRule, ctx *Context, x *XRD) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = errors.Errorf("panic: %v", p)
		}
	}()
	return rule.Check(ctx, x)
}

// validate applies the structural check from crossplane xrd convert. An XRD
// that fails it is invalid, and is not linted further.
func validate(x *XRD) error {
	_, _, err := crd.FromXRD(x.Object)
	return err
}

// rootFinding returns an error finding that attaches to the document root.
func rootFinding(x *XRD, rule, msg string) Finding {
	line, col := x.rootPosition()
	return Finding{
		Rule:     rule,
		Severity: SeverityError,
		File:     x.File,
		Line:     line,
		Column:   col,
		XRD:      x.Name,
		Message:  msg,
	}
}
