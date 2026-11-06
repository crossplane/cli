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

package diffprocessor

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	kunstructured "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/kube-openapi/pkg/spec3"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	composed "github.com/crossplane/crossplane-runtime/v2/pkg/resource/unstructured/composed"
	ucomposite "github.com/crossplane/crossplane-runtime/v2/pkg/resource/unstructured/composite"

	apiextensionsv1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1"
	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"

	"github.com/crossplane/cli/v2/internal/containerruntime"
	dt "github.com/crossplane/cli/v2/internal/diff/result"
	"github.com/crossplane/cli/v2/pkg/render"
)

// RenderFn is the render abstraction injected into diff processors. Callers
// provide the Function CRs they already have; the implementation owns the
// engine and FunctionAddresses lifecycle.
type RenderFn func(ctx context.Context, log logging.Logger, in RenderInputs) (render.CompositionOutputs, error)

// RenderInputs carries what the diff processor already holds. It deliberately
// omits FunctionAddrs — that's engine state, not caller state.
type RenderInputs struct {
	CompositeResource   *ucomposite.Unstructured
	Composition         *apiextensionsv1.Composition
	Functions           []pkgv1.Function
	FunctionCredentials []corev1.Secret
	ObservedResources   []composed.Unstructured
	RequiredResources   []kunstructured.Unstructured
	RequiredSchemas     []spec3.OpenAPI

	// XRD is the CompositeResourceDefinition the binary should consider
	// when rendering. Set by RenderToStableState based on a defClient lookup
	// so the binary can pick the right composite.Schema (Legacy vs Modern)
	// for the input XR's GVK. The renderer then writes canonical fields at
	// the path the cluster CRD declares (spec.* for v1, spec.crossplane.*
	// for v2), making dry-run apply succeed and the rendered desired
	// comparable against cluster state. Optional; when nil the binary
	// falls back to SchemaModern.
	XRD *kunstructured.Unstructured
}

// EngineRenderFn is the default RenderFn implementation. It renders through one
// render.Session, which lazily starts function runtimes on first use and reuses
// them across subsequent calls. It is safe for concurrent use: the Session
// serializes starting functions, so renders run in parallel and a function
// needed by concurrent renders is started once.
//
// A single `xr` invocation can render against XRs from multiple compositions
// whose function pipelines overlap but aren't identical. The Session accepts
// each composition's functions in turn (crossplane/cli#159): it sets up the
// engine's environment once, starts only the functions it has not started, and
// on Close stops them all and runs the Setup cleanups. Function containers
// follow their own cleanup annotations, as in render.
type EngineRenderFn struct {
	session *render.Session

	// image is the fully-resolved render image reference the engine will pull,
	// or "" when a local binary backend was selected. Written once at
	// construction and never mutated.
	image string
}

// NewEngineRenderFn constructs an EngineRenderFn.
//
// binaryPath, version, and image select the render backend via
// render.EngineFlags (they are mutually exclusive upstream, grouped under the
// "crossplane-selector" xor):
//   - binaryPath (CrossplaneBinary): drives the upstream localRenderEngine
//     against a local `crossplane` binary.
//   - version (CrossplaneVersion): the docker engine pulls
//     xpkg.crossplane.io/crossplane/crossplane:<version>.
//   - image (CrossplaneImage): the docker engine pulls that full image ref.
//
// When all three are empty the docker engine pulls
// xpkg.crossplane.io/crossplane/crossplane:stable. Both engines now capture
// stderr into the returned error and honour exit code 3 (partial-output-on-
// fatal — crossplane/crossplane#7455) per crossplane/cli#91, so we no longer
// wrap them. Our EngineRenderFn.Render still expects (rsp != nil, err != nil)
// on pipeline fatal and surfaces both — see the comment there.
//
// The docker engine renders on a Docker network: by default one it creates in
// its first Setup and removes when the Session closes, labelled with the
// Session's ID. network, when set (--crossplane-docker-network, then
// CROSSPLANE_DOCKER_NETWORK, then a function annotation), names the user's
// network instead, passed as
// render.EngineFlags.CrossplaneDockerNetwork
// (crossplane/cli#65): the engine then runs the crossplane-render container on
// it AND annotates each fn at Setup time so its container joins it too —
// closing both halves of the "crossplane-diff inside a container" case
// (crossplane/cli#75). The engine never creates or removes that network, and
// neither does the Session: it carries no session label.
func NewEngineRenderFn(log logging.Logger, advisories dt.Advisories, binaryPath, version, image, network string) *EngineRenderFn {
	// Normalize before the value reaches EngineFlags: upstream formats it into
	// the tag verbatim and only v-prefixed tags are published, so the bare form
	// ValidateMinRenderVersion accepts would otherwise pass validation and then
	// fail at pull time. This is the single EngineFlags construction site, so
	// normalizing here also covers callers that set the version programmatically
	// via WithCrossplaneVersion rather than through the CLI flag.
	version = NormalizeRenderVersion(version)

	// A full image reference is floor-checked at the CLI boundary only as far as
	// its tag permits; when it carries no comparable version there is nothing to
	// check, and the user should hear that rather than assume the minimum was
	// enforced. Advisory only — an unverifiable reference is not an invalid one.
	if w := UncomparableRenderImageWarning(image); w != "" {
		advisories.Raise(dt.OutputWarning{Message: w})
	}

	flags := render.EngineFlags{
		CrossplaneBinary:        binaryPath,
		CrossplaneVersion:       version,
		CrossplaneImage:         image,
		CrossplaneDockerNetwork: network,
	}

	// The local engine runs a binary and pulls nothing; otherwise record the
	// reference render's own resolver picks, so staleRenderBackendHint reports
	// what actually ran.
	resolvedImage := ""
	if binaryPath == "" {
		resolvedImage = render.CrossplaneImageFromFlags(&flags)
	}

	return &EngineRenderFn{
		// A failure to lease the run or reclaim a dead run's Docker resources
		// never stops a render, but it leaves resources behind, so it is
		// reported.
		session: render.NewSession(flags, log,
			render.WithLeaseDir(containerruntime.DefaultLeaseDir()),
			render.WithWarn(func(err error) {
				advisories.Raise(dt.OutputWarning{
					Message: "Cannot lease this run or reclaim Docker containers or networks left behind by an earlier run; the next run will retry",
					Context: map[string]string{"error": err.Error()},
				})
			}),
		),
		image: resolvedImage,
	}
}

// Render performs one render. It is safe for concurrent use: only starting
// functions is serialized (by the Session); the render itself runs unlocked,
// so concurrent renders proceed in parallel. Setup runs only on invocations
// that introduce previously-unseen functions; renders whose fns are all
// already running skip straight to building the request.
func (e *EngineRenderFn) Render(ctx context.Context, _ logging.Logger, in RenderInputs) (render.CompositionOutputs, error) {
	fnAddrs, err := e.startFunctions(ctx, in.Functions)
	if err != nil {
		return render.CompositionOutputs{}, err
	}

	req, err := render.BuildCompositeRequest(render.CompositionInputs{
		CompositeResource:   in.CompositeResource,
		Composition:         in.Composition,
		FunctionAddrs:       fnAddrs,
		FunctionCredentials: in.FunctionCredentials,
		ObservedResources:   in.ObservedResources,
		RequiredResources:   in.RequiredResources,
		RequiredSchemas:     in.RequiredSchemas,
		XRD:                 in.XRD,
	})
	if err != nil {
		return render.CompositionOutputs{}, errors.Wrap(err, "cannot build render request")
	}

	rsp, renderErr := e.session.Render(ctx, req)
	// When a pipeline step returns SEVERITY_FATAL, the binary exits with a
	// distinct code, surfaces a populated rsp, and returns an error. Treat
	// this case as "we have partial output; preserve it AND the error so
	// the caller (RenderToStableState) can iterate on RequiredResources
	// even when the pipeline fataled."
	//
	// Anything else with rsp == nil (including a hypothetical (nil, nil) from
	// a misbehaving Engine implementation) is a hard failure — we can't parse
	// what we don't have.
	if rsp == nil {
		if renderErr == nil {
			return render.CompositionOutputs{}, errors.New("render engine returned nil response with no error")
		}

		outErr := errors.Wrap(renderErr, "cannot render")

		// Augment, never replace: the hint rides along with the original
		// failure (and everything it wraps) rather than standing in for it.
		if hint := e.staleRenderBackendHint(renderErr); hint != "" {
			outErr = errors.Errorf("%w; %s", outErr, hint)
		}

		return render.CompositionOutputs{}, outErr
	}

	out, err := render.ParseCompositeResponse(rsp.GetComposite())
	if err != nil {
		return render.CompositionOutputs{}, errors.Wrap(err, "cannot parse render response")
	}

	if renderErr != nil {
		return out, errors.Wrap(renderErr, "render returned partial output after pipeline fatal")
	}

	return out, nil
}

// startFunctions starts whichever of fns are not running yet and returns the
// addresses of all of fns. Starting functions is shared by the run, so a
// composite's XRTimeout neither bounds nor counts it, including the wait for
// another composite's start.
func (e *EngineRenderFn) startFunctions(ctx context.Context, fns []pkgv1.Function) (map[string]string, error) {
	var addrs map[string]string

	err := OutsideXRDeadline(ctx, func(run context.Context) error {
		var err error

		addrs, err = e.session.Start(run, fns)

		return err
	})

	return addrs, err
}

// staleRenderBackendHint returns remediation text when err is
// render.ErrStaleRenderBackend, which the render engines report for a backend
// older than `crossplane internal render`, or "" when it is not.
//
// This shape is otherwise a dead end for the user: a bare kong usage error naming
// an argument they never passed, with nothing connecting it to a render image
// that has quietly gone stale. Upstream pulls the render image only when it is
// absent, so a locally cached :stable tag is frozen at whatever was first pulled
// — for however long that machine lives.
func (e *EngineRenderFn) staleRenderBackendHint(err error) string {
	if !errors.Is(err, render.ErrStaleRenderBackend) {
		return ""
	}

	if e.image == "" {
		return fmt.Sprintf("the crossplane render binary does not support `crossplane internal render`, so it predates %s, the minimum this tool supports; point --crossplane-binary at a newer binary",
			MinCrossplaneRenderVersion)
	}

	return fmt.Sprintf("the cached render image %q does not support `crossplane internal render`, so it predates %s, the minimum this tool supports; re-pull it with `docker pull %s`",
		e.image, MinCrossplaneRenderVersion, e.image)
}

// Cleanup stops every function runtime started across the engine's lifetime,
// runs the Setup cleanups (removing the network the engine created), and
// releases the run's lease, keeping it when something may be left behind for a
// later run to reclaim. Idempotent and safe to call when Render was never
// invoked.
func (e *EngineRenderFn) Cleanup(ctx context.Context) error {
	return e.session.Close(ctx)
}
