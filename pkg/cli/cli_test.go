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

package cli_test

import (
	"context"
	"io"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/google/go-cmp/cmp"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	"github.com/crossplane/cli/v2/cmd/crossplane/project"
	"github.com/crossplane/cli/v2/pkg/cli"
)

// bindingProbe takes the same bindings the embedded crossplane commands'
// Run methods take, so running it proves a host parser configured with
// Options and BindRuntime can resolve them all. It records what it received
// so the test can check the host's values arrive, not just values of the
// right types.
type bindingProbe struct {
	gotCtx context.Context
	gotCfg *cli.Config
}

func (c *bindingProbe) Run(ctx context.Context, _ logging.Logger, _ cli.SpinnerPrinter, cfg *cli.Config) error {
	c.gotCtx = ctx
	c.gotCfg = cfg
	return nil
}

type hostCLI struct {
	Project project.Cmd   `cmd:""`
	Probe   *bindingProbe `cmd:""`
}

// TestHostEmbedding verifies that a host CLI embedding the crossplane project
// commands can construct a parser with Options (kong.Vars present), parse a
// project command, and resolve every binding its Run methods require.
func TestHostEmbedding(t *testing.T) {
	cfg := &cli.Config{}
	logger := logging.NewNopLogger()
	host := &hostCLI{Probe: &bindingProbe{}}

	parser, err := kong.New(host,
		append([]kong.Option{kong.Name("host"), kong.Exit(func(int) {})},
			cli.Options(logger, cfg)...)...)
	if err != nil {
		t.Fatalf("kong.New() with cli.Options() failed: %v", err)
	}

	t.Run("ProjectVarsInterpolate", func(t *testing.T) {
		// Parsing a project subcommand exercises the ${project_file} var; a
		// wrong value would still parse, so check the interpolated default.
		if _, err := parser.Parse([]string{"project", "stop"}); err != nil {
			t.Fatalf("parser.Parse(project stop) failed: %v", err)
		}
		if diff := cmp.Diff("crossplane-project.yaml", host.Project.Stop.ProjectFile); diff != "" {
			t.Errorf("project stop --project-file default: -want, +got:\n%s", diff)
		}
	})

	t.Run("RuntimeBindingsResolve", func(t *testing.T) {
		k, err := parser.Parse([]string{"probe"})
		if err != nil {
			t.Fatalf("parser.Parse(probe) failed: %v", err)
		}
		ctx := context.Background()
		cli.BindRuntime(k, cli.NewSpinnerPrinter(io.Discard, false), ctx)
		if err := k.Run(); err != nil {
			t.Fatalf("k.Run() failed to resolve bindings: %v", err)
		}
		if host.Probe.gotCfg != cfg {
			t.Error("probe received a different *cli.Config than the host supplied")
		}
		if host.Probe.gotCtx != ctx {
			t.Error("probe received a different context.Context than the host bound")
		}
	})
}
