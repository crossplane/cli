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
// case funnels through. It ports crossplane-diff's command layer
// (origin/main 6a140b6: cmd/diff/main.go's CommonCmdFields and
// engineLimitVars, cmd_utils.go's defaultProcessorOptions,
// comp.go/xr.go's processor wiring and Run bodies) onto this repository's
// ported engine, driving it with CLI-style arguments the way the real
// commands' kong wiring does.
//
// It exists because `crossplane project simulate` does not yet cover the
// whole surface these cases need. When it does, replace runSimulate's
// body with a call to the real command and no case table changes: the
// cases are CLI args plus an expected exit code and expected output, and
// runSimulate is the only thing standing between them and execution.
//
// # Gap and environment skips
//
// Cases the current simulate surface cannot express are ported in full
// and skipped, never dropped, so each names what it waits for:
//
//   - gap:<feature> — the simulate command lacks the feature this case
//     needs. Grep the skip reasons to find what to enable as features
//     land.
//   - gap:flag-parity-not-merged (<flag>) — the flag exists on branch
//     jco-simulate-flag-parity but is not merged into this branch yet.
//     The harness already wires these flags, so the case runs as soon as
//     that branch merges.
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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alecthomas/kong"
	"k8s.io/client-go/rest"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	ld "github.com/crossplane/cli/v2/cmd/crossplane/common/load"
	"github.com/crossplane/cli/v2/internal/diff/client"
	dp "github.com/crossplane/cli/v2/internal/diff/diffprocessor"
	"github.com/crossplane/cli/v2/internal/diff/ref"
	"github.com/crossplane/cli/v2/internal/diff/renderer"
	tu "github.com/crossplane/cli/v2/internal/diff/testutils"
	"github.com/crossplane/cli/v2/internal/docker"
)

// simArgs mirrors the crossplane-diff flags the integration cases pass:
// the CommonCmdFields subset the runner threads, plus each command's
// positional files. Defaults match crossplane-diff's, including the
// engineLimitVars defaults main.go feeds kong.
type simArgs struct {
	Timeout             time.Duration `default:"1m"                                                                                         help:"How long to run before timing out."`
	Namespace           string        `help:"Namespace of the composite resources."`
	Output              string        `default:"diff"                                                                                       enum:"diff,json,yaml"                         help:"Output format (diff, json, or yaml)."                            name:"output"         short:"o"`
	OutputVersion       string        `default:"v1"                                                                                         enum:"v1,v2"                                  help:"Schema version of structured (-o json|yaml) output (v1 or v2)."  name:"output-version"`
	NoColor             bool          `help:"Disable colorized output."                                                                     name:"no-color"`
	MaxNestedDepth      int           `default:"${max_nested_depth}"                                                                        help:"Maximum depth for nested XR recursion." name:"max-nested-depth"`
	MaxIterations       int           `default:"${max_iterations}"                                                                          help:"Maximum render iterations."             name:"max-iterations"`
	IgnorePaths         []string      `help:"Paths to ignore in diffs."                                                                     name:"ignore-paths"`
	FunctionCredentials string        `help:"A YAML file or directory of YAML files specifying Secret credentials to pass to Functions."    name:"function-credentials"                   placeholder:"PATH"`
	EventualState       bool          `help:"Show eventual state after all reconciliation cycles complete."                                 name:"eventual-state"`
	DryRunOn            string        `default:"all"                                                                                        enum:"existing,all"                           help:"Which resources to verify against the apiserver with a dry run." name:"dry-run-on"`
	Resource            []string      `help:"Composite resources to scope to."                                                              sep:","`
	IncludeManual       bool          `help:"Include composites with a manual update policy."                                               name:"include-manual"`
	AnalyzeUnchanged    bool          `help:"Analyze compositions identical to their in-cluster version (deprecated; --analyze-on=always)." name:"analyze-unchanged"`
	AnalyzeOn           string        `help:"Smallest composition change that triggers per-composite impact analysis."                      name:"analyze-on"`
	Files               []string      `arg:""                                                                                               help:"Input files."                           optional:""`
}

// parseSimulateArgs parses one case's CLI-style args. A parse failure is
// fatal, as it was when kong parsed crossplane-diff's own commands.
func parseSimulateArgs(t *testing.T, testType DiffTestType, args []string) *simArgs {
	t.Helper()

	var a simArgs
	parser, err := kong.New(&a, engineLimitVars(), kong.Name(string(testType)))
	if err != nil {
		t.Fatalf("failed to create kong parser: %v", err)
	}

	if _, err := parser.Parse(args); err != nil {
		t.Fatalf("failed to parse args %v: %v", args, err)
	}

	return &a
}

// engineLimitVars ports crossplane-diff main.go's kong.Vars of the same
// name: the engine's defaults for --max-nested-depth and
// --max-iterations, so the flags' defaults follow the engine rather than
// a number duplicated here.
func engineLimitVars() kong.Option {
	return kong.Vars{
		"max_nested_depth": strconv.Itoa(dp.DefaultMaxNestedDepth),
		"max_iterations":   strconv.Itoa(dp.DefaultMaxRenderIterations),
	}
}

// simulateGap returns the reason the parsed args cannot run against the
// current simulate surface, or "" when they can. The runner calls it
// before starting envtest, so a skipped case costs nothing, and
// runSimulate never executes args it could not express.
//
// Keep this in sync with cmd/crossplane/project/simulate.go's flags: a
// flag that lands there moves out of here, and its cases start running.
func simulateGap(testType DiffTestType, a *simArgs) string {
	// The project simulate command previews compositions from a project;
	// XR files as input (crossplane-diff's xr command, issue #367's
	// `project simulate -f xr.yaml`) is not implemented yet.
	if testType == XRDiffTest {
		return "gap: simulate has no xr mode (XR files as input) yet"
	}

	// Flags that exist on the flag-parity branch but are not merged into
	// this branch yet. The harness already wires them; the cases run as
	// soon as the merge lands.
	flagParity := []struct {
		set  bool
		flag string
	}{
		{a.NoColor, "--no-color"},
		{len(a.IgnorePaths) > 0, "--ignore-paths"},
		{a.FunctionCredentials != "", "--function-credentials"},
		{a.EventualState, "--eventual-state"},
		{a.AnalyzeUnchanged, "--analyze-unchanged (simulate's --analyze-on=always)"},
		{a.AnalyzeOn != "", "--analyze-on"},
		{a.DryRunOn != "" && a.DryRunOn != "all", "--dry-run-on"},
	}
	for _, f := range flagParity {
		if f.set {
			return "gap:flag-parity-not-merged (" + f.flag + ")"
		}
	}

	// simulate scopes to one composite resource with --name (with
	// --namespace for a namespaced one); it cannot express the repeated
	// or comma-separated --resource scoping these cases need.
	if len(a.Resource) > 1 {
		return "gap: simulate has no repeated --resource scoping (single --name only)"
	}

	return ""
}

// runSimulate runs one case's args against the diff engine wired the way
// crossplane-diff's comp and xr commands wire it, and returns the exit
// code the command would exit with, the error it would fail with, and
// everything it wrote. Fresh clients per call, the way a second CLI
// invocation would see cold caches; the determinism cases rely on it.
func runSimulate(t *testing.T, cfg *rest.Config, testType DiffTestType, args []string, stdout, stderr io.Writer) (int, error) {
	t.Helper()

	a := parseSimulateArgs(t, testType, args)

	// Bind an advisory collector the way main() does, so advisories land
	// on stderr AND in structured output.
	logger := tu.TestLogger(t, true)
	advisories := dp.NewAdvisoryCollector(stderr)

	exitCode := dp.ExitCodeSuccess

	run := func() (int, error) {
		clients, err := client.New(cfg, logger)
		if err != nil {
			return dp.ExitCodeToolError, errors.Wrap(err, "cannot create clients")
		}

		opts := processorOptions(a, stdout, stderr, logger, advisories)
		xrProc := dp.NewDiffProcessor(clients.K8sClients, clients.XpClients, opts...)

		loader, err := ld.NewCompositeLoader(a.Files)
		if err != nil {
			return dp.ExitCodeToolError, errors.Wrap(err, "cannot create resource loader")
		}

		ctx, cancel, err := initializeClients(context.Background(), a.Timeout, clients, logger)
		if err != nil {
			return dp.ExitCodeToolError, err
		}
		defer cancel()

		// An interrupted run reports the interruption, not the cancelled
		// calls it caused. Registered after cancel, so it runs before it:
		// the run's own cancel must not read as an interrupt.
		err = func() (err error) {
			defer func() {
				if dp.Interrupted(ctx) {
					exitCode, err = dp.ExitCodeInterrupted, dp.ErrInterrupted
				}
			}()

			// Covers paths that return before rendering; Cleanup is
			// idempotent.
			defer dp.CleanupDetached(ctx, procFor(testType, xrProc, clients, opts), logger)

			switch testType {
			case XRDiffTest:
				resources, err := loader.Load()
				if err != nil {
					return errors.Wrap(err, "cannot load resources")
				}

				if err := xrProc.Initialize(ctx); err != nil {
					return errors.Wrap(err, "cannot initialize diff processor")
				}

				hasDiffs, err := xrProc.PerformDiff(ctx, resources, clients.XpClients.Composition.FindMatchingComposition)
				exitCode = dp.DetermineExitCode(err, hasDiffs)
				if err != nil {
					return errors.Wrap(err, "unable to process one or more resources")
				}

				return nil

			case CompositionDiffTest:
				proc := dp.NewCompDiffProcessor(xrProc, clients.XpClients.Composition, opts...)

				if err := proc.Initialize(ctx); err != nil {
					return errors.Wrap(err, "cannot initialize composition diff processor")
				}

				compositions, err := loader.Load()
				if err != nil {
					return errors.Wrap(err, "cannot load compositions")
				}

				// A single --resource scopes to one composite resource, as
				// simulate's --name does; the repeated and comma-separated
				// forms never reach here (simulateGap skips them first).
				refs, err := ref.ParseAll(a.Resource)
				if err != nil {
					return err
				}

				hasDiffs, err := proc.DiffComposition(ctx, compositions, a.Namespace, refs)
				exitCode = dp.DetermineExitCode(err, hasDiffs)
				if err != nil {
					return errors.Wrap(err, "unable to process composition diff")
				}

				return nil
			}

			t.Fatalf("unknown test type %q", testType)
			return nil
		}()

		return exitCode, err
	}

	return run()
}

// procFor builds the composition processor for a comp run, for
// CleanupDetached: the comp command cleans up the composition processor,
// which wraps the XR processor the render engine runs on.
func procFor(testType DiffTestType, xrProc dp.DiffProcessor, clients *client.Clients, opts []dp.ProcessorOption) dp.Cleaner {
	if testType != CompositionDiffTest {
		return xrProc
	}
	return dp.NewCompDiffProcessor(xrProc, clients.XpClients.Composition, opts...)
}

// initializeClients ports crossplane-diff's initializeAppContext over the
// ported client bundle: the run context bounded by the timeout, with the
// clients initialized on it.
func initializeClients(sigCtx context.Context, timeout time.Duration, clients *client.Clients, log logging.Logger) (context.Context, context.CancelFunc, error) {
	ctx, cancel := context.WithTimeout(sigCtx, timeout)
	if err := clients.Initialize(ctx, log); err != nil {
		cancel()
		return nil, nil, errors.Wrap(err, "cannot initialize client")
	}

	return ctx, cancel, nil
}

// processorOptions returns the options both processors get, replicating
// crossplane-diff's defaultProcessorOptions (cmd/diff/cmd_utils.go) — the
// single source of truth for behavior defaults in the command layer —
// plus the wiring its comp and xr commands add around the logger, the
// advisory channel, stdout and stderr.
func processorOptions(a *simArgs, stdout, stderr io.Writer, logger logging.Logger, advisories *dp.AdvisoryCollector) []dp.ProcessorOption {
	// IgnorePaths carries only what the user asked to mask. The paths that
	// are always filtered (kubectl's last-applied-configuration) are
	// stripped unconditionally by the renderer instead — see
	// renderer.alwaysIgnoredPaths. Prepending them here would make "the
	// user passed --ignore-paths" indistinguishable from "we always ignore
	// something", which comp-diff relies on to tell an edited composition
	// from a merely re-applied one.
	opts := make([]dp.ProcessorOption, 0, 15)
	opts = append(opts,
		dp.WithLogger(logger),
		dp.WithAdvisories(advisories),
		dp.WithColorize(!a.NoColor),
		dp.WithMaxNestedDepth(a.maxNestedDepth()),
		dp.WithMaxRenderIterations(a.maxIterations()),
		dp.WithEventualState(a.EventualState),
		dp.WithIgnorePaths(a.IgnorePaths),
		dp.WithDryRunOn(dp.DryRunOn(a.DryRunOn)),
		dp.WithIncludeManual(a.IncludeManual),
		dp.WithMinimizeComposition(false),
		dp.WithAnalyzeOn(a.analyzeOn()),
		dp.WithStdout(stdout),
		dp.WithStderr(stderr),
	)

	var outputFormat renderer.OutputFormat

	switch renderer.OutputFormat(a.Output) {
	case renderer.OutputFormatJSON:
		outputFormat = renderer.OutputFormatJSON
	case renderer.OutputFormatYAML:
		outputFormat = renderer.OutputFormatYAML
	case renderer.OutputFormatDiff:
		outputFormat = renderer.OutputFormatDiff
	default:
		// Empty string or unrecognized values fall back to the human-readable diff.
		outputFormat = renderer.OutputFormatDiff
	}

	opts = append(opts, dp.WithOutputFormat(outputFormat), dp.WithOutputVersion(renderer.OutputVersion(a.OutputVersion)))

	// Function credentials load at parse time in crossplane-diff (a kong
	// MapperValue); the cases that pass them are gap-skipped before this
	// point, so nothing reaches the processor without them.

	return opts
}

// analyzeOn ports crossplane-diff comp.go's analyzeOn(): the
// --analyze-on value, or --analyze-unchanged's equivalent "always", or
// the engine default with neither.
func (a *simArgs) analyzeOn() dp.AnalyzeOn {
	if a.AnalyzeOn != "" {
		return dp.AnalyzeOn(a.AnalyzeOn)
	}
	if a.AnalyzeUnchanged {
		return dp.AnalyzeOnAlways
	}
	return dp.AnalyzeOnAnyChange
}

// maxNestedDepth and maxIterations resolve the kong-var defaults, which
// kong leaves as "${max_nested_depth}" text when the vars are not fed to
// the parser — engineLimitVars feeds them, so these only guard that.
func (a *simArgs) maxNestedDepth() int {
	if strings.HasPrefix(strconv.Itoa(a.MaxNestedDepth), "$") || a.MaxNestedDepth == 0 {
		return dp.DefaultMaxNestedDepth
	}
	return a.MaxNestedDepth
}

func (a *simArgs) maxIterations() int {
	if a.MaxIterations == 0 {
		return dp.DefaultMaxRenderIterations
	}
	return a.MaxIterations
}

// dockerAvailable reports whether the local Docker daemon answers, which
// the render engine needs to run functions as containers. Probed once per
// test binary.
var dockerAvailable = sync.OnceValue(func() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return docker.Check(ctx) == nil
})

// ExitCode carries the exit code a run determined, matching
// crossplane-diff's cmd/diff type the case tables were written against.
type ExitCode struct {
	Code int
}

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
