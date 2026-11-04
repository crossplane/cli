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

// This file is the simulate harness: runSimulateArgvWith runs the real
// `crossplane project simulate` command through kong against a case's
// envtest cluster, started by internal/testutils/integration. A case that
// the environment cannot run (no Docker daemon) skips with an env:<reason>.
//
// testdata/README.md describes how to run the suite and read its coverage.

package project

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

	"github.com/crossplane/cli/v2/internal/config"
	dp "github.com/crossplane/cli/v2/internal/diff/diffprocessor"
	tu "github.com/crossplane/cli/v2/internal/diff/testutils"
	"github.com/crossplane/cli/v2/internal/docker"
	"github.com/crossplane/cli/v2/internal/simulate"
	"github.com/crossplane/cli/v2/internal/terminal"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"
)

// defaultTimeout is the --timeout the cases pass to simulate.
const defaultTimeout = 60 * time.Second

// runSimulateArgv runs `crossplane project simulate` with argv against cfg,
// rendering with renderBinary when it is set.
func runSimulateArgv(t *testing.T, cfg *rest.Config, argv []string, renderBinary string, stdout, stderr io.Writer) (int, error) {
	t.Helper()

	return runSimulateArgvWith(t, cfg, withRenderBinary(argv, renderBinary), &simulate.Hooks{}, stdout, stderr)
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
func runSimulateArgvWith(t *testing.T, cfg *rest.Config, argv []string, hooks *simulate.Hooks, stdout, stderr io.Writer) (int, error) {
	t.Helper()

	var cli struct {
		Project Cmd `cmd:""`
	}
	parser, err := kong.New(&cli,
		kong.Name("crossplane"),
		kong.Vars{"project_file": clixpkg.ProjectFile},
		kong.Writers(stdout, stderr),
		kong.Exit(func(int) { t.Fatal("kong tried to exit the test binary") }),
		kong.BindTo(context.Background(), (*context.Context)(nil)),
		kong.BindTo(tu.TestLogger(t, true), (*logging.Logger)(nil)),
		kong.BindTo(terminal.NewNopSpinnerPrinter(), (*terminal.SpinnerPrinter)(nil)),
		kong.Bind(&config.Config{}),
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
	// Exit as main does: a silent ExitError, such as changes found, exits
	// with its code and prints nothing; any other error is printed the way
	// FatalIfErrorf does and exits with its ExitCoder's code or 1.
	if code, ok := simulate.SilentExit(err); ok {
		return code, nil
	}
	if err != nil {
		parser.Errorf("%s", err)
	}

	var ec kong.ExitCoder
	switch {
	case err == nil:
		return 0, nil
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

// localCrossplaneBinary returns the absolute path to a locally built
// `crossplane` binary at _output/bin/crossplane in the repo root, or "" when
// there is none. With a binary, the render engine runs `crossplane internal
// render` directly, which is faster; without one, callers pass "" and it
// renders in a Docker container, which works wherever Docker does.
//
// Build the binary from the repo root with:
//
//	go build -o _output/bin/crossplane ./cmd/crossplane
//
// CROSSPLANE_RENDER_BINARY overrides the path.
//
// Callers pass the path to the command as --crossplane-binary=<path> rather
// than through a process-global env var, so parallel tests don't race on
// shared state.
func localCrossplaneBinary(t *testing.T) string {
	t.Helper()

	if override := os.Getenv("CROSSPLANE_RENDER_BINARY"); override != "" {
		return override
	}

	absPath, err := filepath.Abs("../../../_output/bin/crossplane")
	if err != nil {
		t.Fatalf("cannot resolve crossplane binary path: %v", err)
	}

	if _, err := os.Stat(absPath); err != nil {
		t.Logf("local crossplane binary not present at %s; falling back to docker render engine. Build it for faster iteration: go build -o _output/bin/crossplane ./cmd/crossplane", absPath)
		return ""
	}

	return absPath
}
