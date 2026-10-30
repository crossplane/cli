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
	"regexp"
	"sort"
	"strings"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

// reservedPrefix is the prefix of the rules the linter reports itself.
const reservedPrefix = "lint/"

var ruleID = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*/[a-z0-9]+(-[a-z0-9]+)*$`)

// A Registry holds the rules a run can use, keyed by ID.
type Registry struct {
	rules map[string]SchemaRule
}

// NewRegistry returns a Registry holding the supplied rules. It returns an
// error if two rules share an ID, or if a rule's ID or default severity is
// malformed.
func NewRegistry(rules ...SchemaRule) (*Registry, error) {
	r := &Registry{rules: make(map[string]SchemaRule, len(rules))}
	for _, rule := range rules {
		m := rule.Meta()
		if !ruleID.MatchString(m.ID) {
			return nil, errors.Errorf("rule ID %q must be <prefix>/<name> in lower-case kebab case", m.ID)
		}
		if strings.HasPrefix(m.ID, reservedPrefix) {
			return nil, errors.Errorf("rule ID %q uses the reserved prefix %q", m.ID, reservedPrefix)
		}
		if _, err := ParseSeverity(string(m.DefaultSeverity)); err != nil && m.DefaultSeverity != "" {
			return nil, errors.Wrapf(err, "rule %q", m.ID)
		}
		if _, ok := r.rules[m.ID]; ok {
			return nil, errors.Errorf("rule %q is registered twice", m.ID)
		}
		r.rules[m.ID] = rule
	}
	return r, nil
}

// Get returns the rule with the supplied ID.
func (r *Registry) Get(id string) (SchemaRule, bool) {
	rule, ok := r.rules[id]
	return rule, ok
}

// All returns every rule, ordered by ID.
func (r *Registry) All() []SchemaRule {
	out := make([]SchemaRule, 0, len(r.rules))
	for _, rule := range r.rules {
		out = append(out, rule)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Meta().ID < out[j].Meta().ID })
	return out
}
