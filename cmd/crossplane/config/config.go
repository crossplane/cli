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

// Package config contains the `crossplane config` subcommands.
package config

// ConfigPath is the resolved config file path. It is bound by main so that
// subcommands can receive it as a Run() argument. Using a typed alias keeps
// the binding distinct from any other string value Kong may know about.
type ConfigPath string //nolint:revive // The "Config" stutter is intentional; this is the type Kong binds.

// Cmd groups subcommands for inspecting and modifying the CLI config file.
type Cmd struct {
	// Keep subcommands sorted alphabetically.
	Set  setCmd  `cmd:"" help:"Set a config value and write it to the config file."`
	View viewCmd `cmd:"" help:"Print the current effective config as YAML."`
}

// Help returns the extended help for the config command.
func (c *Cmd) Help() string {
	return `
Manage the crossplane CLI configuration file.

The config file location is, in priority order:
  1. The --config flag.
  2. The CROSSPLANE_CONFIG environment variable.
  3. $XDG_CONFIG_HOME/crossplane/config.yaml (or ~/.config/crossplane/config.yaml).

Examples:
  # Show the current effective config.
  crossplane config view

  # Enable alpha commands.
  crossplane config set features.enableAlpha true
`
}
