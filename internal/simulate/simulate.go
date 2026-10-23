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

// Package simulate plans managed resources against the providers' own diff
// servers, one resource at a time, falling back to an approximate client-side
// diff when no server can plan a resource.
package simulate

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	diffv1alpha1 "github.com/crossplane/upjet/v2/proto/diff/v1alpha1"
	"google.golang.org/protobuf/types/known/structpb"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"

	"github.com/crossplane/cli/v2/internal/plan"
)

// Router routes a resource's group and kind to a diff server client. It is
// the slice of diffserver.Manager the per-resource flow needs, extracted so
// tests can fake routing.
type Router interface {
	Route(gk schema.GroupKind) (diffv1alpha1.PlanServiceClient, bool)
}

// A Planner plans desired resources one at a time against the diff servers
// its Router routes to. It caches the object stores it gathers, so reuse one
// Planner across a run. A Planner is not safe for concurrent use: the engine
// runs XRs sequentially, and the object store cache (like ProviderManaged's
// kind map and ImageCache) is unlocked.
type Planner struct {
	kube             client.Client
	rmapper          meta.RESTMapper
	defaultNamespace string
	router           Router
	timeout          time.Duration
	stores           *objectStoreCache
	crds             *crdDefaults
}

// NewPlanner returns a Planner that reads live state with kubeClient and
// rmapper, resolves unset namespaces to defaultNamespace, and bounds each
// diff server plan by timeout, when positive.
func NewPlanner(kubeClient client.Client, rmapper meta.RESTMapper, defaultNamespace string, r Router, timeout time.Duration) *Planner {
	return &Planner{
		kube:             kubeClient,
		rmapper:          rmapper,
		defaultNamespace: defaultNamespace,
		router:           r,
		timeout:          timeout,
		stores:           newObjectStoreCache(),
		crds:             newCRDDefaults(),
	}
}

// Plan plans a single resource: fetch its live counterpart, then plan it
// with PlanWithLive. Failures become per-resource errors so one resource
// never fails the whole run.
func (p *Planner) Plan(ctx context.Context, desired *unstructured.Unstructured) *plan.ResourcePlan {
	if desired.GetName() == "" {
		rp := plan.NewResourcePlan(desired)
		rp.Error = "resource has no name"
		return rp
	}

	live, err := p.fetchLive(ctx, desired)
	if err != nil {
		rp := plan.NewResourcePlan(desired)
		rp.Error = fmt.Sprintf("cannot fetch live resource: %s", err)
		return rp
	}
	return p.PlanWithLive(ctx, desired, live)
}

// PlanWithLive plans a single resource against an already-fetched live
// counterpart, or nil when none exists: route it to a diff server, and fall
// back to the approximate client-side diff when no server can plan it.
// Failures become per-resource errors so one resource never fails the whole
// run.
func (p *Planner) PlanWithLive(ctx context.Context, desired, live *unstructured.Unstructured) *plan.ResourcePlan {
	// Crossplane's external tags survive every apply (the provider's
	// initializer re-adds them), so a preview must never claim they would be
	// removed.
	plan.MergeExternalTags(desired, live)

	gk := desired.GroupVersionKind().GroupKind()
	planClient, ok := p.router.Route(gk)
	if !ok {
		// The fallback diffs desired against live verbatim, but live was
		// defaulted by the API server on admission and applying the manifest
		// would default it again. Apply the CRD's defaults to desired first,
		// so defaulted fields the manifest omits do not plan as removals.
		return plan.Fallback(p.crds.apply(ctx, p.kube, p.rmapper, desired), live, fmt.Sprintf("no diff server for %s", gk))
	}

	pctx := ctx
	if p.timeout > 0 {
		var cancel context.CancelFunc
		pctx, cancel = context.WithTimeout(ctx, p.timeout)
		defer cancel()
	}

	store := p.stores.gather(ctx, p.kube, p.rmapper, p.defaultNamespace, desired, live)

	rp, err := plan.Plan(pctx, planClient, desired, live, store)
	if err != nil {
		if plan.IsNotSupported(err) {
			return plan.Fallback(p.crds.apply(ctx, p.kube, p.rmapper, desired), live, "the provider cannot compute a diff for this resource")
		}
		rp := plan.NewResourcePlan(desired)
		rp.Error = fmt.Sprintf("diff server cannot plan: %s", err)
		return rp
	}
	return rp
}

// CanPlan reports whether a diff server serves resources of kind gk.
func (p *Planner) CanPlan(gk schema.GroupKind) bool {
	_, ok := p.router.Route(gk)
	return ok
}

// fetchLive returns the live counterpart of the desired resource, or nil when
// it does not exist (including when its kind is not installed on the
// cluster), in which case the resource plans as a create.
func (p *Planner) fetchLive(ctx context.Context, desired *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	gvk := desired.GroupVersionKind()

	mapping, err := p.rmapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		// The kind is not served by the cluster: nothing can exist yet. The
		// diff server can still plan the resource as a create.
		return nil, nil //nolint:nilerr // Intentional: an unserved kind means no live counterpart, not a failure.
	}

	key := client.ObjectKey{Name: desired.GetName()}
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		key.Namespace = desired.GetNamespace()
		if key.Namespace == "" {
			key.Namespace = p.defaultNamespace
		}
	}

	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(gvk)
	if err := p.kube.Get(ctx, key, live); err != nil {
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
	switch err := kubeClient.Get(ctx, key, pc); {
	case err == nil:
		if s, err := structpb.NewStruct(pc.Object); err == nil {
			store = []*structpb.Struct{s}
		}
	case apierrors.IsNotFound(err):
	default:
		// Any other failure, notably a timed-out XR's expired context, must
		// not poison the cache for the XRs after it.
		return nil
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

// ImageCache remembers which provider image serves each kind, so a run
// looks a kind up on the cluster once however many composites use it. The
// zero value is not usable; use NewImageCache.
type ImageCache struct {
	images map[schema.GroupKind]string
}

// NewImageCache returns an empty ImageCache.
func NewImageCache() *ImageCache {
	return &ImageCache{images: map[schema.GroupKind]string{}}
}

// RelevantProviderImages returns the package images of exactly the providers
// that serve the given resources, so simulate never starts a diff server it
// will not route to. Discovery follows the ownership chain on the cluster:
// each resource's kind maps to a CRD, the CRD is controlled by the active
// ProviderRevision, and the revision carries the resolved package image.
// Kinds that resolve to no provider are skipped; their resources degrade to
// the approximate client-side diff. So are kinds whose lookup fails (such as
// forbidden): ProviderManaged treats them the same way, and a warning to warn
// names each such kind once. cache carries answers across calls.
func RelevantProviderImages(ctx context.Context, kubeClient client.Client, rmapper meta.RESTMapper, resources []*unstructured.Unstructured, preferOriginal bool, cache *ImageCache, warn io.Writer) []string {
	seenImage := map[string]bool{}
	var images []string

	for _, r := range resources {
		gvk := r.GroupVersionKind()
		gk := gvk.GroupKind()
		img, ok := cache.images[gk]
		if !ok {
			var err error
			img, err = providerImage(ctx, kubeClient, rmapper, gvk, preferOriginal)
			if err != nil {
				_, _ = fmt.Fprintf(warn, "Warning: cannot look up the provider for %s, so its diff stays client-side only: %s\n", gk, err)
			}
			cache.images[gk] = img
		}
		if img != "" && !seenImage[img] {
			seenImage[img] = true
			images = append(images, img)
		}
	}
	return images
}

// providerImage returns the package image of the provider serving gvk, or
// empty when none does.
func providerImage(ctx context.Context, kubeClient client.Client, rmapper meta.RESTMapper, gvk schema.GroupVersionKind, preferOriginal bool) (string, error) {
	revision, crdName, err := controllingRevision(ctx, kubeClient, rmapper, gvk)
	if err != nil || revision == "" {
		return "", err
	}

	prv := &pkgv1.ProviderRevision{}
	if err := kubeClient.Get(ctx, client.ObjectKey{Name: revision}, prv); err != nil {
		return "", errors.Wrapf(err, "cannot get provider revision %s owning CRD %s", revision, crdName)
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
	return img, nil
}

// ProviderManaged returns a predicate reporting whether a resource's kind is
// served by a provider: whether its CRD is controlled by a ProviderRevision,
// as RelevantProviderImages discovers. Answers are cached per group and kind.
// A lookup that fails answers false, so the resource keeps the engine's
// verdict alone. XRs are never provider-managed: their CRDs are controlled by
// their CompositeResourceDefinition.
func ProviderManaged(ctx context.Context, kubeClient client.Client, rmapper meta.RESTMapper) func(*unstructured.Unstructured) bool {
	managed := map[schema.GroupKind]bool{}

	// The engine diffs one resource at a time, so the cache needs no lock.
	return func(u *unstructured.Unstructured) bool {
		gvk := u.GroupVersionKind()
		if m, ok := managed[gvk.GroupKind()]; ok {
			return m
		}
		revision, _, err := controllingRevision(ctx, kubeClient, rmapper, gvk)
		managed[gvk.GroupKind()] = err == nil && revision != ""
		return managed[gvk.GroupKind()]
	}
}

// controllingRevision returns the name of the ProviderRevision controlling the
// CRD that serves gvk, and that CRD's name. The revision is empty when the
// kind is not installed or its CRD is not provider-installed.
func controllingRevision(ctx context.Context, kubeClient client.Client, rmapper meta.RESTMapper, gvk schema.GroupVersionKind) (string, string, error) {
	gk := gvk.GroupKind()
	mapping, err := rmapper.RESTMapping(gk, gvk.Version)
	if err != nil {
		return "", "", nil //nolint:nilerr // Intentional: the kind is not installed, so nothing serves it.
	}

	crd := &unstructured.Unstructured{}
	crd.SetGroupVersionKind(schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"})
	crdName := mapping.Resource.Resource + "." + gk.Group
	if err := kubeClient.Get(ctx, client.ObjectKey{Name: crdName}, crd); err != nil {
		if apierrors.IsNotFound(err) {
			return "", crdName, nil
		}
		return "", crdName, errors.Wrapf(err, "cannot get CRD %s", crdName)
	}

	// After an upgrade every revision of the provider still owns the CRD;
	// only the active revision holds the controller reference, and that is
	// the one serving the resources.
	for _, ref := range crd.GetOwnerReferences() {
		if ref.Kind == "ProviderRevision" && strings.HasPrefix(ref.APIVersion, "pkg.crossplane.io/") && ref.Controller != nil && *ref.Controller {
			return ref.Name, crdName, nil
		}
	}
	return "", crdName, nil // Not provider-installed (e.g. a hand-applied CRD).
}
