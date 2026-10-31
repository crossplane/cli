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
	"strings"

	"gopkg.in/yaml.v3"
)

// disableNextLine is the directive that suppresses findings on the node that
// starts on the next line, and on everything beneath it.
const disableNextLine = "crossplane-lint-disable-next-line"

// A directive is one disable comment.
type directive struct {
	line   int
	column int
	// rules maps each rule the directive names to whether it suppressed
	// anything.
	rules map[string]bool
	order []string
}

// directives indexes disable comments by the node they precede.
type directives map[*yaml.Node][]*directive

// collectDirectives finds every disable comment in a document.
func collectDirectives(doc *yaml.Node) directives {
	out := directives{}
	var visit func(n *yaml.Node)
	visit = func(n *yaml.Node) {
		out[n] = append(out[n], parseDirectives(n)...)
		for _, c := range n.Content {
			visit(c)
		}
	}
	visit(doc)
	return out
}

// parseDirectives reads the disable comments in a node's head comment, in the
// form "# crossplane-lint-disable-next-line rule-a, rule-b -- reason".
func parseDirectives(n *yaml.Node) []*directive {
	if n.HeadComment == "" {
		return nil
	}
	base := n.Line
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		base = n.Content[0].Line
	}

	lines := strings.Split(n.HeadComment, "\n")
	var out []*directive
	for i, l := range lines {
		text := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "#"))
		rest, ok := strings.CutPrefix(text, disableNextLine)
		if !ok || (rest != "" && rest[0] != ' ') {
			continue
		}
		rest, _, _ = strings.Cut(rest, "--")
		d := &directive{line: base - (len(lines) - i), column: n.Column, rules: map[string]bool{}}
		for r := range strings.SplitSeq(rest, ",") {
			r = strings.TrimSpace(r)
			if r == "" {
				continue
			}
			if _, dup := d.rules[r]; !dup {
				d.order = append(d.order, r)
			}
			d.rules[r] = false
		}
		out = append(out, d)
	}
	return out
}

// suppress reports whether a directive on any of the anchors names rule, and
// marks every such directive as used.
func (ds directives) suppress(rule string, a anchors) bool {
	if strings.HasPrefix(rule, reservedPrefix) {
		return false
	}
	suppressed := false
	for _, n := range a {
		for _, d := range ds[n] {
			if _, ok := d.rules[rule]; ok {
				d.rules[rule] = true
				suppressed = true
			}
		}
	}
	return suppressed
}

// unused returns a lint/unused-disable finding for each rule a directive
// names that suppressed nothing.
func (ds directives) unused(x *XRD) []Finding {
	var out []Finding
	for _, list := range ds {
		for _, d := range list {
			if len(d.order) == 0 {
				out = append(out, unusedFinding(x, d, "disable comment names no rules"))
				continue
			}
			for _, r := range d.order {
				if d.rules[r] {
					continue
				}
				msg := fmt.Sprintf("disable comment for %s suppressed nothing", r)
				if strings.HasPrefix(r, reservedPrefix) {
					msg = fmt.Sprintf("%s cannot be suppressed", r)
				}
				out = append(out, unusedFinding(x, d, msg))
			}
		}
	}
	return out
}

func unusedFinding(x *XRD, d *directive, msg string) Finding {
	return Finding{
		Rule:     RuleUnusedDisable,
		Severity: SeverityWarn,
		File:     x.File,
		Line:     d.line,
		Column:   d.column,
		XRD:      x.Name,
		Message:  msg,
	}
}
