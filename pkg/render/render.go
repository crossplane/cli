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

// Package render implements the composition and operation rendering engine,
// available for import by external tools interacting with Crossplane.
package render

import (
	"context"
	"fmt"
	"io"
	"strings"

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

// FunctionAddresses maps function names to their gRPC target addresses.
type FunctionAddresses struct {
	addrs map[string]string
	// runtimes holds every runtime started, in start order. It's a slice
	// rather than a map keyed by name so that Functions sharing a name all
	// get stopped, even though only the last one's address is used.
	runtimes []startedRuntime
}

// startedRuntime is a started runtime and the name of its Function.
type startedRuntime struct {
	RuntimeContext

	name string
}

// Addresses returns the function name to gRPC address map.
func (fa *FunctionAddresses) Addresses() map[string]string {
	return fa.addrs
}

// Stop all function runtimes. Every runtime is stopped even if some fail; the
// returned error joins all failures. Each runtime bounds its own cleanup.
func (fa *FunctionAddresses) Stop(ctx context.Context) error {
	var errs []error
	for _, rt := range fa.runtimes {
		if err := rt.Stop(ctx); err != nil {
			errs = append(errs, errors.Wrapf(err, "cannot stop function %q runtime (target %q)", rt.name, rt.Target))
		}
	}
	return errors.Join(errs...)
}

// StartFunctionRuntimes starts the runtime for each function and returns their
// gRPC addresses. The caller must call Stop on the returned FunctionAddresses
// when done.
func StartFunctionRuntimes(ctx context.Context, log logging.Logger, fns []pkgv1.Function) (*FunctionAddresses, error) {
	return startFunctionRuntimes(ctx, log, fns, GetRuntime)
}

// startFunctionRuntimes implements StartFunctionRuntimes, using getRuntime to
// get each Function's runtime.
func startFunctionRuntimes(ctx context.Context, log logging.Logger, fns []pkgv1.Function, getRuntime func(pkgv1.Function, logging.Logger) (Runtime, error)) (*FunctionAddresses, error) {
	fa := &FunctionAddresses{addrs: make(map[string]string, len(fns))}

	for _, fn := range fns {
		rt, err := getRuntime(fn, log)
		if err != nil {
			return nil, stopStarted(ctx, fa, errors.Wrapf(err, "cannot get runtime for Function %q", fn.GetName()))
		}

		rctx, err := rt.Start(ctx)
		if err != nil {
			return nil, stopStarted(ctx, fa, errors.Wrapf(err, "cannot start Function %q", fn.GetName()))
		}

		fa.addrs[fn.GetName()] = rctx.Target
		fa.runtimes = append(fa.runtimes, startedRuntime{name: fn.GetName(), RuntimeContext: rctx})
	}

	return fa, nil
}

// stopStarted stops the runtimes started before a later Function failed to
// start. It returns startErr, joined with any stop errors. StopFunctionRuntimes
// detaches the stop from ctx's cancellation, since ctx may be why the start
// failed.
func stopStarted(ctx context.Context, fa *FunctionAddresses, startErr error) error {
	if err := StopFunctionRuntimes(ctx, fa); err != nil {
		return errors.Join(startErr, err)
	}
	return startErr
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

// injectSessionAnnotation annotates fns so their containers carry the supplied
// session ID. Functions that already carry the annotation are left untouched.
// An empty session is a no-op, so containers fall back to the per-process
// session ID.
func injectSessionAnnotation(fns []pkgv1.Function, session string) {
	if session == "" {
		return
	}

	for i := range fns {
		if fns[i].Annotations == nil {
			fns[i].Annotations = make(map[string]string)
		}

		if _, ok := fns[i].Annotations[AnnotationKeyRuntimeDockerSession]; !ok {
			fns[i].Annotations[AnnotationKeyRuntimeDockerSession] = session
		}
	}
}

// StopFunctionRuntimes stops all function runtimes and returns all failures
// joined. Cleanup runs even if ctx is already cancelled, so it can follow an
// interrupted render. Each runtime bounds its own cleanup.
func StopFunctionRuntimes(ctx context.Context, fa *FunctionAddresses) error {
	if fa == nil {
		return nil
	}
	return fa.Stop(context.WithoutCancel(ctx))
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
