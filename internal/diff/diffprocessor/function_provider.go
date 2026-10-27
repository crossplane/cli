/*
Copyright 2025 The Crossplane Authors.

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

package diffprocessor

import (
	"context"
	"strings"
	"sync"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"

	apiextensionsv1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1"
	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"
	pkgv1beta1 "github.com/crossplane/crossplane/apis/v2/pkg/v1beta1"

	"github.com/crossplane/cli/v2/cmd/crossplane/render"
	xp "github.com/crossplane/cli/v2/internal/diff/client/crossplane"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"
)

// FunctionProvider provides functions for rendering compositions.
// Different implementations can fetch functions on-demand or return cached functions.
type FunctionProvider interface {
	// GetFunctionsForComposition returns the functions needed to render a composition.
	GetFunctionsForComposition(ctx context.Context, comp *apiextensionsv1.Composition) ([]pkgv1.Function, error)

	// Cleaner stops and removes any resources created during function execution.
	// For providers that don't create resources (like the wrappers), this is a no-op.
	Cleaner
}

// EnvDockerNetwork is the environment variable that specifies which Docker
// network the crossplane-render container and function containers should
// join when no network is passed explicitly. NewEngineRenderFn reads this once
// and routes the value through
// render.EngineFlags.CrossplaneDockerNetwork; the upstream docker engine
// then both runs the render container on that network and annotates fns
// to join it at Setup time. This is needed when crossplane-diff runs
// inside a Docker container (e.g. a GitHub Actions container job).
const EnvDockerNetwork = "CROSSPLANE_DIFF_DOCKER_NETWORK"

// CachedFunctionProvider lazy-loads functions and caches them by composition name and pipeline
// (see functionCacheKey).
// This is appropriate for the comp command, where many XRs use the same composition:
// the cluster is asked for a composition's functions once, however many XRs render it.
// The function containers themselves are started, reused across renders and removed
// by the render engine, which names them for the run that owns them (see EngineRenderFn).
//
// It is safe for concurrent use, and every call returns its own copy of the functions: the
// providers that wrap it (ImageConfigFunctionProvider, EnvInjectingFunctionProvider) and the
// render engine modify the functions they are given.
type CachedFunctionProvider struct {
	fnClient xp.FunctionClient
	mu       sync.Mutex
	cache    map[string][]pkgv1.Function
	logger   logging.Logger
}

// NewCachedFunctionProvider creates a new CachedFunctionProvider.
func NewCachedFunctionProvider(fnClient xp.FunctionClient, logger logging.Logger) FunctionProvider {
	return &CachedFunctionProvider{
		fnClient: fnClient,
		cache:    make(map[string][]pkgv1.Function),
		logger:   logger,
	}
}

// GetFunctionsForComposition fetches and caches functions on first call per composition.
func (p *CachedFunctionProvider) GetFunctionsForComposition(ctx context.Context, comp *apiextensionsv1.Composition) ([]pkgv1.Function, error) {
	compName := comp.GetName()
	key := functionCacheKey(comp)

	// Held across the fetch, so each composition's functions are fetched once however many
	// concurrent renders ask for them.
	p.mu.Lock()
	defer p.mu.Unlock()

	if cached, ok := p.cache[key]; ok {
		p.logger.Debug("Using cached functions", "composition", compName, "count", len(cached))
		return deepCopyFunctions(cached), nil
	}

	// Cache miss - fetch and cache functions
	p.logger.Debug("Fetching functions for caching", "composition", compName)

	fns, err := p.fnClient.GetFunctionsFromPipeline(ctx, comp)
	if err != nil {
		return nil, errors.Wrap(err, "cannot get functions from pipeline")
	}

	p.logger.Debug("Fetched functions for caching", "composition", compName, "count", len(fns))

	p.cache[key] = fns

	return deepCopyFunctions(fns), nil
}

// functionCacheKey keys the cache by composition name plus the ordered pipeline function names, the
// only inputs the fetch depends on. The name alone is not enough: a CompositionRevision is rendered
// under its Composition's name but may run a different pipeline. Kubernetes names cannot contain
// "/", so the join is unambiguous.
func functionCacheKey(comp *apiextensionsv1.Composition) string {
	parts := make([]string, 0, len(comp.Spec.Pipeline)+1)
	parts = append(parts, comp.GetName())

	for _, step := range comp.Spec.Pipeline {
		parts = append(parts, step.FunctionRef.Name)
	}

	return strings.Join(parts, "/")
}

// deepCopyFunctions returns a copy of fns that shares no maps or slices with it.
func deepCopyFunctions(fns []pkgv1.Function) []pkgv1.Function {
	out := make([]pkgv1.Function, len(fns))
	for i := range fns {
		fns[i].DeepCopyInto(&out[i])
	}

	return out
}

// Cleanup is a no-op for CachedFunctionProvider: the render engine owns the containers.
func (p *CachedFunctionProvider) Cleanup(_ context.Context) error {
	return nil
}

// ImageConfigFunctionProvider wraps another FunctionProvider and rewrites each
// function's package with the ImageConfig that matches it, as Crossplane does
// when it pulls a package, so the render pulls function images from a mirror.
type ImageConfigFunctionProvider struct {
	inner  FunctionProvider
	store  xpkg.ConfigStore
	logger logging.Logger
}

// NewImageConfigFunctionProvider wraps inner, rewriting every function package
// a rewriting ImageConfig in ics matches.
func NewImageConfigFunctionProvider(inner FunctionProvider, ics []pkgv1beta1.ImageConfig, logger logging.Logger) FunctionProvider {
	return &ImageConfigFunctionProvider{
		inner:  inner,
		store:  clixpkg.NewStaticImageConfigStore(ics),
		logger: logger,
	}
}

// GetFunctionsForComposition delegates to the wrapped provider and rewrites
// each returned function's package.
func (p *ImageConfigFunctionProvider) GetFunctionsForComposition(ctx context.Context, comp *apiextensionsv1.Composition) ([]pkgv1.Function, error) {
	fns, err := p.inner.GetFunctionsForComposition(ctx, comp)
	if err != nil {
		return nil, err
	}

	for i := range fns {
		ic, rewritten, err := p.store.RewritePath(ctx, fns[i].Spec.Package)
		if err != nil {
			return nil, errors.Wrapf(err, "cannot rewrite the package of function %q with ImageConfig %q", fns[i].GetName(), ic)
		}

		if rewritten == "" {
			continue
		}

		p.logger.Debug("Rewriting function package",
			"function", fns[i].GetName(),
			"imageConfig", ic,
			"from", fns[i].Spec.Package,
			"to", rewritten)
		fns[i].Spec.Package = rewritten
	}

	return fns, nil
}

// Cleanup delegates to the wrapped provider.
func (p *ImageConfigFunctionProvider) Cleanup(ctx context.Context) error {
	return p.inner.Cleanup(ctx)
}

// EnvMaxRecvMessageSize is the container env var crossplane-diff injects to
// raise a function's gRPC max receive size. It is intentionally unprefixed and
// function-agnostic, mirroring the SDK-wide TLS_SERVER_CERTS_DIR convention
// shared by crossplane composition functions: functions that bind it honor it,
// functions that don't simply ignore it.
const EnvMaxRecvMessageSize = "MAX_RECV_MESSAGE_SIZE"

// EnvInjectingFunctionProvider wraps another FunctionProvider and upserts a set
// of env pairs into each function's runtime-docker-env annotation before
// returning them. Used to raise the function gRPC max-recv-message-size so large
// XRs don't trip the function-sdk-go 4MB default under `crossplane render`
// (which, unlike the in-cluster runtime, does not apply DeploymentRuntimeConfig).
type EnvInjectingFunctionProvider struct {
	inner  FunctionProvider
	envs   map[string]string
	logger logging.Logger
}

// NewEnvInjectingFunctionProvider wraps inner, upserting the given env pairs
// into each returned function's runtime-docker-env annotation.
func NewEnvInjectingFunctionProvider(inner FunctionProvider, envs map[string]string, logger logging.Logger) FunctionProvider {
	return &EnvInjectingFunctionProvider{
		inner:  inner,
		envs:   envs,
		logger: logger,
	}
}

// GetFunctionsForComposition delegates to the wrapped provider and upserts the
// configured env pairs into each function's runtime-docker-env annotation. The
// upsert is idempotent so repeated calls over a caching inner provider don't
// duplicate pairs.
func (p *EnvInjectingFunctionProvider) GetFunctionsForComposition(ctx context.Context, comp *apiextensionsv1.Composition) ([]pkgv1.Function, error) {
	fns, err := p.inner.GetFunctionsForComposition(ctx, comp)
	if err != nil {
		return nil, err
	}

	for i := range fns {
		if fns[i].Annotations == nil {
			fns[i].Annotations = make(map[string]string)
		}

		cur := fns[i].Annotations[render.AnnotationKeyRuntimeEnvironmentVariables]
		for k, v := range p.envs {
			cur = upsertEnvPair(cur, k, v)
		}

		fns[i].Annotations[render.AnnotationKeyRuntimeEnvironmentVariables] = cur

		p.logger.Debug("Injected function runtime env",
			"function", fns[i].GetName(),
			render.AnnotationKeyRuntimeEnvironmentVariables, cur)
	}

	return fns, nil
}

// Cleanup delegates to the wrapped provider.
func (p *EnvInjectingFunctionProvider) Cleanup(ctx context.Context) error {
	return p.inner.Cleanup(ctx)
}

// upsertEnvPair sets key=val in a comma-separated "k=v,k=v" list, replacing an
// existing entry for key (idempotent) or appending a new one. Blank segments
// are dropped.
func upsertEnvPair(cur, key, val string) string {
	pairs := make([]string, 0, 4)
	replaced := false

	for seg := range strings.SplitSeq(cur, ",") {
		if seg == "" {
			continue
		}

		if k, _, ok := strings.Cut(seg, "="); ok && k == key {
			pairs = append(pairs, key+"="+val)
			replaced = true

			continue
		}

		pairs = append(pairs, seg)
	}

	if !replaced {
		pairs = append(pairs, key+"="+val)
	}

	return strings.Join(pairs, ",")
}
