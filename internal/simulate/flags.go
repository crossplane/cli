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
	"time"

	"github.com/spf13/afero"
	corev1 "k8s.io/api/core/v1"
	un "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	dp "github.com/crossplane/cli/v2/internal/diff/diffprocessor"
	"github.com/crossplane/cli/v2/internal/diffserver"
	"github.com/crossplane/cli/v2/internal/kube"
	xprender "github.com/crossplane/cli/v2/pkg/render"
)

// Flags are the flags, and the run state, of a simulate command.
type Flags struct {
	// EngineFlags select the crossplane render backend, as for render. Its
	// SessionID labels the diff server containers; the render's containers
	// are labelled with the run's own ID, which reclaims them.
	xprender.EngineFlags `prefix:"" set:"session_id_help=Session ID to apply as the simulate.crossplane.io/session label on diff server containers. Defaults to a random per-process ID. Ignored with --skip-plan."`

	// Run sets these; nil with --skip-plan (servers also when Hooks.Router is set).
	xrPlanner *XRPlanner
	// keep is the equal-diff filter the engine and xrPlanner share.
	keep    func(*un.Unstructured) bool
	servers *diffserver.Manager
	// noServersNoted is set once the "no diff servers" line is printed.
	noServersNoted bool
	// Validate loads this from --function-credentials.
	functionCredentials []corev1.Secret
	// Validate sets these from scope(), so --name is parsed once.
	ns   string
	refs []k8stypes.NamespacedName

	Input          []string `default:"${project_file}"   help:"Project file (default), or changed Composition or composite resource files. Takes one path per flag; repeat it for more. - reads standard input." placeholder:"PATH" sep:"none" short:"f"`
	MaxConcurrency uint     `default:"8"                 help:"Max concurrent function builds."`
	CacheDir       string   `env:"CROSSPLANE_XPKG_CACHE" help:"Directory for cached xpkg package contents (functions and providers)."                                                                            name:"cache-dir"`

	Context       string        `help:"Kubernetes context to simulate against. Defaults to the current context."                                                                                             predictor:"context"                                                          short:"c"`
	Namespace     string        `help:"Only simulate composite resources in this namespace. Defaults to all namespaces."                                                                                     predictor:"namespace"                                                        short:"n"`
	Name          []string      `help:"Only simulate the composite resource with this [namespace/]name. Repeatable or comma-separated. A bare name pairs with --namespace, or is cluster scoped without it." sep:","`
	IncludePinned bool          `help:"Render every composite resource pinned by a Manual update policy against the new Composition."`
	MaxIterations int           `default:"20"                                                                                                                                                                help:"Max render iterations per composite resource to resolve requirements."`
	Timeout       time.Duration `default:"2m"                                                                                                                                                                help:"Max time to simulate one composite resource."`
	Output        string        `default:"plan"                                                                                                                                                              enum:"plan,diff,json,yaml"                                                   help:"Output format. One of: plan, diff, json, yaml." short:"o"`

	NoColor             bool     `help:"Disable colorized output."`
	Compact             bool     `help:"Show compact diffs with minimal context."`
	MaxNestedDepth      int      `default:"10"                                                                                          help:"Maximum depth for nested XR recursion."`
	IgnorePaths         []string `help:"Paths to ignore in diffs (e.g., 'metadata.annotations[argocd.argoproj.io/tracking-id]')."`
	FunctionCredentials string   `help:"A YAML file or directory of YAML files specifying Secret credentials to pass to Functions."     placeholder:"PATH"                                                                    predictor:"yaml_file_or_directory"                                                                             type:"path"`
	EventualState       bool     `help:"Show eventual state after all reconciliation cycles complete (useful with function-sequencer)."`
	MaxRecvMessageSize  int      `env:"MAX_RECV_MESSAGE_SIZE"                                                                           help:"Max gRPC message size (MB) for render function containers (4 MB if undefined)."`
	MinimizeComposition bool     `help:"Collapse each changed Composition to one line in -o diff output."`
	AnalyzeOn           string   `default:""                                                                                            enum:",spec-change,any-change,always"                                                 help:"Smallest Composition change that triggers impact analysis: spec-change, any-change (default) or always."`
	DryRunOn            string   `default:"all"                                                                                         enum:"existing,all"                                                                   help:"Which resources to verify with a server-side dry run: existing or all (default)."`

	SkipPlan     bool   `help:"Don't run provider diff servers; show only the cluster diff."`
	Providers    string `help:"YAML file or directory of Provider manifests to run as diff servers, instead of discovering them from the cluster." name:"providers"     optional:"" predictor:"yaml_file_or_directory" type:"path"`
	ImageConfigs string `help:"YAML file of ImageConfig manifests that rewrite package paths for pulls, replacing the project's imageConfigs."     name:"image-configs" optional:"" predictor:"yaml_file"              type:"existingfile"`

	Impersonation kube.ImpersonationFlags `embed:""`

	// Hooks is for tests; the CLI never sets it.
	Hooks *Hooks `kong:"-"`
}

// Hooks replace what project simulate otherwise takes from its
// environment, so a test can run the real command against a cluster it
// started. Fields left zero keep the CLI's behavior.
type Hooks struct {
	// RESTConfig replaces loading the kubeconfig.
	RESTConfig *rest.Config

	// Terminal colorizes diffs as if stdout were a terminal, unless
	// --no-color is set.
	Terminal bool

	// Router, when set, routes provider plans through it instead of starting
	// diff servers.
	Router Router

	// ChangedFunctions marks these functions as changed in any mode, as
	// project mode's digest detection would. Test-only.
	ChangedFunctions []string
}

// hookChangedFunctions returns the functions a test marked as changed.
func (c *Flags) hookChangedFunctions() []string {
	if c.Hooks == nil {
		return nil
	}

	return c.Hooks.ChangedFunctions
}

// Validate rejects flag values the simulation can't run with. kong calls it
// during parsing, so an unsupported render backend fails before any build or
// cluster connection.
func (c *Flags) Validate() error {
	if c.MaxIterations < 1 {
		return errors.Errorf("--max-iterations must be at least 1, got %d", c.MaxIterations)
	}

	// Bad --name/--namespace values fail here, before any build.
	ns, refs, err := c.scope()
	if err != nil {
		return err
	}
	c.ns, c.refs = ns, refs

	if c.FunctionCredentials != "" {
		secrets, err := xprender.LoadCredentials(afero.NewOsFs(), c.FunctionCredentials)
		if err != nil {
			return errors.Wrapf(err, "cannot load function credentials from %q", c.FunctionCredentials)
		}
		c.functionCredentials = secrets
	}

	if c.Timeout <= 0 {
		return errors.Errorf("--timeout must be greater than 0, got %s", c.Timeout)
	}

	// The render backend flags are mutually exclusive (kong xor), so at most
	// one of these has anything to check.
	if c.CrossplaneImage != "" {
		return dp.ValidateMinRenderImage(c.CrossplaneImage)
	}
	if c.CrossplaneVersion != "" {
		return dp.ValidateMinRenderVersion(c.CrossplaneVersion)
	}

	return nil
}

// Credentials returns the Secrets Validate loaded from --function-credentials.
func (c *Flags) Credentials() []corev1.Secret {
	return c.functionCredentials
}
