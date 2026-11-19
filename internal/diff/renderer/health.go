package renderer

import (
	"fmt"
	"io"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	dt "github.com/crossplane/cli/v2/internal/diff/result"
	"github.com/crossplane/cli/v2/internal/health"
)

// refKey identifies ref across API versions, as health.Key does.
func refKey(ref corev1.ObjectReference) string {
	return health.Key(ref.APIVersion, ref.Kind, ref.Namespace, ref.Name)
}

// diffKey identifies d's resource across API versions, as health.Key does.
func diffKey(d *dt.ResourceDiff) string {
	return health.Key(d.Gvk.GroupVersion().String(), d.Gvk.Kind, d.Namespace, d.ResourceName)
}

// refName returns how the diff list names ref, e.g. "Bucket/my-bucket".
func refName(ref corev1.ObjectReference) string {
	return ref.Kind + "/" + ref.Name
}

// unhealthyBlock returns the lines listing what a composite composes that is
// not healthy and has no changes in diffs, or nil when there is nothing. A
// resource a nested composite composes is nested under that composite, as in
// plan output. It skips the keys in skip, which another entry lists.
func unhealthyBlock(h dt.CompositeHealth, diffs map[string]*dt.ResourceDiff, skip map[string]bool, full bool) []string {
	listed := map[string]string{}

	for _, d := range diffs {
		if d.DiffType != dt.DiffTypeEqual {
			listed[diffKey(d)] = getKindName(d)
		}
	}

	var items []health.Item

	for _, r := range h.Composed {
		id := refKey(r.ObjectReference)
		if !r.Health.Unhealthy() || listed[id] != "" || skip[id] {
			continue
		}

		it := health.Item{ID: id, Name: refName(r.ObjectReference), Health: r.Health}
		if r.Via != nil {
			it.Via, it.ViaName = refKey(*r.Via), refName(*r.Via)
		}

		items = append(items, it)
	}

	return health.Block(items, listed, full)
}

// joinLines returns lines, each ended by a newline, or "" when there are
// none.
func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}

	return strings.Join(lines, "\n") + "\n"
}

// write writes s to w, or nothing when s is empty.
func write(w io.Writer, s string) error {
	if s == "" {
		return nil
	}

	_, err := fmt.Fprint(w, s)

	return errors.Wrap(err, "cannot write health")
}

// healthDescription returns h's description for a line of its own: the first
// line of health.Lines, with any wrapped message indented beneath it.
func healthDescription(h *health.Health, full bool) []string {
	lines := health.Lines(h, full)
	for i := 1; i < len(lines); i++ {
		lines[i] = "    " + lines[i]
	}

	return lines
}

// tallyComposite adds a top-level composite with ref and health h, and what
// it composes, to t.
func tallyComposite(t *health.Tally, ref corev1.ObjectReference, h dt.CompositeHealth, diffs map[string]*dt.ResourceDiff) {
	t.Composite(refKey(ref), h.XR)

	for _, d := range diffs {
		t.Resource(diffKey(d), health.Of(d.Current.Raw))
	}

	for _, r := range h.Composed {
		t.Resource(refKey(r.ObjectReference), r.Health)
	}
}

// writeHealthLine writes the run's Health: line to w, or nothing when nothing
// in it is unhealthy.
func writeHealthLine(w io.Writer, t *health.Tally) error {
	line := health.SummaryLine(t.Counts())
	if line == "" {
		return nil
	}

	_, err := fmt.Fprintln(w, line)

	return errors.Wrap(err, "cannot write health summary")
}
