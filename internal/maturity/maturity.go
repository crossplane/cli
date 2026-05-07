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

// Package maturity implements maturity-level gating for kong commands.
//
// Commands are tagged with `maturity:"alpha"` or `maturity:"beta"` (GA
// commands have no tag). When a maturity level is not enabled, commands
// at that level are hidden from help output but still callable. Their
// own help text is annotated with a banner indicating the level.
package maturity

import (
	"fmt"
	"strings"

	"github.com/alecthomas/kong"
)

// Level is a command maturity level.
type Level string

const (
	// LevelGA is the default level for stable, generally available commands.
	LevelGA Level = ""
	// LevelBeta marks commands that may change before becoming GA.
	LevelBeta Level = "beta"
	// LevelAlpha marks experimental commands that may be removed.
	LevelAlpha Level = "alpha"
)

const tagKey = "maturity"

// Apply walks the kong model and applies maturity gating:
//   - Nodes whose effective level is not in enabled are marked Hidden.
//   - Every non-GA node has a banner prepended to its Help and Detail so
//     invokers can tell from `--help` what maturity they are using.
//
// A node's effective level is the maturity tag on the node itself, or
// inherited from its nearest tagged ancestor.
func Apply(app *kong.Application, enabled map[Level]bool) {
	enabled[LevelGA] = true

	_ = kong.Visit(app, func(node kong.Visitable, next kong.Next) error {
		n, ok := node.(*kong.Node)
		if !ok {
			return next(nil)
		}
		level := effectiveLevel(n)
		if !enabled[level] {
			n.Hidden = true
		}
		if level != LevelGA {
			n.Help = fmt.Sprintf("[%s] %s", strings.ToUpper(string(level)), n.Help)
			n.Detail = detailForLevel(level, n.Detail)
		}
		return next(nil)
	})

	var detailStr string

	switch {
	case enabled[LevelBeta] && enabled[LevelAlpha]:
		detailStr = "Alpha and beta features are enabled. Manage enabled features with \"crossplane config set\"."
	case enabled[LevelBeta]:
		detailStr = "Beta features are enabled. Manage enabled features with \"crossplane config set\"."
	case enabled[LevelAlpha]:
		detailStr = "Alpha features are enabled. Manage enabled features with \"crossplane config set\"."
	default:
		detailStr = "Alpha and beta features are disabled. To enable them use \"crossplane config set\"."
	}

	app.Detail += "\n\n" + detailStr
}

// effectiveLevel returns the level configured for the node or its nearest
// ancestor that has a level. If no ancestor has a level, LevelGA is returned.
func effectiveLevel(n *kong.Node) Level {
	for cur := n; cur != nil; cur = cur.Parent {
		if cur.Tag == nil {
			continue
		}
		if v := cur.Tag.Get(tagKey); v != "" {
			return Level(v)
		}
	}
	return LevelGA
}

func detailForLevel(l Level, detail string) string {
	banners := map[Level]string{
		LevelAlpha: "NOTE: Alpha features are experimental and may be changed or removed in a future release.",
		LevelBeta:  "NOTE: Beta features may be changed in a future release.",
	}

	if b := banners[l]; b != "" {
		return b + "\n\n" + detail
	}

	return detail
}
