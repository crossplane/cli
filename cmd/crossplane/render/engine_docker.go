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
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"

	"github.com/crossplane/cli/v2/internal/docker"
	renderv1alpha1 "github.com/crossplane/cli/v2/proto/render/v1alpha1"
)

// runContainerFn matches docker.RunContainer's signature. It's a seam for
// testing the engine's failure-mode handling without a real Docker daemon —
// see runContainer below.
type runContainerFn func(ctx context.Context, img string, opts ...docker.RunContainerOption) ([]byte, []byte, error)

// dockerRenderEngine executes crossplane internal render in a Docker container.
type dockerRenderEngine struct {
	// image is the Crossplane Docker image reference.
	image string
	// network is the Docker network to connect the container to. When set,
	// the container joins this network so it can reach function containers.
	network string

	log logging.Logger

	// runContainer is the function used to actually run the render container.
	// Production callers leave it nil and Render falls through to
	// docker.RunContainer. Tests inject a fake so we can exercise the engine's
	// behaviour around the docker package's return contract — particularly
	// the exit-code-3 partial-output recovery path and the difference between
	// *docker.ContainerExitError and other failure types like image-pull
	// errors. Spinning up a real container that exits with a chosen code on
	// demand is awkward; replacing the call at the package level via a global
	// var would race in parallel tests. A per-instance unexported field gives
	// us hermetic, sub-second tests with no shared mutable state.
	runContainer runContainerFn
}

func (e *dockerRenderEngine) CheckContextSupport() error {
	if runtime.GOOS == "windows" {
		return errors.New("context handling via --context-values/--context-files/--include-context is not supported on Windows")
	}
	if host := os.Getenv("DOCKER_HOST"); host != "" && !strings.HasPrefix(host, "unix://") {
		return errors.New("context handling via --context-values/--context-files/--include-context requires a local Docker daemon or Crossplane controller binary")
	}

	return nil
}

// Setup creates a temporary Docker network, records its name so the render
// container joins it, and annotates the supplied functions so their
// containers also join it. The returned cleanup function removes the
// network.
func (e *dockerRenderEngine) Setup(ctx context.Context, fns []pkgv1.Function) (func(), error) {
	networkID, networkName, err := createRenderNetwork(ctx)
	if err != nil {
		return func() {}, errors.Wrap(err, "cannot create Docker network for rendering")
	}

	e.network = networkName
	injectNetworkAnnotation(fns, networkName)

	cleanup := func() { //nolint:contextcheck // Detached context for cleanup.
		_ = removeRenderNetwork(context.Background(), networkID)
	}

	return cleanup, nil
}

// Render marshals the request, runs it through a Docker container, and returns
// the response.
//
// Stderr from the container is captured (via docker.RunContainer's stderr
// return) and surfaced in returned errors so callers can inspect fatal
// pipeline messages programmatically. When the container exits with
// ExitCodePipelineFatal (a pipeline step returned SEVERITY_FATAL) and stdout
// carries a partial RenderResponse, Render parses it and returns both the
// partial response AND a non-nil error containing stderr — letting callers
// recover the partial output (e.g. RequiredResources) and iterate.
func (e *dockerRenderEngine) Render(ctx context.Context, req *renderv1alpha1.RenderRequest) (*renderv1alpha1.RenderResponse, error) {
	// Update any localhost function addresses if needed.
	if cinput := req.GetComposite(); cinput != nil {
		cinput.Functions = RewriteAddressesForDocker(cinput.GetFunctions())
	}
	if oinput := req.GetOperation(); oinput != nil {
		oinput.Functions = RewriteAddressesForDocker(oinput.GetFunctions())
	}

	data, err := proto.Marshal(req)
	if err != nil {
		return nil, errors.Wrap(err, "cannot marshal render request")
	}

	opts := []docker.RunContainerOption{
		docker.RunWithCommand([]string{"internal", "render"}),
		docker.RunWithStdin(data),
		// Let the container access any functions running in development mode on
		// the host.
		docker.RunWithExtraHosts([]string{"host.docker.internal:host-gateway"}),
	}
	if e.network != "" {
		opts = append(opts, docker.RunWithNetworkName(e.network))
	}

	// Bind-mount the directory of every unix-socket function target into the
	// render container at the same path so unix:// targets are reachable.
	for _, fn := range getFunctionInputs(req) {
		addr := fn.GetAddress()
		if !strings.HasPrefix(addr, "unix://") {
			continue
		}
		dir := filepath.Dir(strings.TrimPrefix(addr, "unix://"))
		opts = append(opts, docker.RunWithBindMount(dir, dir))
	}

	e.log.Debug("Running crossplane internal render in Docker", "image", e.image, "network", e.network)

	run := e.runContainer
	if run == nil {
		run = docker.RunContainer
	}

	stdout, stderr, err := run(ctx, e.image, opts...)
	if err != nil {
		var exitErr *docker.ContainerExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode == ExitCodePipelineFatal && len(stdout) > 0 {
			// Pipeline-fatal with partial output. Parse the partial response
			// and return both it and the stderr-bearing error.
			rsp := &renderv1alpha1.RenderResponse{}
			if uerr := proto.Unmarshal(stdout, rsp); uerr != nil {
				return nil, errors.Errorf("cannot unmarshal partial render response after pipeline fatal: %s: %s", uerr.Error(), exitErr.Stderr)
			}
			return rsp, errors.Errorf("crossplane internal render in Docker: pipeline returned fatal: %s", exitErr.Stderr)
		}
		// stderr may have been folded into err already (ContainerExitError
		// stringifies it); fall back to the captured stderr buffer otherwise
		// so messages from non-exit failures (e.g. image pull) aren't lost.
		return nil, errors.Errorf("cannot run crossplane internal render in Docker: %s: %s", err.Error(), stderr)
	}

	rsp := &renderv1alpha1.RenderResponse{}
	if err := proto.Unmarshal(stdout, rsp); err != nil {
		return nil, errors.Wrap(err, "cannot unmarshal render response")
	}

	return rsp, nil
}

// getFunctionInputs returns the FunctionInput list regardless of which oneof
// variant the RenderRequest carries.
func getFunctionInputs(req *renderv1alpha1.RenderRequest) []*renderv1alpha1.FunctionInput {
	switch in := req.GetInput().(type) {
	case *renderv1alpha1.RenderRequest_Composite:
		return in.Composite.GetFunctions()
	case *renderv1alpha1.RenderRequest_Operation:
		return in.Operation.GetFunctions()
	default:
		return nil
	}
}
