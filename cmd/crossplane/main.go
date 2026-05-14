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

	"github.com/crossplane/cli/v2/cmd/crossplane/cluster"
	"github.com/crossplane/cli/v2/cmd/crossplane/completion"
	"github.com/crossplane/cli/v2/cmd/crossplane/composition"
	configcmd "github.com/crossplane/cli/v2/cmd/crossplane/config"
	"github.com/crossplane/cli/v2/cmd/crossplane/operation"
	renderxr "github.com/crossplane/cli/v2/cmd/crossplane/render/xr"
	"github.com/crossplane/cli/v2/cmd/crossplane/resource"
	"github.com/crossplane/cli/v2/cmd/crossplane/version"
	"github.com/crossplane/cli/v2/cmd/crossplane/xpkg"
	"github.com/crossplane/cli/v2/cmd/crossplane/xrd"
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

// The top-level crossplane CLI.
type cli struct {
	// Subcommands and flags will appear in the CLI help output in the same
	// order they're specified here. Keep them in alphabetical order.

	// Subcommands.
	Cluster     cluster.Cmd     `cmd:"" help:"Inspect a Crossplane cluster."                                            maturity:"beta"`
	Composition composition.Cmd `cmd:"" help:"Work with Crossplane Compositions."`
	Config      configcmd.Cmd   `cmd:"" help:"View and modify the crossplane CLI config file."`
	Operation   operation.Cmd   `cmd:"" help:"Work with Crossplane Operations."                                         maturity:"alpha"`
	Resource    resource.Cmd    `cmd:"" help:"Work with Crossplane resources."                                          maturity:"beta"`
	Version     version.Cmd     `cmd:"" help:"Print the client and server version information for the current context."`
	XPKG        xpkg.Cmd        `cmd:"" help:"Work with Crossplane packages."`
	XRD         xrd.Cmd         `cmd:"" help:"Work with Crossplane Composite Resource Definitions (XRDs)."              maturity:"beta"`

	// Hidden top-level alias for render, since it's GA but has moved.
	Render renderxr.Cmd `cmd:"" help:"Render Crossplane compositions locally using functions." hidden:""`

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
			FlagsLast:           true,
			Compact:             true,
			WrapUpperBound:      80,
			NoExpandSubcommands: true,
		}),
		kong.UsageOnError())

	kongplete.Complete(parser,
		kongplete.WithPredictors(completion.Predictors()),
	)

	maturity.Apply(parser.Model, map[maturity.Level]bool{
		// Beta features are enabled by default.
		maturity.LevelBeta: !cfg.Features.DisableBeta,
		// Alpha features must be explicitly enabled.
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

		if v, ok := strings.CutPrefix(a, "--config="); ok {
			if v == "" {
				return "", errors.New("flag --config requires a value")
			}
			return v, nil
		}

		if i+1 < len(args) {
			return args[i+1], nil
		}

		return "", errors.New("flag --config requires a value")
	}

	return "", nil
}
