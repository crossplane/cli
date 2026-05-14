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
	"os"
	"regexp"
	"slices"
	"strings"
	"text/template"

	"github.com/alecthomas/kong"

	"github.com/crossplane/crossplane-runtime/v2/pkg/version"

	_ "embed"
)

//go:embed docs-templates/command-reference.md.tmpl
var docsTmpl string

type docsCmd struct {
	OutputFile string `default:"command-reference.md" help:"Path to write the generated command-reference markdown file." name:"output-file" short:"o" type:"path"`

	tmpl *template.Template
}

// AfterApply prepares the template for execution.
func (d *docsCmd) AfterApply() error {
	// Use [[ ]] as the action delimiters so the template can contain Hugo
	// shortcodes like {{< table >}} without conflict.
	t, err := template.New("docs").Delims("[[", "]]").Parse(docsTmpl)
	if err != nil {
		return err
	}
	d.tmpl = t
	return nil
}

// docsPositional describes a positional argument in the generated docs.
type docsPositional struct {
	Display  string
	Help     string
	Required bool
}

// docsFlag describes a flag in the generated docs.
type docsFlag struct {
	Long     string
	Short    string
	Help     string
	Required bool
}

// docsCommand is the per-command template input.
type docsCommand struct {
	FullPath   string
	Heading    string
	SubHeading string
	Help       string
	Detail     string
	Summary    string
	Positional []docsPositional
	Flags      []docsFlag
}

// docsInput is the top-level template input.
type docsInput struct {
	Version  string
	Commands []docsCommand
}

// Run walks the kong model and writes the generated docs file.
func (d *docsCmd) Run(ctx *kong.Context) error {
	root := ctx.Model.Node

	ver := version.New().GetVersionString()
	if ver == "" {
		ver = "(development build)"
	}
	input := docsInput{
		Version: ver,
	}

	if err := traverseChildren(root, func(n *kong.Node) error {
		input.Commands = append(input.Commands, buildDocsCommand(n))
		return nil
	}); err != nil {
		return err
	}

	slices.SortFunc(input.Commands, func(a, b docsCommand) int {
		return strings.Compare(a.FullPath, b.FullPath)
	})

	var buf bytes.Buffer
	if err := d.tmpl.Execute(&buf, input); err != nil {
		return err
	}
	return os.WriteFile(d.OutputFile, buf.Bytes(), 0o644) //nolint:gosec // 0644 is a fine mode for docs.
}

func buildDocsCommand(n *kong.Node) docsCommand {
	headingLevel := min(depth(n)+1, 6)
	dc := docsCommand{
		FullPath:   n.FullPath(),
		Heading:    strings.Repeat("#", headingLevel),
		SubHeading: strings.Repeat("#", min(headingLevel+1, 6)),
		Help:       n.Help,
		Detail:     normalizeDetail(n.Detail, headingLevel),
		Summary:    n.Summary(),
	}
	for _, p := range n.Positional {
		dc.Positional = append(dc.Positional, docsPositional{
			Display:  p.Summary(),
			Help:     p.Help,
			Required: p.Required,
		})
	}
	for _, f := range n.Flags {
		if f.Hidden {
			continue
		}
		df := docsFlag{
			Long:     "--" + f.Name,
			Help:     f.Help,
			Required: f.Required,
		}
		if f.Short != 0 {
			df.Short = "-" + string(f.Short)
		}
		// Append the placeholder for non-bool, non-counter flags so the docs
		// look like the CLI help.
		if !f.IsBool() && !f.IsCounter() {
			df.Long = fmt.Sprintf("--%s=%s", f.Name, f.FormatPlaceHolder())
		}
		dc.Flags = append(dc.Flags, df)
	}
	return dc
}

// normalizeDetail prepares embedded help markdown for inclusion in the
// generated Hugo docs page by:
//
//  1. Demoting headings as needed so the content nests below the command's
//     heading at headingLevel.
//  2. Replacing any special blockquotes with pretty hugo ones.
//  3. Adding pretty hugo table annotations to tables.
func normalizeDetail(detail string, headingLevel int) string {
	detail = strings.TrimSpace(detail)
	if headingLevel == 0 {
		return detail
	}

	// We'll demote each heading so that it's at least headingLevel+1
	// deep. I.e., nesting an H1 under an H1 results in an H2.
	demote := headingLevel - 1
	lines := strings.SplitSeq(detail, "\n")

	var (
		sb        strings.Builder
		codeBlock = false
		bq        = false
		table     = false
	)
	for line := range lines {
		// Skip any lines in a code block (```).
		if strings.HasPrefix(line, "```") {
			codeBlock = !codeBlock
			sb.WriteString(line + "\n")
			continue
		}
		if codeBlock {
			sb.WriteString(line + "\n")
			continue
		}

		// Demote headings, capping at H6.
		if strings.HasPrefix(line, "#") {
			line = strings.Repeat("#", demote) + line
		}

		// Convert special blockquotes into pretty Hugo blocks.
		bqRE := regexp.MustCompile(`^> \*\*(\w+):\*\* `)
		bqMatch := bqRE.FindStringSubmatch(line)
		if bqMatch != nil {
			bq = true
			fmt.Fprintf(&sb, "{{<hint \"%s\" >}}\n", strings.ToLower(bqMatch[1]))
			line = strings.TrimPrefix(line, bqMatch[0])
			sb.WriteString(line + "\n")
			continue
		}
		if bq {
			if !strings.HasPrefix(line, ">") {
				sb.WriteString("{{< /hint >}}\n")
				bq = false
			}
			line = strings.TrimSpace(strings.TrimPrefix(line, ">"))
		}

		// Add pretty table markers to tables.
		if strings.HasPrefix(line, "|") && !table {
			table = true
			sb.WriteString("{{<table \"table table-sm table-striped\" >}}\n")
		}
		if table && !strings.HasPrefix(line, "|") {
			sb.WriteString("{{< /table >}}\n")
			table = false
		}

		sb.WriteString(line + "\n")
	}

	return strings.TrimSpace(sb.String())
}

func depth(n *kong.Node) int {
	d := 0
	for cur := n.Parent; cur != nil; cur = cur.Parent {
		d++
	}
	return d
}

// traverseChildren walks the kong tree calling fn on each non-hidden node.
func traverseChildren(root *kong.Node, fn func(*kong.Node) error) error {
	root.Aliases = nil

	if root.Hidden {
		return nil
	}
	if err := fn(root); err != nil {
		return err
	}
	for _, node := range root.Children {
		if err := traverseChildren(node, fn); err != nil {
			return err
		}
	}
	return nil
}
