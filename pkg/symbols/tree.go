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

package symbols

// A tree connector is printed in front of a node as a corner or a branch, then
// a line, then a space: "├─ " for a child that has following siblings, "└─ "
// for the last child, "├◀ " for a resource that is related to the tree and not
// part of it.
const (
	// TreeBranch is the corner of a child that has following siblings.
	TreeBranch = "├"

	// TreeCorner is the corner of the last child.
	TreeCorner = "└"

	// TreeLineDash is the line that leads from the corner to a node of the tree.
	TreeLineDash = "─"

	// TreeLineArrow replaces TreeLineDash for a resource that is only related to
	// the tree: it points at the resource it relates to.
	TreeLineArrow = "◀"

	// TreePipe is the vertical continuation, printed in front of the descendants
	// of a node that has following siblings.
	TreePipe = "│  "
)
