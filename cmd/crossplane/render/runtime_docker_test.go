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

package render

import (
	"context"
	"io"
	"net"
	"strconv"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"
)

type mockPullClient struct {
	MockPullImage func(_ context.Context, ref string, options client.ImagePullOptions) (client.ImagePullResponse, error)
}

func (m *mockPullClient) ImagePull(ctx context.Context, ref string, options client.ImagePullOptions) (client.ImagePullResponse, error) {
	return m.MockPullImage(ctx, ref, options)
}

var _ pullClient = &mockPullClient{}

type mockContainerClient struct {
	MockImagePull        func(ctx context.Context, ref string, options client.ImagePullOptions) (client.ImagePullResponse, error)
	MockContainerInspect func(ctx context.Context, containerID string, options client.ContainerInspectOptions) (client.ContainerInspectResult, error)
	MockContainerCreate  func(ctx context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error)
	MockContainerStart   func(ctx context.Context, containerID string, options client.ContainerStartOptions) (client.ContainerStartResult, error)
	MockContainerStop    func(ctx context.Context, containerID string, options client.ContainerStopOptions) (client.ContainerStopResult, error)
	MockContainerRemove  func(ctx context.Context, containerID string, options client.ContainerRemoveOptions) (client.ContainerRemoveResult, error)
}

func (m *mockContainerClient) ImagePull(ctx context.Context, ref string, options client.ImagePullOptions) (client.ImagePullResponse, error) {
	return m.MockImagePull(ctx, ref, options)
}

func (m *mockContainerClient) ContainerInspect(ctx context.Context, containerID string, options client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	return m.MockContainerInspect(ctx, containerID, options)
}

func (m *mockContainerClient) ContainerCreate(ctx context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	return m.MockContainerCreate(ctx, options)
}

func (m *mockContainerClient) ContainerStart(ctx context.Context, containerID string, options client.ContainerStartOptions) (client.ContainerStartResult, error) {
	return m.MockContainerStart(ctx, containerID, options)
}

func (m *mockContainerClient) ContainerStop(ctx context.Context, containerID string, options client.ContainerStopOptions) (client.ContainerStopResult, error) {
	return m.MockContainerStop(ctx, containerID, options)
}

func (m *mockContainerClient) ContainerRemove(ctx context.Context, containerID string, options client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	return m.MockContainerRemove(ctx, containerID, options)
}

var _ containerClient = &mockContainerClient{}

// imagePullDone is a MockImagePull that reports an image pull that has
// already completed.
func imagePullDone(context.Context, string, client.ImagePullOptions) (client.ImagePullResponse, error) {
	return pullDoneResponse{}, nil
}

// pullDoneResponse is an ImagePullResponse whose body is empty. Only the
// io.ReadCloser methods PullImage uses are implemented; the embedded
// interface is nil.
type pullDoneResponse struct {
	client.ImagePullResponse
}

func (pullDoneResponse) Read([]byte) (int, error) { return 0, io.EOF }

func (pullDoneResponse) Close() error { return nil }

// createContainerReturns returns a MockContainerCreate that creates a
// container with the supplied ID.
func createContainerReturns(id string) func(context.Context, client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	return func(context.Context, client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
		return client.ContainerCreateResult{ID: id}, nil
	}
}

// startContainer returns a MockContainerStart that starts the container with
// the supplied ID, and returns an error for any other container.
func startContainer(id string) func(context.Context, string, client.ContainerStartOptions) (client.ContainerStartResult, error) {
	return func(_ context.Context, containerID string, _ client.ContainerStartOptions) (client.ContainerStartResult, error) {
		if diff := cmp.Diff(id, containerID); diff != "" {
			return client.ContainerStartResult{}, errors.Errorf("ContainerStart(...): -want container ID, +got container ID:\n%s", diff)
		}
		return client.ContainerStartResult{}, nil
	}
}

// inspectContainerOnNetwork returns a MockContainerInspect that reports the
// container with the supplied ID as running with the supplied name, attached
// to the supplied Docker network. It returns an error for any other container.
func inspectContainerOnNetwork(id, name, networkName string) func(context.Context, string, client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	return func(_ context.Context, containerID string, _ client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
		if diff := cmp.Diff(id, containerID); diff != "" {
			return client.ContainerInspectResult{}, errors.Errorf("ContainerInspect(...): -want container ID, +got container ID:\n%s", diff)
		}
		return client.ContainerInspectResult{Container: container.InspectResponse{
			ID:   id,
			Name: "/" + name,
			NetworkSettings: &container.NetworkSettings{
				Networks: map[string]*network.EndpointSettings{networkName: {}},
			},
		}}, nil
	}
}

func TestGetRuntimeDocker(t *testing.T) {
	type args struct {
		fn pkgv1.Function
	}

	type want struct {
		rd  *RuntimeDocker
		err error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"SuccessAllSet": {
			reason: "should return a RuntimeDocker with all fields set according to the supplied Function's annotations",
			args: args{
				fn: pkgv1.Function{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							AnnotationKeyRuntimeDockerCleanup:        string(AnnotationValueRuntimeDockerCleanupOrphan),
							AnnotationKeyRuntimeDockerPullPolicy:     string(AnnotationValueRuntimeDockerPullPolicyAlways),
							AnnotationKeyRuntimeDockerImage:          "test-image-from-annotation",
							AnnotationKeyRuntimeEnvironmentVariables: "KCL_DEFAULT_REGISTRY=registry.example.com,ANOTHER_ENV_VAR=another-value",
							AnnotationKeyRuntimeDockerNetwork:        "test-network",
						},
					},
					Spec: pkgv1.FunctionSpec{
						PackageSpec: pkgv1.PackageSpec{
							Package: "test-package",
						},
					},
				},
			},
			want: want{
				rd: &RuntimeDocker{
					Image:       "test-image-from-annotation",
					Cleanup:     AnnotationValueRuntimeDockerCleanupOrphan,
					PullPolicy:  AnnotationValueRuntimeDockerPullPolicyAlways,
					Env:         []string{"KCL_DEFAULT_REGISTRY=registry.example.com", "ANOTHER_ENV_VAR=another-value"},
					BindAddress: "127.0.0.1",
					Network:     "test-network",
				},
			},
		},
		"SuccessNamedContainer": {
			reason: "should return a RuntimeDocker with the correct name.",
			args: args{
				fn: pkgv1.Function{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							AnnotationKeyRuntimeDockerCleanup:        string(AnnotationValueRuntimeDockerCleanupOrphan),
							AnnotationKeyRuntimeNamedContainer:       "test-container-name-function",
							AnnotationKeyRuntimeDockerImage:          "test-image-from-annotation",
							AnnotationKeyRuntimeEnvironmentVariables: "SKIPPED_KEYvalue,KCL_DEFAULT_REGISTRY=registry.example.com",
						},
					},
					Spec: pkgv1.FunctionSpec{
						PackageSpec: pkgv1.PackageSpec{
							Package: "test-package",
						},
					},
				},
			},
			want: want{
				rd: &RuntimeDocker{
					Image:       "test-image-from-annotation",
					Cleanup:     AnnotationValueRuntimeDockerCleanupOrphan,
					Name:        "test-container-name-function",
					PullPolicy:  AnnotationValueRuntimeDockerPullPolicyIfNotPresent,
					Env:         []string{"KCL_DEFAULT_REGISTRY=registry.example.com"},
					BindAddress: "127.0.0.1",
				},
			},
		},
		"SuccessDefaults": {
			reason: "should return a RuntimeDocker with default fields set if no annotation are set",
			args: args{
				fn: pkgv1.Function{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{},
					},
					Spec: pkgv1.FunctionSpec{
						PackageSpec: pkgv1.PackageSpec{
							Package: "test-package",
						},
					},
				},
			},
			want: want{
				rd: &RuntimeDocker{
					Image:       "test-package",
					Cleanup:     AnnotationValueRuntimeDockerCleanupRemove,
					PullPolicy:  AnnotationValueRuntimeDockerPullPolicyIfNotPresent,
					BindAddress: "127.0.0.1",
				},
			},
		},
		"ErrorUnknownAnnotationValueCleanup": {
			reason: "should return an error if the supplied Function has an unknown cleanup annotation value",
			args: args{
				fn: pkgv1.Function{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							AnnotationKeyRuntimeDockerCleanup: "wrong",
						},
					},
					Spec: pkgv1.FunctionSpec{
						PackageSpec: pkgv1.PackageSpec{
							Package: "test-package",
						},
					},
				},
			},
			want: want{
				err: cmpopts.AnyError,
			},
		},
		"ErrorUnknownAnnotationPullPolicy": {
			reason: "should return an error if the supplied Function has an unknown pull policy annotation value",
			args: args{
				fn: pkgv1.Function{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							AnnotationKeyRuntimeDockerPullPolicy: "wrong",
						},
					},
					Spec: pkgv1.FunctionSpec{
						PackageSpec: pkgv1.PackageSpec{
							Package: "test-package",
						},
					},
				},
			},
			want: want{
				err: cmpopts.AnyError,
			},
		},
		"AnnotationsCleanupSetToStop": {
			reason: "should return a RuntimeDocker with all fields set according to the supplied Function's annotations",
			args: args{
				fn: pkgv1.Function{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							AnnotationKeyRuntimeDockerCleanup:        string(AnnotationValueRuntimeDockerCleanupStop),
							AnnotationKeyRuntimeEnvironmentVariables: "SKIPPED_KEYvalue",
						},
					},
					Spec: pkgv1.FunctionSpec{
						PackageSpec: pkgv1.PackageSpec{
							Package: "test-package",
						},
					},
				},
			},
			want: want{
				rd: &RuntimeDocker{
					Image:       "test-package",
					Cleanup:     AnnotationValueRuntimeDockerCleanupStop,
					PullPolicy:  AnnotationValueRuntimeDockerPullPolicyIfNotPresent,
					Env:         nil,
					BindAddress: "127.0.0.1",
				},
			},
		},
		"SuccessWithNetwork": {
			reason: "should return a RuntimeDocker with Network set when the network annotation is provided",
			args: args{
				fn: pkgv1.Function{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							AnnotationKeyRuntimeDockerNetwork: "my-custom-network",
						},
					},
					Spec: pkgv1.FunctionSpec{
						PackageSpec: pkgv1.PackageSpec{
							Package: "test-package",
						},
					},
				},
			},
			want: want{
				rd: &RuntimeDocker{
					Image:       "test-package",
					Cleanup:     AnnotationValueRuntimeDockerCleanupRemove,
					PullPolicy:  AnnotationValueRuntimeDockerPullPolicyIfNotPresent,
					BindAddress: "127.0.0.1",
					Network:     "my-custom-network",
				},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rd, err := GetRuntimeDocker(tc.args.fn, logging.NewNopLogger())
			if diff := cmp.Diff(tc.want.rd, rd, cmpopts.IgnoreUnexported(RuntimeDocker{}), cmpopts.IgnoreFields(RuntimeDocker{}, "Keychain")); diff != "" {
				t.Errorf("\n%s\nGetRuntimeDocker(...): -want, +got:\n%s", tc.reason, diff)
			}

			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nGetRuntimeDocker(...): -want error, +got error:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestRuntimeDockerStart(t *testing.T) {
	const (
		image         = "xpkg.crossplane.io/crossplane-contrib/function-dummy:v0.1.0"
		containerID   = "container-id"
		containerName = "fn-container"
		dockerNetwork = "render-net"
	)

	errBoom := errors.New("boom")

	type args struct {
		pullPolicy DockerPullPolicy
		cli        *mockContainerClient
	}
	type want struct {
		rctx RuntimeContext
		err  error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"CreatesAndStartsContainer": {
			reason: "Start should create, start, and inspect the Function's container through the injected client, without pulling an image that is present.",
			args: args{
				pullPolicy: AnnotationValueRuntimeDockerPullPolicyIfNotPresent,
				cli: &mockContainerClient{
					MockContainerCreate:  createContainerReturns(containerID),
					MockContainerStart:   startContainer(containerID),
					MockContainerInspect: inspectContainerOnNetwork(containerID, containerName, dockerNetwork),
				},
			},
			want: want{
				rctx: RuntimeContext{Target: net.JoinHostPort(containerName, strconv.Itoa(FunctionPort))},
			},
		},
		"PullsImage": {
			reason: "Start should pull the Function's image through the injected client when the pull policy is Always.",
			args: args{
				pullPolicy: AnnotationValueRuntimeDockerPullPolicyAlways,
				cli: &mockContainerClient{
					MockImagePull: func(ctx context.Context, ref string, options client.ImagePullOptions) (client.ImagePullResponse, error) {
						if diff := cmp.Diff(image, ref); diff != "" {
							return nil, errors.Errorf("ImagePull(...): -want ref, +got ref:\n%s", diff)
						}
						return imagePullDone(ctx, ref, options)
					},
					MockContainerCreate:  createContainerReturns(containerID),
					MockContainerStart:   startContainer(containerID),
					MockContainerInspect: inspectContainerOnNetwork(containerID, containerName, dockerNetwork),
				},
			},
			want: want{
				rctx: RuntimeContext{Target: net.JoinHostPort(containerName, strconv.Itoa(FunctionPort))},
			},
		},
		"PullError": {
			reason: "Start should return an error, without creating a container, when the injected client cannot pull the image.",
			args: args{
				pullPolicy: AnnotationValueRuntimeDockerPullPolicyAlways,
				cli: &mockContainerClient{
					MockImagePull: func(context.Context, string, client.ImagePullOptions) (client.ImagePullResponse, error) {
						return nil, errBoom
					},
				},
			},
			want: want{
				err: errBoom,
			},
		},
		"StartError": {
			reason: "Start should return an error when the injected client cannot start the container.",
			args: args{
				pullPolicy: AnnotationValueRuntimeDockerPullPolicyIfNotPresent,
				cli: &mockContainerClient{
					MockContainerCreate: createContainerReturns(containerID),
					MockContainerStart: func(context.Context, string, client.ContainerStartOptions) (client.ContainerStartResult, error) {
						return client.ContainerStartResult{}, errBoom
					},
				},
			},
			want: want{
				err: errBoom,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := &RuntimeDocker{
				Image:        image,
				Cleanup:      AnnotationValueRuntimeDockerCleanupRemove,
				PullPolicy:   tc.args.pullPolicy,
				Keychain:     authn.NewMultiKeychain(),
				Network:      dockerNetwork,
				log:          logging.NewNopLogger(),
				dockerClient: tc.args.cli,
			}

			rctx, err := r.Start(t.Context())

			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nStart(...): -want error, +got error:\n%s", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.rctx, rctx, cmpopts.IgnoreFields(RuntimeContext{}, "Stop")); diff != "" {
				t.Errorf("\n%s\nStart(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestRuntimeDockerStop(t *testing.T) {
	const (
		containerID   = "container-id"
		containerName = "fn-container"
		dockerNetwork = "render-net"
	)

	errStop := errors.New("stop boom")
	errRemove := errors.New("remove boom")

	// The Stop and Remove policies give the container containerStopGracePeriod
	// (3s) to exit, and Remove force removes it.
	grace := 3
	wantStopOptions := client.ContainerStopOptions{Timeout: &grace}
	wantRemoveOptions := client.ContainerRemoveOptions{Force: true}

	type args struct {
		cleanup DockerCleanup
		// stop and remove are the cleanup client's ContainerStop and
		// ContainerRemove. A nil mock must not be called. The test checks
		// the arguments each non-nil mock is called with.
		stop   func(ctx context.Context, containerID string, options client.ContainerStopOptions) (client.ContainerStopResult, error)
		remove func(ctx context.Context, containerID string, options client.ContainerRemoveOptions) (client.ContainerRemoveResult, error)
	}
	type want struct {
		// errs are the errors the returned error must wrap. When empty, Stop
		// must return nil.
		errs []error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"Stop": {
			reason: "The Stop cleanup policy should stop the container with the grace period and leave it in place.",
			args: args{
				cleanup: AnnotationValueRuntimeDockerCleanupStop,
				stop: func(context.Context, string, client.ContainerStopOptions) (client.ContainerStopResult, error) {
					return client.ContainerStopResult{}, nil
				},
			},
		},
		"StopError": {
			reason: "The Stop cleanup policy should return an error when the container cannot be stopped.",
			args: args{
				cleanup: AnnotationValueRuntimeDockerCleanupStop,
				stop: func(context.Context, string, client.ContainerStopOptions) (client.ContainerStopResult, error) {
					return client.ContainerStopResult{}, errStop
				},
			},
			want: want{
				errs: []error{errStop},
			},
		},
		"Remove": {
			reason: "The Remove cleanup policy should stop the container with the grace period, then force remove it.",
			args: args{
				cleanup: AnnotationValueRuntimeDockerCleanupRemove,
				stop: func(context.Context, string, client.ContainerStopOptions) (client.ContainerStopResult, error) {
					return client.ContainerStopResult{}, nil
				},
				remove: func(context.Context, string, client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
					return client.ContainerRemoveResult{}, nil
				},
			},
		},
		"RemoveStopError": {
			reason: "The Remove cleanup policy should still force remove the container when the graceful stop fails, and succeed if removal does.",
			args: args{
				cleanup: AnnotationValueRuntimeDockerCleanupRemove,
				stop: func(context.Context, string, client.ContainerStopOptions) (client.ContainerStopResult, error) {
					return client.ContainerStopResult{}, errStop
				},
				remove: func(context.Context, string, client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
					return client.ContainerRemoveResult{}, nil
				},
			},
		},
		"RemoveError": {
			reason: "The Remove cleanup policy should return an error when the container cannot be removed.",
			args: args{
				cleanup: AnnotationValueRuntimeDockerCleanupRemove,
				stop: func(context.Context, string, client.ContainerStopOptions) (client.ContainerStopResult, error) {
					return client.ContainerStopResult{}, nil
				},
				remove: func(context.Context, string, client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
					return client.ContainerRemoveResult{}, errRemove
				},
			},
			want: want{
				errs: []error{errRemove},
			},
		},
		"RemoveStopAndRemoveError": {
			reason: "The Remove cleanup policy should return both errors when the container can be neither stopped nor removed.",
			args: args{
				cleanup: AnnotationValueRuntimeDockerCleanupRemove,
				stop: func(context.Context, string, client.ContainerStopOptions) (client.ContainerStopResult, error) {
					return client.ContainerStopResult{}, errStop
				},
				remove: func(context.Context, string, client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
					return client.ContainerRemoveResult{}, errRemove
				},
			},
			want: want{
				errs: []error{errRemove, errStop},
			},
		},
		"Orphan": {
			reason: "The Orphan cleanup policy should leave the container running without calling Docker.",
			args: args{
				cleanup: AnnotationValueRuntimeDockerCleanupOrphan,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cli := &mockContainerClient{
				MockContainerCreate:  createContainerReturns(containerID),
				MockContainerStart:   startContainer(containerID),
				MockContainerInspect: inspectContainerOnNetwork(containerID, containerName, dockerNetwork),
			}
			// A stop error is discarded when removal succeeds, so report
			// unexpected arguments with t.Errorf rather than as an error.
			if stop := tc.args.stop; stop != nil {
				cli.MockContainerStop = func(ctx context.Context, id string, options client.ContainerStopOptions) (client.ContainerStopResult, error) {
					if diff := cmp.Diff(containerID, id); diff != "" {
						t.Errorf("\n%s\nContainerStop(...): -want container ID, +got container ID:\n%s", tc.reason, diff)
					}
					if diff := cmp.Diff(wantStopOptions, options); diff != "" {
						t.Errorf("\n%s\nContainerStop(...): -want options, +got options:\n%s", tc.reason, diff)
					}
					return stop(ctx, id, options)
				}
			}
			if remove := tc.args.remove; remove != nil {
				cli.MockContainerRemove = func(ctx context.Context, id string, options client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
					if diff := cmp.Diff(containerID, id); diff != "" {
						t.Errorf("\n%s\nContainerRemove(...): -want container ID, +got container ID:\n%s", tc.reason, diff)
					}
					if diff := cmp.Diff(wantRemoveOptions, options); diff != "" {
						t.Errorf("\n%s\nContainerRemove(...): -want options, +got options:\n%s", tc.reason, diff)
					}
					return remove(ctx, id, options)
				}
			}
			r := &RuntimeDocker{
				Image:        "xpkg.crossplane.io/crossplane-contrib/function-dummy:v0.1.0",
				Cleanup:      tc.args.cleanup,
				PullPolicy:   AnnotationValueRuntimeDockerPullPolicyIfNotPresent,
				Keychain:     authn.NewMultiKeychain(),
				Network:      dockerNetwork,
				log:          logging.NewNopLogger(),
				dockerClient: cli,
			}

			rctx, err := r.Start(t.Context())
			if err != nil {
				t.Fatalf("\n%s\nStart(...): unexpected error: %v", tc.reason, err)
			}

			err = rctx.Stop(t.Context())

			// The returned error may join several errors, so check that it
			// wraps each wanted error in turn.
			wantErrs := tc.want.errs
			if len(wantErrs) == 0 {
				wantErrs = []error{nil}
			}
			for _, want := range wantErrs {
				if diff := cmp.Diff(want, err, cmpopts.EquateErrors()); diff != "" {
					t.Errorf("\n%s\nStop(...): -want error, +got error:\n%s", tc.reason, diff)
				}
			}
		})
	}
}
