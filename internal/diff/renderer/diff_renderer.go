package renderer

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	dt "github.com/crossplane/cli/v2/internal/diff/result"
)

// flattenGroups merges every group's diffs into a single map, for the human
// renderer's flat block. The only caller is the composition renderer, which hands it a single
// identity-less group, so no two inputs' diffs are merged here.
// A nil group.Diffs is a safe no-op under maps.Copy.
func flattenGroups(groups []dt.XRDiffGroup) map[string]*dt.ResourceDiff {
	out := make(map[string]*dt.ResourceDiff)
	for _, g := range groups {
		maps.Copy(out, g.Diffs)
	}

	return out
}

// DiffRenderer handles rendering diffs to output.
type DiffRenderer interface {
	// RenderDiffs formats and outputs diffs, grouped by input XR, to
	// DiffOptions.Stdout. The errs parameter contains the union of resource
	// processing errors (the top-level/global error list). A structured
	// renderer includes them in its output; no renderer writes them to stderr,
	// because the processor returns them to the command, which prints them once.
	//
	// The warnings parameter carries non-fatal advisories for structured output only. Unlike errors,
	// warnings have ALREADY been written to stderr, at the moment they were raised (see
	// diffprocessor.AdvisoryCollector) — emitting them at render time instead would lose any warning
	// raised during a run that fails before rendering, and would report them out of chronological
	// order with the work that produced them. The human renderer therefore ignores this parameter.
	//
	// One kind of warning is instead derived here, from the groups: the summary of added resources
	// that could not be verified against the apiserver (see dt.DryRunWarnings). Deciding what to summarise
	// is a presentation decision, and the DryRunInfo it is built from only exists once the diffs do.
	// Every renderer writes that summary to stderr; a structured one also appends it to warnings[],
	// after the warnings passed in.
	RenderDiffs(groups []dt.XRDiffGroup, errs []dt.OutputError, warnings []dt.OutputWarning) error
}

// DefaultDiffRenderer implements the DiffRenderer interface.
type DefaultDiffRenderer struct {
	logger   logging.Logger
	diffOpts DiffOptions
}

// NewDiffRenderer creates a new DefaultDiffRenderer with the given options.
func NewDiffRenderer(logger logging.Logger, diffOpts DiffOptions) DiffRenderer {
	return &DefaultDiffRenderer{
		logger:   logger,
		diffOpts: diffOpts,
	}
}

func getKindName(d *dt.ResourceDiff) string {
	return fmt.Sprintf("%s/%s", d.Gvk.Kind, d.ResourceName)
}

// diffCounts holds the per-diff-type tally produced while rendering a list of
// diffs. outputCount is the number of diffs that actually emitted content
// (non-equal with a non-empty rendered body).
type diffCounts struct {
	added    int
	modified int
	removed  int
	equal    int
	replaced int
	output   int
}

// renderDiffList renders a flat set of diffs (sorted by kind/name) to stdout in
// the human-readable +++/~~~/--- form and returns the per-type counts. It does
// not print a summary; callers decide whether to emit a per-section or
// aggregate summary from the returned counts. Equal diffs are skipped.
func (r *DefaultDiffRenderer) renderDiffList(diffs map[string]*dt.ResourceDiff) (diffCounts, error) {
	stdout := r.diffOpts.Stdout

	// Sort by GetKindName, which is how it's displayed to the user. The same kind and name in two
	// namespaces display alike, so GetDiffKey breaks the tie: map order must not reach the output.
	d := slices.AppendSeq(make([]*dt.ResourceDiff, 0, len(diffs)), maps.Values(diffs))
	slices.SortFunc(d, func(a, b *dt.ResourceDiff) int {
		return cmp.Or(
			cmp.Compare(getKindName(a), getKindName(b)),
			cmp.Compare(a.GetDiffKey(), b.GetDiffKey()),
		)
	})

	var counts diffCounts

	for _, diff := range d {
		resourceID := getKindName(diff)

		var header string

		// The added/modified/removed counters increment here, before the
		// content-empty check below. This relies on the invariant that a
		// non-equal ResourceDiff always renders non-empty content (its
		// LineDiffs are non-trivial by construction). If that ever ceased to
		// hold, counts.output could lag the type counters, desyncing the
		// summary line from the number of rendered blocks.
		switch diff.DiffType {
		case dt.DiffTypeAdded:
			counts.added++
			header = fmt.Sprintf("+++ %s", resourceID)
		case dt.DiffTypeRemoved:
			counts.removed++
			header = fmt.Sprintf("--- %s", resourceID)
		case dt.DiffTypeModified:
			counts.modified++
			header = fmt.Sprintf("~~~ %s", resourceID)
		case dt.DiffTypeEqual:
			counts.equal++
			// Skip rendering equal resources
			continue
		}

		if r.diffOpts.Replaced != nil && r.diffOpts.Replaced(diff) {
			counts.replaced++
			header += " (requires replacement)"
		}

		content := FormatDiff(diff.LineDiffs, r.diffOpts)

		if content != "" {
			if _, err := fmt.Fprintf(stdout, "%s\n%s\n---\n", header, content); err != nil {
				r.logger.Debug("Error writing diff to output", "resource", resourceID, "error", err)
				return counts, errors.Wrap(err, "failed to write diff to output")
			}

			counts.output++
		} else {
			r.logger.Debug("Empty diff content, skipping output", "resource", resourceID)
		}
	}

	return counts, nil
}

// String renders the tally as a single log-friendly field. Implementing
// Stringer lets callers log a diffCounts as one value ("counts", counts)
// instead of spelling out every field; fmt-based logr sinks honor it. (A
// structured-logging pass could later swap this for a logr.Marshaler to regain
// per-field queryability.)
func (c diffCounts) String() string {
	s := fmt.Sprintf("added=%d modified=%d removed=%d equal=%d", c.added, c.modified, c.removed, c.equal)
	if c.replaced > 0 {
		s += fmt.Sprintf(" replaced=%d", c.replaced)
	}

	return s + fmt.Sprintf(" output=%d", c.output)
}

// summaryLine formats a "N added, N modified, N removed" fragment, omitting
// zero categories, then " (N requires replacement)" when any diff would be
// replaced. Returns "" when there is nothing to report.
func (c diffCounts) summaryLine() string {
	parts := make([]string, 0, 3)

	if c.added > 0 {
		parts = append(parts, fmt.Sprintf("%d added", c.added))
	}

	if c.modified > 0 {
		parts = append(parts, fmt.Sprintf("%d modified", c.modified))
	}

	if c.removed > 0 {
		parts = append(parts, fmt.Sprintf("%d removed", c.removed))
	}

	line := strings.Join(parts, ", ")
	if c.replaced > 0 && line != "" {
		line += fmt.Sprintf(" (%d requires replacement)", c.replaced)
	}

	return line
}

// identitylessGroups wraps a flat diff map as a single group with no XR
// identity, the shape callers use when there is no single owning XR (the
// composition renderer's downstream diffs).
func identitylessGroups(diffs map[string]*dt.ResourceDiff) []dt.XRDiffGroup {
	return []dt.XRDiffGroup{{Diffs: diffs}}
}

// RenderDiffs formats and prints the diffs to r.diffOpts.Stdout as a single flat block. It does not
// print errs; the command prints the error the processor returns.
//
// The warnings parameter is intentionally unused: warnings reach humans via stderr when they are
// raised, not at render time. See the DiffRenderer interface comment.
func (r *DefaultDiffRenderer) RenderDiffs(groups []dt.XRDiffGroup, errs []dt.OutputError, _ []dt.OutputWarning) error {
	r.logger.Debug("Rendering diffs to output",
		"groupCount", len(groups),
		"errorCount", len(errs),
		"useColors", r.diffOpts.UseColors,
		"compact", r.diffOpts.Compact)

	if err := r.renderFlat(flattenGroups(groups)); err != nil {
		return err
	}

	// The unverified-additions summary comes from groups with an input XR only: the composition renderer
	// reuses this one with an identity-less group once per composition and writes one run-wide summary itself.
	var xrGroups []dt.XRDiffGroup

	for _, g := range groups {
		if g.XR.Kind != "" || g.XR.Name != "" {
			xrGroups = append(xrGroups, g)
		}
	}

	return writeWarnings(r.diffOpts.Stderr, dt.XRDryRunWarnings(xrGroups))
}

// renderFlat renders a single flat block of diffs plus a "Summary:" line. This
// is the pre-grouping behavior, used for identity-less groups (comp reuse).
func (r *DefaultDiffRenderer) renderFlat(diffs map[string]*dt.ResourceDiff) error {
	counts, err := r.renderDiffList(diffs)
	if err != nil {
		return err
	}

	r.logger.Debug("Diff rendering complete", "counts", counts)

	if counts.output > 0 {
		if line := counts.summaryLine(); line != "" {
			if _, err := fmt.Fprintf(r.diffOpts.Stdout, "\nSummary: %s\n", line); err != nil {
				return errors.Wrap(err, "failed to write summary to output")
			}
		}
	}

	return nil
}
