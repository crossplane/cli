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

// Package resource contains commands for working with Crossplane resources.
package resource

import (
	"github.com/crossplane/cli/v2/cmd/crossplane/resource/convert"
	"github.com/crossplane/cli/v2/cmd/crossplane/trace"
	"github.com/crossplane/cli/v2/cmd/crossplane/validate"
)

// Cmd contains commands for working with Crossplane resources.
type Cmd struct {
	Convert  convert.Cmd  `cmd:"" help:"Convert a Crossplane resource to a different kind." maturity:"alpha"`
	Trace    trace.Cmd    `cmd:"" help:"Trace a Crossplane resource for troubleshooting."`
	Validate validate.Cmd `cmd:"" help:"Validate Crossplane resources."`
}
