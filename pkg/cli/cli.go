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

// Package cli exposes the minimal surface a host CLI needs to embed the
// crossplane CLI's command packages (e.g. cmd/crossplane/project) in its own
// Kong command tree: the types bound into Kong's context by the crossplane
// CLI's main, constructors for them, and the kong.Vars its command struct
// tags reference.
package cli

import (
	"context"
	"io"

	"github.com/alecthomas/kong"
	"github.com/spf13/afero"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	runtimexpkg "github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"

	"github.com/crossplane/cli/v2/internal/async"
	"github.com/crossplane/cli/v2/internal/config"
	"github.com/crossplane/cli/v2/internal/maturity"
	"github.com/crossplane/cli/v2/internal/terminal"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"
)

// SpinnerPrinter prints progress spinners to the console. The crossplane
// commands receive one via Kong binding; see BindRuntime.
type SpinnerPrinter = terminal.SpinnerPrinter

// Config is the crossplane CLI configuration, carrying feature flags that
// affect command behavior. The crossplane commands receive a *Config via Kong
// binding; see Options.
type Config = config.Config

// EventChannel carries asynchronous progress events during long-running
// operations such as builds.
type EventChannel = async.EventChannel

// NewSpinnerPrinter returns a SpinnerPrinter writing to out. If pretty is
// true, animated spinners are rendered; otherwise plain text.
func NewSpinnerPrinter(out io.Writer, pretty bool) SpinnerPrinter {
	return terminal.NewSpinnerPrinter(out, pretty)
}

// NewNopSpinnerPrinter returns a SpinnerPrinter that renders nothing and just
// runs the wrapped functions.
func NewNopSpinnerPrinter() SpinnerPrinter {
	return terminal.NewNopSpinnerPrinter()
}

// LoadConfig loads the crossplane CLI configuration from path. A missing
// file yields the default configuration (current version, all feature flags
// off), not an error.
func LoadConfig(fs afero.Fs, path string) (*Config, error) {
	return config.Load(fs, path)
}

// ResolveConfigPath resolves the configuration file path from the given flag
// value, falling back to the CROSSPLANE_CONFIG environment variable and then
// the default per-user location, the same lookup order as the standalone
// crossplane CLI.
func ResolveConfigPath(flag string) string {
	return config.ResolvePath(flag)
}

// Vars returns the kong.Vars referenced by the crossplane command structs'
// tags (e.g. ${project_file}). Hosts must include them in their parser
// options or kong.Must will fail at startup.
//
// Kong variables are parser-wide: these keys (project_file,
// package_metadata_file) are shared with the host's own commands. A host
// that uses the same variable names with different values should pass its
// own kong.Vars after these options, since later values win.
func Vars() kong.Vars {
	return kong.Vars{
		"project_file":          clixpkg.ProjectFile,
		"package_metadata_file": runtimexpkg.MetaFile,
	}
}

// Options returns the parse-time kong options required to embed crossplane
// commands: Vars plus bindings for the logger and configuration their Run
// methods take. Append these to the host's own kong.Must options.
func Options(logger logging.Logger, cfg *Config) []kong.Option {
	return []kong.Option{
		Vars(),
		kong.BindTo(logger, (*logging.Logger)(nil)),
		kong.Bind(cfg),
	}
}

// BindRuntime performs the post-parse bindings the crossplane CLI's main does
// before running a command: the spinner printer and the signal-aware context.
//
//nolint:revive // The kong.Context is the subject here; it reads better first.
func BindRuntime(k *kong.Context, sp SpinnerPrinter, sigCtx context.Context) {
	k.BindTo(sp, (*SpinnerPrinter)(nil))
	k.BindTo(sigCtx, (*context.Context)(nil))
}

// ApplyMaturity applies the crossplane CLI's maturity gating to a host
// CLI's kong model based on the feature flags in cfg: beta commands are
// shown by default, alpha commands are hidden from help until enabled (an
// explicitly typed command name still runs), and non-GA commands get a
// maturity banner in their help. Hosts embedding crossplane commands call
// this after constructing their parser so embedded commands are gated by
// the same configuration file as the crossplane CLI itself.
//
// Gating walks the entire kong model, so host commands carrying their own
// maturity tags are gated by the same flags; hosts that gate their commands
// differently should not reuse the maturity tag.
func ApplyMaturity(app *kong.Application, cfg *Config) {
	maturity.Apply(app, map[maturity.Level]bool{
		maturity.LevelBeta:  !cfg.Features.DisableBeta,
		maturity.LevelAlpha: cfg.Features.EnableAlpha,
	})
}
