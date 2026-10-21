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
	"strconv"
	"strings"
)

// A FieldChange path joins map keys that are plain identifiers with ".",
// writes any other key as ["key"] (Go-quoted), and list indexes as [N]:
//
//	metadata.annotations["crossplane.io/composite"]
//	spec.pipeline[0].step

// JoinKey appends the map key k to path.
func JoinKey(path, k string) string {
	if !isIdent(k) {
		return path + "[" + strconv.Quote(k) + "]"
	}
	if path == "" {
		return k
	}
	return path + "." + k
}

// isIdent reports whether k can be written bare: letters, digits, '_' and
// '-', not starting with a digit.
func isIdent(k string) bool {
	if k == "" || (k[0] >= '0' && k[0] <= '9') {
		return false
	}
	for _, r := range k {
		switch {
		case r == '_', r == '-', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// PathSegments splits path into display levels: each map key unquoted, with
// any list indexes kept on the key they follow (e.g. "pipeline[0]"). A
// malformed quoted key is kept as written from that point on.
func PathSegments(path string) []string {
	var segs []string
	cur := ""
	flush := func() {
		if cur != "" {
			segs = append(segs, cur)
		}
		cur = ""
	}
	for i := 0; i < len(path); {
		switch {
		case path[i] == '.':
			flush()
			i++
		case strings.HasPrefix(path[i:], `["`):
			q, err := strconv.QuotedPrefix(path[i+1:])
			end := i + 1 + len(q)
			if err != nil || end >= len(path) || path[end] != ']' {
				cur += path[i:]
				i = len(path)
				continue
			}
			k, _ := strconv.Unquote(q)
			flush()
			cur = k
			i = end + 1
		case path[i] == '[':
			j := strings.IndexByte(path[i:], ']')
			if j < 0 {
				cur += path[i:]
				i = len(path)
				continue
			}
			cur += path[i : i+j+1]
			i += j + 1
		default:
			j := strings.IndexAny(path[i:], ".[")
			if j < 0 {
				j = len(path) - i
			}
			cur += path[i : i+j]
			i += j
		}
	}
	flush()
	return segs
}
