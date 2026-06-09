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
	"os/exec"

	"google.golang.org/protobuf/proto"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"

	renderv1alpha1 "github.com/crossplane/cli/v2/proto/render/v1alpha1"
)

// localRenderEngine executes a local crossplane binary for rendering.
type localRenderEngine struct {
	// BinaryPath is the path to the crossplane binary.
	BinaryPath string
}

func (e *localRenderEngine) CheckContextSupport() error {
	return nil
}

// Setup is a no-op for the local engine. Function containers publish ports to
// localhost, so there's nothing extra to configure.
func (e *localRenderEngine) Setup(_ context.Context, _ []pkgv1.Function) (func(), error) {
	return func() {}, nil
}

// Render marshals the request, runs it through a local binary, and returns
// the response.
//
// Stderr is captured into the returned error so callers can surface fatal
// pipeline messages programmatically. When the binary exits with
// ExitCodePipelineFatal (a pipeline step returned SEVERITY_FATAL) and stdout
// carries a partial RenderResponse, Render parses it and returns both the
// partial response AND a non-nil error containing stderr — letting callers
// recover the partial output (e.g. RequiredResources) and iterate.
func (e *localRenderEngine) Render(ctx context.Context, req *renderv1alpha1.RenderRequest) (*renderv1alpha1.RenderResponse, error) {
	data, err := proto.Marshal(req)
	if err != nil {
		return nil, errors.Wrap(err, "cannot marshal render request")
	}

	var stderr bytes.Buffer

	cmd := exec.CommandContext(ctx, e.BinaryPath, "internal", "render") //nolint:gosec // The binary path is user-supplied via CLI flag.
	cmd.Stdin = bytes.NewReader(data)
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == ExitCodePipelineFatal && len(out) > 0 {
			// Pipeline-fatal with partial output. Parse the partial response
			// and return both it and the stderr-bearing error.
			rsp := &renderv1alpha1.RenderResponse{}
			if uerr := proto.Unmarshal(out, rsp); uerr != nil {
				return nil, errors.Wrapf(uerr, "cannot unmarshal partial render response after pipeline fatal: %s", stderr.String())
			}
			return rsp, errors.Errorf("crossplane internal render: pipeline returned fatal: %s", stderr.String())
		}
		return nil, errors.Wrapf(err, "crossplane internal render returned error with output: %s", stderr.String())
	}

	rsp := &renderv1alpha1.RenderResponse{}
	if err := proto.Unmarshal(out, rsp); err != nil {
		return nil, errors.Wrap(err, "cannot unmarshal render response")
	}

	return rsp, nil
}
