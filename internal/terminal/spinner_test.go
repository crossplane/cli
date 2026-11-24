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

package terminal

import (
	"io"
	"strings"
	"testing"
)

// TestMultiSpinner_Keyed drives the keyed entry API directly, without the tea
// program running: Add/UpdateText/Success/Fail mutate the model and View
// renders it, so the rendered lines pin how keys identify entries whose
// titles change over time.
func TestMultiSpinner_Keyed(t *testing.T) {
	t.Parallel()

	type op struct {
		action     string // add, update, success, fail
		key, title string
	}

	tests := map[string]struct {
		reason string
		ops    []op
		// wantLines[i] lists substrings line i must contain (the resolution
		// glyphs are styled, so they are asserted separately from the
		// titles); the view must have exactly this many lines.
		wantLines [][]string
		// wantAbsent must not appear anywhere in the view.
		wantAbsent []string
	}{
		"AddCreatesOneLinePerKey": {
			reason: "Each new key adds one line; re-adding a key must not duplicate it.",
			ops: []op{
				{"add", "a", "first"},
				{"add", "b", "second"},
				{"add", "a", "first again"},
			},
			wantLines:  [][]string{{"first"}, {"second"}},
			wantAbsent: []string{"first again"},
		},
		"UpdateTextRetitlesInPlace": {
			reason: "An update under the same key replaces the title on the existing line instead of adding one.",
			ops: []op{
				{"add", "sim", "Simulating changes"},
				{"update", "sim", "Simulating changes (1/3 composites: x)"},
				{"update", "sim", "Simulating changes (2/3 composites: y)"},
			},
			wantLines:  [][]string{{"Simulating changes (2/3 composites: y)"}},
			wantAbsent: []string{"(1/3"},
		},
		"UpdateTextUnknownKeyIsNoop": {
			reason: "An update for a key that was never added must not create a line.",
			ops: []op{
				{"add", "a", "first"},
				{"update", "ghost", "never shown"},
			},
			wantLines:  [][]string{{"first"}},
			wantAbsent: []string{"never shown"},
		},
		"SuccessResolvesAndRetitles": {
			reason: "Success marks the keyed line with a checkmark and restores the given title, so a progress line resolves back to its base text.",
			ops: []op{
				{"add", "sim", "Simulating changes"},
				{"update", "sim", "Simulating changes (3/3 composites: z)"},
				{"success", "sim", "Simulating changes"},
			},
			wantLines:  [][]string{{"✓", "Simulating changes"}},
			wantAbsent: []string{"(3/3"},
		},
		"FailResolvesWithCross": {
			reason: "Fail marks the keyed line with a cross.",
			ops: []op{
				{"add", "a", "doomed"},
				{"fail", "a", "doomed"},
			},
			wantLines: [][]string{{"✗", "doomed"}},
		},
		"TextKeyedEntriesUnaffected": {
			reason: "Callers keying by text (key == title) keep their behavior; resolving one entry leaves the others running.",
			ops: []op{
				{"add", "step one", "step one"},
				{"add", "step two", "step two"},
				{"success", "step one", "step one"},
			},
			wantLines: [][]string{{"✓", "step one"}, {"step two"}},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			m := &MultiSpinner{out: io.Discard}
			for _, o := range tt.ops {
				switch o.action {
				case "add":
					m.Add(o.key, o.title)
				case "update":
					m.UpdateText(o.key, o.title)
				case "success":
					m.Success(o.key, o.title)
				case "fail":
					m.Fail(o.key, o.title)
				}
			}

			content := m.View().Content
			lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
			if len(lines) == 1 && lines[0] == "" {
				lines = nil
			}

			if len(lines) != len(tt.wantLines) {
				t.Fatalf("\n%s\nView(): got %d lines, want %d:\n%s", tt.reason, len(lines), len(tt.wantLines), content)
			}
			for i, wants := range tt.wantLines {
				for _, want := range wants {
					if !strings.Contains(lines[i], want) {
						t.Errorf("\n%s\nView(): line %d = %q, want it to contain %q", tt.reason, i, lines[i], want)
					}
				}
			}
			for _, absent := range tt.wantAbsent {
				if strings.Contains(content, absent) {
					t.Errorf("\n%s\nView(): must not contain %q:\n%s", tt.reason, absent, content)
				}
			}
		})
	}
}
