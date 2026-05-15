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

package main

import (
	"bytes"
	"fmt"
	"go/doc"
	"io"
	"os"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/charmbracelet/x/term"

	"github.com/crossplane/cli/v2/internal/style"
)

// helpPrinter is our custom help printer for kong. It works much like the
// upstream kong help printer except we format detailed help using markdown. We
// have also removed options we don't use to simplify the code.
func helpPrinter(options kong.HelpOptions, ctx *kong.Context) error {
	if ctx.Empty() {
		options.Summary = false
	}
	w := newHelpWriter(ctx, options)
	selected := ctx.Selected()
	if selected == nil {
		printApp(w, ctx.Model)
	} else {
		printCommand(w, ctx.Model, selected)
	}
	return w.Write(ctx.Stdout)
}

func printApp(w *helpWriter, app *kong.Application) {
	if !w.NoAppSummary {
		w.Printf("Usage: %s%s", app.Name, app.Summary())
	}
	printNodeDetail(w, app.Node)
	cmds := app.Leaves(true)
	if len(cmds) > 0 && app.HelpFlag != nil {
		w.Print("")
		if w.Summary {
			w.Printf(`Run "%s --help" for more information.`, app.Name)
		} else {
			w.Printf(`Run "%s <command> --help" for more information on a command.`, app.Name)
		}
	}
}

func printCommand(w *helpWriter, app *kong.Application, cmd *kong.Command) {
	if !w.NoAppSummary {
		w.Printf("Usage: %s %s", app.Name, cmd.Summary())
	}
	printNodeDetail(w, cmd)
	if w.Summary && app.HelpFlag != nil {
		w.Print("")
		w.Printf(`Run "%s --help" for more information.`, cmd.FullPath())
	}
}

func printNodeDetail(w *helpWriter, node *kong.Node) {
	if node.Help != "" {
		w.Print("")
		w.Wrap(node.Help)
	}
	if w.Summary {
		return
	}
	// Upstream kong formats detailed help with the go/doc package. We use
	// markdown via the glamour library.
	if node.Detail != "" {
		w.Print(style.RenderMarkdown(node.Detail))
	}
	if len(node.Positional) > 0 {
		w.Print("Arguments:")
		writePositionals(w.Indent(), node.Positional)
	}
	printFlags := func() {
		if flags := node.AllFlags(true); len(flags) > 0 {
			groupedFlags := collectFlagGroups(flags)
			for _, group := range groupedFlags {
				if group.Metadata.Title != "" {
					w.Print("")
					w.Wrap(group.Metadata.Title)
				}
				if group.Metadata.Description != "" {
					w.Indent().Wrap(group.Metadata.Description)
					w.Print("")
				}
				writeFlags(w.Indent(), group.Flags)
			}
		}
	}
	if !w.FlagsLast {
		printFlags()
	}

	cmds := node.Children
	if len(cmds) > 0 {
		iw := w.Indent()
		groupedCmds := collectCommandGroups(cmds)
		for _, group := range groupedCmds {
			if group.Metadata.Title != "" {
				w.Print("")
				w.Wrap(group.Metadata.Title)
			}
			if group.Metadata.Description != "" {
				w.Indent().Wrap(group.Metadata.Description)
				w.Print("")
			}

			writeCompactCommandList(group.Commands, iw)
		}
	}
	if w.FlagsLast {
		printFlags()
	}
}

func writeCompactCommandList(cmds []*kong.Node, iw *helpWriter) {
	rows := [][2]string{}
	for _, cmd := range cmds {
		if cmd.Hidden {
			continue
		}
		rows = append(rows, [2]string{cmd.Path(), cmd.Help})
	}
	writeTwoColumns(iw, rows)
}

type helpFlagGroup struct {
	Metadata *kong.Group
	Flags    [][]*kong.Flag
}

func collectFlagGroups(flags [][]*kong.Flag) []helpFlagGroup {
	// Group keys in order of appearance.
	groups := []*kong.Group{}
	// Flags grouped by their group key.
	flagsByGroup := map[string][][]*kong.Flag{}

	for _, levelFlags := range flags {
		levelFlagsByGroup := map[string][]*kong.Flag{}

		for _, flag := range levelFlags {
			key := ""
			if flag.Group != nil {
				key = flag.Group.Key
				groupAlreadySeen := false
				for _, group := range groups {
					if key == group.Key {
						groupAlreadySeen = true
						break
					}
				}
				if !groupAlreadySeen {
					groups = append(groups, flag.Group)
				}
			}

			levelFlagsByGroup[key] = append(levelFlagsByGroup[key], flag)
		}

		for key, flags := range levelFlagsByGroup {
			flagsByGroup[key] = append(flagsByGroup[key], flags)
		}
	}

	out := []helpFlagGroup{}
	// Ungrouped flags are always displayed first.
	if ungroupedFlags, ok := flagsByGroup[""]; ok {
		out = append(out, helpFlagGroup{
			Metadata: &kong.Group{Title: "Flags:"},
			Flags:    ungroupedFlags,
		})
	}
	for _, group := range groups {
		out = append(out, helpFlagGroup{Metadata: group, Flags: flagsByGroup[group.Key]})
	}
	return out
}

type helpCommandGroup struct {
	Metadata *kong.Group
	Commands []*kong.Node
}

func collectCommandGroups(nodes []*kong.Node) []helpCommandGroup {
	// Groups in order of appearance.
	groups := []*kong.Group{}
	// Nodes grouped by their group key.
	nodesByGroup := map[string][]*kong.Node{}

	for _, node := range nodes {
		key := ""
		if group := node.ClosestGroup(); group != nil {
			key = group.Key
			if _, ok := nodesByGroup[key]; !ok {
				groups = append(groups, group)
			}
		}
		nodesByGroup[key] = append(nodesByGroup[key], node)
	}

	out := []helpCommandGroup{}
	// Ungrouped nodes are always displayed first.
	if ungroupedNodes, ok := nodesByGroup[""]; ok {
		out = append(out, helpCommandGroup{
			Metadata: &kong.Group{Title: "Commands:"},
			Commands: ungroupedNodes,
		})
	}
	for _, group := range groups {
		out = append(out, helpCommandGroup{Metadata: group, Commands: nodesByGroup[group.Key]})
	}
	return out
}

type helpWriter struct {
	kong.HelpOptions

	indent        string
	width         int
	lines         *[]string
	helpFormatter kong.HelpValueFormatter
}

func newHelpWriter(_ *kong.Context, options kong.HelpOptions) *helpWriter {
	// Don't use kong's insane width guessing algorithm. Just get the term size
	// and cap at 120 to make paragraphs nice to read.
	wrapWidth, _, _ := term.GetSize(os.Stdout.Fd())
	wrapWidth = min(wrapWidth, 120)

	lines := []string{}
	if options.WrapUpperBound > 0 && wrapWidth > options.WrapUpperBound {
		wrapWidth = options.WrapUpperBound
	}
	w := &helpWriter{
		indent:        "",
		width:         wrapWidth,
		lines:         &lines,
		helpFormatter: kong.DefaultHelpValueFormatter,
		HelpOptions:   options,
	}
	return w
}

func (h *helpWriter) Printf(format string, args ...any) {
	h.Print(fmt.Sprintf(format, args...))
}

func (h *helpWriter) Print(text string) {
	*h.lines = append(*h.lines, strings.TrimRight(h.indent+text, " "))
}

// Indent returns a new helpWriter indented by two characters.
func (h *helpWriter) Indent() *helpWriter {
	return &helpWriter{indent: h.indent + "  ", lines: h.lines, width: h.width - 2, HelpOptions: h.HelpOptions, helpFormatter: h.helpFormatter}
}

func (h *helpWriter) String() string {
	return strings.Join(*h.lines, "\n")
}

func (h *helpWriter) Write(w io.Writer) error {
	for _, line := range *h.lines {
		_, err := io.WriteString(w, line+"\n")
		if err != nil {
			return err
		}
	}
	return nil
}

func (h *helpWriter) Wrap(text string) {
	w := bytes.NewBuffer(nil)
	doc.ToText(w, strings.TrimSpace(text), "", "    ", h.width) //nolint:staticcheck // cross-package links not possible
	for line := range strings.SplitSeq(strings.TrimSpace(w.String()), "\n") {
		h.Print(line)
	}
}

func writePositionals(w *helpWriter, args []*kong.Positional) {
	rows := make([][2]string, 0, len(args))
	for _, arg := range args {
		rows = append(rows, [2]string{arg.Summary(), w.helpFormatter(arg)})
	}
	writeTwoColumns(w, rows)
}

func writeFlags(w *helpWriter, groups [][]*kong.Flag) {
	rows := [][2]string{}
	haveShort := false
	for _, group := range groups {
		for _, flag := range group {
			if flag.Short != 0 {
				haveShort = true
				break
			}
		}
	}
	for i, group := range groups {
		if i > 0 {
			rows = append(rows, [2]string{"", ""})
		}
		for _, flag := range group {
			if !flag.Hidden {
				rows = append(rows, [2]string{formatFlag(haveShort, flag), w.helpFormatter(flag.Value)})
			}
		}
	}
	writeTwoColumns(w, rows)
}

const (
	defaultIndent        = 2
	defaultColumnPadding = 4
)

func writeTwoColumns(w *helpWriter, rows [][2]string) {
	maxLeft := max(375*w.width/1000, 30)
	// Find size of first column.
	leftSize := 0
	for _, row := range rows {
		if c := len(row[0]); c > leftSize && c < maxLeft {
			leftSize = c
		}
	}

	offsetStr := strings.Repeat(" ", leftSize+defaultColumnPadding)

	for _, row := range rows {
		buf := bytes.NewBuffer(nil)
		doc.ToText(buf, row[1], "", strings.Repeat(" ", defaultIndent), w.width-leftSize-defaultColumnPadding) //nolint:staticcheck // cross-package links not possible
		lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")

		line := fmt.Sprintf("%-*s", leftSize, row[0])
		if len(row[0]) < maxLeft {
			line += fmt.Sprintf("%*s%s", defaultColumnPadding, "", lines[0])
			lines = lines[1:]
		}
		w.Print(line)
		for _, line := range lines {
			w.Printf("%s%s", offsetStr, line)
		}
	}
}

const negatableDefault = "_"

// haveShort will be true if there are short flags present at all in the help. Useful for column alignment.
func formatFlag(haveShort bool, flag *kong.Flag) string {
	flagString := ""
	name := flag.Name
	isBool := flag.IsBool()
	isCounter := flag.IsCounter()

	short := ""
	if flag.Short != 0 {
		short = "-" + string(flag.Short) + ", "
	} else if haveShort {
		short = "    "
	}

	if isBool && flag.Tag.Negatable == negatableDefault {
		name = "[no-]" + name
	} else if isBool && flag.Tag.Negatable != "" {
		name += "/" + flag.Tag.Negatable
	}

	flagString += fmt.Sprintf("%s--%s", short, name)

	if !isBool && !isCounter {
		flagString += fmt.Sprintf("=%s", flag.FormatPlaceHolder())
	}
	return flagString
}
