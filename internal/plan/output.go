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

package plan

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

// The output formats simulate commands share. A unified diff format
// deliberately does not exist yet: crossplane-diff renders real
// full-manifest unified diffs today, and that renderer is what a future
// -o diff should be once its machinery moves into this repo, rather than a
// lower-fidelity imitation built from the plan layer's field changes.
const (
	FormatPlan = "plan"
	FormatJSON = "json"
	FormatYAML = "yaml"
)

// Summary counts plans by what they would do.
type Summary struct {
	Create      int `json:"create"`
	Update      int `json:"update"`
	Destroy     int `json:"destroy"`
	Replace     int `json:"replace"`
	NoOp        int `json:"noop"`
	Approximate int `json:"approximate"`
	Errored     int `json:"errored"`
}

// Summarize counts the given plans.
func Summarize(plans []*ResourcePlan) Summary {
	var s Summary
	for _, p := range plans {
		if p.Error != "" {
			s.Errored++
			continue
		}
		if p.Approximate {
			s.Approximate++
		}
		switch p.Action {
		case ActionCreate:
			s.Create++
		case ActionUpdate:
			s.Update++
		case ActionDelete:
			s.Destroy++
		case ActionReplace:
			s.Replace++
		case ActionNoOp:
			s.NoOp++
		}
	}
	return s
}

// ResultFormatVersion versions the serialized result schema so automation
// consuming it can detect incompatible changes. The schema is alpha; the
// version moves to 1.0 when it stabilizes.
const ResultFormatVersion = "0.1"

// Result is the serialized form of a simulate run, for -o json|yaml. Errored
// is the primary gate for wrapping automation: when true, at least one
// resource could not be planned and the run's answer is incomplete.
type Result struct {
	FormatVersion string          `json:"formatVersion"`
	Errored       bool            `json:"errored"`
	Summary       Summary         `json:"summary"`
	Plans         []*ResourcePlan `json:"plans"`
}

// NewResult assembles the serializable Result for the given plans.
func NewResult(plans []*ResourcePlan) Result {
	s := Summarize(plans)
	return Result{
		FormatVersion: ResultFormatVersion,
		Errored:       s.Errored > 0,
		Summary:       s,
		Plans:         plans,
	}
}

// A Renderer writes resource plans to w in one output format.
type Renderer interface {
	Render(w io.Writer, plans []*ResourcePlan) error
}

// NewRenderer returns the Renderer for the given output format.
func NewRenderer(format string) (Renderer, error) {
	switch format {
	case FormatPlan:
		return planRenderer{}, nil
	case FormatJSON:
		return jsonRenderer{}, nil
	case FormatYAML:
		return yamlRenderer{}, nil
	}
	return nil, errors.Errorf("unknown output format %q", format)
}

// jsonRenderer renders the serialized Result as indented JSON.
type jsonRenderer struct{}

// Render writes the plans to w as JSON.
func (jsonRenderer) Render(w io.Writer, plans []*ResourcePlan) error {
	out, err := json.MarshalIndent(NewResult(plans), "", "  ")
	if err != nil {
		return errors.Wrap(err, "cannot marshal plans to JSON")
	}
	_, err = fmt.Fprintln(w, string(out))
	return err
}

// yamlRenderer renders the serialized Result as YAML.
type yamlRenderer struct{}

// Render writes the plans to w as YAML.
func (yamlRenderer) Render(w io.Writer, plans []*ResourcePlan) error {
	out, err := yaml.Marshal(NewResult(plans))
	if err != nil {
		return errors.Wrap(err, "cannot marshal plans to YAML")
	}
	_, err = fmt.Fprint(w, string(out))
	return err
}

// planRenderer renders the human-readable plan format: a header and change
// tree per resource, then a summary with warnings.
type planRenderer struct{}

// Render writes the plans to w in the human-readable plan format.
func (planRenderer) Render(w io.Writer, plans []*ResourcePlan) error {
	for _, p := range plans {
		_, _ = fmt.Fprintln(w, header(p))
		if p.Error != "" {
			_, _ = fmt.Fprintf(w, "    cannot plan: %s\n", p.Error)
			_, _ = fmt.Fprintln(w)
			continue
		}
		if p.Approximate {
			_, _ = fmt.Fprintf(w, "    (approximate: %s)\n", p.FallbackReason)
		}
		renderChangeTree(w, p.Changes)
		_, _ = fmt.Fprintln(w)
	}
	renderSummary(w, plans)
	return nil
}

// header returns the per-resource line, e.g.
// "[~] Key.kms.aws.upbound.io my-key (update)".
func header(p *ResourcePlan) string {
	gvk := p.GroupVersionKind()
	name := p.Name
	if p.Namespace != "" {
		name = p.Namespace + "/" + name
	}
	what := string(p.Action)
	if p.Error != "" {
		what = "error"
	}
	return fmt.Sprintf("%s %s.%s %s (%s)", symbol(p), gvk.Kind, gvk.Group, name, what)
}

func symbol(p *ResourcePlan) string {
	if p.Error != "" {
		return "[!]"
	}
	if p.Approximate {
		return "[?]"
	}
	switch p.Action {
	case ActionCreate:
		return "[+]"
	case ActionUpdate:
		return "[~]"
	case ActionReplace:
		return "[-/+]"
	case ActionDelete:
		return "[-]"
	case ActionNoOp:
		return "[=]"
	}
	return "[?]"
}

// renderChangeTree prints changes as the user's manifest, annotated: nested
// field names as YAML-style keys, changed leaves marked inline.
func renderChangeTree(w io.Writer, changes []FieldChange) {
	var prev []string
	for _, c := range sortedChanges(changes) {
		segs := strings.Split(c.Path, ".")
		if len(segs) == 0 {
			continue
		}
		parents, leaf := segs[:len(segs)-1], segs[len(segs)-1]

		// Print the parent keys that differ from the previous change's path.
		common := 0
		for common < len(parents) && common < len(prev) && parents[common] == prev[common] {
			common++
		}
		for i := common; i < len(parents); i++ {
			_, _ = fmt.Fprintf(w, "    %s%s:\n", strings.Repeat("  ", i), parents[i])
		}
		prev = parents

		indent := "    " + strings.Repeat("  ", len(parents))
		note := ""
		if c.RequiresReplace {
			note = "  # forces replacement"
		}
		switch {
		case c.Actual != nil && c.Planned != nil:
			_, _ = fmt.Fprintf(w, "%s~ %s: %s -> %s%s\n", indent, leaf, formatValue(c.Actual), formatValue(c.Planned), note)
		case c.Planned != nil:
			_, _ = fmt.Fprintf(w, "%s+ %s: %s%s\n", indent, leaf, formatValue(c.Planned), note)
		case c.Actual != nil:
			_, _ = fmt.Fprintf(w, "%s- %s: %s%s\n", indent, leaf, formatValue(c.Actual), note)
		}
	}
}

func renderSummary(w io.Writer, plans []*ResourcePlan) {
	s := Summarize(plans)

	line := fmt.Sprintf("Plan: %d to add, %d to change, %d to destroy", s.Create, s.Update, s.Destroy)
	if s.Replace > 0 {
		verb := "requires"
		if s.Replace > 1 {
			verb = "require"
		}
		line += fmt.Sprintf(", %d %s replacement", s.Replace, verb)
	}
	_, _ = fmt.Fprintln(w, line+".")

	if s.Replace > 0 {
		fields := replaceFields(plans)
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintf(w, "Warning: %s force replacement: %s.\n", pluralize(s.Replace, "resource has fields that", "resources have fields that"), strings.Join(fields, ", "))
		_, _ = fmt.Fprintln(w, "Crossplane providers never destroy and recreate to apply a change.")
		_, _ = fmt.Fprintln(w, "Applying this leaves the resource unsynced until it is replaced manually.")
	}

	if s.Approximate > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintf(w, "Warning: %s approximate, computed client-side without a provider diff\n", pluralize(s.Approximate, "result is", "results are"))
		_, _ = fmt.Fprintln(w, "server: no replacement detection, no computed values.")
	}

	if s.Errored > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintf(w, "Warning: %s could not be planned.\n", pluralize(s.Errored, "resource", "resources"))
	}
}

func pluralize(n int, singular, plural string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %s", n, plural)
}

// replaceFields returns the deduplicated, sorted field names (last path
// segments) that force replacement across all plans.
func replaceFields(plans []*ResourcePlan) []string {
	set := map[string]struct{}{}
	for _, p := range plans {
		for _, f := range lastSegments(p.ReplacePaths) {
			set[f] = struct{}{}
		}
	}
	return slices.Sorted(maps.Keys(set))
}

func lastSegments(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		segs := strings.Split(p, ".")
		out = append(out, segs[len(segs)-1])
	}
	return out
}

func sortedChanges(changes []FieldChange) []FieldChange {
	out := make([]FieldChange, len(changes))
	copy(out, changes)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// formatValue renders one side of a change for humans.
func formatValue(v *Value) string {
	switch {
	case v == nil:
		return "(none)"
	case v.Sensitive:
		return "(sensitive value)"
	case v.Unknown:
		return "(known after apply)"
	case v.Unresolved:
		return "(unresolved reference)"
	}
	switch t := v.Value.(type) {
	case nil:
		return "null"
	case string:
		return fmt.Sprintf("%q", t)
	case map[string]any, []any:
		out, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprintf("%v", t)
		}
		return string(out)
	default:
		return fmt.Sprintf("%v", t)
	}
}
