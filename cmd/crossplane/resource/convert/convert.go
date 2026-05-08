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

// Package convert contains Crossplane CLI subcommands for converting between
// Crossplane resource kinds.
package convert

import (
	"github.com/crossplane/cli/v2/cmd/crossplane/resource/convert/claimtoxr"
)

// Cmd converts a Crossplane resource to a different kind.
type Cmd struct {
	ClaimToXR claimtoxr.Cmd `cmd:"claim-to-xr" help:"Convert a Crossplane Claim to a Composite Resource (XR)."`
}

// Help returns help message for the convert command.
func (c *Cmd) Help() string {
	return `
This command converts a Crossplane resource to a different kind.

Currently supported conversions:
  * Claim -> Composite Resource (XR)

Examples:
  # Convert a Claim YAML file to an XR YAML file.
  crossplane resource convert claim-to-xr claim.yaml -o xr.yaml
`
}
