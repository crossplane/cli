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

// Package project contains commands for working with Crossplane projects.
package project

// Cmd contains project subcommands.
type Cmd struct {
	Init  initCmd  `cmd:"" help:"Initialize a new project."`
	Build buildCmd `cmd:"" help:"Build a project into Crossplane packages."`
	Run   runCmd   `cmd:"" help:"Build and run a project in a local dev control plane."`
	Stop  stopCmd  `cmd:"" help:"Tear down a local dev control plane."`
}
