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

// Package style contains the shared style for the Crossplane CLI.
package style

import (
	"os"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/x/term"
	"github.com/muesli/termenv"

	_ "embed"
)

// RenderMarkdown formats markdown-formatted text for output to the terminal. If
// anything fails, the raw markdown is returned.
func RenderMarkdown(md string) string {
	wrapWidth, _, _ := term.GetSize(os.Stdout.Fd())
	wrapWidth = min(wrapWidth, 120)

	tr, err := glamour.NewTermRenderer(
		getStyleOpt(),
		glamour.WithWordWrap(wrapWidth),
	)
	if err != nil {
		return md
	}

	formatted, err := tr.Render(md)
	if err != nil {
		return md
	}

	return formatted
}

var (
	//go:embed light.json
	lightStylesheet []byte
	//go:embed dark.json
	darkStylesheet []byte
)

func getStyleOpt() glamour.TermRendererOption {
	if !term.IsTerminal(os.Stdout.Fd()) {
		return glamour.WithStandardStyle(styles.AsciiStyle)
	}

	if termenv.HasDarkBackground() {
		return glamour.WithStylesFromJSONBytes(darkStylesheet)
	}

	return glamour.WithStylesFromJSONBytes(lightStylesheet)
}
