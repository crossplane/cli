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
	"context"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"

	diffv1alpha1 "github.com/crossplane/upjet/v2/proto/diff/v1alpha1"
	"github.com/spf13/afero"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	runtimexpkg "github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"

	"github.com/crossplane/crossplane/apis/v2/pkg"
	pkgv1beta1 "github.com/crossplane/crossplane/apis/v2/pkg/v1beta1"

	"github.com/crossplane/cli/v2/cmd/crossplane/common/load"
	"github.com/crossplane/cli/v2/internal/async"
	"github.com/crossplane/cli/v2/internal/containerruntime"
	"github.com/crossplane/cli/v2/internal/diffserver"
	"github.com/crossplane/cli/v2/internal/project/projectfile"
	"github.com/crossplane/cli/v2/internal/terminal"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"
)

// Per-provider runtime annotations on Provider manifests passed via
// --providers, mirroring render's per-function runtime annotations.
const (
	// AnnotationKeyRuntime selects how a provider's diff server is run:
	// Docker (the default) runs the package image as a local container,
	// Development connects to a diff server already running outside Docker,
	// for example from source or under a debugger.
	AnnotationKeyRuntime = "simulate.crossplane.io/runtime"

	// AnnotationKeyDockerPublishAddress sets the address Docker publishes
	// this provider's diff server port on. Defaults to loopback only.
	AnnotationKeyDockerPublishAddress = "simulate.crossplane.io/runtime-docker-publish-address"
	// AnnotationKeyDockerTarget sets the host used to connect to this
	// provider's diff server when it differs from the publish address, e.g.
	// with a VM-based or remote Docker daemon.
	AnnotationKeyDockerTarget = "simulate.crossplane.io/runtime-docker-target"

	// AnnotationKeyDockerCleanup configures how a provider's diff server
	// container is cleaned up once the run is done: Remove (the default)
	// tears it down, Orphan leaves it running so later runs reuse it without
	// paying startup.
	AnnotationKeyDockerCleanup = "simulate.crossplane.io/runtime-docker-cleanup"

	// AnnotationKeyDevelopmentTarget is the gRPC target of a Development
	// provider's diff server. Defaults to localhost:9099, upjet's default
	// for `internal diff-server --address`.
	AnnotationKeyDevelopmentTarget = "simulate.crossplane.io/runtime-development-target"

	// AnnotationKeyDevelopmentAPIGroups lists, comma-separated, the API
	// groups a Development provider's diff server serves. No package is
	// fetched for a Development provider, so nothing else says which
	// resources route to it. Legacy and namespaced (.m.) groups are distinct
	// and must both be listed.
	AnnotationKeyDevelopmentAPIGroups = "simulate.crossplane.io/runtime-development-api-groups"

	// defaultDevelopmentTarget is upjet's default diff server address.
	defaultDevelopmentTarget = "localhost:9099"
)

// Servers is what a simulation needs from its diff servers: start them,
// route resources to them, and stop them. *ServerSet implements it.
type Servers interface {
	Router
	Ensure(ctx context.Context, inputs []diffserver.Input) error
	// Stop must stop the servers even when ctx is already cancelled.
	Stop(ctx context.Context)
}

var _ Servers = (*ServerSet)(nil)

// ServerSet runs provider diff servers for a simulation, starting each
// provider's server the first time it is asked for. It routes resources to
// the servers it runs.
type ServerSet struct {
	sp      terminal.SpinnerPrinter
	stderr  io.Writer
	mgr     *diffserver.Manager
	handled map[string]bool
	mu      sync.Mutex
}

// NewServerSet returns a ServerSet that starts no servers until Ensure.
func NewServerSet(sp terminal.SpinnerPrinter, log logging.Logger, stderr io.Writer, pkgs runtimexpkg.Client, session string) *ServerSet {
	return &ServerSet{
		sp:      sp,
		stderr:  stderr,
		mgr:     diffserver.NewManager(pkgs, log, diffserver.WithSessionID(session)),
		handled: map[string]bool{},
	}
}

// Ensure starts a server for each input whose Image has not been asked for
// before, and prints the ones it skipped. Inputs it has already handled,
// whether they started or were skipped, are not retried.
func (s *ServerSet) Ensure(ctx context.Context, inputs []diffserver.Input) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var fresh []diffserver.Input
	for _, in := range inputs {
		// A Development provider has no image; its manifest name identifies it.
		key := in.Image
		if key == "" {
			key = in.Name
		}
		if s.handled[key] {
			continue
		}
		s.handled[key] = true
		fresh = append(fresh, in)
	}
	if len(fresh) == 0 {
		return nil
	}

	var skipped []diffserver.Skipped
	if err := s.sp.WrapAsyncWithSuccessSpinners(func(ch async.EventChannel) error {
		var err error
		skipped, err = s.mgr.Start(ctx, fresh, ch)
		return err
	}); err != nil {
		return errors.Wrap(err, "cannot start diff servers")
	}
	for _, sk := range skipped {
		_, _ = fmt.Fprintf(s.stderr, "Not running a diff server for %s: %s\n", sk.Image, sk.Reason)
	}
	return nil
}

// Route implements Router.
func (s *ServerSet) Route(gk schema.GroupKind) (diffv1alpha1.PlanServiceClient, bool) {
	return s.mgr.Route(gk)
}

// Stop stops every server; it runs even when ctx was cancelled, so an
// interrupted run still tears its containers down.
func (s *ServerSet) Stop(ctx context.Context) {
	if err := s.mgr.Stop(context.WithoutCancel(ctx)); err != nil {
		_, _ = fmt.Fprintf(s.stderr, "Warning: cannot clean up diff servers: %s\n", err)
	}
}

// StartDiffServers starts a diff server for each input and returns the set to
// plan through and a func that stops the servers. It prints noProviders to
// stderr when there are no inputs, and the servers it skipped.
func StartDiffServers(ctx context.Context, sp terminal.SpinnerPrinter, log logging.Logger, stderr io.Writer,
	pkgs runtimexpkg.Client, inputs []diffserver.Input, session, noProviders string,
) (*ServerSet, func(), error) {
	if len(inputs) == 0 {
		_, _ = fmt.Fprintln(stderr, noProviders)
	}
	set := NewServerSet(sp, log, stderr, pkgs, session)
	if err := set.Ensure(ctx, inputs); err != nil {
		set.Stop(ctx)
		return nil, nil, err
	}
	return set, func() { set.Stop(ctx) }, nil
}

// ImageConfigs returns the ImageConfigs to rewrite provider image paths with:
// the imageConfigsFile when given, else the imageConfigs of the project
// the command happens to run in, if any. Running outside a project is the
// norm for this command, so a missing project file is only an error when the
// user asked for one explicitly with projectFile.
func ImageConfigs(imageConfigsFile, projectFile string) ([]pkgv1beta1.ImageConfig, error) {
	if imageConfigsFile != "" {
		ics, err := LoadImageConfigs(imageConfigsFile)
		return ics, errors.Wrapf(err, "cannot load image configs from %q", imageConfigsFile)
	}

	name, explicit := projectFile, true
	if name == "" {
		name, explicit = clixpkg.ProjectFile, false
	}
	projFilePath, err := filepath.Abs(name)
	if err != nil {
		return nil, errors.Wrapf(err, "cannot determine path for %q", name)
	}
	if _, err := os.Stat(projFilePath); err != nil {
		if explicit {
			return nil, errors.Wrapf(err, "cannot read project file %q", name)
		}
		return nil, nil // Best effort: no project file means no project image configs.
	}
	projFS := afero.NewBasePathFs(afero.NewOsFs(), filepath.Dir(projFilePath))
	proj, err := projectfile.Parse(projFS, filepath.Base(projFilePath))
	if err != nil {
		return nil, errors.Wrapf(err, "cannot parse project file %q", name)
	}
	return proj.Spec.ImageConfigs, nil
}

// NeedsDocker reports whether any input runs its diff server as a container.
func NeedsDocker(inputs []diffserver.Input) bool {
	for _, in := range inputs {
		if in.Runtime != diffserver.RuntimeDevelopment {
			return true
		}
	}
	return false
}

// LoadProviderInputs reads Provider manifests and turns them into diff server
// inputs per their simulate.crossplane.io runtime annotations: the package
// image from spec.package with per-provider Docker overrides, or, for the
// Development runtime, the target of an already running server and the API
// groups it serves. A group can route to only one development server, so
// manifests that declare the same group are rejected up front.
func LoadProviderInputs(path string) ([]diffserver.Input, error) {
	loader, err := load.NewLoader(path)
	if err != nil {
		return nil, err
	}
	objs, err := loader.Load()
	if err != nil {
		return nil, err
	}

	inputs := make([]diffserver.Input, 0, len(objs))
	declared := map[string]string{} // API group -> the Development provider declaring it
	for _, o := range objs {
		if o.GetKind() != "Provider" {
			return nil, errors.Errorf("expected only Provider manifests, found %s", o.GetKind())
		}
		in, err := providerInput(o)
		if err != nil {
			return nil, err
		}
		for _, g := range in.APIGroups {
			if other, ok := declared[g]; ok {
				return nil, errors.Errorf("providers %q and %q both declare the API group %q in their %s annotation: a group can route to only one development diff server",
					other, o.GetName(), g, AnnotationKeyDevelopmentAPIGroups)
			}
			declared[g] = o.GetName()
		}
		inputs = append(inputs, in)
	}
	return inputs, nil
}

// providerInput turns one Provider manifest into a diff server input per its
// runtime annotation.
func providerInput(o *unstructured.Unstructured) (diffserver.Input, error) {
	switch r := diffserver.Runtime(o.GetAnnotations()[AnnotationKeyRuntime]); r {
	case diffserver.RuntimeDocker, "":
		return dockerInput(o)
	case diffserver.RuntimeDevelopment:
		return developmentInput(o)
	default:
		return diffserver.Input{}, errors.Errorf("provider %q has an unsupported %s annotation value %q: must be %s or %s",
			o.GetName(), AnnotationKeyRuntime, r, diffserver.RuntimeDocker, diffserver.RuntimeDevelopment)
	}
}

// dockerInput reads a provider whose package image runs as a container: the
// image from spec.package and the Docker overrides from its annotations.
func dockerInput(o *unstructured.Unstructured) (diffserver.Input, error) {
	in := diffserver.Input{}
	if in.Image, _, _ = unstructured.NestedString(o.Object, "spec", "package"); in.Image == "" {
		return diffserver.Input{}, errors.Errorf("provider %q has no spec.package", o.GetName())
	}
	annotations := o.GetAnnotations()
	if v := annotations[AnnotationKeyDockerPublishAddress]; v != "" {
		addr, err := netip.ParseAddr(v)
		if err != nil {
			return diffserver.Input{}, errors.Wrapf(err, "provider %q has an invalid %s annotation", o.GetName(), AnnotationKeyDockerPublishAddress)
		}
		in.PublishAddress = addr
	}
	in.Target = annotations[AnnotationKeyDockerTarget]
	switch cleanup := diffserver.Cleanup(annotations[AnnotationKeyDockerCleanup]); cleanup {
	case diffserver.CleanupRemove, diffserver.CleanupOrphan, "":
		in.Cleanup = cleanup
	case containerruntime.CleanupStop:
		// Render's Stop policy is deliberately unsupported: a stopped
		// diff server serves nothing, and restarting one costs nearly as
		// much as starting it.
		fallthrough
	default:
		return diffserver.Input{}, errors.Errorf("provider %q has an invalid %s annotation %q: must be %s or %s",
			o.GetName(), AnnotationKeyDockerCleanup, cleanup, diffserver.CleanupRemove, diffserver.CleanupOrphan)
	}
	return in, nil
}

// developmentInput reads a provider whose diff server the user already runs.
// Nothing is fetched and nothing starts for it, so spec.package is not
// needed and the Docker annotations are ignored, the way render ignores them
// on a Development function. The served API groups must be declared instead.
func developmentInput(o *unstructured.Unstructured) (diffserver.Input, error) {
	annotations := o.GetAnnotations()
	in := diffserver.Input{
		Runtime:           diffserver.RuntimeDevelopment,
		Name:              o.GetName(),
		DevelopmentTarget: defaultDevelopmentTarget,
	}
	if t := annotations[AnnotationKeyDevelopmentTarget]; t != "" {
		in.DevelopmentTarget = t
	}
	for g := range strings.SplitSeq(annotations[AnnotationKeyDevelopmentAPIGroups], ",") {
		if g = strings.TrimSpace(g); g != "" {
			in.APIGroups = append(in.APIGroups, g)
		}
	}
	if len(in.APIGroups) == 0 {
		return diffserver.Input{}, errors.Errorf("provider %q uses the Development runtime but declares no API groups: set its %s annotation to the comma-separated groups its diff server serves, for example ec2.aws.upbound.io,ec2.aws.m.upbound.io",
			o.GetName(), AnnotationKeyDevelopmentAPIGroups)
	}
	return in, nil
}

// LoadImageConfigs reads ImageConfig manifests from a YAML file.
func LoadImageConfigs(path string) ([]pkgv1beta1.ImageConfig, error) {
	loader, err := load.NewLoader(path)
	if err != nil {
		return nil, err
	}
	objs, err := loader.Load()
	if err != nil {
		return nil, err
	}

	ics := make([]pkgv1beta1.ImageConfig, 0, len(objs))
	for _, o := range objs {
		if o.GetKind() != "ImageConfig" {
			return nil, errors.Errorf("expected only ImageConfig manifests, found %s", o.GetKind())
		}
		ic := pkgv1beta1.ImageConfig{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(o.Object, &ic); err != nil {
			return nil, errors.Wrapf(err, "cannot parse ImageConfig %s", o.GetName())
		}
		ics = append(ics, ic)
	}
	return ics, nil
}

// kubeScheme is built once and never mutated, so concurrent NewKubeClient
// calls don't race on shared scheme maps.
//
//nolint:gochecknoglobals // Built once, read-only afterwards.
var kubeScheme = sync.OnceValues(func() (*runtime.Scheme, error) {
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := pkg.AddToScheme(s); err != nil {
		return nil, err
	}
	return s, nil
})

// NewKubeClient returns a client and a discovery-backed REST mapper for cfg.
func NewKubeClient(cfg *rest.Config) (client.Client, meta.RESTMapper, error) {
	sch, err := kubeScheme()
	if err != nil {
		return nil, nil, errors.Wrap(err, "cannot build scheme")
	}
	cl, err := client.New(cfg, client.Options{Scheme: sch})
	if err != nil {
		return nil, nil, errors.Wrap(err, "cannot init kubeclient")
	}

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, nil, errors.Wrap(err, "cannot get discovery client")
	}
	d := memory.NewMemCacheClient(discoveryClient)
	return cl, restmapper.NewShortcutExpander(restmapper.NewDeferredDiscoveryRESTMapper(d), d, nil), nil
}
