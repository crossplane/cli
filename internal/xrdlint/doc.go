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

// Package xrdlint checks CompositeResourceDefinitions against a configurable
// set of rules, in the manner of ESLint.
//
// The package is internal while `crossplane xrd lint` is alpha. Its API is not
// a stability promise; it may move to pkg/ once the command reaches beta.
//
// A rule is a SchemaRule held in a Registry. A Config overrides each rule's
// default severity and supplies its options. A Runner applies the enabled
// rules to XRDs loaded with Load or Parse, honours inline disable comments,
// and returns a Result that a Renderer writes out.
package xrdlint
