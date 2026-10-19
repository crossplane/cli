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

package project

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/alecthomas/kong"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/afero"
	un "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8stypes "k8s.io/apimachinery/pkg/types"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	runtimexpkg "github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"

	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"

	"github.com/crossplane/cli/v2/internal/config"
	"github.com/crossplane/cli/v2/internal/dependency"
	"github.com/crossplane/cli/v2/internal/diff/client"
	dp "github.com/crossplane/cli/v2/internal/diff/diffprocessor"
	"github.com/crossplane/cli/v2/internal/diff/renderer"
	"github.com/crossplane/cli/v2/internal/docker"
	"github.com/crossplane/cli/v2/internal/kube"
	"github.com/crossplane/cli/v2/internal/project"
	"github.com/crossplane/cli/v2/internal/project/projectfile"
	"github.com/crossplane/cli/v2/internal/terminal"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"

	_ "embed"
)

//go:embed help/simulate.md
var simulateHelp string

// simulateCmd previews how a project's compositions would change the
// composite resources in a cluster.
type simulateCmd struct {
	ProjectFile    string `default:"${project_file}"   help:"Path to project definition."                 short:"f"`
	MaxConcurrency uint   `default:"8"                 help:"Max concurrent function builds."`
	CacheDir       string `env:"CROSSPLANE_XPKG_CACHE" help:"Directory for cached xpkg package contents." name:"cache-dir"`

	Context       string        `help:"Kubernetes context to simulate against. Defaults to the current context."                                       predictor:"context"                                                          short:"c"`
	Namespace     string        `help:"Only simulate composite resources in this namespace. Defaults to all namespaces."                               predictor:"namespace"                                                        short:"n"`
	Name          string        `help:"Only simulate the composite resource with this name. Use with --namespace to name a namespaced resource."`
	IncludePinned bool          `help:"Also simulate composite resources with a Manual composition update policy, which don't adopt the new revision."`
	MaxIterations int           `default:"20"                                                                                                          help:"Max render iterations per composite resource to resolve requirements."`
	Timeout       time.Duration `default:"1m"                                                                                                          help:"Max time for the simulation, not counting function builds."`
	Output        string        `default:"diff"                                                                                                        enum:"diff,json,yaml"                                                        help:"Output format. One of: diff, json, yaml." short:"o"`

	// CrossplaneVersion and CrossplaneImage select the crossplane render
	// backend, as they do for crossplane-diff. When neither is set the render
	// engine pulls xpkg.crossplane.io/crossplane/crossplane:stable.
	CrossplaneVersion string `help:"Pin the crossplane render version, for example v2.3.4. Minimum v2.3.4."                                  placeholder:"VERSION" xor:"crossplane-render-backend"`
	CrossplaneImage   string `help:"Override the full crossplane render image reference. Minimum v2.3.4 when its tag is a semantic version." placeholder:"IMAGE"   xor:"crossplane-render-backend"`

	Impersonation kube.ImpersonationFlags `embed:""`
}

func (c *simulateCmd) Help() string {
	return simulateHelp
}

// Validate rejects flag values the simulation can't run with. kong calls it
// during parsing, so an unsupported render backend fails before any build or
// cluster connection.
func (c *simulateCmd) Validate() error {
	if c.MaxIterations < 1 {
		return errors.Errorf("--max-iterations must be at least 1, got %d", c.MaxIterations)
	}

	// The two flags are mutually exclusive (kong xor), so at most one of
	// these has anything to check.
	if c.CrossplaneImage != "" {
		return dp.ValidateMinRenderImage(c.CrossplaneImage)
	}
	if c.CrossplaneVersion != "" {
		return dp.ValidateMinRenderVersion(c.CrossplaneVersion)
	}

	return nil
}

// Run builds the project's functions and previews its compositions against
// the cluster.
func (c *simulateCmd) Run(sigCtx context.Context, k *kong.Context, logger logging.Logger, sp terminal.SpinnerPrinter, cliCfg *config.Config) error {
	if err := docker.Check(sigCtx); err != nil {
		return errors.Wrap(err, "cannot connect to Docker, which project simulate needs to build and run functions")
	}

	// Load the kubeconfig before the slow function build, so a bad context
	// fails fast.
	restCfg, _, err := kube.RESTConfig(c.Context, c.Impersonation)
	if err != nil {
		return err
	}

	projFilePath, err := filepath.Abs(c.ProjectFile)
	if err != nil {
		return err
	}
	projFS := afero.NewBasePathFs(afero.NewOsFs(), filepath.Dir(projFilePath))
	projFileName := filepath.Base(projFilePath)

	proj, err := projectfile.Parse(projFS, projFileName)
	if err != nil {
		return errors.Wrapf(err, "failed to parse project file %q", c.ProjectFile)
	}

	comps, err := project.LoadCompositions(proj, projFS)
	if err != nil {
		return errors.Wrap(err, "cannot load compositions from project")
	}
	if len(comps) == 0 {
		return errors.Errorf("no Compositions found under the project's APIs path %q", proj.Spec.Paths.APIs)
	}

	fns, err := project.LoadFunctions(sigCtx, logger, sp, cliCfg, projFS, projFilePath, projFileName, c.MaxConcurrency, c.newClientAndResolver)
	if err != nil {
		return err
	}

	clients, err := client.New(restCfg, logger)
	if err != nil {
		return err
	}

	opts := c.processorOptions(k, logger, fns)
	xrProc := dp.NewDiffProcessor(clients.K8sClients, clients.XpClients, opts...)
	proc := dp.NewCompDiffProcessor(xrProc, clients.XpClients.Composition, opts...)

	// ponytail: the timeout bounds the whole simulation because the diff
	// engine has no per-XR deadline. Make it per composite resource once the
	// engine supports one.
	ctx, cancel := context.WithTimeout(sigCtx, c.Timeout)
	defer cancel()

	return c.simulate(ctx, logger, clients.Initialize, proc, comps)
}

// simulate initializes the clients and processor and previews comps, mapping
// the result to the command's exit status. ctx must not be cancelled by its
// owner until simulate returns, so a cancellation reads as an interrupt.
func (c *simulateCmd) simulate(ctx context.Context, logger logging.Logger, initClients func(context.Context, logging.Logger) error, proc dp.CompDiffProcessor, comps []*un.Unstructured) (err error) {
	// An interrupted run reports the interruption, not the cancelled calls it
	// caused.
	defer func() {
		if dp.Interrupted(ctx) {
			err = exitError{error: dp.ErrInterrupted, code: dp.ExitCodeInterrupted}
		}
	}()

	// Covers paths that return before rendering; Cleanup is idempotent.
	defer dp.CleanupDetached(ctx, proc, logger)

	if err := initClients(ctx, logger); err != nil {
		return errors.Wrap(err, "cannot initialize clients")
	}
	if err := proc.Initialize(ctx); err != nil {
		return errors.Wrap(err, "cannot initialize composition diff processor")
	}

	ns, refs := c.scope()
	_, err = proc.DiffComposition(ctx, comps, ns, refs)
	if err != nil {
		// DiffComposition has already rendered the result, including its
		// errors. A run that failed only schema validation exits 2.
		return exitError{error: errors.Wrap(err, "cannot simulate project"), code: dp.DetermineExitCode(err, false)}
	}

	return nil
}

// scope returns the namespace and composite resource refs DiffComposition
// limits its impact analysis to. A --name selects one composite resource,
// namespaced when --namespace is set and cluster scoped otherwise, so the
// namespace moves into its ref.
func (c *simulateCmd) scope() (string, []k8stypes.NamespacedName) {
	if c.Name == "" {
		return c.Namespace, nil
	}
	return "", []k8stypes.NamespacedName{{Namespace: c.Namespace, Name: c.Name}}
}

// processorOptions returns the diff processor options for this run. Both the
// XR and composition processors get the same options, as crossplane-diff
// does, so they share one advisory collector.
func (c *simulateCmd) processorOptions(k *kong.Context, logger logging.Logger, fns []pkgv1.Function) []dp.ProcessorOption {
	opts := []dp.ProcessorOption{
		dp.WithLogger(logger),
		dp.WithAdvisories(dp.NewAdvisoryCollector(k.Stderr)),
		dp.WithStdout(k.Stdout),
		dp.WithStderr(k.Stderr),
		dp.WithColorize(isTerminal(k.Stdout)),
		dp.WithOutputFormat(renderer.OutputFormat(c.Output)),
		dp.WithIncludeManual(c.IncludePinned),
		dp.WithMaxRenderIterations(c.MaxIterations),
		dp.WithFunctionProviderFactory(dp.NewProjectFunctionProviderFactory(fns)),
	}

	if c.CrossplaneVersion != "" {
		opts = append(opts, dp.WithCrossplaneVersion(c.CrossplaneVersion))
	}
	if c.CrossplaneImage != "" {
		opts = append(opts, dp.WithCrossplaneImage(c.CrossplaneImage))
	}

	return opts
}

func (c *simulateCmd) newClientAndResolver(extraOpts ...clixpkg.ClientOption) (runtimexpkg.Client, *clixpkg.Resolver, error) {
	cacheDir := c.CacheDir
	if cacheDir == "" {
		cacheDir = dependency.DefaultCacheDir()
	}

	opts := append([]clixpkg.ClientOption{clixpkg.WithCacheDir(afero.NewOsFs(), cacheDir)}, extraOpts...)
	xpkgClient, err := clixpkg.NewClient(clixpkg.NewRemoteFetcher(), opts...)
	if err != nil {
		return nil, nil, errors.Wrap(err, "cannot create xpkg client")
	}
	return xpkgClient, clixpkg.NewResolver(xpkgClient), nil
}

// isTerminal reports whether w is a terminal, so diffs are colorized only
// when a person reads them.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(f.Fd())
}

// exitError is an error that sets the command's exit status. kong's
// FatalIfErrorf honours it.
type exitError struct {
	error

	code int
}

// ExitCode implements kong.ExitCoder.
func (e exitError) ExitCode() int {
	return e.code
}

// Unwrap returns the underlying error.
func (e exitError) Unwrap() error {
	return e.error
}
