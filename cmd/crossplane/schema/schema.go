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

// Package schema contains commands for managing the generated schemas of a
// project.
package schema

// Cmd contains commands for managing schemas.
type Cmd struct {
	Update   updateCmd   `cmd:"" help:"Update schemas and the schema lock from the project's APIs and dependencies."`
	Generate generateCmd `cmd:"" help:"Recreate missing and modified schemas from the schema lock."`
	Status   statusCmd   `cmd:"" help:"Show generated schemas that differ from the schema lock."`
	Clean    cleanCmd    `cmd:"" help:"Remove generated schemas and the schema lock."`
}
