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

package health

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Marker flags an object that is not healthy in text output. The diff
// renderer's text output already uses non-ASCII status glyphs, and the marker
// is not a colour, so it stays with colours off.
const Marker = "⚠"

// BlockHeading heads the block listing a composite's resources that are not
// healthy and have no changes.
const BlockHeading = "Unhealthy resources without changes:"

const (
	// messageRunes is how many runes of a condition message a one-line
	// description keeps, its ellipsis included.
	messageRunes = 120
	// wrapRunes is the width a full message is wrapped to.
	wrapRunes = 100
)

// Tier returns the marker and the tier of h alone, e.g. "⚠ failing", or "" when
// h is nil or healthy.
func Tier(h *Health) string {
	if !h.Unhealthy() {
		return ""
	}

	name, _, _ := tier(h)

	return Marker + " " + name
}

// tier returns the name of h's tier, and the condition it was read from as
// "Type=Status" with that condition. A paused object has no condition, as its
// conditions are stale.
func tier(h *Health) (name, cond string, c *Condition) {
	switch h.State {
	case Failing:
		return "failing", "Synced=False", h.Synced
	case NotReady:
		return "not ready", "Ready=False", h.Ready
	case Paused, Healthy:
	}

	return "paused", "", nil
}

// Lines describes h for text output, or returns nil when h is nil or healthy.
// The first line is the marker, the tier, the condition it was read from with
// that condition's reason, and its message cut to one line, e.g.
// "⚠ failing (Synced=False, ReconcileError): cannot update". With full, the
// message instead follows in full, wrapped, one element per line. A paused
// object's conditions are stale, so it is described by its tier alone.
func Lines(h *Health, full bool) []string {
	if !h.Unhealthy() {
		return nil
	}

	name, cond, c := tier(h)
	if cond == "" {
		return []string{Marker + " " + name}
	}

	line := Marker + " " + name + " (" + cond

	if c == nil {
		return []string{line + ")"}
	}

	if c.Reason != "" {
		line += ", " + c.Reason
	}

	line += ")"

	switch {
	case strings.TrimSpace(c.Message) == "":
		return []string{line}
	case full:
		return append([]string{line}, wrap(c.Message)...)
	default:
		return []string{line + ": " + truncate(c.Message)}
	}
}

// Item is a resource a block of unhealthy resources lists.
type Item struct {
	// ID identifies the resource, to match another item's Via against.
	ID string
	// Name is how the block prints the resource.
	Name string
	// Via is the ID of the nested composite that composes the resource, and
	// ViaName how the block prints it. Both are empty when the block's own
	// composite composes it.
	Via, ViaName string

	Health *Health
}

// Block returns the lines of the block listing items, a composite's resources
// that are not healthy and have no changes, or nil when there are none. The
// heading is unindented and each item is indented two spaces per level below
// it, with any wrapped message four more.
//
// An item a nested composite composes is nested under that composite: under
// its own item, or, for a composite listed elsewhere with changes, under a
// line naming it. listed maps the IDs of the resources listed elsewhere to that
// line. An item whose nested composite is neither names it, as "(via ...)".
func Block(items []Item, listed map[string]string, full bool) []string {
	if len(items) == 0 {
		return nil
	}

	ids := make(map[string]bool, len(items))
	for _, it := range items {
		ids[it.ID] = true
	}

	nested := func(it Item) bool {
		return it.Via != "" && (ids[it.Via] || listed[it.Via] != "")
	}

	children := map[string][]Item{}

	for _, it := range items {
		if nested(it) {
			children[it.Via] = append(children[it.Via], it)
		}
	}

	lines := []string{BlockHeading}
	done := map[string]bool{}

	var add func(it Item, depth int)

	add = func(it Item, depth int) {
		if done[it.ID] {
			return
		}

		done[it.ID] = true
		indent := strings.Repeat("  ", depth+1)

		name := it.Name
		if it.Via != "" && !nested(it) {
			name += " (via " + it.ViaName + ")"
		}

		desc := Lines(it.Health, full)
		if len(desc) == 0 {
			lines = append(lines, indent+name)
		} else {
			lines = append(lines, indent+name+" "+desc[0])
			for _, l := range desc[1:] {
				lines = append(lines, indent+"    "+l)
			}
		}

		for _, c := range children[it.ID] {
			add(c, depth+1)
		}
	}

	shown := map[string]bool{}

	for _, it := range items {
		switch {
		case !nested(it):
			add(it, 0)
		case !ids[it.Via] && !shown[it.Via]:
			// A composite listed elsewhere heads its resources here too.
			shown[it.Via] = true
			lines = append(lines, "  "+listed[it.Via])

			for _, c := range children[it.Via] {
				add(c, 1)
			}
		}
	}

	// Items whose nesting loops back on itself have no root; list them
	// rather than lose them.
	for _, it := range items {
		add(it, 0)
	}

	return lines
}

// truncate returns msg on one line, whitespace collapsed to single spaces,
// cut to messageRunes with an ellipsis.
func truncate(msg string) string {
	msg = strings.Join(strings.Fields(msg), " ")
	if utf8.RuneCountInString(msg) <= messageRunes {
		return msg
	}

	return string([]rune(msg)[:messageRunes-1]) + "…"
}

// wrap returns msg's lines, blank ones dropped, each wrapped at word
// boundaries to wrapRunes. A word longer than that has a line to itself.
func wrap(msg string) []string {
	var out []string

	for line := range strings.SplitSeq(msg, "\n") {
		cur := ""

		for w := range strings.FieldsSeq(line) {
			if cur != "" && utf8.RuneCountInString(cur)+1+utf8.RuneCountInString(w) > wrapRunes {
				out = append(out, cur)
				cur = ""
			}

			if cur != "" {
				cur += " "
			}

			cur += w
		}

		if cur != "" {
			out = append(out, cur)
		}
	}

	return out
}

// Key identifies a resource across API versions, for matching an Item's Via
// against the resource it names.
func Key(apiVersion, kind, namespace, name string) string {
	gk := schema.FromAPIVersionAndKind(apiVersion, kind).GroupKind()
	return gk.String() + "/" + namespace + "/" + name
}

// Tally counts a run's health: the composites it lists, and the other
// resources by state, each once. Resources are matched by Key, and the first
// health a resource is added with wins. A composite added as a resource is
// counted only as a composite, whichever was added first.
type Tally struct {
	composites map[string]bool
	resources  map[string]State
	order      []string
}

// Composite adds a composite with key and health h.
func (t *Tally) Composite(key string, h *Health) {
	if t.composites == nil {
		t.composites = map[string]bool{}
	}

	if _, ok := t.composites[key]; !ok {
		t.composites[key] = h.Unhealthy()
	}
}

// Resource adds a resource with key and health h. A resource with no health
// is not counted.
func (t *Tally) Resource(key string, h *Health) {
	if h == nil {
		return
	}

	if t.resources == nil {
		t.resources = map[string]State{}
	}

	if _, ok := t.resources[key]; !ok {
		t.resources[key] = h.State
		t.order = append(t.order, key)
	}
}

// Counts returns what t counted.
func (t *Tally) Counts() Counts {
	c := Counts{Composites: len(t.composites)}

	for _, unhealthy := range t.composites {
		if unhealthy {
			c.UnhealthyComposites++
		}
	}

	for _, key := range t.order {
		if _, ok := t.composites[key]; ok {
			continue
		}

		switch t.resources[key] {
		case Failing:
			c.Failing++
		case NotReady:
			c.NotReady++
		case Paused:
			c.Paused++
		case Healthy:
		}
	}

	return c
}

// Counts are a run's health counts, for its summary line.
type Counts struct {
	// Composites is how many composites the run listed, and
	// UnhealthyComposites how many of those are not healthy.
	Composites, UnhealthyComposites int
	// Failing, NotReady and Paused count the other resources by state.
	Failing, NotReady, Paused int
}

// SummaryLine returns the line summarizing c, e.g. "Health: 1 of 2 XRs
// unhealthy before apply; 12 resources failing (Synced=False), 2 not ready,
// 1 paused.", with only the non-zero parts, or "" when nothing is unhealthy.
func SummaryLine(c Counts) string {
	var parts []string

	if c.UnhealthyComposites > 0 {
		noun := "XRs"
		if c.Composites == 1 {
			noun = "XR"
		}

		parts = append(parts, fmt.Sprintf("%d of %d %s unhealthy before apply", c.UnhealthyComposites, c.Composites, noun))
	}

	var resources []string

	for _, n := range []struct {
		count int
		what  string
	}{
		{c.Failing, "failing (Synced=False)"},
		{c.NotReady, "not ready"},
		{c.Paused, "paused"},
	} {
		if n.count == 0 {
			continue
		}

		if len(resources) == 0 {
			noun := "resources"
			if n.count == 1 {
				noun = "resource"
			}

			resources = append(resources, fmt.Sprintf("%d %s %s", n.count, noun, n.what))

			continue
		}

		resources = append(resources, fmt.Sprintf("%d %s", n.count, n.what))
	}

	if len(resources) > 0 {
		parts = append(parts, strings.Join(resources, ", "))
	}

	if len(parts) == 0 {
		return ""
	}

	return "Health: " + strings.Join(parts, "; ") + "."
}
