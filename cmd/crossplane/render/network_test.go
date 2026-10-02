package render

import (
	"context"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/moby/moby/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"
)

func TestSetDefaultCrossplaneDockerNetwork(t *testing.T) {
	type args struct {
		flags     EngineFlags
		functions []pkgv1.Function
	}
	type want struct {
		flags EngineFlags
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"ExplicitNetworkIsPreserved": {
			reason: "An explicit --crossplane-docker-network value should not be overwritten by function annotations.",
			args: args{
				flags: EngineFlags{CrossplaneDockerNetwork: "explicit-network"},
				functions: []pkgv1.Function{
					functionWithAnnotations(map[string]string{AnnotationKeyRuntimeDockerNetwork: "function-network"}),
				},
			},
			want: want{
				flags: EngineFlags{CrossplaneDockerNetwork: "explicit-network"},
			},
		},
		"FirstFunctionAnnotationIsUsed": {
			reason: "When no explicit network is set, the render engine should join the first function runtime Docker network.",
			args: args{
				functions: []pkgv1.Function{
					functionWithAnnotations(map[string]string{"example.org/other": "ignored"}),
					functionWithAnnotations(map[string]string{AnnotationKeyRuntimeDockerNetwork: "first-network"}),
					functionWithAnnotations(map[string]string{AnnotationKeyRuntimeDockerNetwork: "second-network"}),
				},
			},
			want: want{
				flags: EngineFlags{CrossplaneDockerNetwork: "first-network"},
			},
		},
		"NoNetwork": {
			reason: "The flags should remain unchanged when no function has a runtime Docker network annotation.",
			args: args{
				functions: []pkgv1.Function{
					functionWithAnnotations(nil),
					functionWithAnnotations(map[string]string{"example.org/other": "ignored"}),
				},
			},
			want: want{},
		},
		"NoFunctionsPreservesDefaultBehavior": {
			reason: "No functions should leave CrossplaneDockerNetwork unset so engine setup can use its default temporary network behavior.",
			want:   want{},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Log(tc.reason)

			tc.args.flags.SetDefaultCrossplaneDockerNetwork(tc.args.functions)
			if diff := cmp.Diff(tc.want.flags, tc.args.flags); diff != "" {
				t.Errorf("SetDefaultCrossplaneDockerNetwork(...), -want, +got:\n%s", diff)
			}
		})
	}
}

type mockNetworkClient struct {
	MockNetworkCreate func(ctx context.Context, name string, options client.NetworkCreateOptions) (client.NetworkCreateResult, error)
	MockNetworkRemove func(ctx context.Context, networkID string, options client.NetworkRemoveOptions) (client.NetworkRemoveResult, error)
}

func (m *mockNetworkClient) NetworkCreate(ctx context.Context, name string, options client.NetworkCreateOptions) (client.NetworkCreateResult, error) {
	return m.MockNetworkCreate(ctx, name, options)
}

func (m *mockNetworkClient) NetworkRemove(ctx context.Context, networkID string, options client.NetworkRemoveOptions) (client.NetworkRemoveResult, error) {
	return m.MockNetworkRemove(ctx, networkID, options)
}

var _ networkClient = &mockNetworkClient{}

// renderNetworkPrefix is the prefix of the temporary network name
// createRenderNetwork generates.
const renderNetworkPrefix = "crossplane-render-"

// createRenderNetworkReturns returns a MockNetworkCreate that returns the
// supplied network ID and error. It returns an error instead when it is not
// asked to create a render bridge network.
func createRenderNetworkReturns(id string, err error) func(context.Context, string, client.NetworkCreateOptions) (client.NetworkCreateResult, error) {
	return func(_ context.Context, name string, options client.NetworkCreateOptions) (client.NetworkCreateResult, error) {
		if !strings.HasPrefix(name, renderNetworkPrefix) {
			return client.NetworkCreateResult{}, errors.Errorf("NetworkCreate(...): name %q does not have prefix %q", name, renderNetworkPrefix)
		}
		if diff := cmp.Diff(client.NetworkCreateOptions{Driver: "bridge"}, options); diff != "" {
			return client.NetworkCreateResult{}, errors.Errorf("NetworkCreate(...): -want options, +got options:\n%s", diff)
		}
		return client.NetworkCreateResult{ID: id}, err
	}
}

func TestCreateRenderNetwork(t *testing.T) {
	errBoom := errors.New("boom")

	type args struct {
		cli networkClient
	}
	type want struct {
		id string
		// namePrefix is a prefix the returned network name must have. The
		// rest of the name is random.
		namePrefix string
		err        error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"CreatesBridgeNetwork": {
			reason: "createRenderNetwork should create a uniquely named bridge network through the supplied client and return its ID and name.",
			args: args{
				cli: &mockNetworkClient{MockNetworkCreate: createRenderNetworkReturns("network-id", nil)},
			},
			want: want{
				id:         "network-id",
				namePrefix: renderNetworkPrefix,
			},
		},
		"NetworkCreateError": {
			reason: "createRenderNetwork should return an error when the client cannot create the network.",
			args: args{
				cli: &mockNetworkClient{MockNetworkCreate: createRenderNetworkReturns("", errBoom)},
			},
			want: want{
				err: errBoom,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			id, networkName, err := createRenderNetwork(t.Context(), tc.args.cli)

			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ncreateRenderNetwork(...): -want error, +got error:\n%s", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.id, id); diff != "" {
				t.Errorf("\n%s\ncreateRenderNetwork(...): -want ID, +got ID:\n%s", tc.reason, diff)
			}
			if !strings.HasPrefix(networkName, tc.want.namePrefix) {
				t.Errorf("\n%s\ncreateRenderNetwork(...): name %q does not have prefix %q", tc.reason, networkName, tc.want.namePrefix)
			}
		})
	}
}

func TestRemoveRenderNetwork(t *testing.T) {
	errBoom := errors.New("boom")

	// removeNetworkReturns returns a MockNetworkRemove that returns the
	// supplied error, or an error of its own when asked to remove any network
	// other than network-id.
	removeNetworkReturns := func(err error) func(context.Context, string, client.NetworkRemoveOptions) (client.NetworkRemoveResult, error) {
		return func(_ context.Context, networkID string, _ client.NetworkRemoveOptions) (client.NetworkRemoveResult, error) {
			if diff := cmp.Diff("network-id", networkID); diff != "" {
				return client.NetworkRemoveResult{}, errors.Errorf("NetworkRemove(...): -want network ID, +got network ID:\n%s", diff)
			}
			return client.NetworkRemoveResult{}, err
		}
	}

	type args struct {
		cli       networkClient
		networkID string
	}
	type want struct {
		err error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"RemovesNetwork": {
			reason: "removeRenderNetwork should remove the network with the supplied ID through the supplied client.",
			args: args{
				cli:       &mockNetworkClient{MockNetworkRemove: removeNetworkReturns(nil)},
				networkID: "network-id",
			},
		},
		"NetworkRemoveError": {
			reason: "removeRenderNetwork should return an error when the client cannot remove the network.",
			args: args{
				cli:       &mockNetworkClient{MockNetworkRemove: removeNetworkReturns(errBoom)},
				networkID: "network-id",
			},
			want: want{
				err: errBoom,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := removeRenderNetwork(t.Context(), tc.args.cli, tc.args.networkID)

			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nremoveRenderNetwork(...): -want error, +got error:\n%s", tc.reason, diff)
			}
		})
	}
}
