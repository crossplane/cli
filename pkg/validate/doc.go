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

// Package validate exposes the CLI's validation logic as a programmatic,
// I/O-free API. SchemaValidate returns a structured *ValidationResult that
// callers can inspect directly or hand to the sibling render package for
// human or machine-readable output.
//
// Downstream consumers (for example crossplane-diff) should pin a specific
// crossplane/cli version. Type and function signatures may evolve.
package validate
