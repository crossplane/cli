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

package project

import (
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

// IgnoreMatcher evaluates paths in a project to determine if they should be
// ignored during a build. Patterns follow gitignore semantics and are
// evaluated relative to the project root.
type IgnoreMatcher struct {
	m gitignore.Matcher
}

// NewIgnoreMatcher returns an IgnoreMatcher with the given patterns. It
// returns nil when there are no patterns; a nil matcher matches nothing.
func NewIgnoreMatcher(patterns []string) *IgnoreMatcher {
	if len(patterns) == 0 {
		return nil
	}

	ps := make([]gitignore.Pattern, len(patterns))
	for i, p := range patterns {
		ps[i] = gitignore.ParsePattern(p, nil)
	}

	return &IgnoreMatcher{
		m: gitignore.NewMatcher(ps),
	}
}

// Match returns whether the given path should be ignored. Paths should be
// relative to the project root.
func (im *IgnoreMatcher) Match(p string, isDir bool) bool {
	if im == nil {
		return false
	}
	return im.m.Match(strings.Split(strings.Trim(filepath.ToSlash(p), "/"), "/"), isDir)
}
