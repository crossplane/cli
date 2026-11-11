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

package simulate

import (
	"strings"

	_ "embed"
)

// Help fragments the simulate commands share, so their helps can't drift
// apart. Each command's Help returns its own intro, then Help(...), then its
// examples.
var (
	//go:embed help/output.md
	helpOutput string
	//go:embed help/plans.md
	helpPlans string
	//go:embed help/engine.md
	helpEngine string
	//go:embed help/imageconfigs.md
	helpImageConfigs string
	//go:embed help/diff.md
	helpDiff string
	//go:embed help/selection.md
	helpSelection string
	//go:embed help/exit.md
	helpExit string
)

// Help returns the shared help sections for a command's flags. selection adds
// the section on CompositionFlags (--namespace, --name, --include-pinned,
// --analyze-on, --minimize-composition); a command that doesn't embed
// CompositionFlags, such as one that takes composite resource files, passes
// false.
func Help(selection bool) string {
	parts := []string{helpOutput, helpPlans, helpEngine, helpImageConfigs, helpDiff}
	if selection {
		parts = append(parts, helpSelection)
	}
	parts = append(parts, helpExit)
	return strings.Join(parts, "\n")
}
