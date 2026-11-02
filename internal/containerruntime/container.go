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

package containerruntime

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	"github.com/crossplane/cli/v2/internal/async"
)

// A Container runs one package image as a local Docker container serving a
// gRPC API on Port.
type Container struct {
	// Image to run.
	Image string

	// Name of the container. When set, an existing container with this name
	// is reused (and restarted if needed) instead of creating a new one.
	// Empty means Docker assigns a random name and nothing is reused.
	Name string

	// Cmd is the command the container runs, e.g. a server subcommand and
	// its flags.
	Cmd []string

	// Port is the TCP port the server binds inside the container.
	Port int

	// Env is the list of environment variables to set for the container.
	Env []string

	// Labels are applied at creation time. A pre-existing named container
	// that is reused is not relabeled, since Docker can't change labels on
	// an existing container.
	Labels map[string]string

	// BindAddress is the host address Docker publishes Port to. Use 0.0.0.0
	// to publish to all host interfaces for remote Docker access.
	BindAddress string

	// Target is the host address to use when connecting to the container.
	// If empty, it defaults to the published HostIP from Docker inspect.
	// When published on 0.0.0.0, set this explicitly (e.g. the remote
	// Docker host).
	Target string

	// Network is the Docker network to connect the container to. When empty
	// (the default), the container uses the default Docker network and is
	// reached via host port bindings. When set, the container joins the
	// specified network and is reached via its Docker hostname on Port.
	Network string

	// PullPolicy controls how the image is pulled. Empty means PullDefault.
	PullPolicy PullPolicy

	// Keychain to use for pulling images from private registries. Nil means
	// the default keychain.
	Keychain authn.Keychain

	// Log is the logger for this container's lifecycle. Nil means no logs.
	Log logging.Logger

	// Client manages the container. Production callers leave it nil and
	// Start builds a real client from the environment. Tests substitute a
	// fake to exercise container lifecycle and cleanup handling without a
	// real Docker daemon.
	Client Client

	// Events receives progress events for the container's long-running steps,
	// today only the image pull, which happens exactly when the daemon does
	// not already have the image. Nil means no events are sent.
	Events async.EventChannel
}

// Client is the subset of the Docker client a Container uses to pull its
// image and manage its container.
type Client interface {
	PullClient
	CleanupClient
	ContainerInspect(ctx context.Context, containerID string, options client.ContainerInspectOptions) (client.ContainerInspectResult, error)
	ContainerCreate(ctx context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error)
	ContainerStart(ctx context.Context, containerID string, options client.ContainerStartOptions) (client.ContainerStartResult, error)
}

// CleanupClient is the subset of the Docker client Running.Stop uses to
// clean up a container.
type CleanupClient interface {
	ContainerStop(ctx context.Context, containerID string, options client.ContainerStopOptions) (client.ContainerStopResult, error)
	ContainerRemove(ctx context.Context, containerID string, options client.ContainerRemoveOptions) (client.ContainerRemoveResult, error)
}

// PullClient is the subset of the Docker client PullImage uses.
type PullClient interface {
	ImagePull(ctx context.Context, ref string, options client.ImagePullOptions) (client.ImagePullResponse, error)
}

var _ Client = (*client.Client)(nil)

// Running is a handle to a started container.
type Running struct {
	// Addr is the host:port address the container's server is reachable on.
	Addr string

	// ID of the container.
	ID string

	// Reused reports whether Start found an existing container by name
	// rather than creating a new one.
	Reused bool

	cli CleanupClient
}

// Start ensures the container is running and returns a handle carrying the
// address to reach it on and its cleanup lifecycle.
func (c *Container) Start(ctx context.Context) (*Running, error) {
	cli := c.Client
	if cli == nil {
		mc, err := client.New(client.FromEnv)
		if err != nil {
			return nil, errors.Wrap(err, "cannot create Docker client using environment variables")
		}
		cli = mc
	}
	if c.Log == nil {
		c.Log = logging.NewNopLogger()
	}
	if c.Keychain == nil {
		c.Keychain = authn.DefaultKeychain
	}
	if c.PullPolicy == "" {
		c.PullPolicy = PullDefault
	}

	// Try to find an existing container with the supplied container name.
	containerID, err := c.find(ctx, cli)
	if err != nil {
		return nil, err
	}
	reused := containerID != ""

	// Create new container if not found.
	if containerID == "" {
		containerID, err = c.create(ctx, cli)
		if err != nil {
			return nil, err
		}
	}

	addr, err := c.start(ctx, cli, containerID)
	if err != nil {
		return nil, err
	}

	return &Running{Addr: addr, ID: containerID, Reused: reused, cli: cli}, nil
}

// Stop finishes the container according to the cleanup policy: Orphan leaves
// it running, Stop stops it with the grace period, and Remove stops it and
// then force removes it even if the graceful stop fails.
func (r *Running) Stop(ctx context.Context, cleanup Cleanup) error {
	grace := int(StopGracePeriod / time.Second)
	stopOpts := client.ContainerStopOptions{Timeout: &grace}

	switch cleanup {
	case CleanupOrphan:
		return nil
	case CleanupStop:
		if err := stopContainer(ctx, r.cli, r.ID, stopOpts); err != nil {
			return errors.Wrap(err, "cannot stop Docker container")
		}
	case CleanupRemove:
		// Give the container a chance to exit gracefully, then force remove
		// it whether or not the stop succeeded, so a container that's slow
		// to exit on SIGTERM can't cause removal to be skipped. A stop
		// failure only matters if removal fails too.
		var stopErr error
		if err := stopContainer(ctx, r.cli, r.ID, stopOpts); err != nil {
			stopErr = errors.Wrap(err, "cannot stop Docker container")
		}
		if err := removeContainer(ctx, r.cli, r.ID); err != nil {
			return errors.Join(errors.Wrap(err, "cannot remove Docker container"), stopErr)
		}
	}

	return nil
}

func (c *Container) find(ctx context.Context, cli Client) (string, error) {
	if c.Name == "" {
		return "", nil
	}

	inspect, err := cli.ContainerInspect(ctx, c.Name, client.ContainerInspectOptions{})
	if err != nil {
		if errdefs.IsNotFound(err) {
			return "", nil // Container doesn't exist, but that's not an error
		}
		return "", errors.Wrapf(err, "cannot inspect Docker container %q", c.Name)
	}

	return inspect.Container.ID, nil
}

func (c *Container) create(ctx context.Context, cli Client) (string, error) {
	c.Log.Debug("Starting Docker container runtime setup", "image", c.Image)

	// Let Docker automatically allocate an available port on the bind address.
	// This avoids race conditions and works reliably with Docker daemons.
	port := network.MustParsePort(fmt.Sprintf("%d/tcp", c.Port))

	cfg := &container.Config{
		Image:        c.Image,
		Cmd:          c.Cmd,
		ExposedPorts: network.PortSet{port: struct{}{}},
		Env:          c.Env,
		Labels:       c.Labels,
	}
	hcfg := &container.HostConfig{}
	var ncfg *network.NetworkingConfig

	if c.Network != "" {
		// When a Docker network is specified, the container joins that
		// network and is reached via its Docker hostname on the container
		// port. No host port bindings are needed.
		ncfg = &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				c.Network: {},
			},
		}
		c.Log.Debug("Connecting container to Docker network", "network", c.Network)
	} else {
		// PortBinding.HostIP is a netip.Addr in the Moby API types, where it
		// was a string; reject a malformed bind address here rather than
		// letting it reach the daemon as a zero value.
		bindAddr, err := netip.ParseAddr(c.BindAddress)
		if err != nil {
			return "", errors.Wrapf(err, "invalid bind address %q", c.BindAddress)
		}
		hcfg.PortBindings = network.PortMap{
			port: []network.PortBinding{{
				HostIP:   bindAddr,
				HostPort: "0", // "0" => engine allocates an ephemeral port
			}},
		}
	}

	options, err := c.getPullOptions()
	if err != nil {
		// We can continue to pull an image if we don't have the PullOptions with RegistryAuth
		// as long as the image is from a public registry. Therefore, we log the error message and continue.
		c.Log.Info("Cannot get pull options", "image", c.Image, "err", err)
	}

	if c.PullPolicy == PullAlways {
		c.Log.Debug("Pulling image with pullPolicy: Always", "image", c.Image)

		if err := c.pullImage(ctx, cli, options); err != nil {
			return "", err
		}
	}

	c.Log.Debug("Creating Docker container", "image", c.Image, "name", c.Name, "network", c.Network)

	rsp, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:           cfg,
		HostConfig:       hcfg,
		NetworkingConfig: ncfg,
		Name:             c.Name,
	})
	if err != nil {
		// TODO: If Docker ever exposes a structured way to distinguish
		// network-not-found from image-not-found (e.g. via errdefs or a typed
		// error), handle the network case here to avoid an unnecessary image
		// pull attempt. For now, both surface as errdefs.IsNotFound and
		// string-matching on the daemon message is too fragile.
		if !errdefs.IsNotFound(err) {
			// Non-image-not-found error: could be network misconfiguration,
			// daemon permissions, etc.
			if c.Network != "" {
				return "", errors.Wrapf(err, "cannot create Docker container for image %q on network %q; verify the network exists and is accessible by the Docker daemon", c.Image, c.Network)
			}
			return "", errors.Wrapf(err, "cannot create Docker container for image %q", c.Image)
		}
		if c.PullPolicy == PullNever {
			// Image not found and we're not allowed to pull it.
			return "", errors.Wrapf(err, "cannot create Docker container: image %q not found and pull policy is %q", c.Image, c.PullPolicy)
		}

		// The image was not found, but we're allowed to pull it.
		c.Log.Debug("Image not found, pulling", "image", c.Image)

		if err := c.pullImage(ctx, cli, options); err != nil {
			return "", err
		}

		rsp, err = cli.ContainerCreate(ctx, client.ContainerCreateOptions{
			Config:           cfg,
			HostConfig:       hcfg,
			NetworkingConfig: ncfg,
			Name:             c.Name,
		})
		if err != nil {
			if c.Network != "" {
				return "", errors.Wrapf(err, "cannot create Docker container for image %q on network %q; verify the network exists and is accessible by the Docker daemon", c.Image, c.Network)
			}
			return "", errors.Wrapf(err, "cannot create Docker container for image %q", c.Image)
		}
	}

	return rsp.ID, nil
}

// pullImage pulls the container's image, reporting the pull on the Events
// channel so callers can show it as its own step: it is the dominant cost of
// a cold start, and it only happens at all when the daemon does not have the
// image yet.
func (c *Container) pullImage(ctx context.Context, cli Client, options client.ImagePullOptions) error {
	desc := "Pulling image " + c.Image
	c.Events.SendEvent(desc, async.EventStatusStarted)
	if err := PullImage(ctx, cli, c.Image, options); err != nil {
		c.Events.SendEvent(desc, async.EventStatusFailure)
		return errors.Wrapf(err, "cannot pull Docker image %q", c.Image)
	}
	c.Events.SendEvent(desc, async.EventStatusSuccess)
	return nil
}

// start ensures the container is running and returns its address.
func (c *Container) start(ctx context.Context, cli Client, containerID string) (string, error) {
	// Start the container (idempotent - safe to call on running containers)
	if _, err := cli.ContainerStart(ctx, containerID, client.ContainerStartOptions{}); err != nil {
		return "", errors.Wrap(err, "cannot start Docker container")
	}

	// When using a Docker network, containers are resolvable by name within
	// the network. Inspect the container to validate it is attached to the
	// specified network and to get its DNS-resolvable name (Docker's embedded
	// DNS resolves container names and hostnames, but not container IDs).
	if c.Network != "" {
		inspect, err := cli.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
		if err != nil {
			return "", errors.Wrap(err, "cannot inspect Docker container")
		}
		if _, ok := inspect.Container.NetworkSettings.Networks[c.Network]; !ok {
			return "", errors.Errorf("container %q is not connected to Docker network %q; verify the network exists and the container is attached to it", strings.TrimPrefix(inspect.Container.Name, "/"), c.Network)
		}
		hostname := strings.TrimPrefix(inspect.Container.Name, "/")
		if hostname == "" {
			return "", errors.Errorf("cannot determine hostname for container %q on network %q", containerID, c.Network)
		}
		addr := net.JoinHostPort(hostname, fmt.Sprintf("%d", c.Port))
		c.Log.Debug("Container reachable on network", "network", c.Network, "address", addr)
		return addr, nil
	}

	// Inspect the container to get the actual allocated host port.
	inspect, err := cli.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return "", errors.Wrap(err, "cannot inspect Docker container")
	}

	// Look up the specific container port instead of taking the first one
	p := network.MustParsePort(fmt.Sprintf("%d/tcp", c.Port))
	if len(inspect.Container.NetworkSettings.Ports[p]) == 0 {
		return "", errors.Errorf("container %q has no published binding for port %s", c.Name, p.Port())
	}

	binding := inspect.Container.NetworkSettings.Ports[p][0]
	host := c.Target
	if host == "" && binding.HostIP.IsValid() {
		host = binding.HostIP.String()
	}
	if host == "" {
		return "", errors.Errorf("container %q has port binding for %s but no host address", c.Name, p.Port())
	}

	return net.JoinHostPort(host, binding.HostPort), nil
}

func (c *Container) getPullOptions() (client.ImagePullOptions, error) {
	// Resolve auth token by looking into keychain
	ref, err := name.ParseReference(c.Image)
	if err != nil {
		return client.ImagePullOptions{}, errors.Wrapf(err, "Image is not a valid reference %s", c.Image)
	}

	auth, err := c.Keychain.Resolve(ref.Context().Registry)
	if err != nil {
		return client.ImagePullOptions{}, errors.Wrapf(err, "Cannot resolve auth for %s", ref.Context().RegistryStr())
	}

	authConfig, err := auth.Authorization()
	if err != nil {
		return client.ImagePullOptions{}, errors.Wrapf(err, "Cannot get auth config for %s", ref.Context().RegistryStr())
	}

	token, err := authConfig.MarshalJSON()
	if err != nil {
		return client.ImagePullOptions{}, errors.Wrapf(err, "Cannot marshal auth config for %s", ref.Context().RegistryStr())
	}

	return client.ImagePullOptions{
		RegistryAuth: base64.StdEncoding.EncodeToString(token),
	}, nil
}

// stopContainer stops a container, bounding the request by StopTimeout.
func stopContainer(ctx context.Context, cli CleanupClient, containerID string, opts client.ContainerStopOptions) error {
	ctx, cancel := context.WithTimeout(ctx, StopTimeout)
	defer cancel()

	_, err := cli.ContainerStop(ctx, containerID, opts)
	return err
}

// removeContainer force removes a container, bounding the request by
// RemoveTimeout.
func removeContainer(ctx context.Context, cli CleanupClient, containerID string) error {
	ctx, cancel := context.WithTimeout(ctx, RemoveTimeout)
	defer cancel()

	_, err := cli.ContainerRemove(ctx, containerID, client.ContainerRemoveOptions{Force: true})
	return err
}

// PullImage pulls the supplied image using the supplied client. It blocks until
// the image has either finished pulling or hit an error.
func PullImage(ctx context.Context, p PullClient, image string, options client.ImagePullOptions) error {
	out, err := p.ImagePull(ctx, image, options)
	if err != nil {
		return err
	}
	defer out.Close() //nolint:errcheck // TODO(negz): Can this error?

	// Each line read from out is a JSON object containing the status of the
	// pull - similar to the progress bars you'd see if running docker pull. It
	// seems that consuming all of this output is the best way to block until
	// the image is actually pulled before we try to run it.
	_, err = io.Copy(io.Discard, out)

	return err
}
