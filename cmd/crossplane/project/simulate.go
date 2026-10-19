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
	corev1 "k8s.io/api/core/v1"
	un "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	runtimexpkg "github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"

	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"

	ld "github.com/crossplane/cli/v2/cmd/crossplane/common/load"
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
	ExitCode      bool          `help:"Exit with status 3 when the simulation finds changes."`

	NoColor                  bool                `help:"Disable colorized output."`
	Compact                  bool                `help:"Show compact diffs with minimal context."`
	MaxNestedDepth           int                 `default:"10"                                                                                                                                                                       help:"Maximum depth for nested XR recursion."`
	IgnorePaths              []string            `help:"Paths to ignore in diffs (e.g., 'metadata.annotations[argocd.argoproj.io/tracking-id]')."`
	FunctionCredentials      FunctionCredentials `help:"A YAML file or directory of YAML files specifying Secret credentials to pass to Functions."                                                                                  placeholder:"PATH"`
	FunctionRegistryOverride string              `help:"Override the registry for all function images (e.g., 'my-company.registry.io')."`
	EventualState            bool                `help:"Show eventual state after all reconciliation cycles complete (useful with function-sequencer)."`
	MaxRecvMessageSize       int                 `env:"MAX_RECV_MESSAGE_SIZE"                                                                                                                                                        help:"Max gRPC message size (MB) for render function containers (4MB if undefined)."`
	MinimizeComposition      bool                `help:"Collapse each changed composition to a single marker line (human-readable output only; JSON/YAML keeps full detail; errors and no-change compositions still print in full)."`
	AnalyzeOn                string              `default:""                                                                                                                                                                         enum:",spec-change,any-change,always"                                                help:"Smallest composition change that triggers per-composite impact analysis. \"spec-change\": only when the composition spec changes. \"any-change\": also when only its metadata changes, which still creates a new CompositionRevision that composites re-point to. \"always\": even when the composition is identical, for a pre-edit convergence baseline. Defaults to any-change."`
	DryRunOn                 string              `default:"all"                                                                                                                                                                      enum:"existing,all"                                                                  help:"Which resources to verify against the apiserver with a dry run: 'all' also dry-run creates added resources so their diffs include server-side defaulting and admission (needs the 'create' verb; degrades per-resource without it), 'existing' only resources already in the cluster."`

	// CrossplaneVersion and CrossplaneImage select the crossplane render
	// backend, as they do for crossplane-diff. When neither is set the render
	// engine pulls xpkg.crossplane.io/crossplane/crossplane:stable.
	CrossplaneVersion string `help:"Pin the crossplane render version, for example v2.3.4. Minimum v2.3.4."                                  placeholder:"VERSION" xor:"crossplane-render-backend"`
	CrossplaneImage   string `help:"Override the full crossplane render image reference. Minimum v2.3.4 when its tag is a semantic version." placeholder:"IMAGE"   xor:"crossplane-render-backend"`

	Impersonation kube.ImpersonationFlags `embed:""`
}

// FunctionCredentials holds Secret credentials loaded from a file path. It
// implements kong.MapperValue to load the secrets at parse time, so a bad
// path fails before any build or cluster connection.
type FunctionCredentials struct {
	Path    string
	Secrets []corev1.Secret
}

// Decode implements kong.MapperValue.
func (f *FunctionCredentials) Decode(ctx *kong.DecodeContext) error {
	var path string
	if err := ctx.Scan.PopValueInto("path", &path); err != nil {
		return err
	}
	if path == "" {
		return nil
	}

	secrets, err := loadFunctionCredentials(path)
	if err != nil {
		return err
	}
	if len(secrets) == 0 {
		return errors.Errorf("no Secret resources found in %q: file must contain v1/Secret resources", path)
	}

	f.Path = path
	f.Secrets = secrets
	return nil
}

// loadFunctionCredentials loads the v1 Secrets in a YAML file or directory.
// Other resources are skipped.
func loadFunctionCredentials(path string) ([]corev1.Secret, error) {
	loader, err := ld.NewLoader(path)
	if err != nil {
		return nil, errors.Wrapf(err, "cannot create loader for path %q", path)
	}
	resources, err := loader.Load()
	if err != nil {
		return nil, errors.Wrapf(err, "cannot load resources from %q", path)
	}

	secrets := make([]corev1.Secret, 0, len(resources))
	for _, res := range resources {
		if res.GetKind() != "Secret" || res.GetAPIVersion() != "v1" {
			continue
		}
		secret := corev1.Secret{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(res.UnstructuredContent(), &secret); err != nil {
			return nil, errors.Wrapf(err, "cannot convert Secret %q to corev1.Secret", res.GetName())
		}
		secrets = append(secrets, secret)
	}
	return secrets, nil
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
	hasDiffs, err := proc.DiffComposition(ctx, comps, ns, refs)
	if err != nil {
		// DiffComposition has already rendered the result, including its
		// errors. A run that failed only schema validation exits 2.
		return exitError{error: errors.Wrap(err, "cannot simulate project"), code: dp.DetermineExitCode(err, false)}
	}

	// Changes are what a simulation is for, so they fail the run only when
	// asked, as with git diff --exit-code.
	if c.ExitCode && hasDiffs {
		return exitError{error: errChangesDetected, code: dp.ExitCodeDiffDetected}
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
		dp.WithColorize(!c.NoColor && isTerminal(k.Stdout)),
		dp.WithCompact(c.Compact),
		dp.WithMaxNestedDepth(c.MaxNestedDepth),
		dp.WithEventualState(c.EventualState),
		dp.WithMinimizeComposition(c.MinimizeComposition),
		dp.WithAnalyzeOn(dp.AnalyzeOn(c.AnalyzeOn)),
		dp.WithDryRunOn(dp.DryRunOn(c.DryRunOn)),
		dp.WithIgnorePaths(append([]string{lastAppliedPath}, c.IgnorePaths...)),
		dp.WithOutputFormat(renderer.OutputFormat(c.Output)),
		dp.WithIncludeManual(c.IncludePinned),
		dp.WithMaxRenderIterations(c.MaxIterations),
		dp.WithFunctionProviderFactory(dp.NewProjectFunctionProviderFactory(fns)),
	}

	if len(c.FunctionCredentials.Secrets) > 0 {
		opts = append(opts, dp.WithFunctionCredentials(c.FunctionCredentials.Secrets))
	}
	if c.FunctionRegistryOverride != "" {
		opts = append(opts, dp.WithFunctionRegistryOverride(c.FunctionRegistryOverride))
	}
	if c.MaxRecvMessageSize > 0 {
		opts = append(opts, dp.WithMaxRecvMessageSize(c.MaxRecvMessageSize))
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

// lastAppliedPath is always ignored in diffs, as in crossplane-diff.
const lastAppliedPath = "metadata.annotations[kubectl.kubernetes.io/last-applied-configuration]"

// errChangesDetected is the error a run with --exit-code returns when it
// finds changes.
var errChangesDetected = errors.New("changes detected")

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
