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

package render

import (
	"bytes"
	"context"
	"fmt"

	"github.com/alecthomas/kong"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"

	renderv1alpha1 "github.com/crossplane/cli/v2/proto/render/v1alpha1"
)

// DefaultCrossplaneImage is the default Crossplane image used for rendering.
const DefaultCrossplaneImage = "xpkg.crossplane.io/crossplane/crossplane"

// ErrStaleRenderBackend reports that the render backend is a crossplane binary
// or image that predates `crossplane internal render`. Render errors carrying
// this condition match it with errors.Is.
var ErrStaleRenderBackend = errors.New("render backend predates `crossplane internal render`")

// staleRenderBackendError marks err as ErrStaleRenderBackend without changing
// its message.
type staleRenderBackendError struct{ err error }

func (e *staleRenderBackendError) Error() string        { return e.err.Error() }
func (e *staleRenderBackendError) Unwrap() error        { return e.err }
func (e *staleRenderBackendError) Is(target error) bool { return target == ErrStaleRenderBackend }

// classifyRenderError marks err as ErrStaleRenderBackend when the backend's
// stderr carries the stale-backend signature, and otherwise returns it as is.
func classifyRenderError(err error, stderr []byte) error {
	// staleRenderBackendSignature is the kong usage error a crossplane CLI that
	// lacks the `internal` command prints when asked to run `internal render`. It
	// cannot come from a pipeline or connectivity failure on a current backend.
	const staleRenderBackendSignature = "unexpected argument internal"

	if err == nil || !bytes.Contains(stderr, []byte(staleRenderBackendSignature)) {
		return err
	}
	return &staleRenderBackendError{err: err}
}

// An Engine executes a crossplane internal render request and returns the
// response.
type Engine interface {
	// CheckContextSupport validates whether context injection and collection
	// works with this engine in the current runtime environment.
	CheckContextSupport() error

	// Setup performs engine-specific pre-render preparation, such as
	// creating Docker networks and annotating functions so their containers
	// can reach the render engine. It may mutate fns.
	//
	// Setup may be called more than once on the same engine to integrate
	// additional functions into an environment created by a prior call.
	// Only the call that creates a new environment returns a real cleanup;
	// calls that integrate fns into an environment that already exists
	// (because a prior Setup call established it, or because the engine was
	// pre-configured to use an externally-managed environment) return a
	// no-op cleanup, as do calls on engines with nothing to clean up. The
	// real cleanup, if any, must be called when rendering is done; callers
	// can safely defer every returned cleanup in LIFO order without
	// coordinating which one is real.
	Setup(ctx context.Context, fns []pkgv1.Function) (cleanup func(), err error)

	// Render executes the render request and returns the response.
	//
	// On a pipeline-fatal exit (ExitCodePipelineFatal — see
	// crossplane/crossplane#7455), Render may return BOTH a non-nil partial
	// response AND a non-nil error. Callers that need to recover
	// output.RequiredResources (or any other partial output) must check the
	// returned response even when err != nil. Standard "nil-rsp on err"
	// callers can ignore this; the response will simply be nil for them on
	// any other failure mode.
	Render(ctx context.Context, req *renderv1alpha1.RenderRequest) (*renderv1alpha1.RenderResponse, error)
}

// EngineFlags contains flags for configuring the render engine. It is embedded
// by render command structs to provide shared engine configuration.
type EngineFlags struct {
	CrossplaneVersion       string `help:"Version of the Crossplane image to use for rendering. Defaults to the latest stable version."                                                                                 placeholder:"VERSION"                                                                                             xor:"crossplane-selector"`
	CrossplaneImage         string `help:"Override the full Crossplane Docker image reference for rendering."                                                                                                           placeholder:"IMAGE"                                                                                               xor:"crossplane-selector"`
	CrossplaneBinary        string `help:"Path to a local crossplane binary to use instead of Docker. Ignores --crossplane-docker-network."                                                                             placeholder:"PATH"                                                                                                type:"existingfile"       xor:"crossplane-selector"`
	CrossplaneDockerNetwork string `env:"CROSSPLANE_DOCKER_NETWORK"                                                                                                                                                     help:"The docker network to start the crossplane container in (also $$${env}). Ignored with --crossplane-binary."`
	SessionID               string `help:"${session_id_help=Session ID to apply as the render.crossplane.io/session label on Docker containers and networks that render creates. Defaults to a random per-process ID.}" placeholder:"ID"`
}

// AfterApply warns when --crossplane-docker-network is passed on the command
// line with --crossplane-binary, which ignores it. A network from
// CROSSPLANE_DOCKER_NETWORK alone is silent: the environment may export it for
// every command.
func (f *EngineFlags) AfterApply(k *kong.Context) error {
	if f.CrossplaneBinary == "" {
		return nil
	}
	for _, p := range k.Path {
		if p.Flag != nil && !p.Resolved && p.Flag.Name == "crossplane-docker-network" {
			_, _ = fmt.Fprintln(k.Stderr, "warning: --crossplane-docker-network is ignored with --crossplane-binary")
			return nil
		}
	}
	return nil
}

// NewEngineFromFlags creates an Engine from the flag configuration. If a binary
// path is set, it returns a local engine. Otherwise it returns a Docker engine
// using the resolved image reference.
func NewEngineFromFlags(f *EngineFlags, log logging.Logger) Engine {
	if f.CrossplaneBinary != "" {
		return &localRenderEngine{BinaryPath: f.CrossplaneBinary, session: f.SessionID}
	}

	return &dockerRenderEngine{image: CrossplaneImageFromFlags(f), network: f.CrossplaneDockerNetwork, session: f.SessionID, log: log}
}

// CrossplaneImageFromFlags returns the image reference the Docker engine pulls.
func CrossplaneImageFromFlags(f *EngineFlags) string {
	if f.CrossplaneImage != "" {
		return f.CrossplaneImage
	}

	if f.CrossplaneVersion != "" {
		return fmt.Sprintf("%s:%s", DefaultCrossplaneImage, f.CrossplaneVersion)
	}

	return DefaultCrossplaneImage + ":stable"
}
