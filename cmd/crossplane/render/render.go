/*
Copyright 2023 The Crossplane Authors.

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

// Package render implements helpers shared by the render subcommands
// (xr and op).
package render

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	fnv1 "github.com/crossplane/function-sdk-go/proto/v1"
	corev1 "k8s.io/api/core/v1"
	kunstructured "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/kube-openapi/pkg/spec3"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource/unstructured/composed"
	ucomposite "github.com/crossplane/crossplane-runtime/v2/pkg/resource/unstructured/composite"

	apiextensionsv1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1"
	opsv1alpha1 "github.com/crossplane/crossplane/apis/v2/ops/v1alpha1"
	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"

	renderv1alpha1 "github.com/crossplane/cli/v2/proto/render/v1alpha1"
)

// ExitCodePipelineFatal is the exit code that `crossplane internal render`
// returns when a pipeline step responds with SEVERITY_FATAL. The binary
// populates stdout with a partial RenderResponse on this code so callers can
// recover output.RequiredResources (and similar) and iterate. See
// crossplane/crossplane#7455 for the upstream contract.
const ExitCodePipelineFatal = 3

// CompositionInputs contains all inputs to the render process.
type CompositionInputs struct {
	CompositeResource   *ucomposite.Unstructured
	Composition         *apiextensionsv1.Composition
	FunctionAddrs       map[string]string
	FunctionCredentials []corev1.Secret
	ObservedResources   []composed.Unstructured
	RequiredResources   []kunstructured.Unstructured
	RequiredSchemas     []spec3.OpenAPI

	// XRD is the CompositeResourceDefinition the binary should consider
	// when rendering. The binary uses it to pick the right composite.Schema
	// (Legacy vs Modern) for the input XR's GVK, mirroring the production
	// reconciler. Optional; when nil the binary falls back to its default
	// behavior (Schema=Modern).
	XRD *kunstructured.Unstructured
}

// CompositionOutputs contains all outputs from the render process.
type CompositionOutputs struct {
	CompositeResource *ucomposite.Unstructured
	ComposedResources []composed.Unstructured
	Results           []kunstructured.Unstructured
	Context           *kunstructured.Unstructured
	RequiredResources []*fnv1.ResourceSelector
	RequiredSchemas   []*fnv1.SchemaSelector
}

// OperationInputs contains all inputs to the render process for an operation.
type OperationInputs struct {
	Operation           *opsv1alpha1.Operation
	FunctionAddrs       map[string]string
	FunctionCredentials []corev1.Secret
	RequiredResources   []kunstructured.Unstructured
	RequiredSchemas     []spec3.OpenAPI
}

// OperationOutputs contains all outputs from the render process.
type OperationOutputs struct {
	Operation         *opsv1alpha1.Operation
	AppliedResources  []kunstructured.Unstructured
	Results           []kunstructured.Unstructured
	Context           *kunstructured.Unstructured
	RequiredResources []*fnv1.ResourceSelector
	RequiredSchemas   []*fnv1.SchemaSelector
}

const (
	// runtimeStopMargin is how much longer than containerStopGracePeriod
	// StopFunctionRuntimes waits for each runtime to stop. It covers killing
	// and removing the container once the grace period has expired.
	runtimeStopMargin = 5 * time.Second

	// runtimeStopTimeout bounds how long StopFunctionRuntimes waits for each
	// runtime to stop.
	runtimeStopTimeout = containerStopGracePeriod + runtimeStopMargin
)

// FunctionAddresses maps function names to their gRPC target addresses.
type FunctionAddresses struct {
	addrs    map[string]string
	contexts map[string]RuntimeContext
}

// Addresses returns the function name to gRPC address map.
func (fa *FunctionAddresses) Addresses() map[string]string {
	return fa.addrs
}

// Stop all function runtimes. Every runtime is stopped even if some fail; the
// returned error joins all failures.
func (fa *FunctionAddresses) Stop(ctx context.Context) error {
	return fa.stop(ctx, 0)
}

// stop stops every function runtime and returns all failures joined. If
// timeout is positive each runtime gets its own timeout derived from ctx.
func (fa *FunctionAddresses) stop(ctx context.Context, timeout time.Duration) error {
	var errs []error
	for name, rctx := range fa.contexts {
		sctx, cancel := ctx, context.CancelFunc(func() {})
		if timeout > 0 {
			sctx, cancel = context.WithTimeout(ctx, timeout)
		}
		if err := rctx.Stop(sctx); err != nil {
			errs = append(errs, errors.Wrapf(err, "cannot stop function %q runtime (target %q)", name, rctx.Target))
		}
		cancel()
	}
	if len(errs) == 0 {
		return nil
	}
	return errors.Join(errs...)
}

// StartFunctionRuntimes starts the runtime for each function and returns their
// gRPC addresses. The caller must call Stop on the returned FunctionAddresses
// when done.
func StartFunctionRuntimes(ctx context.Context, log logging.Logger, fns []pkgv1.Function) (*FunctionAddresses, error) {
	addrs := make(map[string]string, len(fns))
	contexts := make(map[string]RuntimeContext, len(fns))

	for _, fn := range fns {
		rt, err := GetRuntime(fn, log)
		if err != nil {
			return nil, errors.Wrapf(err, "cannot get runtime for Function %q", fn.GetName())
		}

		rctx, err := rt.Start(ctx)
		if err != nil {
			return nil, errors.Wrapf(err, "cannot start Function %q", fn.GetName())
		}

		addrs[fn.GetName()] = rctx.Target
		contexts[fn.GetName()] = rctx
	}

	return &FunctionAddresses{addrs: addrs, contexts: contexts}, nil
}

// RewriteAddressesForDocker rewrites function addresses so they are reachable
// from inside a Docker container. Addresses targeting localhost or 127.0.0.1
// are rewritten to host.docker.internal.
func RewriteAddressesForDocker(fns []*renderv1alpha1.FunctionInput) []*renderv1alpha1.FunctionInput {
	for _, fn := range fns {
		fn.Address = strings.Replace(fn.GetAddress(), "localhost:", "host.docker.internal:", 1)
		fn.Address = strings.Replace(fn.GetAddress(), "127.0.0.1:", "host.docker.internal:", 1)
	}
	return fns
}

// injectNetworkAnnotation sets the Docker network annotation
// on all functions without existing runtime-docker-network annotation
// so their containers join the specified network.
func injectNetworkAnnotation(fns []pkgv1.Function, networkName string) {
	for i := range fns {
		if fns[i].Annotations == nil {
			fns[i].Annotations = make(map[string]string)
		}

		_, ok := fns[i].Annotations[AnnotationKeyRuntimeDockerNetwork]
		if !ok {
			fns[i].Annotations[AnnotationKeyRuntimeDockerNetwork] = networkName
		}
	}
}

// StopFunctionRuntimes stops all function runtimes and returns all failures
// joined. Cleanup runs even if ctx is already cancelled: each runtime gets its
// own timeout derived from ctx without its cancellation, so a slow runtime
// can't starve the others and cleanup stays bounded.
func StopFunctionRuntimes(ctx context.Context, fa *FunctionAddresses) error {
	if fa == nil {
		return nil
	}
	return fa.stop(context.WithoutCancel(ctx), runtimeStopTimeout)
}

// OverrideFunctionAnnotations applies annotation overrides from flags to
// functions.
func OverrideFunctionAnnotations(fns []pkgv1.Function, annotations []string) error {
	for i := range fns {
		if fns[i].Annotations == nil {
			fns[i].Annotations = make(map[string]string)
		}
		for _, annotation := range annotations {
			parts := strings.SplitN(annotation, "=", 2)
			if len(parts) != 2 {
				return errors.Errorf("invalid function annotation format %q, expected key=value", annotation)
			}
			key, value := parts[0], parts[1]
			fns[i].Annotations[key] = value // Flags override existing annotations
		}
	}
	return nil
}

// WarnCleanupFailure writes a short warning about err to w. It does nothing if
// err is nil. Callers use it to surface function runtime cleanup failures that
// would otherwise only be visible with --verbose.
func WarnCleanupFailure(w io.Writer, err error) {
	if err == nil {
		return
	}
	_, _ = fmt.Fprintf(w, "warning: failed to clean up function runtimes; some resources (e.g. containers) may need to be removed manually: %v\n", err)
}
