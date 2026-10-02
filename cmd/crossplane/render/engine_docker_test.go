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
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/moby/moby/client"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"

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

func TestDockerRenderEngineSetup(t *testing.T) {
	// These cases all exercise the early-return branch of Setup — where
	// e.network is already non-empty, either because NewEngineFromFlags
	// populated it from --crossplane-docker-network or because a prior Setup
	// call on the same engine stored its created network there. The branch
	// must annotate the supplied functions so their containers join the
	// network, never create a second network, and always return a no-op
	// cleanup. The create-new-network branch is covered separately by
	// TestDockerRenderEngineSetupCreatesNetwork.
	//
	// The MultiBatchAnnotatesAdditionalFunctions case simulates the
	// in-process multi-composition use case from crossplane/cli#96: a
	// downstream tool (crossplane-diff) calls Setup once per Composition it
	// encounters. Pre-seeding e.network stands in for the prior Setup call
	// that would have created the network, keeping the test hermetic.
	const presetNetwork = "preset-net"

	// batch holds a single Setup invocation: the fns to pass in and the
	// expected fn state after Setup returns. Multi-call cases provide more
	// than one batch; the runner invokes Setup once per batch in order.
	type batch struct {
		fns     []pkgv1.Function
		wantFns []pkgv1.Function
	}

	cases := map[string]struct {
		reason  string
		engine  *dockerRenderEngine
		batches []batch
	}{
		"AnnotatesFunctionsWhenNetworkPreset": {
			reason: "When e.network is set, Setup must inject the network annotation on every fn that does not already carry one, so that crossplane-diff-style multi-batch callers can re-Setup to add new fns to the same network.",
			engine: &dockerRenderEngine{network: presetNetwork, log: logging.NewNopLogger()},
			batches: []batch{{
				fns: []pkgv1.Function{
					functionWithAnnotations(nil),
					functionWithAnnotations(nil),
				},
				wantFns: []pkgv1.Function{
					functionWithAnnotations(map[string]string{AnnotationKeyRuntimeDockerNetwork: presetNetwork}),
					functionWithAnnotations(map[string]string{AnnotationKeyRuntimeDockerNetwork: presetNetwork}),
				},
			}},
		},
		"PreservesUserSetFunctionAnnotation": {
			reason: "If a fn already carries a runtime-docker-network annotation, Setup must not overwrite it. This preserves the don't-overwrite contract from PR #65 for users who pin their fns to a specific network.",
			engine: &dockerRenderEngine{network: presetNetwork, log: logging.NewNopLogger()},
			batches: []batch{{
				fns: []pkgv1.Function{
					functionWithAnnotations(map[string]string{AnnotationKeyRuntimeDockerNetwork: "user-pinned-net"}),
				},
				wantFns: []pkgv1.Function{
					functionWithAnnotations(map[string]string{AnnotationKeyRuntimeDockerNetwork: "user-pinned-net"}),
				},
			}},
		},
		"NilFunctionsList": {
			reason: "A nil fns slice is a boundary case: Setup must succeed without panicking on the nil-map guard inside injectNetworkAnnotation.",
			engine: &dockerRenderEngine{network: presetNetwork, log: logging.NewNopLogger()},
			batches: []batch{{
				fns:     nil,
				wantFns: nil,
			}},
		},
		"EmptyFunctionsList": {
			reason: "An empty (non-nil) fns slice is the other boundary: Setup must succeed, the slice stays empty, and no spurious entries appear.",
			engine: &dockerRenderEngine{network: presetNetwork, log: logging.NewNopLogger()},
			batches: []batch{{
				fns:     []pkgv1.Function{},
				wantFns: []pkgv1.Function{},
			}},
		},
		"MultiBatchAnnotatesAdditionalFunctions": {
			reason: "Two consecutive Setup calls on the same engine — the crossplane/cli#96 in-process multi-composition pattern — must annotate every batch with the same network. Each call returns a no-op cleanup that is safe to defer in LIFO order.",
			engine: &dockerRenderEngine{network: presetNetwork, log: logging.NewNopLogger()},
			batches: []batch{
				{
					fns: []pkgv1.Function{
						functionWithAnnotations(nil),
						functionWithAnnotations(nil),
					},
					wantFns: []pkgv1.Function{
						functionWithAnnotations(map[string]string{AnnotationKeyRuntimeDockerNetwork: presetNetwork}),
						functionWithAnnotations(map[string]string{AnnotationKeyRuntimeDockerNetwork: presetNetwork}),
					},
				},
				{
					fns: []pkgv1.Function{
						functionWithAnnotations(nil),
					},
					wantFns: []pkgv1.Function{
						functionWithAnnotations(map[string]string{AnnotationKeyRuntimeDockerNetwork: presetNetwork}),
					},
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cleanups := make([]func(), 0, len(tc.batches))
			for i, b := range tc.batches {
				cleanup, err := tc.engine.Setup(context.Background(), b.fns)
				if err != nil {
					t.Fatalf("\n%s\nSetup(batch %d): unexpected error: %v", tc.reason, i, err)
				}
				if cleanup == nil {
					t.Fatalf("\n%s\nSetup(batch %d): cleanup is nil, want non-nil", tc.reason, i)
				}
				cleanups = append(cleanups, cleanup)

				if diff := cmp.Diff(b.wantFns, b.fns); diff != "" {
					t.Errorf("\n%s\nSetup(batch %d): fns -want, +got:\n%s", tc.reason, i, diff)
				}
			}

			// Defer-LIFO: all cleanups in this test are no-ops (we pre-seed
			// e.network, so no call took the create-network branch). Calling
			// them must not panic.
			for _, cleanup := range slices.Backward(cleanups) {
				cleanup()
			}

			if tc.engine.network != presetNetwork {
				t.Errorf("\n%s\nSetup(...): e.network mutated from %q to %q (early-return branch must not change it)", tc.reason, presetNetwork, tc.engine.network)
			}
		})
	}
}

func TestDockerRenderEngineSetupCreatesNetwork(t *testing.T) {
	// When e.network is unset, Setup must create a temporary network through
	// the engine's network client, record its name, annotate the supplied
	// functions to join it, and return a cleanup that removes it through the
	// same client.
	errBoom := errors.New("boom")

	type args struct {
		create func(ctx context.Context, name string, options client.NetworkCreateOptions) (client.NetworkCreateResult, error)
	}
	type want struct {
		err error
		// created is whether Setup should create a network, annotate the
		// functions to join it, and return a cleanup that removes it.
		created bool
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"CreatesNetwork": {
			reason: "Setup should create a network, annotate the functions to join it, and return a cleanup that removes it.",
			args: args{
				create: createRenderNetworkReturns("network-id", nil),
			},
			want: want{
				created: true,
			},
		},
		"NetworkCreateError": {
			reason: "Setup should return an error and a no-op cleanup, leaving the functions unannotated, when it cannot create the network.",
			args: args{
				create: createRenderNetworkReturns("", errBoom),
			},
			want: want{
				err: errBoom,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// Cleanup discards NetworkRemove's result, so record whether it
			// was called. A cleanup that shouldn't remove anything leaves
			// MockNetworkRemove nil.
			removed := false
			cli := &mockNetworkClient{MockNetworkCreate: tc.args.create}
			if tc.want.created {
				cli.MockNetworkRemove = func(_ context.Context, networkID string, _ client.NetworkRemoveOptions) (client.NetworkRemoveResult, error) {
					if diff := cmp.Diff("network-id", networkID); diff != "" {
						t.Errorf("\n%s\nNetworkRemove(...): -want network ID, +got network ID:\n%s", tc.reason, diff)
					}
					removed = true
					return client.NetworkRemoveResult{}, nil
				}
			}
			e := &dockerRenderEngine{log: logging.NewNopLogger(), networks: cli}
			fns := []pkgv1.Function{functionWithAnnotations(nil)}

			cleanup, err := e.Setup(t.Context(), fns)

			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nSetup(...): -want error, +got error:\n%s", tc.reason, diff)
			}

			wantFns := []pkgv1.Function{functionWithAnnotations(nil)}
			if tc.want.created {
				if !strings.HasPrefix(e.network, renderNetworkPrefix) {
					t.Errorf("\n%s\nSetup(...): e.network = %q, want a network with prefix %q", tc.reason, e.network, renderNetworkPrefix)
				}
				wantFns = []pkgv1.Function{functionWithAnnotations(map[string]string{AnnotationKeyRuntimeDockerNetwork: e.network})}
			} else if e.network != "" {
				t.Errorf("\n%s\nSetup(...): e.network = %q, want it unset", tc.reason, e.network)
			}
			if diff := cmp.Diff(wantFns, fns); diff != "" {
				t.Errorf("\n%s\nSetup(...): -want fns, +got fns:\n%s", tc.reason, diff)
			}

			cleanup()

			if diff := cmp.Diff(tc.want.created, removed); diff != "" {
				t.Errorf("\n%s\nSetup(...) cleanup: -want network removed, +got network removed:\n%s", tc.reason, diff)
			}
		})
	}
}

// nonExitError is a stand-in for non-*ContainerExitError failures (e.g. image
// pull errors) returned by docker.RunContainer.
type nonExitError struct{ msg string }

func (e *nonExitError) Error() string { return e.msg }
