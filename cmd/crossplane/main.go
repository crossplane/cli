/*
Copyright 2020 The Crossplane Authors.

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

// Package main implements Crossplane's crank CLI - aka crossplane CLI.
package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/spf13/afero"
	"github.com/willabides/kongplete"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	"github.com/crossplane/cli/v2/cmd/crossplane/completion"
	configcmd "github.com/crossplane/cli/v2/cmd/crossplane/config"
	"github.com/crossplane/cli/v2/cmd/crossplane/convert"
	"github.com/crossplane/cli/v2/cmd/crossplane/render/op"
	"github.com/crossplane/cli/v2/cmd/crossplane/render/xr"
	"github.com/crossplane/cli/v2/cmd/crossplane/top"
	"github.com/crossplane/cli/v2/cmd/crossplane/trace"
	"github.com/crossplane/cli/v2/cmd/crossplane/validate"
	"github.com/crossplane/cli/v2/cmd/crossplane/version"
	"github.com/crossplane/cli/v2/cmd/crossplane/xpkg"
	"github.com/crossplane/cli/v2/internal/config"
	"github.com/crossplane/cli/v2/internal/maturity"
)

var _ = kong.Must(&cli{})

type (
	verboseFlag bool
)

func (v verboseFlag) BeforeApply(ctx *kong.Context) error { //nolint:unparam // BeforeApply requires this signature.
	logger := logging.NewLogrLogger(zap.New(zap.UseDevMode(true)))
	ctx.BindTo(logger, (*logging.Logger)(nil))

	return nil
}

// renderCmd groups the render subcommands.
//
// TODO(adamwg): This should be cmd/crossplane/render.Cmd, but we need to move
// the shared parts of render into internal/ and pkg/ first so that the xr and
// op subcommand packages don't import cmd/crossplane/render.
type renderCmd struct {
	XR xr.Cmd `cmd:"" default:"withargs"          help:"Render a composite resource (XR)."`
	Op op.Cmd `cmd:"" help:"Render an operation." maturity:"alpha"`
}

// The top-level crossplane CLI.
type cli struct {
	// Subcommands and flags will appear in the CLI help output in the same
	// order they're specified here. Keep them in alphabetical order.

	// Subcommands.
	Config   configcmd.Cmd `cmd:"" help:"View and modify the crossplane CLI config file."`
	Convert  convert.Cmd   `cmd:"" help:"Convert a Crossplane resource to a newer version or kind."                maturity:"beta"`
	Render   renderCmd     `cmd:"" help:"Render Crossplane resources locally using functions."`
	Top      top.Cmd       `cmd:"" help:"Display resource (CPU/memory) usage by Crossplane related pods."          maturity:"beta"`
	Trace    trace.Cmd     `cmd:"" help:"Trace a Crossplane resource for troubleshooting."                         maturity:"beta"`
	Validate validate.Cmd  `cmd:"" help:"Validate Crossplane resources."                                           maturity:"beta"`
	Version  version.Cmd   `cmd:"" help:"Print the client and server version information for the current context."`
	XPKG     xpkg.Cmd      `cmd:"" help:"Manage Crossplane packages."`

	// Flags.
	ConfigPath string      `env:"CROSSPLANE_CONFIG"                  help:"Path to the crossplane CLI config file." name:"config" placeholder:"PATH"`
	Verbose    verboseFlag `help:"Print verbose logging statements." name:"verbose"`

	// Completion
	Completions kongplete.InstallCompletions `cmd:"" help:"Get shell (bash/zsh/fish) completions. You can source this command to get completions for the login shell. Example: 'source <(crossplane completions)'"`
}

func main() {
	logger := logging.NewNopLogger()

	// Apply maturity gating before Parse so --help reflects the user's config.
	// We need the config path before Parse runs, so look for --config in argv
	// ourselves rather than parsing twice.
	flagVal, err := configFlag(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "crossplane: %v\n", err)
		os.Exit(1)
	}
	cfgPath := config.ResolvePath(flagVal)

	cfg, err := config.Load(afero.NewOsFs(), cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "crossplane: %v\n", err)
		os.Exit(1)
	}

	parser := kong.Must(&cli{},
		kong.Name("crossplane"),
		kong.Description("A command line tool for interacting with Crossplane."),
		// Binding a variable to kong context makes it available to all commands
		// at runtime.
		kong.BindTo(logger, (*logging.Logger)(nil)),
		kong.BindTo(configcmd.ConfigPath(cfgPath), (*configcmd.ConfigPath)(nil)),
		kong.ConfigureHelp(kong.HelpOptions{
			FlagsLast:      true,
			Compact:        true,
			WrapUpperBound: 80,
		}),
		kong.UsageOnError())

	kongplete.Complete(parser,
		kongplete.WithPredictors(completion.Predictors()),
	)

	maturity.Apply(parser.Model, map[maturity.Level]bool{
		maturity.LevelBeta:  cfg.Features.EnableBeta,
		maturity.LevelAlpha: cfg.Features.EnableAlpha,
	})

	ctx, err := parser.Parse(os.Args[1:])
	parser.FatalIfErrorf(err)

	err = ctx.Run()
	ctx.FatalIfErrorf(err)
}

// configFlag scans argv for the --config flag and returns its value or "" if
// the config flag is not present.
func configFlag(args []string) (string, error) {
	for i, a := range args {
		if !strings.HasPrefix(a, "--config") {
			continue
		}

		if v := strings.TrimPrefix(a, "--config="); v != "" {
			return v, nil
		}

		if i+1 < len(args) {
			return args[i+1], nil
		}

		return "", errors.New("flag --config requires a value")
	}

	return "", nil
}
