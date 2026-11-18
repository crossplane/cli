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
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestLines(t *testing.T) {
	long := strings.Repeat("word ", 40) // 200 runes
	failing := func(msg string) *Health {
		return &Health{State: Failing, Synced: &Condition{Status: "False", Reason: "ReconcileError", Message: msg}}
	}

	type args struct {
		h    *Health
		full bool
	}
	cases := map[string]struct {
		reason string
		args   args
		want   []string
	}{
		"Nil": {
			reason: "No live object prints nothing.",
		},
		"Healthy": {
			reason: "A healthy object prints nothing, so healthy output is unchanged.",
			args:   args{h: &Health{State: Healthy}},
		},
		"Failing": {
			reason: "A failing object names the tier, the Synced condition's reason and its message.",
			args:   args{h: failing("cannot update: denied")},
			want:   []string{"⚠ failing (Synced=False, ReconcileError): cannot update: denied"},
		},
		"NotReady": {
			reason: "A not-ready object is described by its Ready condition.",
			args:   args{h: &Health{State: NotReady, Ready: &Condition{Status: "False", Reason: "Creating"}}},
			want:   []string{"⚠ not ready (Ready=False, Creating)"},
		},
		"Paused": {
			reason: "A paused object's conditions are stale, so it is described by its tier alone.",
			args:   args{h: &Health{State: Paused, Synced: &Condition{Status: "False", Reason: "ReconcileError", Message: "x"}}},
			want:   []string{"⚠ paused"},
		},
		"NoReason": {
			reason: "A condition without a reason prints none.",
			args:   args{h: &Health{State: Failing, Synced: &Condition{Status: "False", Message: "boom"}}},
			want:   []string{"⚠ failing (Synced=False): boom"},
		},
		"Collapsed": {
			reason: "A multi-line message prints on one line, whitespace collapsed.",
			args:   args{h: failing("first line\n  second\tline\n")},
			want:   []string{"⚠ failing (Synced=False, ReconcileError): first line second line"},
		},
		"Truncated": {
			reason: "A message over 120 runes is cut to 120, its ellipsis included.",
			args:   args{h: failing(long)},
			want:   []string{"⚠ failing (Synced=False, ReconcileError): " + strings.Repeat("word ", 23) + "word…"},
		},
		"TruncatedRunes": {
			reason: "Truncation counts runes, not bytes, so a multi-byte message isn't split mid-rune.",
			args:   args{h: failing(strings.Repeat("é", 130))},
			want:   []string{"⚠ failing (Synced=False, ReconcileError): " + strings.Repeat("é", 119) + "…"},
		},
		"Full": {
			reason: "With full, the message follows the first line in full, wrapped at word boundaries, its own line breaks kept.",
			args:   args{h: failing(long + "\nsecond"), full: true},
			want: []string{
				"⚠ failing (Synced=False, ReconcileError)",
				strings.TrimSpace(strings.Repeat("word ", 20)),
				strings.TrimSpace(strings.Repeat("word ", 20)),
				"second",
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := Lines(tc.args.h, tc.args.full)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("\n%s\nLines(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestBlock(t *testing.T) {
	failing := &Health{State: Failing, Synced: &Condition{Status: "False", Reason: "ReconcileError", Message: "boom"}}
	notReady := &Health{State: NotReady, Ready: &Condition{Status: "False", Reason: "Creating"}}

	type args struct {
		items  []Item
		listed map[string]string
	}
	cases := map[string]struct {
		reason string
		args   args
		want   []string
	}{
		"None": {
			reason: "No unhealthy resources print no block.",
		},
		"Direct": {
			reason: "Each resource prints on one line under the heading, in order.",
			args: args{items: []Item{
				{ID: "a", Name: "RouteTable.ec2 rt", Health: failing},
				{ID: "b", Name: "Subnet.ec2 sn", Health: notReady},
			}},
			want: []string{
				"Unhealthy resources without changes:",
				"  RouteTable.ec2 rt ⚠ failing (Synced=False, ReconcileError): boom",
				"  Subnet.ec2 sn ⚠ not ready (Ready=False, Creating)",
			},
		},
		"NestedUnderListedComposite": {
			reason: "A resource a nested composite composes is indented under that composite when the block lists it too.",
			args: args{items: []Item{
				{ID: "rt", Name: "RouteTable.ec2 rt", Via: "x", ViaName: "XSubnet.example.org x", Health: failing},
				{ID: "x", Name: "XSubnet.example.org x", Health: notReady},
			}},
			want: []string{
				"Unhealthy resources without changes:",
				"  XSubnet.example.org x ⚠ not ready (Ready=False, Creating)",
				"    RouteTable.ec2 rt ⚠ failing (Synced=False, ReconcileError): boom",
			},
		},
		"NestedUnderPlannedComposite": {
			reason: "A resource whose nested composite has a plan is indented under a line naming that composite.",
			args: args{
				items:  []Item{{ID: "rt", Name: "RouteTable.ec2 rt", Via: "x", ViaName: "XSubnet.example.org x", Health: failing}},
				listed: map[string]string{"x": "XSubnet.example.org x"},
			},
			want: []string{
				"Unhealthy resources without changes:",
				"  XSubnet.example.org x",
				"    RouteTable.ec2 rt ⚠ failing (Synced=False, ReconcileError): boom",
			},
		},
		"ViaNotListed": {
			reason: "A resource whose nested composite is not listed names it on its line.",
			args: args{items: []Item{
				{ID: "rt", Name: "RouteTable.ec2 rt", Via: "x", ViaName: "XSubnet.example.org x", Health: failing},
			}},
			want: []string{
				"Unhealthy resources without changes:",
				"  RouteTable.ec2 rt (via XSubnet.example.org x) ⚠ failing (Synced=False, ReconcileError): boom",
			},
		},
		"DeeplyNested": {
			reason: "Nesting follows each via, however deep.",
			args: args{items: []Item{
				{ID: "y", Name: "XInner y", Via: "x", ViaName: "XOuter x", Health: notReady},
				{ID: "rt", Name: "RouteTable rt", Via: "y", ViaName: "XInner y", Health: failing},
				{ID: "x", Name: "XOuter x", Health: notReady},
			}},
			want: []string{
				"Unhealthy resources without changes:",
				"  XOuter x ⚠ not ready (Ready=False, Creating)",
				"    XInner y ⚠ not ready (Ready=False, Creating)",
				"      RouteTable rt ⚠ failing (Synced=False, ReconcileError): boom",
			},
		},
		"Cycle": {
			reason: "Resources whose vias loop back on each other are still listed, once each.",
			args: args{items: []Item{
				{ID: "a", Name: "A", Via: "b", ViaName: "B", Health: failing},
				{ID: "b", Name: "B", Via: "a", ViaName: "A", Health: notReady},
			}},
			want: []string{
				"Unhealthy resources without changes:",
				"  A ⚠ failing (Synced=False, ReconcileError): boom",
				"    B ⚠ not ready (Ready=False, Creating)",
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := Block(tc.args.items, tc.args.listed, false)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("\n%s\nBlock(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestBlockFullMessages(t *testing.T) {
	type args struct {
		items []Item
	}
	cases := map[string]struct {
		reason string
		args   args
		want   []string
	}{
		"Wrapped": {
			reason: "With full messages, a message prints in full beneath its resource, indented four more.",
			args: args{items: []Item{{ID: "rt", Name: "RouteTable rt", Health: &Health{
				State: Failing, Synced: &Condition{Status: "False", Reason: "ReconcileError", Message: "line one\nline two"},
			}}}},
			want: []string{
				"Unhealthy resources without changes:",
				"  RouteTable rt ⚠ failing (Synced=False, ReconcileError)",
				"      line one",
				"      line two",
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := Block(tc.args.items, nil, true)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("\n%s\nBlock(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestTally(t *testing.T) {
	failing := &Health{State: Failing}
	notReady := &Health{State: NotReady}

	type args struct {
		add func(*Tally)
	}
	cases := map[string]struct {
		reason string
		args   args
		want   Counts
	}{
		"Empty": {
			reason: "Nothing added counts nothing.",
			args:   args{add: func(*Tally) {}},
		},
		"EachOnce": {
			reason: "A resource added twice counts once, under the health it was first added with.",
			args: args{add: func(t *Tally) {
				t.Resource("a", failing)
				t.Resource("a", notReady)
				t.Resource("b", &Health{State: Paused})
				t.Resource("c", nil)
			}},
			want: Counts{Failing: 1, Paused: 1},
		},
		"CompositesCountedAsComposites": {
			reason: "Composites count once each, the unhealthy ones apart, and not again as resources, whichever is added first.",
			args: args{add: func(t *Tally) {
				t.Resource("x", failing)
				t.Composite("x", failing)
				t.Composite("y", nil)
				t.Composite("y", failing)
				t.Resource("y", failing)
				t.Resource("r", notReady)
			}},
			want: Counts{Composites: 2, UnhealthyComposites: 1, NotReady: 1},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var tl Tally
			tc.args.add(&tl)
			if diff := cmp.Diff(tc.want, tl.Counts()); diff != "" {
				t.Errorf("\n%s\nCounts(): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestSummaryLine(t *testing.T) {
	type args struct {
		c Counts
	}
	cases := map[string]struct {
		reason string
		args   args
		want   string
	}{
		"AllHealthy": {
			reason: "With nothing unhealthy there is no line.",
			args:   args{c: Counts{Composites: 2}},
		},
		"Everything": {
			reason: "Every non-zero part prints, in the documented wording.",
			args:   args{c: Counts{Composites: 2, UnhealthyComposites: 1, Failing: 12, NotReady: 2, Paused: 1}},
			want:   "Health: 1 of 2 XRs unhealthy before apply; 12 resources failing (Synced=False), 2 not ready, 1 paused.",
		},
		"OnlyResources": {
			reason: "Healthy composites are left out, and one resource is singular.",
			args:   args{c: Counts{Composites: 1, NotReady: 1}},
			want:   "Health: 1 resource not ready.",
		},
		"OnlyComposite": {
			reason: "One composite is singular, and zero resource counts are left out.",
			args:   args{c: Counts{Composites: 1, UnhealthyComposites: 1}},
			want:   "Health: 1 of 1 XR unhealthy before apply.",
		},
		"PausedOnly": {
			reason: "The first non-zero resource count carries the noun.",
			args:   args{c: Counts{Paused: 3}},
			want:   "Health: 3 resources paused.",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, SummaryLine(tc.args.c)); diff != "" {
				t.Errorf("\n%s\nSummaryLine(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}
