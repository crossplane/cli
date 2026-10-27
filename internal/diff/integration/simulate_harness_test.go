//go:build integration

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

// This file is the simulate harness: the one function every integration
// case funnels through. The cases are crossplane-diff's (origin/main
// 6a140b6, plus later upstream additions through d93eb87), written as its comp and xr commands' CLI args plus an expected
// exit code and output. runSimulate translates those args into
// `crossplane project simulate` args and runs the real command through
// kong against the case's envtest cluster, so no case table changes.
//
// # Gap and environment skips
//
// Cases the current simulate surface cannot express are ported in full
// and skipped, never dropped, so each names what it waits for:
//
//   - gap:<feature> — the simulate command lacks the feature this case
//     needs. Grep the skip reasons to find what to enable as features
//     land.
//   - env:<reason> — the environment lacks something the case needs.
//     The render engine runs functions as Docker containers, so without
//     a Docker daemon no rendering case can run; cases that would not
//     render cannot be told apart upfront, so they skip too.
package integration

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/alecthomas/kong"
	"k8s.io/client-go/rest"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	"github.com/crossplane/cli/v2/cmd/crossplane/common"
	"github.com/crossplane/cli/v2/cmd/crossplane/project"
	"github.com/crossplane/cli/v2/internal/config"
	dp "github.com/crossplane/cli/v2/internal/diff/diffprocessor"
	"github.com/crossplane/cli/v2/internal/diff/ref"
	tu "github.com/crossplane/cli/v2/internal/diff/testutils"
	"github.com/crossplane/cli/v2/internal/docker"
	"github.com/crossplane/cli/v2/internal/simulate"
	"github.com/crossplane/cli/v2/internal/terminal"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"
)

// simArgs mirrors the crossplane-diff flags the integration cases pass,
// plus each command's positional files, so runSimulate can translate
// them into project simulate's flags.
type simArgs struct {
	Timeout                time.Duration `default:"1m"                                                         help:"How long to run before timing out."`
	CrossplaneRenderBinary string        `help:"Local crossplane binary to render with."                       name:"crossplane-render-binary"`
	Namespace              string        `help:"Namespace of the composite resources."`
	Output                 string        `default:"diff"                                                       enum:"diff,json,yaml"                     help:"Output format (diff, json, or yaml)."                            name:"output"     short:"o"`
	NoColor                bool          `help:"Disable colorized output."                                     name:"no-color"`
	IgnorePaths            []string      `help:"Paths to ignore in diffs."                                     name:"ignore-paths"`
	FunctionCredentials    string        `help:"Secret credentials to pass to Functions."                      name:"function-credentials"               placeholder:"PATH"`
	EventualState          bool          `help:"Show eventual state after all reconciliation cycles complete." name:"eventual-state"`
	DryRunOn               string        `default:"all"                                                        enum:"existing,all"                       help:"Which resources to verify against the apiserver with a dry run." name:"dry-run-on"`
	Resource               []string      `help:"Composite resources to scope to."                              sep:","`
	IncludeManual          bool          `help:"Include composites with a manual update policy."               name:"include-manual"`
	AnalyzeUnchanged       bool          `help:"Analyze compositions identical to their in-cluster version."   name:"analyze-unchanged"`
	AnalyzeOn              string        `help:"Smallest composition change that triggers impact analysis."    name:"analyze-on"`
	Files                  []string      `arg:""                                                               help:"Input files."                       optional:""`
}

// parseSimulateArgs parses one case's CLI-style args. A parse failure is
// fatal, as it was when kong parsed crossplane-diff's own commands.
func parseSimulateArgs(t *testing.T, testType DiffTestType, args []string) *simArgs {
	t.Helper()

	var a simArgs
	parser, err := kong.New(&a, kong.Name(string(testType)))
	if err != nil {
		t.Fatalf("failed to create kong parser: %v", err)
	}

	if _, err := parser.Parse(args); err != nil {
		t.Fatalf("failed to parse args %v: %v", args, err)
	}

	return &a
}

// simulateGap returns the reason the parsed args cannot run against the
// current simulate surface, or "" when they can. The runner calls it
// before starting envtest, so a skipped case costs nothing, and
// runSimulate never executes args it could not express.
func simulateGap(_ DiffTestType, _ *simArgs) string {
	return ""
}

// simulateArgv translates the parsed crossplane-diff args into project
// simulate's: the input files as -f, each --resource as a --name (and
// --namespace), --include-manual as --include-pinned, and
// --analyze-unchanged as its replacement --analyze-on=always. The output
// is passed explicitly: crossplane-diff's default diff, since simulate
// defaults to -o plan, and json or yaml as plan.Result.
func simulateArgv(t *testing.T, a *simArgs) []string {
	t.Helper()

	argv := []string{
		"project", "simulate",
		"--timeout=" + a.Timeout.String(),
		"--output=" + a.Output,
		"--dry-run-on=" + a.DryRunOn,
	}
	for _, f := range a.Files {
		argv = append(argv, "-f", f)
	}

	for _, v := range a.Resource {
		r, err := ref.Parse(v)
		if err != nil {
			t.Fatalf("cannot parse --resource %q: %v", v, err)
		}
		argv = append(argv, "--name="+ref.Format(r))
	}
	ns := a.Namespace
	if ns != "" {
		argv = append(argv, "--namespace="+ns)
	}

	if a.NoColor {
		argv = append(argv, "--no-color")
	}
	for _, p := range a.IgnorePaths {
		argv = append(argv, "--ignore-paths="+p)
	}
	if a.FunctionCredentials != "" {
		argv = append(argv, "--function-credentials="+a.FunctionCredentials)
	}
	if a.EventualState {
		argv = append(argv, "--eventual-state")
	}
	if a.IncludeManual {
		argv = append(argv, "--include-pinned")
	}

	switch {
	case a.AnalyzeOn != "":
		argv = append(argv, "--analyze-on="+a.AnalyzeOn)
	case a.AnalyzeUnchanged:
		argv = append(argv, "--analyze-on=always")
	}

	return argv
}

// runSimulate runs one case's args through the real `crossplane project
// simulate` command against cfg, and returns the exit code the command
// would exit with, the error it would fail with, and everything it wrote,
// including the error as main prints it.
// Each call parses a fresh command, so a second run starts with cold
// client caches just as a second CLI invocation would; the determinism
// cases rely on it.
func runSimulate(t *testing.T, cfg *rest.Config, testType DiffTestType, args []string, stdout, stderr io.Writer) (int, error) {
	t.Helper()

	a := parseSimulateArgs(t, testType, args)

	return runSimulateArgv(t, cfg, simulateArgv(t, a), a.CrossplaneRenderBinary, stdout, stderr)
}

// runSimulateArgv runs `crossplane project simulate` with argv against cfg,
// rendering with renderBinary when it is set, as runSimulate does.
func runSimulateArgv(t *testing.T, cfg *rest.Config, argv []string, renderBinary string, stdout, stderr io.Writer) (int, error) {
	t.Helper()

	return runSimulateArgvRouted(t, cfg, argv, renderBinary, nil, stdout, stderr)
}

// runSimulateArgvRouted is runSimulateArgv with provider plans routed through
// router, when it is non-nil, instead of started diff servers.
func runSimulateArgvRouted(t *testing.T, cfg *rest.Config, argv []string, renderBinary string, router simulate.Router, stdout, stderr io.Writer) (int, error) {
	t.Helper()

	return runSimulateArgvWith(t, cfg, withRenderBinary(argv, renderBinary), &project.SimulateHooks{Router: router}, stdout, stderr)
}

// runSimulateArgvHooked is runSimulateArgv with changed marked as changed
// functions, as project mode's digest detection would.
func runSimulateArgvHooked(t *testing.T, cfg *rest.Config, argv, changed []string, stdout, stderr io.Writer) (int, error) {
	t.Helper()

	return runSimulateArgvWith(t, cfg, withRenderBinary(argv, localCrossplaneBinary(t)), &project.SimulateHooks{ChangedFunctions: changed}, stdout, stderr)
}

// withRenderBinary returns argv rendering with the local crossplane binary
// bin, or argv itself, rendering with the render image, when bin is empty.
func withRenderBinary(argv []string, bin string) []string {
	if bin == "" {
		return argv
	}
	return append(slices.Clip(argv), "--crossplane-binary="+bin)
}

// runSimulateArgvWith runs `crossplane project simulate` with argv against
// cfg and hooks, whose RESTConfig and Terminal it sets.
func runSimulateArgvWith(t *testing.T, cfg *rest.Config, argv []string, hooks *project.SimulateHooks, stdout, stderr io.Writer) (int, error) {
	t.Helper()

	var cli struct {
		Project project.Cmd `cmd:""`
	}
	status := &common.ExitStatus{}
	parser, err := kong.New(&cli,
		kong.Name("crossplane"),
		kong.Vars{"project_file": clixpkg.ProjectFile},
		kong.Writers(stdout, stderr),
		kong.Exit(func(int) { t.Fatal("kong tried to exit the test binary") }),
		kong.BindTo(context.Background(), (*context.Context)(nil)),
		kong.BindTo(tu.TestLogger(t, true), (*logging.Logger)(nil)),
		kong.BindTo(terminal.NewNopSpinnerPrinter(), (*terminal.SpinnerPrinter)(nil)),
		kong.Bind(&config.Config{}),
		kong.Bind(status),
	)
	if err != nil {
		t.Fatalf("failed to create kong parser: %v", err)
	}

	kctx, err := parser.Parse(argv)
	if err != nil {
		// Exit as main does: print the error and use its ExitCoder's code or 1.
		parser.Errorf("%s", err)

		var ec kong.ExitCoder
		if errors.As(err, &ec) {
			return ec.ExitCode(), err
		}

		return dp.ExitCodeToolError, err
	}

	// Inject the case's cluster in place of the kubeconfig, and render the
	// way crossplane-diff's tests did: with the local crossplane binary
	// when there is one, and colorized unless --no-color.
	hooks.RESTConfig = cfg
	hooks.Terminal = true
	cli.Project.Simulate.Hooks = hooks

	err = kctx.Run()
	// Exit as main does: print an error the way FatalIfErrorf does, the only
	// place errors are printed, and exit with its ExitCoder's code or 1;
	// otherwise exit with the status the command set.
	if err != nil {
		parser.Errorf("%s", err)
	}

	var ec kong.ExitCoder
	switch {
	case err == nil:
		return status.Code, nil
	case errors.As(err, &ec):
		return ec.ExitCode(), err
	default:
		return dp.ExitCodeToolError, err
	}
}

// dockerAvailable reports whether the local Docker daemon answers, which
// the render engine needs to run functions as containers. Probed once per
// test binary.
var dockerAvailable = sync.OnceValue(func() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return docker.Check(ctx) == nil
})

// envtestBinariesAvailable reports whether the envtest control plane's
// binaries are reachable: KUBEBUILDER_ASSETS points at them, or the
// setup-envtest default install path exists.
func envtestBinariesAvailable() bool {
	if dir := os.Getenv("KUBEBUILDER_ASSETS"); dir != "" {
		if _, err := os.Stat(filepath.Join(dir, "kube-apiserver")); err == nil {
			return true
		}
	}

	_, err := os.Stat("/usr/local/kubebuilder/bin/kube-apiserver")

	return err == nil
}
