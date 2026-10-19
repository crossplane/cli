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

// Package simulate contains the resource simulate command: preview what
// applying managed resources would do, to the cluster and to the cloud
// behind it, by asking the providers' own diff servers.
package simulate

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	diffv1alpha1 "github.com/crossplane/upjet/v2/proto/diff/v1alpha1"
	"github.com/spf13/afero"
	"google.golang.org/protobuf/types/known/structpb"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/restmapper"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	"github.com/crossplane/crossplane/apis/v2/pkg"
	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"
	pkgv1beta1 "github.com/crossplane/crossplane/apis/v2/pkg/v1beta1"

	"github.com/crossplane/cli/v2/cmd/crossplane/common/load"
	"github.com/crossplane/cli/v2/internal/async"
	"github.com/crossplane/cli/v2/internal/containerruntime"
	"github.com/crossplane/cli/v2/internal/diffserver"
	"github.com/crossplane/cli/v2/internal/docker"
	"github.com/crossplane/cli/v2/internal/kube"
	"github.com/crossplane/cli/v2/internal/plan"
	"github.com/crossplane/cli/v2/internal/project/projectfile"
	"github.com/crossplane/cli/v2/internal/terminal"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"

	_ "embed"
)

//go:embed help/simulate.md
var helpDetail string

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

// Cmd previews what applying managed resources would do.
type Cmd struct {
	Filename string `help:"YAML files or directory with managed resources, or '-' for stdin (comma-separated)." predictor:"yaml_file_or_directory" required:"" short:"f"`

	Providers    string        `help:"YAML file or directory with Provider manifests to run as diff servers, overriding discovery from the cluster. Supports per-provider runtime annotations."       name:"providers"                                         optional:""                                     predictor:"yaml_file_or_directory" type:"path"`
	ImageConfigs string        `help:"YAML file with ImageConfig manifests that rewrite provider image paths for local pulls. Defaults to the project's imageConfigs when a project file is present." name:"image-configs"                                     optional:""                                     predictor:"yaml_file"              type:"existingfile"`
	ProjectFile  string        `help:"Path to a project definition file to read imageConfigs from. Defaults to ${project_file} when present."                                                         name:"project-file"                                      optional:""`
	SessionID    string        `help:"Session ID to apply as the simulate.crossplane.io/session label on diff server containers. Defaults to a random per-process ID."                                placeholder:"ID"`
	Context      string        `default:""                                                                                                                                                            help:"Kubernetes context."                               name:"context"                                  predictor:"context"                short:"c"`
	CacheDir     string        `default:"~/.crossplane/cache"                                                                                                                                         help:"Cache directory for downloaded provider packages." name:"cache-dir"                                predictor:"directory"`
	Timeout      time.Duration `default:"2m"                                                                                                                                                          help:"Per-resource plan timeout."`
	Output       string        `default:"plan"                                                                                                                                                        enum:"plan,json,yaml"                                    help:"Output format. One of: plan, json, yaml." name:"output"                      short:"o"`

	Impersonation kube.ImpersonationFlags `embed:""`
}

// Help returns the help message for the simulate command.
func (c *Cmd) Help() string {
	return helpDetail
}

// AfterApply expands the cache directory.
func (c *Cmd) AfterApply() error {
	if strings.HasPrefix(c.CacheDir, "~/") {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return errors.Wrap(err, "cannot determine home directory for cache dir")
		}
		c.CacheDir = filepath.Join(homeDir, c.CacheDir[2:])
	}
	return nil
}

// Run runs the simulate command. ctx is the signal-bound command context from
// main: cancellation on SIGINT/SIGTERM unwinds in-flight plans so the
// deferred diff server cleanup runs.
func (c *Cmd) Run(ctx context.Context, k *kong.Context, logger logging.Logger, sp terminal.SpinnerPrinter) error {
	loader, err := load.NewLoader(c.Filename)
	if err != nil {
		return errors.Wrapf(err, "cannot load resources from %q", c.Filename)
	}
	resources, err := loader.Load()
	if err != nil {
		return errors.Wrapf(err, "cannot load resources from %q", c.Filename)
	}
	if len(resources) == 0 {
		return errors.Errorf("no resources found in %q", c.Filename)
	}

	renderer, err := plan.NewRenderer(c.Output)
	if err != nil {
		return err
	}

	var inputs []diffserver.Input
	if c.Providers != "" {
		if inputs, err = loadProviderInputs(c.Providers); err != nil {
			return errors.Wrapf(err, "cannot load provider manifests from %q", c.Providers)
		}
	}
	// Only a provider whose package image runs as a container needs Docker;
	// a Development provider's server is already running. Discovery from the
	// cluster always yields the former.
	if c.Providers == "" || needsDocker(inputs) {
		if err := docker.Check(ctx); err != nil {
			return errors.Wrap(err, "cannot connect to Docker, which simulate needs to run provider diff servers")
		}
	}

	kubeClient, rmapper, defaultNamespace, err := c.setupKubeClient(logger)
	if err != nil {
		return err
	}

	imageConfigs, err := c.imageConfigs()
	if err != nil {
		return err
	}

	if c.Providers == "" {
		// With local ImageConfigs the rewrites apply to original package
		// references, so discovery must hand those out; without them the
		// cluster's resolved reference is the exact artifact it runs.
		images, err := relevantProviderImages(ctx, kubeClient, rmapper, resources, len(imageConfigs) > 0)
		if err != nil {
			return errors.Wrap(err, "cannot discover the providers serving the given resources")
		}
		inputs = diffserver.Images(images)
	}
	if len(inputs) == 0 {
		_, _ = fmt.Fprintln(k.Stderr, "No providers found to run as diff servers; all results will be approximate.")
	}

	pkgClient, err := clixpkg.NewClient(clixpkg.NewRemoteFetcher(),
		clixpkg.WithCacheDir(afero.NewOsFs(), c.CacheDir),
		clixpkg.WithImageConfigs(imageConfigs),
	)
	if err != nil {
		return errors.Wrap(err, "cannot create package client")
	}

	mgr := diffserver.NewManager(pkgClient, logger, diffserver.WithSessionID(c.SessionID))
	var skipped []diffserver.Skipped
	if err := sp.WrapAsyncWithSuccessSpinners(func(ch async.EventChannel) error {
		var err error
		skipped, err = mgr.Start(ctx, inputs, ch)
		return err
	}); err != nil {
		return errors.Wrap(err, "cannot start diff servers")
	}
	// Cleanup runs even when ctx was cancelled by a signal, so an interrupted
	// run still tears its containers down.
	defer func() {
		if err := mgr.Stop(context.WithoutCancel(ctx)); err != nil {
			_, _ = fmt.Fprintf(k.Stderr, "Warning: cannot clean up diff servers: %s\n", err)
		}
	}()
	for _, s := range skipped {
		_, _ = fmt.Fprintf(k.Stderr, "Not running a diff server for %s: %s\n", s.Image, s.Reason)
	}

	plans := make([]*plan.ResourcePlan, 0, len(resources))
	stores := newObjectStoreCache()
	crds := newCRDDefaults()
	for _, desired := range resources {
		plans = append(plans, c.simulateOne(ctx, kubeClient, rmapper, defaultNamespace, mgr, stores, crds, desired))
	}

	return renderer.Render(k.Stdout, plans)
}

// imageConfigs returns the ImageConfigs to rewrite provider image paths with:
// the --image-configs file when given, else the imageConfigs of the project
// the command happens to run in, if any. Running outside a project is the
// norm for this command, so a missing project file is only an error when the
// user asked for one explicitly with --project-file.
func (c *Cmd) imageConfigs() ([]pkgv1beta1.ImageConfig, error) {
	if c.ImageConfigs != "" {
		ics, err := loadImageConfigs(c.ImageConfigs)
		return ics, errors.Wrapf(err, "cannot load image configs from %q", c.ImageConfigs)
	}

	name, explicit := c.ProjectFile, true
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

// needsDocker reports whether any input runs its diff server as a container.
func needsDocker(inputs []diffserver.Input) bool {
	for _, in := range inputs {
		if in.Runtime != diffserver.RuntimeDevelopment {
			return true
		}
	}
	return false
}

// loadProviderInputs reads Provider manifests and turns them into diff server
// inputs per their simulate.crossplane.io runtime annotations: the package
// image from spec.package with per-provider Docker overrides, or, for the
// Development runtime, the target of an already running server and the API
// groups it serves. A group can route to only one development server, so
// manifests that declare the same group are rejected up front.
func loadProviderInputs(path string) ([]diffserver.Input, error) {
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

// loadImageConfigs reads ImageConfig manifests from a YAML file.
func loadImageConfigs(path string) ([]pkgv1beta1.ImageConfig, error) {
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

// router routes a resource's group and kind to a diff server client. It is
// the slice of diffserver.Manager the per-resource flow needs, extracted so
// tests can fake routing.
type router interface {
	Route(gk schema.GroupKind) (diffv1alpha1.PlanServiceClient, bool)
}

// simulateOne plans a single resource: fetch its live counterpart, route it
// to a diff server, and fall back to the approximate client-side diff when no
// server can plan it. Failures become per-resource errors so one resource
// never fails the whole run.
func (c *Cmd) simulateOne(ctx context.Context, kubeClient client.Client, rmapper meta.RESTMapper, defaultNamespace string, mgr router, stores *objectStoreCache, crds *crdDefaults, desired *unstructured.Unstructured) *plan.ResourcePlan {
	if desired.GetName() == "" {
		p := plan.NewResourcePlan(desired)
		p.Error = "resource has no name"
		return p
	}

	live, err := fetchLive(ctx, kubeClient, rmapper, defaultNamespace, desired)
	if err != nil {
		p := plan.NewResourcePlan(desired)
		p.Error = fmt.Sprintf("cannot fetch live resource: %s", err)
		return p
	}

	// Crossplane's external tags survive every apply (the provider's
	// initializer re-adds them), so a preview must never claim they would be
	// removed.
	plan.MergeExternalTags(desired, live)

	gk := desired.GroupVersionKind().GroupKind()
	planClient, ok := mgr.Route(gk)
	if !ok {
		// The fallback diffs desired against live verbatim, but live was
		// defaulted by the API server on admission and applying the manifest
		// would default it again. Apply the CRD's defaults to desired first,
		// so defaulted fields the manifest omits do not plan as removals.
		return plan.Fallback(crds.apply(ctx, kubeClient, rmapper, desired), live, fmt.Sprintf("no diff server for %s", gk))
	}

	pctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()

	store := stores.gather(ctx, kubeClient, rmapper, defaultNamespace, desired, live)

	p, err := plan.Plan(pctx, planClient, desired, live, store)
	if err != nil {
		if plan.IsNotSupported(err) {
			return plan.Fallback(crds.apply(ctx, kubeClient, rmapper, desired), live, "the provider cannot compute a diff for this resource")
		}
		p := plan.NewResourcePlan(desired)
		p.Error = fmt.Sprintf("diff server cannot plan: %s", err)
		return p
	}
	return p
}

// fetchLive returns the live counterpart of the desired resource, or nil when
// it does not exist (including when its kind is not installed on the
// cluster), in which case the resource plans as a create.
func fetchLive(ctx context.Context, kubeClient client.Client, rmapper meta.RESTMapper, defaultNamespace string, desired *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	gvk := desired.GroupVersionKind()

	mapping, err := rmapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		// The kind is not served by the cluster: nothing can exist yet. The
		// diff server can still plan the resource as a create.
		return nil, nil //nolint:nilerr // Intentional: an unserved kind means no live counterpart, not a failure.
	}

	key := client.ObjectKey{Name: desired.GetName()}
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		key.Namespace = desired.GetNamespace()
		if key.Namespace == "" {
			key.Namespace = defaultNamespace
		}
	}

	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(gvk)
	if err := kubeClient.Get(ctx, key, live); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return live, nil
}

// objectStoreCache caches gathered object stores by the resolved
// ProviderConfig identity, so a run over many resources referencing the same
// ProviderConfig fetches and converts it once rather than per resource.
// Missing objects are cached too: a reference the cluster cannot resolve is
// not refetched for every resource that carries it.
type objectStoreCache struct {
	cache map[objectStoreKey][]*structpb.Struct
}

type objectStoreKey struct {
	gvk schema.GroupVersionKind
	key client.ObjectKey
}

func newObjectStoreCache() *objectStoreCache {
	return &objectStoreCache{cache: map[objectStoreKey][]*structpb.Struct{}}
}

// gather returns the cluster objects the diff server needs to resolve
// the desired resource: today the ProviderConfig it references. The diff
// server is offline and only sees what travels in the request, so anything it
// must resolve is fetched here, with the user's own kubeconfig and RBAC. The
// ProviderConfig's credentials Secret deliberately stays out: the server runs
// offline with placeholder credentials and never needs the real ones.
// Gathering is best effort: a missing object simply stays out of the store,
// and the server reports precisely what it could not resolve.
func (c *objectStoreCache) gather(ctx context.Context, kubeClient client.Client, rmapper meta.RESTMapper, defaultNamespace string, desired, live *unstructured.Unstructured) []*structpb.Struct {
	// Read the reference from the live resource when one exists: the API
	// server has already applied the CRD's defaults there, so its kind and
	// name are authoritative. The desired manifest may omit both.
	src := desired
	if live != nil {
		src = live
	}
	name, _, _ := unstructured.NestedString(src.Object, "spec", "providerConfigRef", "name")
	if name == "" {
		name = "default"
	}
	kind, _, _ := unstructured.NestedString(src.Object, "spec", "providerConfigRef", "kind")
	if kind == "" {
		// Legacy cluster-scoped resources have no kind in the reference and
		// always mean ProviderConfig. Namespaced resources default to
		// ClusterProviderConfig, per the CRD default the API server applies.
		kind = "ProviderConfig"
		gvk := desired.GroupVersionKind()
		if m, err := rmapper.RESTMapping(gvk.GroupKind(), gvk.Version); err == nil && m.Scope.Name() == meta.RESTScopeNameNamespace {
			kind = "ClusterProviderConfig"
		}
	}

	mapping := providerConfigMapping(rmapper, desired.GroupVersionKind().Group, kind)
	if mapping == nil {
		return nil
	}

	key := client.ObjectKey{Name: name}
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		key.Namespace = desired.GetNamespace()
		if key.Namespace == "" {
			key.Namespace = defaultNamespace
		}
	}

	ck := objectStoreKey{gvk: mapping.GroupVersionKind, key: key}
	if store, ok := c.cache[ck]; ok {
		return store
	}

	var store []*structpb.Struct
	pc := &unstructured.Unstructured{}
	pc.SetGroupVersionKind(mapping.GroupVersionKind)
	if err := kubeClient.Get(ctx, key, pc); err == nil {
		if s, err := structpb.NewStruct(pc.Object); err == nil {
			store = []*structpb.Struct{s}
		}
	}
	c.cache[ck] = store
	return store
}

// providerConfigMapping returns the REST mapping for the ProviderConfig kind
// the given resource group's provider serves, or nil when no group serves it.
//
// The ProviderConfig's group relates to the resource's group, but not
// uniformly: most providers serve it one level up (s3.aws.upbound.io →
// aws.upbound.io), others in the resource's own group
// (kubernetes.crossplane.io). Probe from the resource's group toward the root
// and take the first group that serves the kind.
func providerConfigMapping(rmapper meta.RESTMapper, group, kind string) *meta.RESTMapping {
	for {
		if m, err := rmapper.RESTMapping(schema.GroupKind{Group: group, Kind: kind}); err == nil {
			return m
		}
		i := strings.Index(group, ".")
		if i < 0 {
			return nil
		}
		group = group[i+1:]
	}
}

// relevantProviderImages returns the package images of exactly the providers
// that serve the given resources, so simulate never starts a diff server it
// will not route to. Discovery follows the ownership chain on the cluster:
// each resource's kind maps to a CRD, the CRD is controlled by the active
// ProviderRevision, and the revision carries the resolved package image.
// Kinds that resolve to no provider are skipped; their resources degrade to
// the approximate client-side diff.
func relevantProviderImages(ctx context.Context, kubeClient client.Client, rmapper meta.RESTMapper, resources []*unstructured.Unstructured, preferOriginal bool) ([]string, error) {
	seenGK := map[schema.GroupKind]bool{}
	seenImage := map[string]bool{}
	var images []string

	for _, r := range resources {
		gvk := r.GroupVersionKind()
		gk := gvk.GroupKind()
		if seenGK[gk] {
			continue
		}
		seenGK[gk] = true

		mapping, err := rmapper.RESTMapping(gk, gvk.Version)
		if err != nil {
			continue // The kind is not installed; nothing serves it.
		}

		crd := &unstructured.Unstructured{}
		crd.SetGroupVersionKind(schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"})
		crdName := mapping.Resource.Resource + "." + gk.Group
		if err := kubeClient.Get(ctx, client.ObjectKey{Name: crdName}, crd); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, errors.Wrapf(err, "cannot get CRD %s", crdName)
		}

		// After an upgrade every revision of the provider still owns the CRD;
		// only the active revision holds the controller reference, and that is
		// the one serving the resources.
		revision := ""
		for _, ref := range crd.GetOwnerReferences() {
			if ref.Kind == "ProviderRevision" && strings.HasPrefix(ref.APIVersion, "pkg.crossplane.io/") && ref.Controller != nil && *ref.Controller {
				revision = ref.Name
				break
			}
		}
		if revision == "" {
			continue // Not provider-installed (e.g. a hand-applied CRD).
		}

		prv := &pkgv1.ProviderRevision{}
		if err := kubeClient.Get(ctx, client.ObjectKey{Name: revision}, prv); err != nil {
			return nil, errors.Wrapf(err, "cannot get provider revision %s owning CRD %s", revision, crdName)
		}
		// Prefer the resolved package: after the cluster's ImageConfig
		// rewriting, it is the identity of the artifact the cluster actually
		// runs, which is what the preview promises to use. If that reference
		// is not pullable from this machine (e.g. a cluster-only mirror), the
		// manager reports the image as skipped, its resources degrade to the
		// approximate diff, and --provider-images remains the override for
		// picking a locally reachable image instead. With local ImageConfigs
		// in play (preferOriginal) the original reference is handed out
		// instead, because that is what local rewrite rules are written
		// against.
		img := prv.Status.ResolvedPackage
		if preferOriginal || img == "" {
			img = prv.Spec.Package
		}
		if img != "" && !seenImage[img] {
			seenImage[img] = true
			images = append(images, img)
		}
	}
	return images, nil
}

func (c *Cmd) setupKubeClient(logger logging.Logger) (client.Client, meta.RESTMapper, string, error) {
	kubeconfig, clientconfig, err := kube.RESTConfig(c.Context, c.Impersonation)
	if err != nil {
		return nil, nil, "", err
	}

	logger.Debug("Found kubeconfig")

	cl, err := client.New(kubeconfig, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		return nil, nil, "", errors.Wrap(err, "cannot init kubeclient")
	}
	_ = pkg.AddToScheme(cl.Scheme())

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(kubeconfig)
	if err != nil {
		return nil, nil, "", errors.Wrap(err, "cannot get discovery client")
	}
	d := memory.NewMemCacheClient(discoveryClient)
	rmapper := restmapper.NewShortcutExpander(restmapper.NewDeferredDiscoveryRESTMapper(d), d, nil)

	namespace, _, err := clientconfig.Namespace()
	if err != nil {
		return nil, nil, "", errors.Wrap(err, "failed to get namespace from kubeconfig")
	}

	return cl, rmapper, namespace, nil
}
