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
	"fmt"

	"github.com/moby/moby/client"
	"k8s.io/apimachinery/pkg/util/rand"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"

	"github.com/crossplane/cli/v2/internal/docker"
)

// SetDefaultCrossplaneDockerNetwork defaults the Crossplane render engine's
// Docker network to the first function runtime Docker network annotation.
// If network is already set, return with no changes.
func (f *EngineFlags) SetDefaultCrossplaneDockerNetwork(fns []pkgv1.Function) {
	if f.CrossplaneDockerNetwork != "" {
		return
	}

	for _, fn := range fns {
		if value, ok := fn.Annotations[AnnotationKeyRuntimeDockerNetwork]; ok && value != "" {
			f.CrossplaneDockerNetwork = value
			return
		}
	}
}

// networkClient is the subset of the Docker client the render engine uses to
// manage its temporary Docker network.
type networkClient interface {
	NetworkCreate(ctx context.Context, name string, options client.NetworkCreateOptions) (client.NetworkCreateResult, error)
	NetworkRemove(ctx context.Context, networkID string, options client.NetworkRemoveOptions) (client.NetworkRemoveResult, error)
}

var _ networkClient = (*client.Client)(nil)

// newNetworkClient returns a real Docker client built from the environment.
func newNetworkClient() (networkClient, error) {
	cli, err := docker.NewClient()
	if err != nil {
		return nil, errors.Wrap(err, "cannot create Docker client")
	}
	return cli, nil
}

// createRenderNetwork creates a temporary Docker bridge network for render.
// Function containers and the Crossplane render container join this network so
// they can reach each other. Returns the network ID and name.
func createRenderNetwork(ctx context.Context, cli networkClient) (string, string, error) {
	name := fmt.Sprintf("crossplane-render-%s", rand.String(8))

	resp, err := cli.NetworkCreate(ctx, name, client.NetworkCreateOptions{
		Driver: "bridge",
		Labels: managedLabels(),
	})
	if err != nil {
		return "", "", errors.Wrapf(err, "cannot create Docker network %q", name)
	}

	return resp.ID, name, nil
}

// removeRenderNetwork removes a temporary Docker network.
func removeRenderNetwork(ctx context.Context, cli networkClient, networkID string) error {
	_, err := cli.NetworkRemove(ctx, networkID, client.NetworkRemoveOptions{})
	return errors.Wrap(err, "cannot remove Docker network")
}
