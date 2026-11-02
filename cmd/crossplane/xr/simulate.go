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

package xr

import (
	"context"

	"github.com/alecthomas/kong"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	"github.com/crossplane/cli/v2/internal/simulate"
	"github.com/crossplane/cli/v2/internal/terminal"

	_ "embed"
)

var (
	//go:embed help/simulate.md
	simulateHelp string
	//go:embed help/simulate_examples.md
	simulateExamples string
)

// simulateCmd previews how changed composite resources or claims would change
// a cluster.
type simulateCmd struct {
	simulate.Flags `embed:""`

	Input []string `help:"Composite resource or claim file to simulate. Takes one path per flag; repeat it for more. - reads standard input." placeholder:"PATH" required:"" sep:"none" short:"f"`
}

func (c *simulateCmd) Help() string {
	return simulateHelp + simulate.Help(false) + simulateExamples
}

// Run previews the composite resources or claims -f names against the
// cluster, with the cluster's Compositions and functions.
func (c *simulateCmd) Run(sigCtx context.Context, k *kong.Context, logger logging.Logger, sp terminal.SpinnerPrinter) error {
	xrs, err := simulate.LoadInputs(c.Input, simulate.ClassComposite)
	if err != nil {
		return err
	}
	return c.Flags.Run(sigCtx, k, logger, sp, &simulate.Inputs{Composites: xrs})
}
