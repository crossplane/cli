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

	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/structpb"

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

func TestDockerRenderEngineRender(t *testing.T) {
	// A canned response with a distinguishing CompositeResource so a successful
	// (or partial) round-trip through Render asserts the unmarshal path, not
	// just that we got something non-nil back.
	xrStruct, err := structpb.NewStruct(map[string]any{
		"apiVersion": "example.org/v1",
		"kind":       "XR",
		"metadata":   map[string]any{"name": "test-xr"},
	})
	if err != nil {
		t.Fatalf("cannot construct canned XR struct: %v", err)
	}
	cannedRsp := &renderv1alpha1.RenderResponse{
		Output: &renderv1alpha1.RenderResponse_Composite{
			Composite: &renderv1alpha1.CompositeOutput{
				CompositeResource: xrStruct,
			},
		},
	}
	cannedRspBytes, err := proto.Marshal(cannedRsp)
	if err != nil {
		t.Fatalf("cannot marshal canned response: %v", err)
	}

	type args struct {
		runFn func(ctx context.Context, img string, opts ...docker.RunContainerOption) ([]byte, []byte, error)
	}

	type want struct {
		rsp                  *renderv1alpha1.RenderResponse
		wantErr              bool
		wantInErr            []string
		wantSingleOccurrence []string
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"Success": {
			reason: "Render returns the unmarshaled response and no error on a clean exit.",
			args: args{
				runFn: func(_ context.Context, _ string, _ ...docker.RunContainerOption) ([]byte, []byte, error) {
					return cannedRspBytes, nil, nil
				},
			},
			want: want{rsp: cannedRsp},
		},
		"FatalWithPartialOutput": {
			reason: "On exit-3 with non-empty stdout, Render parses the partial response and returns it alongside a stderr-bearing error.",
			args: args{
				runFn: func(_ context.Context, _ string, _ ...docker.RunContainerOption) ([]byte, []byte, error) {
					return cannedRspBytes, []byte("boom: pipeline step requested fatal"), &docker.ContainerExitError{
						ExitCode: ExitCodePipelineFatal,
						Stderr:   []byte("boom: pipeline step requested fatal"),
					}
				},
			},
			want: want{
				rsp:     cannedRsp,
				wantErr: true,
				wantInErr: []string{
					"pipeline returned fatal",
					"boom: pipeline step requested fatal",
				},
			},
		},
		"FatalWithNoPartialOutput": {
			reason: "On exit-3 with empty stdout, Render falls back to the hard-fail path and surfaces stderr exactly once.",
			args: args{
				runFn: func(_ context.Context, _ string, _ ...docker.RunContainerOption) ([]byte, []byte, error) {
					return nil, []byte("boom: no partial"), &docker.ContainerExitError{
						ExitCode: ExitCodePipelineFatal,
						Stderr:   []byte("boom: no partial"),
					}
				},
			},
			want: want{
				wantErr: true,
				wantInErr: []string{
					"crossplane internal render in Docker returned error with output",
					"boom: no partial",
					"container exited with status 3",
				},
				wantSingleOccurrence: []string{"boom: no partial"},
			},
		},
		"HardFailWithExitError": {
			reason: "Non-fatal exit codes wrap the *ContainerExitError; stderr is included once via Wrapf, exit code via the wrapped Error().",
			args: args{
				runFn: func(_ context.Context, _ string, _ ...docker.RunContainerOption) ([]byte, []byte, error) {
					return nil, []byte("the container is sad"), &docker.ContainerExitError{
						ExitCode: 1,
						Stderr:   []byte("the container is sad"),
					}
				},
			},
			want: want{
				wantErr: true,
				wantInErr: []string{
					"crossplane internal render in Docker returned error with output",
					"the container is sad",
					"container exited with status 1",
				},
				wantSingleOccurrence: []string{"the container is sad"},
			},
		},
		"HardFailNonExitError": {
			reason: "Non-exit errors (e.g. image-pull failures) get the captured stderr buffer appended so its content isn't lost.",
			args: args{
				runFn: func(_ context.Context, _ string, _ ...docker.RunContainerOption) ([]byte, []byte, error) {
					return nil, []byte("non-exit stderr"), &nonExitError{msg: "image pull failed"}
				},
			},
			want: want{
				wantErr: true,
				wantInErr: []string{
					"crossplane internal render in Docker returned error with output",
					"image pull failed",
					"non-exit stderr",
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := &dockerRenderEngine{
				image:  "test-image",
				log:    logging.NewNopLogger(),
				runner: &mockContainerRunner{MockRun: tc.args.runFn},
			}

			rsp, err := e.Render(context.Background(), &renderv1alpha1.RenderRequest{})

			switch {
			case tc.want.wantErr && err == nil:
				t.Fatalf("\n%s\nRender(...): want error, got nil", tc.reason)
			case !tc.want.wantErr && err != nil:
				t.Fatalf("\n%s\nRender(...): unexpected error: %v", tc.reason, err)
			}

			for _, s := range tc.want.wantInErr {
				if err == nil {
					t.Errorf("\n%s\nRender(...): error is nil but expected to contain %q", tc.reason, s)
					continue
				}
				if !strings.Contains(err.Error(), s) {
					t.Errorf("\n%s\nRender(...): error %q does not contain %q", tc.reason, err.Error(), s)
				}
			}

			for _, s := range tc.want.wantSingleOccurrence {
				if err == nil {
					t.Errorf("\n%s\nRender(...): error is nil but expected exactly one occurrence of %q", tc.reason, s)
					continue
				}
				if got := strings.Count(err.Error(), s); got != 1 {
					t.Errorf("\n%s\nRender(...): error %q contains %q %d times, want exactly 1 (double-formatting bug?)", tc.reason, err.Error(), s, got)
				}
			}

			if diff := cmp.Diff(tc.want.rsp, rsp, protocmp.Transform()); diff != "" {
				t.Errorf("\n%s\nRender(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

// nonExitError is a stand-in for non-*ContainerExitError failures (e.g. image
// pull errors) returned by docker.RunContainer.
type nonExitError struct{ msg string }

func (e *nonExitError) Error() string { return e.msg }
