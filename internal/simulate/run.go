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

package simulate

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/charmbracelet/x/term"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/spf13/afero"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	un "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	runtimexpkg "github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"

	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"
	pkgv1beta1 "github.com/crossplane/crossplane/apis/v2/pkg/v1beta1"

	"github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/internal/dependency"
	"github.com/crossplane/cli/v2/internal/diff/client"
	xpclient "github.com/crossplane/cli/v2/internal/diff/client/crossplane"
	k8client "github.com/crossplane/cli/v2/internal/diff/client/kubernetes"
	dp "github.com/crossplane/cli/v2/internal/diff/diffprocessor"
	"github.com/crossplane/cli/v2/internal/diff/ref"
	"github.com/crossplane/cli/v2/internal/diff/renderer"
	dt "github.com/crossplane/cli/v2/internal/diff/result"
	"github.com/crossplane/cli/v2/internal/diffserver"
	"github.com/crossplane/cli/v2/internal/docker"
	"github.com/crossplane/cli/v2/internal/kube"
	"github.com/crossplane/cli/v2/internal/project"
	"github.com/crossplane/cli/v2/internal/terminal"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"
)

// Mode is what -f names, which decides where the simulation's
// Compositions and functions come from.
type Mode int

const (
	// ProjectMode simulates a project: its Compositions, with its functions
	// built locally and overlaid on the cluster's.
	ProjectMode Mode = iota
	// CompositionMode simulates changed Compositions against the composite
	// resources on the cluster that use them, with the cluster's functions.
	CompositionMode
	// XRMode simulates changed composite resources (or claims) with the
	// cluster's Compositions and functions.
	XRMode
)

// Packages is how a simulation fetches packages. Functions and providers go
// through one set of ImageConfigs, so --image-configs rewrites both.
type Packages struct {
	ICs []pkgv1beta1.ImageConfig
	// Client is set in project mode, and otherwise by planOptions.
	Client runtimexpkg.Client

	// Proj and DepMgr are set in project mode.
	Proj   *v1alpha1.Project
	DepMgr *dependency.Manager
}

// planOptions sets up provider diff servers and the XRPlanner, and returns
// the processor options that plan each composite resource's composed
// resources. Namespaced resources without a namespace plan in ns, the
// kubeconfig's namespace. pkgs is the project's in project mode; otherwise
// planOptions sets its client. Run stops c.servers when it is set. Planning
// shares the engine's clients' REST mapper and CRD cache, so a run discovers
// the cluster once and fetches each CRD at most once.
func (c *Flags) planOptions(sigCtx context.Context, k *kong.Context, logger logging.Logger, sp terminal.SpinnerPrinter, restCfg *rest.Config, clients *client.Clients, ns string, in *Inputs, pkgs *Packages) ([]dp.ProcessorOption, error) {
	rmapper, crds := clients.Core.Mapper, clients.K8sClients.Schema
	kubeClient, err := NewKubeClient(restCfg, rmapper)
	if err != nil {
		return nil, err
	}

	var router Router
	if c.Hooks != nil && c.Hooks.Router != nil {
		router = c.Hooks.Router
	} else {
		if pkgs.Client == nil {
			client, err := clixpkg.NewClient(clixpkg.WithCacheDir(afero.NewOsFs(), cmp.Or(c.CacheDir, dependency.DefaultCacheDir())), clixpkg.WithImageConfigs(pkgs.ICs))
			if err != nil {
				return nil, errors.Wrap(err, "cannot create xpkg client")
			}
			pkgs.Client = client
		}
		c.servers = diffserver.NewManager(pkgs.Client, logger, diffserver.WithSessionID(c.SessionID))
		router = c.servers
	}

	var ensure EnsureFunc
	if c.servers != nil {
		var upfront []diffserver.Input
		switch providerSource(c.Providers, in.Mode) {
		case providersFlag:
			upfront, err = LoadProviderInputs(c.Providers)
			if err != nil {
				return nil, errors.Wrapf(err, "cannot load provider manifests from %q", c.Providers)
			}
		case providersProject:
			images, err := project.LoadProviderDependencies(pkgs.DepMgr, pkgs.Proj)
			if err != nil {
				return nil, errors.Wrap(err, "cannot load the project's provider dependencies")
			}
			upfront = diffserver.Images(images)
		case providersPerXR:
			cache := NewImageCache()
			ensure = func(ctx context.Context, rs []*un.Unstructured) error {
				imgs := RelevantProviderImages(ctx, kubeClient, rmapper, crds, rs, len(pkgs.ICs) > 0, cache, k.Stderr)
				return EnsureServers(ctx, sp, k.Stderr, c.servers, diffserver.Images(imgs))
			}
		}
		if ensure == nil {
			if err := EnsureServers(sigCtx, sp, k.Stderr, c.servers, upfront); err != nil {
				return nil, err
			}
		}
	}

	c.keep = ProviderManaged(sigCtx, rmapper, crds)
	c.xrPlanner = NewXRPlanner(NewPlanner(kubeClient, rmapper, crds, ns, router, c.Timeout), c.keep, ensure)
	return []dp.ProcessorOption{dp.WithAfterXRDiff(c.xrPlanner.AfterXRDiff), dp.WithKeepEqualDiffs(c.keep)}, nil
}

// source is where a simulation's provider diff servers come from.
type source int

const (
	// providersFlag starts the --providers manifests up front.
	providersFlag source = iota
	// providersProject starts the project's provider dependencies up front.
	providersProject
	// providersPerXR starts the providers each composite resource needs as
	// it is simulated.
	providersPerXR
)

// providerSource picks where provider diff servers come from: --providers
// when set, else the project's provider dependencies in project mode, else
// per composite resource.
func providerSource(providers string, mode Mode) source {
	switch {
	case providers != "":
		return providersFlag
	case mode == ProjectMode:
		return providersProject
	default:
		return providersPerXR
	}
}

// noteNoServers prints NoteNoDiffServers to stderr once, for -o
// diff, which has no notes; the other formats carry it in their notes.
func (c *Flags) noteNoServers(k *kong.Context, notes []string) {
	if c.noServersNoted || c.Output != string(renderer.OutputFormatDiff) || !slices.Contains(notes, NoteNoDiffServers) {
		return
	}
	c.noServersNoted = true
	_, _ = fmt.Fprintln(k.Stderr, NoteNoDiffServers)
}

// restConfig returns the cluster to simulate against and the namespace for
// namespaced resources that set none: the hooks' cluster and "default" when
// set, otherwise the kubeconfig's cluster and namespace. Unlike kubectl's
// -n, simulate's -n filters composite resources, so it doesn't override this
// namespace.
func (c *Flags) restConfig() (*rest.Config, string, error) {
	if c.Hooks != nil && c.Hooks.RESTConfig != nil {
		return c.Hooks.RESTConfig, "default", nil
	}
	cfg, clientconfig, err := kube.RESTConfig(c.Context, c.Impersonation)
	if err != nil {
		return nil, "", err
	}
	ns, _, err := clientconfig.Namespace()
	if err != nil {
		return nil, "", errors.Wrap(err, "failed to get namespace from kubeconfig")
	}
	return cfg, ns, nil
}

// changedFunctions returns the cluster names of the embedded functions whose
// local build differs from the cluster's Function of the same name. A
// Function is unchanged only when it exists, its package is from the same
// repository, and it pins the local index digest; anything else, including a
// tag-only package, counts as changed.
func changedFunctions(ctx context.Context, rc k8client.ResourceClient, digests map[string]project.EmbeddedDigest) ([]string, error) {
	var changed []string
	for fnName, d := range digests {
		fn, err := rc.GetResource(ctx, pkgv1.FunctionGroupVersionKind, "", fnName)
		if kerrors.IsNotFound(err) {
			changed = append(changed, fnName)
			continue
		}
		if err != nil {
			return nil, errors.Wrapf(err, "cannot get Function %q to check whether embedded function changed", fnName)
		}
		pkg, _, _ := un.NestedString(fn.Object, "spec", "package")
		ref, err := name.NewDigest(pkg)
		if err != nil || ref.Context().Name() != d.Repository.Name() || ref.DigestStr() != d.Digest.String() {
			changed = append(changed, fnName)
		}
	}
	slices.Sort(changed)
	return changed, nil
}

// diffCompositions returns the diff simulate runs for Compositions: their
// impact on the composite resources in scope, rendered to k's writers.
func (c *Flags) diffCompositions(k *kong.Context, logger logging.Logger, proc dp.CompDiffProcessor, comps []*un.Unstructured, mode Mode) func(context.Context) (bool, error) {
	msg := "cannot simulate Compositions"
	if mode == ProjectMode {
		msg = "cannot simulate project"
	}
	return func(ctx context.Context) (bool, error) {
		output, err := proc.DiffComposition(ctx, comps, c.ns, c.refs)

		// Render the report even when there are errors (e.g. every
		// composition failed), so structured output stays valid. A render
		// failure is joined with the processing error, so neither is lost.
		changed := output != nil && output.HasChanges()
		if output != nil {
			changed, err = c.renderCompReport(k, logger, output, err)
		}
		return changed, errors.Wrap(err, msg)
	}
}

// setFlags returns the names of the flags set on the command line, not
// those left at their defaults or set from the environment.
func setFlags(k *kong.Context) map[string]bool {
	set := map[string]bool{}
	for _, p := range k.Path {
		if p.Flag != nil {
			set[p.Flag.Name] = true
		}
	}
	return set
}

// processor is what simulate initializes and cleans up around a diff.
type processor interface {
	Initialize(ctx context.Context) error
	dp.Cleaner
}

// simulate initializes the clients and processor and runs diff. A run that
// finds changes and fails nothing returns a silent ExitError that
// exits 3; one that fails only schema validation returns one that exits 2.
// ctx must not be cancelled by its owner until simulate returns, so a
// cancellation reads as an interrupt.
func (c *Flags) simulate(ctx context.Context, logger logging.Logger, initClients func(context.Context, logging.Logger) error, proc processor, diff func(context.Context) (bool, error)) (err error) {
	// An interrupted run reports the interruption, not the cancelled calls it
	// caused. main exits 130 for it.
	defer func() {
		if dp.Interrupted(ctx) {
			err = dp.ErrInterrupted
		}
	}()

	// Covers paths that return before rendering; Cleanup is idempotent.
	defer dp.CleanupDetached(ctx, proc, logger)

	if err := initClients(ctx, logger); err != nil {
		return errors.Wrap(err, "cannot initialize clients")
	}
	if err := proc.Initialize(ctx); err != nil {
		return errors.Wrap(err, "cannot initialize diff processor")
	}

	// Exit codes rank as crossplane-diff's do: a tool error (1) over schema
	// validation (2) over changes found (3) over success (0).
	hasDiffs, err := diff(ctx)
	switch dp.DetermineExitCode(err, hasDiffs) {
	case dp.ExitCodeSchemaValidation:
		return ExitError{Err: err, Code: ExitCodeSchemaValidation}
	case dp.ExitCodeDiffDetected:
		// Changes found are a result, not an error, so nothing is printed.
		return ExitError{Code: ExitCodeChanges}
	}

	return err
}

// scope returns the namespace and composite resource refs DiffComposition
// limits its impact analysis to. Each --name selects one composite resource: a
// bare name is namespaced when --namespace is set and cluster scoped
// otherwise, and namespace/name carries its own namespace. The namespace moves
// into the refs, so it is not also returned.
func (c *Flags) scope() (string, []k8stypes.NamespacedName, error) {
	if len(c.Name) == 0 {
		return c.Namespace, nil, nil
	}
	refs := make([]k8stypes.NamespacedName, 0, len(c.Name))
	for _, v := range c.Name {
		if strings.TrimSpace(v) == "" {
			return "", nil, errors.New("--name must not be empty; pass a name or namespace/name")
		}
		if !strings.Contains(v, "/") {
			refs = append(refs, k8stypes.NamespacedName{Namespace: c.Namespace, Name: strings.TrimSpace(v)})
			continue
		}
		if c.Namespace != "" {
			return "", nil, errors.Errorf("--name %q includes a namespace, so it can't be combined with --namespace; drop --namespace or pass the bare name", v)
		}
		r, err := ref.Parse(v)
		if err != nil {
			return "", nil, errors.Wrapf(err, "invalid --name %q", v)
		}
		refs = append(refs, r)
	}
	return "", refs, nil
}

// processorOptions returns the diff processor options for this run. Both the
// XR and composition processors get the same options, as crossplane-diff
// does, so they share one advisory collector. fns are the project's
// functions, of which embedded are the locally built ones, and ics rewrite
// the other functions' images.
func (c *Flags) processorOptions(k *kong.Context, logger logging.Logger, fns []pkgv1.Function, embedded map[string]project.EmbeddedDigest, ics []pkgv1beta1.ImageConfig) []dp.ProcessorOption {
	c.SetDefaultCrossplaneDockerNetwork(fns)

	opts := []dp.ProcessorOption{
		dp.WithLogger(logger),
		dp.WithAdvisories(dp.NewAdvisoryCollector(k.Stderr)),
		dp.WithMaxNestedDepth(c.MaxNestedDepth),
		dp.WithEventualState(c.EventualState),
		dp.WithAnalyzeOn(dp.AnalyzeOn(c.AnalyzeOn)),
		dp.WithDryRunOn(dp.DryRunOn(c.DryRunOn)),
		dp.WithIgnorePaths(c.IgnorePaths),
		dp.WithIncludeManual(c.IncludePinned),
		dp.WithMaxRenderIterations(c.MaxIterations),
	}

	if len(fns) > 0 || len(ics) > 0 {
		opts = append(opts, dp.WithFunctionProviderFactory(functionProviderFactory(fns, embedded, ics)))
	}

	if len(c.functionCredentials) > 0 {
		opts = append(opts, dp.WithFunctionCredentials(c.functionCredentials))
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
	if c.CrossplaneBinary != "" {
		opts = append(opts, dp.WithCrossplaneRenderBinary(c.CrossplaneBinary))
	}
	if c.CrossplaneDockerNetwork != "" {
		opts = append(opts, dp.WithCrossplaneDockerNetwork(c.CrossplaneDockerNetwork))
	}

	return opts
}

// functionProviderFactory returns the functions a render runs: the project's
// functions in place of the cluster's of the same name, with every image
// except the embedded functions' rewritten by ics. Embedded functions run
// from the images loaded into the Docker daemon, so they keep their package.
func functionProviderFactory(fns []pkgv1.Function, embedded map[string]project.EmbeddedDigest, ics []pkgv1beta1.ImageConfig) func(xpclient.FunctionClient, logging.Logger) dp.FunctionProvider {
	var deps, own []pkgv1.Function
	for _, fn := range fns {
		if _, ok := embedded[fn.GetName()]; ok {
			own = append(own, fn)
			continue
		}
		deps = append(deps, fn)
	}

	return func(fnClient xpclient.FunctionClient, logger logging.Logger) dp.FunctionProvider {
		p := dp.NewCachedFunctionProvider(fnClient, logger)
		if len(deps) > 0 {
			p = dp.NewProjectFunctionProvider(p, deps, logger)
		}
		if len(ics) > 0 {
			p = dp.NewImageConfigFunctionProvider(p, ics, logger)
		}
		if len(own) > 0 {
			p = dp.NewProjectFunctionProvider(p, own, logger)
		}
		return p
	}
}

// diffOptions returns the renderer options the command's flags select,
// writing to k's stdout and stderr. The processors return reports; the command
// renders them with these options. Diffs are colorized only when a person
// reads them: on a terminal, or as if on one under the test hooks.
func (c *Flags) diffOptions(k *kong.Context) renderer.DiffOptions {
	opts := renderer.DefaultDiffOptions()
	opts.UseColors = !c.NoColor && (c.Hooks != nil && c.Hooks.Terminal || isTerminal(k.Stdout))
	opts.Compact = c.Compact
	opts.Stdout = k.Stdout
	opts.Stderr = k.Stderr
	return opts
}

// providerPlans returns the provider plans the run collected, or nil when
// planning was skipped.
func (c *Flags) providerPlans() *ProviderPlans {
	if c.xrPlanner == nil {
		return nil
	}
	return c.xrPlanner.ProviderPlans()
}

// planNotes returns the notes that replace the result's own, or nil to keep
// them. planned is true when planning ran; routed counts the plans a diff
// server served, and considered the resources the planner was asked to route.
func planNotes(skipPlan, planned bool, routed, considered int) []string {
	switch {
	case skipPlan:
		return []string{NoteProviderPlanSkipped}
	case planned && considered > 0 && routed == 0:
		return []string{NoteNoDiffServers}
	}
	return nil
}

func (c *Flags) notes() []string {
	if c.xrPlanner == nil {
		return planNotes(c.SkipPlan, false, 0, 0)
	}
	return planNotes(c.SkipPlan, true, c.xrPlanner.Routed(), c.xrPlanner.Considered())
}

// renderXRReport writes an xr or project run's report in the format -o
// selects, as renderReport does.
func (c *Flags) renderXRReport(k *kong.Context, logger logging.Logger, r *dt.XRReport, runErr error) (bool, error) {
	plans := c.providerPlans()
	return c.renderReport(k, logger, runErr, FromXRReport(r, plans, c.keep), r.HasChanges(), dt.XRDryRunWarnings(r.Groups), func(opts renderer.DiffOptions) error {
		// Annotated even without plans, so a Secret's values are redacted.
		r = AnnotateXRReport(r, plans)
		return renderer.NewDiffRenderer(logger, opts).RenderDiffs(r.Groups, r.Errors, r.Warnings)
	})
}

// renderCompReport writes a composition run's report in the format -o
// selects, as renderReport does.
func (c *Flags) renderCompReport(k *kong.Context, logger logging.Logger, o *dt.CompDiffOutput, runErr error) (bool, error) {
	plans := c.providerPlans()
	return c.renderReport(k, logger, runErr, FromCompReport(o, plans, c.keep), o.HasChanges(), dt.CompDryRunWarnings(o), func(opts renderer.DiffOptions) error {
		opts.MinimizeComposition = c.MinimizeComposition
		// Annotated even without plans, so a Secret's values are redacted.
		o = AnnotateCompReport(o, plans)
		return renderer.NewDefaultCompDiffRenderer(logger, renderer.NewDiffRenderer(logger, opts), opts).RenderCompDiff(o)
	})
}

// renderReport writes a run's report in the format -o selects: renderDiff's
// human diff for diff, and res otherwise, followed by warnings. It returns
// whether the run changed anything, provider plans included, and runErr
// with any render failure joined to it, so neither is lost. changed is
// whether the report itself has changes.
func (c *Flags) renderReport(k *kong.Context, logger logging.Logger, runErr error, res ProjectResult, changed bool, warnings []dt.OutputWarning, renderDiff func(renderer.DiffOptions) error) (bool, error) {
	opts := c.diffOptions(k)
	plans := c.providerPlans()
	if plans != nil {
		opts.Replaced = ReplacedBy(plans)
	}
	n := c.notes()
	if n != nil {
		res.Notes = n
	}
	c.noteNoServers(k, n)
	changed = changed || (plans != nil && res.Summary.Changes() > 0)

	var err error
	if c.Output == string(renderer.OutputFormatDiff) {
		err = renderDiff(opts)
	} else {
		err = c.renderPlan(k, res, warnings)
	}
	if err != nil {
		logger.Debug("Failed to render diffs", "error", err)
		return changed, withRenderError(runErr, errors.Wrap(err, "failed to render diffs"))
	}
	return changed, runErr
}

// renderPlan writes res to stdout in the plan, json or yaml format, then
// writes derived, the warnings derived from the report, to stderr, as the
// engine's renderers do. The run's other warnings went to stderr when they
// were raised.
func (c *Flags) renderPlan(k *kong.Context, res ProjectResult, derived []dt.OutputWarning) error {
	r, err := NewRenderer(c.Output)
	if err != nil {
		return err
	}
	if err := r.Render(k.Stdout, res); err != nil {
		return err
	}
	for _, w := range derived {
		if _, err := fmt.Fprintln(k.Stderr, w.FormatWarning()); err != nil {
			return errors.Wrap(err, "cannot write warning to stderr")
		}
	}
	return nil
}

// withRenderError adds a render failure to a run's error. A joined run error is flattened first, so
// the result reads as one list, the way it did when the processor rendered and joined it itself.
func withRenderError(err, renderErr error) error {
	var errs []error

	switch e := err.(type) { //nolint:errorlint // Flattens only the top-level join, not wrapped errors.
	case nil:
	case interface{ Unwrap() []error }:
		errs = e.Unwrap()
	default:
		errs = []error{err}
	}

	return errors.Join(append(errs, renderErr)...)
}

// isTerminal reports whether w is a terminal, so diffs are colorized only
// when a person reads them.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(f.Fd())
}

// Run previews the inputs against the cluster: a project's compositions, with
// its functions built locally, or changed Composition or composite resource
// files, with functions from the cluster.
func (c *Flags) Run(sigCtx context.Context, k *kong.Context, logger logging.Logger, sp terminal.SpinnerPrinter, in *Inputs) error {
	if err := validateModeFlags(k.Stderr, in.Mode, setFlags(k), c.SkipPlan); err != nil {
		return err
	}

	if err := docker.Check(sigCtx); err != nil {
		return errors.Wrap(err, "cannot connect to Docker, which project simulate needs to run functions")
	}

	// Load the kubeconfig before the slow function build, so a bad context
	// fails fast.
	restCfg, ns, err := c.restConfig()
	if err != nil {
		return err
	}

	var fns []pkgv1.Function
	var digests map[string]project.EmbeddedDigest
	var pkgs *Packages
	comps := in.Resources
	if in.Mode == ProjectMode {
		built, err := in.Build(sigCtx)
		if err != nil {
			return err
		}
		comps, fns, digests, pkgs = built.Compositions, built.Functions, built.Digests, built.Packages
	} else {
		ics, err := ImageConfigs(c.ImageConfigs, "")
		if err != nil {
			return err
		}
		pkgs = &Packages{ICs: ics}
	}

	clients, err := client.New(restCfg, logger)
	if err != nil {
		return err
	}

	changed, err := changedFunctions(sigCtx, clients.K8sClients.Resource, digests)
	if err != nil {
		return err
	}

	opts := append(c.processorOptions(k, logger, fns, digests, pkgs.ICs), dp.WithChangedFunctions(append(changed, c.hookChangedFunctions()...)...), dp.WithXRTimeout(c.Timeout))

	// xrPlanner and servers are for rendering the plans; they stay nil with
	// --skip-plan, and servers also when hooks.Router supplies the router.
	if !c.SkipPlan {
		planOpts, err := c.planOptions(sigCtx, k, logger, sp, restCfg, clients, ns, in, pkgs)
		// Stop even on error: upfront Ensure may have started servers.
		if c.servers != nil {
			defer StopServers(sigCtx, k.Stderr, c.servers)
		}
		if err != nil {
			return err
		}
		opts = append(opts, planOpts...)
	}
	xrProc := dp.NewDiffProcessor(clients.K8sClients, clients.XpClients, opts...)

	ctx := sigCtx

	if in.Mode == XRMode {
		// Compositions and functions come from the cluster, as in
		// crossplane-diff's xr command. A claim is simulated as its XR, but
		// the report names the input, so its result is listed under the
		// claim's name (crossplane/cli#367).
		return c.simulate(ctx, logger, clients.Initialize, xrProc, func(ctx context.Context) (bool, error) {
			if err := checkComposites(ctx, clients.XpClients.Definition, in.Resources); err != nil {
				return false, err
			}
			report, err := xrProc.PerformDiff(ctx, in.Resources, clients.XpClients.Composition.FindMatchingComposition)

			// Render the report even when there are errors, so successful XRs
			// are shown and structured output stays valid. The renderer
			// includes the errors in structured output.
			changed := report != nil && report.HasChanges()
			if report != nil {
				changed, err = c.renderXRReport(k, logger, report, err)
			}
			return changed, errors.Wrap(err, "cannot simulate composite resources")
		})
	}

	proc := dp.NewCompDiffProcessor(xrProc, clients.XpClients.Composition, opts...)
	return c.simulate(ctx, logger, clients.Initialize, proc, c.diffCompositions(k, logger, proc, comps, in.Mode))
}

// checkComposites rejects resources that no XRD in the cluster defines as a
// composite resource or claim, such as package metadata, a Provider or a
// ConfigMap, which the engine would otherwise fail on with no hint at what -f
// accepts. The definition client caches the XRDs when it initializes, so the
// check costs no extra calls.
func checkComposites(ctx context.Context, defs xpclient.DefinitionClient, resources []*un.Unstructured) error {
	for _, r := range resources {
		gvk := r.GroupVersionKind()
		if xrd, err := defs.GetXRDForClaim(ctx, gvk); err == nil && xrd != nil {
			continue
		}
		if xrd, err := defs.GetXRDForXR(ctx, gvk); err == nil && xrd != nil {
			continue
		}
		return errors.Errorf("-f inputs include %s %q (%s), which no XRD in the cluster defines as a composite resource or claim; -f accepts a project file alone, Composition files, or composite resource or claim files", gvk.Kind, r.GetName(), gvk.GroupVersion())
	}
	return nil
}

// validateModeFlags checks the flags set on the command line against mode.
// A flag that does nothing in mode prints a warning to stderr and the run
// continues: --skip-plan runs no diff servers, so --providers and
// --session-id do nothing with it; functions come from the cluster unless -f
// names a project, so --max-concurrency applies only to a project; and
// --cache-dir caches nothing with --skip-plan and no project. A flag that
// would mislead is a usage error that exits 80: composite resource files name
// the composite resources to simulate, so the flags that pick which ones a
// Composition change reaches would look like they scope the run when they
// don't.
func validateModeFlags(stderr io.Writer, mode Mode, set map[string]bool, skipPlan bool) error {
	for _, f := range []string{"providers", "session-id"} {
		if skipPlan && set[f] {
			_, _ = fmt.Fprintf(stderr, "Warning: ignoring --%s; --skip-plan runs no diff servers.\n", f)
		}
	}
	if mode == ProjectMode {
		return nil
	}
	if set["max-concurrency"] {
		_, _ = fmt.Fprintln(stderr, "Warning: ignoring --max-concurrency, which configures a project's function builds; Composition and composite resource files use the cluster's functions.")
	}
	if set["cache-dir"] && skipPlan {
		_, _ = fmt.Fprintln(stderr, "Warning: ignoring --cache-dir; with --skip-plan and no project file there is nothing to cache.")
	}
	if mode != XRMode {
		return nil
	}
	for _, f := range []string{"namespace", "name", "include-pinned", "analyze-on"} {
		if set[f] {
			return ExitError{Code: ExitCodeUsage, Err: errors.Errorf("--%s selects which composite resources a Composition change reaches, so it doesn't apply when -f names composite resource files; drop it, or pass Composition or project files", f)}
		}
	}
	if set["minimize-composition"] {
		_, _ = fmt.Fprintln(stderr, "Warning: ignoring --minimize-composition, which collapses changed Compositions; composite resource files have none.")
	}
	return nil
}

// Built is a built project: what Inputs.Build returns.
type Built struct {
	// Compositions are the project's Compositions.
	Compositions []*un.Unstructured
	// Functions are the project's functions.
	Functions []pkgv1.Function
	// Digests are the locally built functions' digests, by cluster name.
	Digests map[string]project.EmbeddedDigest
	// Packages is how the project's packages are fetched.
	Packages *Packages
}

// Inputs are the loaded -f inputs.
type Inputs struct {
	// Mode is what -f names.
	Mode Mode

	// Build builds the project, in ProjectMode. Run calls it after loading
	// the kubeconfig, so a bad context fails before the slow build.
	Build func(ctx context.Context) (*Built, error)

	// Resources are the Compositions or composite resources to simulate,
	// in the other modes.
	Resources []*un.Unstructured
}
