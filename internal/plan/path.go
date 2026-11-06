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

package plan

import (
	"fmt"

	"github.com/crossplane/crossplane-runtime/v2/pkg/fieldpath"
)

// A FieldChange path is a crossplane-runtime field path, the syntax
// --ignore-paths and the rest of Crossplane use:
//
//	metadata.annotations[crossplane.io/composite]
//	spec.pipeline[0].step

// DisplayLevels splits path into display levels: one per map key, with any
// list indexes kept on the key they follow (e.g. "pipeline[0]"). A path that
// does not parse is one level, as written.
func DisplayLevels(path string) []string {
	if path == "" {
		return nil
	}
	segs, err := fieldpath.Parse(path)
	if err != nil {
		return []string{path}
	}
	var out []string
	for _, s := range segs {
		switch {
		case s.Type == fieldpath.SegmentField:
			out = append(out, s.Field)
		case len(out) > 0:
			out[len(out)-1] += fmt.Sprintf("[%d]", s.Index)
		default:
			out = append(out, fmt.Sprintf("[%d]", s.Index))
		}
	}
	return out
}
