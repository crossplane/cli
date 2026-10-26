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
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	"github.com/charmbracelet/x/term"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/spf13/afero"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	un "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	runtimexpkg "github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"

	apiextensionsv1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1"
	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"
	pkgv1beta1 "github.com/crossplane/crossplane/apis/v2/pkg/v1beta1"

	"github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/cmd/crossplane/common"
	ld "github.com/crossplane/cli/v2/cmd/crossplane/common/load"
	"github.com/crossplane/cli/v2/internal/config"
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
	"github.com/crossplane/cli/v2/internal/plan"
	"github.com/crossplane/cli/v2/internal/project"
	"github.com/crossplane/cli/v2/internal/project/projectfile"
	"github.com/crossplane/cli/v2/internal/simulate"
	"github.com/crossplane/cli/v2/internal/terminal"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"

	_ "embed"
)

//go:embed help/simulate.md
var simulateHelp string

// simulateCmd previews how a project's compositions, or changed Composition
// or composite resource files, would change the composite resources in a
// cluster.
type simulateCmd struct {
	// Run sets these; nil with --skip-plan (servers also when Hooks.Router is set).
	xrPlanner *simulate.XRPlanner
	servers   simulate.Servers
	// noServersNoted is set once the "no diff servers" line is printed.
	noServersNoted bool
	// Validate sets these from scope(), so --name is parsed once.
	ns   string
	refs []k8stypes.NamespacedName

	Input          []string `default:"${project_file}"   help:"Project file (default), or changed Composition or composite resource files. Takes one path per flag; repeat it for more. - reads standard input." placeholder:"PATH" sep:"none" short:"f"`
	MaxConcurrency uint     `default:"8"                 help:"Max concurrent function builds."`
	CacheDir       string   `env:"CROSSPLANE_XPKG_CACHE" help:"Directory for cached xpkg package contents (functions and providers)."                                                                            name:"cache-dir"`

	Context       string        `help:"Kubernetes context to simulate against. Defaults to the current context."                                                                                                                         predictor:"context"                                                                                                                                                                                            short:"c"`
	Namespace     string        `help:"Only simulate composite resources in this namespace. Defaults to all namespaces."                                                                                                                 predictor:"namespace"                                                                                                                                                                                          short:"n"`
	Name          []string      `help:"Only simulate the composite resource with this [namespace/]name. Repeatable or comma-separated. A bare name pairs with --namespace, or is cluster scoped without it."                             sep:","`
	IncludePinned bool          `help:"Render every composite resource with a Manual composition update policy against the new Composition, instead of only those pinned to a changed function, rendered against their pinned revision."`
	MaxIterations int           `default:"20"                                                                                                                                                                                            help:"Max render iterations per composite resource to resolve requirements."`
	Timeout       time.Duration `default:"2m"                                                                                                                                                                                            help:"Max time to simulate one composite resource: its render and its resources' provider plans. Function and provider image pulls don't count, but the render image pull counts against the first composite."`
	Output        string        `default:"plan"                                                                                                                                                                                          enum:"plan,diff,json,yaml"                                                                                                                                                                                     help:"Output format. One of: plan, diff, json, yaml." short:"o"`

	NoColor                  bool                `help:"Disable colorized output."`
	Compact                  bool                `help:"Show compact diffs with minimal context."`
	MaxNestedDepth           int                 `default:"10"                                                                                                                                help:"Maximum depth for nested XR recursion."`
	IgnorePaths              []string            `help:"Paths to ignore in diffs (e.g., 'metadata.annotations[argocd.argoproj.io/tracking-id]')."`
	FunctionCredentials      FunctionCredentials `help:"A YAML file or directory of YAML files specifying Secret credentials to pass to Functions."                                           placeholder:"PATH"`
	FunctionRegistryOverride string              `help:"Override the registry for all function images (e.g., 'my-company.registry.io')."`
	EventualState            bool                `help:"Show eventual state after all reconciliation cycles complete (useful with function-sequencer)."`
	MaxRecvMessageSize       int                 `env:"MAX_RECV_MESSAGE_SIZE"                                                                                                                 help:"Max gRPC message size (MB) for render function containers (4 MB if undefined)."`
	MinimizeComposition      bool                `help:"Collapse each changed composition to a single marker line in -o diff output (errors and no-change compositions still print in full)."`
	AnalyzeOn                string              `default:""                                                                                                                                  enum:",spec-change,any-change,always"                                                 help:"Smallest composition change that triggers per-composite impact analysis. \"spec-change\": only when the composition spec changes. \"any-change\": also when only its metadata changes, which still creates a new CompositionRevision that composites re-point to. \"always\": even when the composition is identical, for a pre-edit convergence baseline. Defaults to any-change."`
	DryRunOn                 string              `default:"all"                                                                                                                               enum:"existing,all"                                                                   help:"Which resources to verify against the apiserver with a dry run: 'all' also dry-run creates added resources so their diffs include server-side defaulting and admission (needs the 'create' verb; degrades per-resource without it), 'existing' only resources already in the cluster."`

	SkipPlan     bool   `help:"Don't run provider diff servers; show only the cluster-layer diff."`
	Providers    string `help:"YAML file or directory with Provider manifests to run as diff servers, overriding discovery from the cluster. Supports per-provider runtime annotations."                                                                                                           name:"providers"     optional:"" predictor:"yaml_file_or_directory" type:"path"`
	ImageConfigs string `help:"YAML file with ImageConfig manifests that rewrite provider image paths for local pulls. Defaults to the project's imageConfigs when -f names a project file; in Composition and XR modes it falls back, best effort, to the project file in the current directory." name:"image-configs" optional:"" predictor:"yaml_file"              type:"existingfile"`
	SessionID    string `help:"Session ID to apply as the simulate.crossplane.io/session label on diff server containers. Defaults to a random per-process ID."                                                                                                                                    placeholder:"ID"`

	// CrossplaneVersion and CrossplaneImage select the crossplane render
	// backend, as they do for crossplane-diff. When neither is set the render
	// engine pulls xpkg.crossplane.io/crossplane/crossplane:stable.
	CrossplaneVersion string `help:"Pin the crossplane render version, for example v2.3.4. Minimum v2.3.4."                                  placeholder:"VERSION" xor:"crossplane-render-backend"`
	CrossplaneImage   string `help:"Override the full crossplane render image reference. Minimum v2.3.4 when its tag is a semantic version." placeholder:"IMAGE"   xor:"crossplane-render-backend"`

	Impersonation kube.ImpersonationFlags `embed:""`

	// Hooks is for tests; the CLI never sets it.
	Hooks *SimulateHooks `kong:"-"`
}

// SimulateHooks replace what project simulate otherwise takes from its
// environment, so a test can run the real command against a cluster it
// started. Fields left zero keep the CLI's behavior.
type SimulateHooks struct {
	// RESTConfig replaces loading the kubeconfig.
	RESTConfig *rest.Config

	// RenderBinary renders with this local crossplane binary instead of the
	// crossplane render image.
	RenderBinary string

	// Terminal colorizes diffs as if stdout were a terminal, unless
	// --no-color is set.
	Terminal bool

	// Router, when set, routes provider plans through it instead of starting
	// diff servers.
	Router simulate.Router

	// Servers, when set, replaces the diff server set that would otherwise
	// start provider servers; everything else still runs. Test-only. It
	// can't be combined with Router.
	Servers simulate.Servers

	// ChangedFunctions marks these functions as changed in any mode, as
	// project mode's digest detection would. Test-only.
	ChangedFunctions []string
}

// hookChangedFunctions returns the functions a test marked as changed.
func (c *simulateCmd) hookChangedFunctions() []string {
	if c.Hooks == nil {
		return nil
	}

	return c.Hooks.ChangedFunctions
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
	if c.Hooks != nil && c.Hooks.Router != nil && c.Hooks.Servers != nil {
		return errors.New("hooks Router and Servers are mutually exclusive")
	}

	if c.MaxIterations < 1 {
		return errors.Errorf("--max-iterations must be at least 1, got %d", c.MaxIterations)
	}

	if c.MaxConcurrency == 0 {
		return errors.Errorf("--max-concurrency must be at least 1, got %d", c.MaxConcurrency)
	}

	// Bad --name/--namespace values fail here, before any build.
	ns, refs, err := c.scope()
	if err != nil {
		return err
	}
	c.ns, c.refs = ns, refs

	if c.Timeout <= 0 {
		return errors.Errorf("--timeout must be greater than 0, got %s", c.Timeout)
	}

	if c.SkipPlan {
		for _, f := range []struct{ name, val string }{{"providers", c.Providers}, {"image-configs", c.ImageConfigs}, {"session-id", c.SessionID}} {
			if f.val != "" {
				return errors.Errorf("--%s cannot be used with --skip-plan", f.name)
			}
		}
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

// Run previews the inputs -f names against the cluster: a project's
// compositions, with its functions built locally, or changed Composition or
// composite resource files, with functions from the cluster.
func (c *simulateCmd) Run(sigCtx context.Context, k *kong.Context, logger logging.Logger, sp terminal.SpinnerPrinter, cliCfg *config.Config, status *common.ExitStatus) error {
	in, err := loadInputs(c.Input)
	if err != nil {
		return err
	}
	if err := validateModeFlags(in.mode, setFlags(k), c.SkipPlan); err != nil {
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
	var proj *v1alpha1.Project
	comps := in.resources
	if in.mode == projectMode {
		comps, fns, digests, proj, err = c.buildProject(sigCtx, logger, sp, cliCfg, in.projectFile)
		if err != nil {
			return err
		}
	}

	clients, err := client.New(restCfg, logger)
	if err != nil {
		return err
	}

	changed, err := changedFunctions(sigCtx, clients.K8sClients.Resource, digests)
	if err != nil {
		return err
	}

	opts := append(c.processorOptions(k, logger, fns), dp.WithChangedFunctions(append(changed, c.hookChangedFunctions()...)...), dp.WithXRTimeout(c.Timeout))

	// xrPlanner and servers are for rendering the plans; they stay nil with
	// --skip-plan, and servers also when Hooks.Router supplies the router.
	if !c.SkipPlan {
		planOpts, err := c.planOptions(sigCtx, k, logger, sp, restCfg, ns, in, proj)
		// Stop even on error: upfront Ensure may have started servers.
		if c.servers != nil {
			defer c.servers.Stop(sigCtx)
		}
		if err != nil {
			return err
		}
		opts = append(opts, planOpts...)
	}
	xrProc := dp.NewDiffProcessor(clients.K8sClients, clients.XpClients, opts...)

	ctx := sigCtx

	if in.mode == xrMode {
		// Compositions and functions come from the cluster, as in
		// crossplane-diff's xr command. A claim is simulated as its XR, but
		// the report names the input, so its result is listed under the
		// claim's name (crossplane/cli#367).
		return c.simulate(ctx, logger, status, clients.Initialize, xrProc, func(ctx context.Context) (bool, error) {
			if err := checkComposites(ctx, clients.XpClients.Definition, in.resources); err != nil {
				return false, err
			}
			report, err := xrProc.PerformDiff(ctx, in.resources, clients.XpClients.Composition.FindMatchingComposition)

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
	return c.simulate(ctx, logger, status, clients.Initialize, proc, c.diffCompositions(k, logger, proc, comps, in.mode))
}

// planOptions sets up provider diff servers and the XRPlanner, and returns
// the processor options that plan each composite resource's composed
// resources. Namespaced resources without a namespace plan in ns, the
// kubeconfig's namespace. Run stops c.servers when it is set.
func (c *simulateCmd) planOptions(sigCtx context.Context, k *kong.Context, logger logging.Logger, sp terminal.SpinnerPrinter, restCfg *rest.Config, ns string, in *inputs, proj *v1alpha1.Project) ([]dp.ProcessorOption, error) {
	kubeClient, rmapper, err := simulate.NewKubeClient(restCfg)
	if err != nil {
		return nil, err
	}

	var ics []pkgv1beta1.ImageConfig
	var router simulate.Router
	var depMgr *dependency.Manager
	if c.Hooks != nil && c.Hooks.Router != nil {
		router = c.Hooks.Router
	} else {
		projectFile := ""
		if in.mode == projectMode {
			projectFile = in.projectFile
		}
		if ics, err = simulate.ImageConfigs(c.ImageConfigs, projectFile); err != nil {
			return nil, err
		}
		var pkgs runtimexpkg.Client
		var resolver *clixpkg.Resolver
		if pkgs, resolver, err = c.newClientAndResolver(clixpkg.WithImageConfigs(ics)); err != nil {
			return nil, errors.Wrap(err, "cannot create package client")
		}
		if in.mode == projectMode {
			projFilePath, err := filepath.Abs(in.projectFile)
			if err != nil {
				return nil, err
			}
			depMgr = dependency.NewManager(proj, afero.NewBasePathFs(afero.NewOsFs(), filepath.Dir(projFilePath)),
				dependency.WithProjectFile(filepath.Base(projFilePath)),
				dependency.WithXpkgClient(pkgs),
				dependency.WithResolver(resolver),
			)
		}
		if c.Hooks != nil && c.Hooks.Servers != nil {
			c.servers = c.Hooks.Servers
		} else {
			c.servers = simulate.NewServerSet(sp, logger, k.Stderr, pkgs, c.SessionID)
		}
		router = c.servers
	}

	var ensure simulate.EnsureServers
	if c.servers != nil {
		// --providers, else the project's provider dependencies, start up
		// front; otherwise servers start per composite resource.
		var upfront []diffserver.Input
		switch {
		case c.Providers != "":
			upfront, err = simulate.LoadProviderInputs(c.Providers)
			if err != nil {
				return nil, errors.Wrapf(err, "cannot load provider manifests from %q", c.Providers)
			}
		case in.mode == projectMode:
			images, err := project.LoadProviderDependencies(depMgr, proj)
			if err != nil {
				return nil, errors.Wrap(err, "cannot load the project's provider dependencies")
			}
			upfront = diffserver.Images(images)
		default:
			cache := simulate.NewImageCache()
			ensure = func(ctx context.Context, rs []*un.Unstructured) error {
				imgs := simulate.RelevantProviderImages(ctx, kubeClient, rmapper, rs, len(ics) > 0, cache, k.Stderr)
				return c.servers.Ensure(ctx, diffserver.Images(imgs))
			}
		}
		if ensure == nil {
			if err := c.servers.Ensure(sigCtx, upfront); err != nil {
				return nil, err
			}
		}
	}

	keep := simulate.ProviderManaged(sigCtx, kubeClient, rmapper)
	c.xrPlanner = simulate.NewXRPlanner(simulate.NewPlanner(kubeClient, rmapper, ns, router, c.Timeout), keep, ensure)
	return []dp.ProcessorOption{dp.WithAfterXRDiff(c.xrPlanner.AfterXRDiff), dp.WithKeepEqualDiffs(keep)}, nil
}

// noteNoServers prints once, when planning is on and no diff server ever
// ran, that the diff shows only the cluster layer. -o diff has no notes.
func (c *simulateCmd) noteNoServers(k *kong.Context) {
	if c.servers == nil || c.servers.Ran() || c.noServersNoted || c.xrPlanner == nil || c.xrPlanner.Considered() == 0 {
		return
	}
	c.noServersNoted = true
	_, _ = fmt.Fprintln(k.Stderr, "No provider diff servers to run; showing the cluster-layer diff.")
}

// restConfig returns the cluster to simulate against and the namespace for
// namespaced resources that set none: the hooks' cluster and "default" when
// set, otherwise the kubeconfig's cluster and namespace. Unlike kubectl's
// -n, simulate's -n filters composite resources, so it doesn't override this
// namespace.
func (c *simulateCmd) restConfig() (*rest.Config, string, error) {
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

// buildProject returns the project's compositions and its locally built
// functions.
func (c *simulateCmd) buildProject(ctx context.Context, logger logging.Logger, sp terminal.SpinnerPrinter, cliCfg *config.Config, projectFile string) ([]*un.Unstructured, []pkgv1.Function, map[string]project.EmbeddedDigest, *v1alpha1.Project, error) {
	projFilePath, err := filepath.Abs(projectFile)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	projFS := afero.NewBasePathFs(afero.NewOsFs(), filepath.Dir(projFilePath))
	projFileName := filepath.Base(projFilePath)

	proj, err := projectfile.Parse(projFS, projFileName)
	if err != nil {
		return nil, nil, nil, nil, errors.Wrapf(err, "failed to parse project file %q", projectFile)
	}

	comps, err := project.LoadCompositions(proj, projFS)
	if err != nil {
		return nil, nil, nil, nil, errors.Wrap(err, "cannot load compositions from project")
	}
	if len(comps) == 0 {
		return nil, nil, nil, nil, errors.Errorf("no Compositions found under the project's APIs path %q", proj.Spec.Paths.APIs)
	}

	fns, digests, err := project.LoadFunctionsWithDigests(ctx, logger, sp, cliCfg, projFS, projFilePath, projFileName, c.MaxConcurrency, c.newClientAndResolver)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return comps, fns, digests, proj, nil
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
func (c *simulateCmd) diffCompositions(k *kong.Context, logger logging.Logger, proc dp.CompDiffProcessor, comps []*un.Unstructured, mode inputMode) func(context.Context) (bool, error) {
	msg := "cannot simulate Compositions"
	if mode == projectMode {
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

// inputMode is what -f names, which decides where the simulation's
// Compositions and functions come from.
type inputMode int

const (
	// projectMode simulates a project: its Compositions, with its functions
	// built locally and overlaid on the cluster's.
	projectMode inputMode = iota
	// compositionMode simulates changed Compositions against the composite
	// resources on the cluster that use them, with the cluster's functions.
	compositionMode
	// xrMode simulates changed composite resources (or claims) with the
	// cluster's Compositions and functions.
	xrMode
)

// inputs are the loaded -f inputs.
type inputs struct {
	mode inputMode

	// projectFile is the project file, in projectMode.
	projectFile string

	// resources are the Compositions or composite resources to simulate,
	// in the other modes.
	resources []*un.Unstructured
}

// loadInputs loads and classifies the -f inputs. A single project file
// selects projectMode. Anything else is loaded as resources: Compositions
// select compositionMode, and resources with no Composition among them
// select xrMode. A project file among other inputs, or Compositions mixed
// with other resources, are rejected.
func loadInputs(paths []string) (*inputs, error) {
	if len(paths) == 1 && paths[0] != "-" {
		isProj, err := isProjectFile(paths[0])
		if err != nil {
			return nil, err
		}
		if isProj {
			return &inputs{mode: projectMode, projectFile: paths[0]}, nil
		}
	}

	loader, err := ld.NewExactCompositeLoader(paths)
	if err != nil {
		return nil, errors.Wrap(err, "cannot load -f inputs")
	}
	resources, err := loader.Load()
	if err != nil {
		return nil, errors.Wrap(err, "cannot load -f inputs")
	}
	// The loader also returns each Composition's pipeline step inputs, which
	// are part of the Composition rather than resources of their own.
	stepInputs, err := pipelineInputs(resources)
	if err != nil {
		return nil, err
	}

	var comps, others []*un.Unstructured
	for _, r := range resources {
		gvk := r.GroupVersionKind()
		switch {
		case gvk.GroupVersion().String() == projectfile.APIVersion && gvk.Kind == projectfile.Kind:
			return nil, errors.New("a project file must be the only -f input, named by its path; pass it alone, or pass Composition or composite resource files without it")
		case gvk == apiextensionsv1.CompositionGroupVersionKind:
			comps = append(comps, r)
		default:
			key, err := json.Marshal(r.Object)
			if err != nil {
				return nil, errors.Wrapf(err, "cannot compare %s %q to Composition pipeline inputs", gvk.Kind, r.GetName())
			}
			if stepInputs[string(key)] > 0 {
				stepInputs[string(key)]--
				continue
			}
			others = append(others, r)
		}
	}

	switch {
	case len(comps) == 0:
		return &inputs{mode: xrMode, resources: others}, nil
	case len(others) == 0:
		return &inputs{mode: compositionMode, resources: comps}, nil
	default:
		return nil, errors.Errorf("-f inputs mix Compositions with other resources (%s %q); simulate Compositions and composite resources in separate runs", others[0].GetKind(), others[0].GetName())
	}
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

// isProjectFile reports whether path is a project file. A directory is not.
func isProjectFile(path string) (bool, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return false, errors.Wrapf(err, "cannot read -f input %q", path)
	}
	if fi.IsDir() {
		return false, nil
	}
	isProj, err := projectfile.IsProjectFile(afero.NewOsFs(), path)
	return isProj, errors.Wrapf(err, "cannot read -f input %q", path)
}

// pipelineInputs returns the JSON of every pipeline step input of the
// Compositions in resources, counted, so loadInputs can tell the inputs the
// loader extracted from those Compositions from resources of their own.
func pipelineInputs(resources []*un.Unstructured) (map[string]int, error) {
	inputs := map[string]int{}
	for _, r := range resources {
		if r.GroupVersionKind() != apiextensionsv1.CompositionGroupVersionKind {
			continue
		}
		steps, _, _ := un.NestedSlice(r.Object, "spec", "pipeline")
		for _, step := range steps {
			m, ok := step.(map[string]any)
			if !ok {
				// Let the engine report the malformed Composition.
				continue
			}
			in, ok := m["input"]
			if !ok || in == nil {
				continue
			}
			key, err := json.Marshal(in)
			if err != nil {
				return nil, errors.Wrapf(err, "cannot read the pipeline inputs of Composition %q", r.GetName())
			}
			inputs[string(key)]++
		}
	}
	return inputs, nil
}

// validateModeFlags rejects flags set on the command line that don't apply
// to mode. Composite resource files name the composite resources to
// simulate, so the flags that pick and tune which ones a Composition change
// reaches don't apply to them, as in crossplane-diff, whose xr command lacks
// its comp command's equivalents. Functions come from the cluster unless -f
// names a project, so --max-concurrency applies only to a project; --cache-dir also caches provider packages, so it applies whenever planning is on.
func validateModeFlags(mode inputMode, set map[string]bool, skipPlan bool) error {
	if mode == projectMode {
		return nil
	}
	if set["max-concurrency"] {
		return errors.New("--max-concurrency configures the project's function builds, so it applies only when -f names a project file; Composition and composite resource files use the cluster's functions")
	}
	if set["cache-dir"] && skipPlan {
		return errors.New("--cache-dir caches function builds in project mode and provider packages when planning; with --skip-plan and no project file there is nothing to cache, so drop it or drop --skip-plan")
	}
	if mode != xrMode {
		return nil
	}
	for _, f := range []string{"namespace", "name", "include-pinned", "minimize-composition", "analyze-on"} {
		if set[f] {
			return errors.Errorf("--%s selects which composite resources a Composition change reaches, so it doesn't apply when -f names composite resource files; drop it, or pass Composition or project files", f)
		}
	}
	return nil
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
// finds changes and fails nothing sets status to 3 and returns nil; one that
// fails only schema validation returns an error that exits 2. ctx must not be
// cancelled by its owner until simulate returns, so a cancellation reads as
// an interrupt.
func (c *simulateCmd) simulate(ctx context.Context, logger logging.Logger, status *common.ExitStatus, initClients func(context.Context, logging.Logger) error, proc processor, diff func(context.Context) (bool, error)) (err error) {
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
	switch code := dp.DetermineExitCode(err, hasDiffs); code {
	case dp.ExitCodeSchemaValidation:
		return exitError{error: err, code: code}
	case dp.ExitCodeDiffDetected:
		// Changes found are a result, not an error, so nothing is printed.
		status.Code = code
		return nil
	}

	return err
}

// scope returns the namespace and composite resource refs DiffComposition
// limits its impact analysis to. Each --name selects one composite resource: a
// bare name is namespaced when --namespace is set and cluster scoped
// otherwise, and namespace/name carries its own namespace. The namespace moves
// into the refs, so it is not also returned.
func (c *simulateCmd) scope() (string, []k8stypes.NamespacedName, error) {
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
// does, so they share one advisory collector.
func (c *simulateCmd) processorOptions(k *kong.Context, logger logging.Logger, fns []pkgv1.Function) []dp.ProcessorOption {
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

	// Project functions, built locally, replace the cluster's. With none,
	// every function comes from the cluster.
	if len(fns) > 0 {
		opts = append(opts, dp.WithFunctionProviderFactory(dp.NewProjectFunctionProviderFactory(fns)))
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
	if c.Hooks != nil && c.Hooks.RenderBinary != "" {
		opts = append(opts, dp.WithCrossplaneRenderBinary(c.Hooks.RenderBinary))
	}

	return opts
}

// diffOptions returns the renderer options the command's flags select,
// writing to k's stdout and stderr. The processors return reports; the command
// renders them with these options. Diffs are colorized only when a person
// reads them: on a terminal, or as if on one under the test hooks.
func (c *simulateCmd) diffOptions(k *kong.Context) renderer.DiffOptions {
	opts := renderer.DefaultDiffOptions()
	opts.UseColors = !c.NoColor && (c.Hooks != nil && c.Hooks.Terminal || isTerminal(k.Stdout))
	opts.Compact = c.Compact
	opts.Stdout = k.Stdout
	opts.Stderr = k.Stderr
	return opts
}

// providerPlans returns the provider plans the run collected, or nil when
// planning was skipped.
func (c *simulateCmd) providerPlans() map[string]*plan.ResourcePlan {
	if c.xrPlanner == nil {
		return nil
	}
	return c.xrPlanner.Plans()
}

// planNotes returns the notes that replace the result's own, or nil to keep
// them. planned is true when planning ran; routed counts the plans a diff
// server served, and considered the resources the planner was asked to route.
func planNotes(skipPlan, planned bool, routed, considered int) []string {
	switch {
	case skipPlan:
		return []string{simulate.NoteProviderPlanSkipped}
	case planned && considered > 0 && routed == 0:
		return []string{simulate.NoteNoDiffServers}
	}
	return nil
}

func (c *simulateCmd) notes() []string {
	if c.xrPlanner == nil {
		return planNotes(c.SkipPlan, false, 0, 0)
	}
	return planNotes(c.SkipPlan, true, c.xrPlanner.Routed(), c.xrPlanner.Considered())
}

// renderXRReport writes an xr or project run's report in the format -o
// selects, as renderReport does.
func (c *simulateCmd) renderXRReport(k *kong.Context, logger logging.Logger, r *dt.XRReport, runErr error) (bool, error) {
	plans := c.providerPlans()
	return c.renderReport(k, logger, runErr, simulate.FromXRReport(r, plans), r.HasChanges(), dt.XRDryRunWarnings(r.Groups), func(opts renderer.DiffOptions) error {
		if plans != nil {
			r = simulate.AnnotateXRReport(r, plans)
		}
		return renderer.NewDiffRenderer(logger, opts).RenderDiffs(r.Groups, r.Errors, r.Warnings)
	})
}

// renderCompReport writes a composition run's report in the format -o
// selects, as renderReport does.
func (c *simulateCmd) renderCompReport(k *kong.Context, logger logging.Logger, o *dt.CompDiffOutput, runErr error) (bool, error) {
	plans := c.providerPlans()
	return c.renderReport(k, logger, runErr, simulate.FromCompReport(o, plans), o.HasChanges(), dt.CompDryRunWarnings(o), func(opts renderer.DiffOptions) error {
		opts.MinimizeComposition = c.MinimizeComposition
		if plans != nil {
			o = simulate.AnnotateCompReport(o, plans)
		}
		return renderer.NewDefaultCompDiffRenderer(logger, renderer.NewDiffRenderer(logger, opts), opts).RenderCompDiff(o)
	})
}

// renderReport writes a run's report in the format -o selects: renderDiff's
// human diff for diff, and res otherwise, followed by warnings. It returns
// whether the run changed anything, provider plans included, and runErr
// with any render failure joined to it, so neither is lost. changed is
// whether the report itself has changes.
func (c *simulateCmd) renderReport(k *kong.Context, logger logging.Logger, runErr error, res plan.Result, changed bool, warnings []dt.OutputWarning, renderDiff func(renderer.DiffOptions) error) (bool, error) {
	c.noteNoServers(k)
	opts := c.diffOptions(k)
	plans := c.providerPlans()
	if plans != nil {
		opts.Replaced = simulate.ReplacedBy(plans)
	}
	if n := c.notes(); n != nil {
		res.Notes = n
	}
	changed = changed || (plans != nil && simulate.Changed(res))

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
func (c *simulateCmd) renderPlan(k *kong.Context, res plan.Result, derived []dt.OutputWarning) error {
	r, err := plan.NewRenderer(c.Output)
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
