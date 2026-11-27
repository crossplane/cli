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

// Package symbols defines the status markers and the tree connectors used in human-readable CLI output.
package symbols

const (
	// MarkerSuccess indicates an operation completed successfully.
	MarkerSuccess = "[✓]"

	// MarkerWarning indicates attention is required; specific fatality is
	// command-specific.
	MarkerWarning = "[!]"

	// MarkerFail indicates an operation failed.
	MarkerFail = "[x]"
)
