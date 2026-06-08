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
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	"github.com/crossplane/cli/v2/internal/docker"
	renderv1alpha1 "github.com/crossplane/cli/v2/proto/render/v1alpha1"
)

type mockContainerRunner struct {
	MockRun func(ctx context.Context, img string, opts ...docker.RunContainerOption) ([]byte, []byte, error)
}

func (m *mockContainerRunner) Run(ctx context.Context, img string, opts ...docker.RunContainerOption) ([]byte, []byte, error) {
	return m.MockRun(ctx, img, opts...)
}

var _ containerRunner = &mockContainerRunner{}

func TestDockerRenderEngine_Render(t *testing.T) {
	rsp := &renderv1alpha1.RenderResponse{
		Output: &renderv1alpha1.RenderResponse_Composite{
			Composite: &renderv1alpha1.CompositeOutput{},
		},
	}
	rspBytes, err := proto.Marshal(rsp)
	if err != nil {
		t.Fatalf("cannot marshal canned response: %v", err)
	}

	cases := map[string]struct {
		runFn     func(ctx context.Context, img string, opts ...docker.RunContainerOption) ([]byte, []byte, error)
		wantRsp   bool
		wantErr   bool
		wantInErr []string
	}{
		"Success": {
			runFn: func(_ context.Context, _ string, _ ...docker.RunContainerOption) ([]byte, []byte, error) {
				return rspBytes, nil, nil
			},
			wantRsp: true,
		},
		"FatalWithPartialOutput": {
			runFn: func(_ context.Context, _ string, _ ...docker.RunContainerOption) ([]byte, []byte, error) {
				return rspBytes, []byte("boom: pipeline step requested fatal"), &docker.ContainerExitError{
					ExitCode: ExitCodePipelineFatal,
					Stderr:   []byte("boom: pipeline step requested fatal"),
				}
			},
			wantRsp: true,
			wantErr: true,
			wantInErr: []string{
				"pipeline returned fatal",
				"boom: pipeline step requested fatal",
			},
		},
		"FatalWithNoPartialOutput": {
			runFn: func(_ context.Context, _ string, _ ...docker.RunContainerOption) ([]byte, []byte, error) {
				return nil, []byte("boom: no partial"), &docker.ContainerExitError{
					ExitCode: ExitCodePipelineFatal,
					Stderr:   []byte("boom: no partial"),
				}
			},
			wantRsp: false,
			wantErr: true,
			wantInErr: []string{
				"cannot run crossplane internal render in Docker",
			},
		},
		"HardFailWithExitError": {
			runFn: func(_ context.Context, _ string, _ ...docker.RunContainerOption) ([]byte, []byte, error) {
				return nil, []byte("the container is sad"), &docker.ContainerExitError{
					ExitCode: 1,
					Stderr:   []byte("the container is sad"),
				}
			},
			wantRsp: false,
			wantErr: true,
			wantInErr: []string{
				"cannot run crossplane internal render in Docker",
				"the container is sad",
			},
		},
		"HardFailNonExitError": {
			runFn: func(_ context.Context, _ string, _ ...docker.RunContainerOption) ([]byte, []byte, error) {
				// e.g. image-pull failure: not a *ContainerExitError.
				return nil, []byte("non-exit stderr"), &nonExitError{msg: "image pull failed"}
			},
			wantRsp: false,
			wantErr: true,
			wantInErr: []string{
				"cannot run crossplane internal render in Docker",
				"image pull failed",
				"non-exit stderr",
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := &dockerRenderEngine{
				image:  "test-image",
				log:    logging.NewNopLogger(),
				runner: &mockContainerRunner{MockRun: tc.runFn},
			}

			rsp, err := e.Render(context.Background(), &renderv1alpha1.RenderRequest{})

			switch {
			case tc.wantErr && err == nil:
				t.Fatalf("Render(): want error, got nil")
			case !tc.wantErr && err != nil:
				t.Fatalf("Render(): unexpected error: %v", err)
			}

			for _, want := range tc.wantInErr {
				if err == nil {
					t.Errorf("Render(): error is nil but expected to contain %q", want)
					continue
				}
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Render(): error %q does not contain %q", err.Error(), want)
				}
			}

			switch {
			case tc.wantRsp && rsp == nil:
				t.Errorf("Render(): want non-nil response, got nil")
			case !tc.wantRsp && rsp != nil:
				t.Errorf("Render(): want nil response, got %+v", rsp)
			}
		})
	}
}

// nonExitError is a stand-in for non-*ContainerExitError failures (e.g. image
// pull errors) returned by docker.RunContainer.
type nonExitError struct{ msg string }

func (e *nonExitError) Error() string { return e.msg }
