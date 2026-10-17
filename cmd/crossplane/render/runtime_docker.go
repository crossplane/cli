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
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"

	"github.com/crossplane/cli/v2/internal/containerruntime"
)

// FunctionPort is the port that Composition Functions listen on inside their container.
const FunctionPort = 9443

// labelDomain prefixes the labels render applies to the Docker resources it
// creates.
const labelDomain = "render.crossplane.io"

// Annotations that can be used to configure the Docker runtime.
const (
	// AnnotationKeyRuntimeDockerCleanup configures how a Function's Docker
	// container should be cleaned up once rendering is done.
	AnnotationKeyRuntimeDockerCleanup = "render.crossplane.io/runtime-docker-cleanup"

	// AnnotationKeyRuntimeDockerImage overrides the Docker image that will be
	// used to run the Function. By default render assumes the Function package
	// (i.e. spec.package) can be used to run the Function.
	AnnotationKeyRuntimeDockerImage = "render.crossplane.io/runtime-docker-image"

	// AnnotationKeyRuntimeNamedContainer sets the Docker container name that will
	// be used for the container. it will also reuse the same container as long as
	// it is available and also try to restart if it is not running.
	AnnotationKeyRuntimeNamedContainer = "render.crossplane.io/runtime-docker-name"

	// AnnotationKeyRuntimeEnvironmentVariables sets the environment variables
	// that will be used for the container. This is helpful to control kpm registry
	// access to use a different registry.
	// It is a comma separated string of key=value pairs e.g. "key1=value1,key2=value2".
	AnnotationKeyRuntimeEnvironmentVariables = "render.crossplane.io/runtime-docker-env"

	// AnnotationKeyRuntimeDockerPublishAddress configures the host address that
	// Docker should publish (bind) the Function's container port to. Defaults to 127.0.0.1.
	// Use 0.0.0.0 to publish to all host interfaces for remote Docker access.
	AnnotationKeyRuntimeDockerPublishAddress = "render.crossplane.io/runtime-docker-publish-address"

	// AnnotationKeyRuntimeDockerTarget configures the address that the render
	// CLI should use to connect to the Function's Docker container.
	AnnotationKeyRuntimeDockerTarget = "render.crossplane.io/runtime-docker-target"

	// AnnotationKeyRuntimeDockerNetwork specifies which Docker network the
	// Function container should be connected to. When set, the container is
	// reached via its Docker hostname on port 9443 rather than via host port
	// bindings. This is useful when the render process itself runs inside a
	// Docker container (e.g. a GitHub Actions container job) and function
	// containers must be on the same network to be reachable.
	AnnotationKeyRuntimeDockerNetwork = "render.crossplane.io/runtime-docker-network"

	// AnnotationKeyRuntimeDockerSession sets the value of the
	// render.crossplane.io/session label on the Function's Docker container.
	// When unset, a per-process session ID is used. Render engines set this
	// annotation from their configured session ID.
	AnnotationKeyRuntimeDockerSession = "render.crossplane.io/runtime-docker-session"
)

// Labels that render applies to the Docker resources it creates, so that
// leftovers (e.g. after a crash or with an Orphan cleanup policy) can be
// identified and swept.
const (
	// LabelKeyManagedBy is applied to every Docker container and network
	// created by render. Its value is always LabelValueManagedByCrossplane.
	LabelKeyManagedBy = labelDomain + "/managed-by"

	// LabelValueManagedByCrossplane is the value of LabelKeyManagedBy.
	LabelValueManagedByCrossplane = containerruntime.LabelValueManagedBy

	// LabelKeySession is applied to every Docker container and network created
	// by render. Its value identifies the render session that created the
	// resource, so cleanup can be scoped to a single run. Callers may supply
	// the session ID (see EngineFlags.SessionID); otherwise a per-process ID is
	// generated.
	LabelKeySession = labelDomain + "/session"

	// LabelKeyCleanup is applied to Function containers and records the
	// effective DockerCleanup policy the container was created with.
	LabelKeyCleanup = labelDomain + "/cleanup"
)

// sessionID returns an identifier unique to this process. It is the session ID
// used for Docker resources when the caller doesn't supply one.
//
//nolint:gochecknoglobals // Must be stable for the life of the process.
var sessionID = containerruntime.SessionID

// managedLabels returns the labels applied to every Docker resource render
// creates. An empty session falls back to the per-process session ID.
func managedLabels(session string) map[string]string {
	return containerruntime.ManagedLabels(labelDomain, session)
}

// functionContainerLabels returns the labels applied to a Function container
// created with the supplied session and cleanup policy.
func functionContainerLabels(session string, cleanup DockerCleanup) map[string]string {
	return containerruntime.ContainerLabels(labelDomain, session, cleanup)
}

// DockerCleanup specifies what Docker should do with a Function container after
// it has been run.
type DockerCleanup = containerruntime.Cleanup

// Supported AnnotationKeyRuntimeDockerCleanup values.
const (
	// AnnotationValueRuntimeDockerCleanupStop stops the container once
	// rendering is done, waiting up to the grace period for it to exit on
	// SIGTERM before Docker kills it.
	AnnotationValueRuntimeDockerCleanupStop = containerruntime.CleanupStop

	// AnnotationValueRuntimeDockerCleanupRemove is the default. It stops the
	// container once rendering is done, waiting up to the grace period for it
	// to exit on SIGTERM, then force removes it. The container is removed
	// even if the graceful stop fails.
	AnnotationValueRuntimeDockerCleanupRemove = containerruntime.CleanupRemove

	// AnnotationValueRuntimeDockerCleanupOrphan leaves the container running
	// once rendering is done.
	AnnotationValueRuntimeDockerCleanupOrphan = containerruntime.CleanupOrphan

	AnnotationValueRuntimeDockerCleanupDefault = containerruntime.CleanupDefault
)

// AnnotationKeyRuntimeDockerPullPolicy can be added to a Function to control how its runtime
// image is pulled.
const AnnotationKeyRuntimeDockerPullPolicy = "render.crossplane.io/runtime-docker-pull-policy"

// DockerPullPolicy can be added to a Function to control how its runtime image
// is pulled by Docker.
type DockerPullPolicy = containerruntime.PullPolicy

// Supported pull policies.
const (
	// AnnotationValueRuntimeDockerPullPolicyAlways always pulls the image.
	AnnotationValueRuntimeDockerPullPolicyAlways = containerruntime.PullAlways

	// AnnotationValueRuntimeDockerPullPolicyNever never pulls the image.
	AnnotationValueRuntimeDockerPullPolicyNever = containerruntime.PullNever

	// AnnotationValueRuntimeDockerPullPolicyIfNotPresent pulls the image if
	// it's not present.
	AnnotationValueRuntimeDockerPullPolicyIfNotPresent = containerruntime.PullIfNotPresent

	AnnotationValueRuntimeDockerPullPolicyDefault = containerruntime.PullDefault
)

// RuntimeDocker uses a Docker daemon to run a Function.
type RuntimeDocker struct {
	// Image to run
	Image string

	// Container name
	Name string

	// Cleanup controls how the containers are handled after rendering.
	Cleanup DockerCleanup

	// PullPolicy controls how the runtime image is pulled.
	PullPolicy DockerPullPolicy

	// Keychain to use for pulling images from private registry.
	Keychain authn.Keychain

	// log is the logger for this runtime.
	log logging.Logger

	// Env is the list of environment variables to set for the container.
	Env []string

	// BindAddress is the address to bind the function container to.
	BindAddress string

	// Target is the host address to use when connecting to the function.
	// If empty, it defaults to the published HostIP from Docker inspect.
	// When published on 0.0.0.0, set this explicitly (e.g. the remote Docker host).
	Target string

	// Network is the Docker network to connect the Function container to.
	// When empty (the default), the container uses the default Docker network
	// and is reached via host port bindings. When set, the container joins
	// the specified network and is reached via its Docker hostname on port 9443.
	Network string

	// Session is the render session ID applied as the LabelKeySession label.
	// When empty, the per-process session ID is used.
	Session string

	// dockerClient manages the Function's container. Production callers leave
	// it nil and Start builds a real client from the environment. Tests
	// substitute a fake to exercise container lifecycle and cleanup handling
	// without a real Docker daemon.
	dockerClient containerruntime.Client
}

// GetDockerPullPolicy extracts PullPolicy configuration from the supplied
// Function.
func GetDockerPullPolicy(fn pkgv1.Function) (DockerPullPolicy, error) {
	switch p := DockerPullPolicy(fn.GetAnnotations()[AnnotationKeyRuntimeDockerPullPolicy]); p {
	case AnnotationValueRuntimeDockerPullPolicyAlways, AnnotationValueRuntimeDockerPullPolicyNever, AnnotationValueRuntimeDockerPullPolicyIfNotPresent:
		return p, nil
	case "":
		return AnnotationValueRuntimeDockerPullPolicyDefault, nil
	default:
		return "", errors.Errorf("unsupported %q annotation value %q (unknown pull policy)", AnnotationKeyRuntimeDockerPullPolicy, p)
	}
}

// GetDockerCleanup extracts Cleanup configuration from the supplied Function.
func GetDockerCleanup(fn pkgv1.Function) (DockerCleanup, error) {
	switch c := DockerCleanup(fn.GetAnnotations()[AnnotationKeyRuntimeDockerCleanup]); c {
	case AnnotationValueRuntimeDockerCleanupStop, AnnotationValueRuntimeDockerCleanupOrphan, AnnotationValueRuntimeDockerCleanupRemove:
		return c, nil
	case "":
		return AnnotationValueRuntimeDockerCleanupDefault, nil
	default:
		return "", errors.Errorf("unsupported %q annotation value %q (unknown cleanup policy)", AnnotationKeyRuntimeDockerCleanup, c)
	}
}

// GetRuntimeDocker extracts RuntimeDocker configuration from the supplied
// Function.
func GetRuntimeDocker(fn pkgv1.Function, log logging.Logger) (*RuntimeDocker, error) {
	cleanup, err := GetDockerCleanup(fn)
	if err != nil {
		return nil, errors.Wrapf(err, "cannot get cleanup policy for Function %q", fn.GetName())
	}
	// TODO(negz): Pull package in case it has a different controller image? I
	// hope in most cases Functions will use 'fat' packages, and it's possible
	// to manually override with an annotation so maybe not worth it.
	pullPolicy, err := GetDockerPullPolicy(fn)
	if err != nil {
		return nil, errors.Wrapf(err, "cannot get pull policy for Function %q", fn.GetName())
	}

	r := &RuntimeDocker{
		Image:       fn.Spec.Package,
		Name:        "",
		Cleanup:     cleanup,
		PullPolicy:  pullPolicy,
		Keychain:    authn.DefaultKeychain,
		log:         log,
		BindAddress: "127.0.0.1", // Default to localhost for security
	}

	if i := fn.GetAnnotations()[AnnotationKeyRuntimeDockerImage]; i != "" {
		r.Image = i
	}

	if i := fn.GetAnnotations()[AnnotationKeyRuntimeNamedContainer]; i != "" {
		r.Name = i
	}

	if i := fn.GetAnnotations()[AnnotationKeyRuntimeEnvironmentVariables]; i != "" {
		pairs := strings.SplitSeq(i, ",")
		for pair := range pairs {
			if !strings.Contains(pair, "=") {
				r.log.Debug("ignoring invalid environment variable", "pair", pair)
				continue
			}

			r.Env = append(r.Env, pair)
		}
	}

	if i := fn.GetAnnotations()[AnnotationKeyRuntimeDockerPublishAddress]; i != "" {
		r.BindAddress = i
	}

	if i := fn.GetAnnotations()[AnnotationKeyRuntimeDockerTarget]; i != "" {
		r.Target = i
	}

	if i := fn.GetAnnotations()[AnnotationKeyRuntimeDockerNetwork]; i != "" {
		r.Network = i
	}

	if i := fn.GetAnnotations()[AnnotationKeyRuntimeDockerSession]; i != "" {
		r.Session = i
	}

	return r, nil
}

var _ Runtime = &RuntimeDocker{}

// Start a Function as a Docker container.
func (r *RuntimeDocker) Start(ctx context.Context) (RuntimeContext, error) {
	c := &containerruntime.Container{
		Image:       r.Image,
		Name:        r.Name,
		Cmd:         []string{"--insecure"},
		Port:        FunctionPort,
		Env:         r.Env,
		Labels:      functionContainerLabels(r.Session, r.Cleanup),
		BindAddress: r.BindAddress,
		Target:      r.Target,
		Network:     r.Network,
		PullPolicy:  r.PullPolicy,
		Keychain:    r.Keychain,
		Log:         r.log,
		Client:      r.dockerClient,
	}

	running, err := c.Start(ctx)
	if err != nil {
		return RuntimeContext{}, err
	}

	return RuntimeContext{
		Target: running.Addr,
		Stop: func(ctx context.Context) error {
			return running.Stop(ctx, r.Cleanup)
		},
	}, nil
}
