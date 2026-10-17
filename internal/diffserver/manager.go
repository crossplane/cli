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

// Package diffserver runs provider packages as local diff server containers
// and routes resources to them by group and kind, the way render runs
// function packages. Whether a package can serve diffs is declared in its
// metadata (the DiffServer capability), and which resources it serves comes
// from the CRDs it ships, so everything is known before a container starts.
package diffserver

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"time"

	diffv1alpha1 "github.com/crossplane/upjet/v2/proto/diff/v1alpha1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	runtimexpkg "github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"

	pkgmetav1 "github.com/crossplane/crossplane/apis/v2/pkg/meta/v1"

	"github.com/crossplane/cli/v2/internal/containerruntime"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"
)

const (
	// CapabilityDiffServer is the package capability a provider declares when
	// its image serves the diff protocol via `internal diff-server`.
	CapabilityDiffServer = "DiffServer"

	// containerPort is the port the diff server binds inside the container,
	// upjet's default for `internal diff-server --address`.
	containerPort = 9099

	// containerPrefix names diff server containers. The full name carries the
	// package digest, so a changed image never matches a stale container and
	// orphaned leftovers are easy to find.
	containerPrefix = "crossplane-diff-server-"

	readyPollInterval   = 500 * time.Millisecond
	defaultReadyTimeout = 60 * time.Second
)

// Cleanup is how a diff server container is cleaned up when the manager
// stops, following render's per-function cleanup policies.
type Cleanup = containerruntime.Cleanup

const (
	// CleanupRemove is the default: stop and remove the container.
	CleanupRemove = containerruntime.CleanupRemove

	// CleanupOrphan leaves the container running, so a later run finds it by
	// its deterministic name and reuses it without paying startup. Render's
	// function runtimes support a third policy, Stop; a stopped diff server
	// serves nothing and restarting costs nearly as much as starting, so
	// simulate omits it.
	CleanupOrphan = containerruntime.CleanupOrphan
)

// labelDomain prefixes the labels simulate applies to the Docker containers
// it creates.
const labelDomain = "simulate.crossplane.io"

// Labels applied to the Docker containers simulate creates, mirroring
// render's, so leftovers (a crash, or an Orphan cleanup policy) can be
// identified and swept.
const (
	// LabelKeyManagedBy is applied to every diff server container. Its value
	// is always LabelValueManagedBy.
	LabelKeyManagedBy = labelDomain + "/managed-by"

	// LabelValueManagedBy is the value of LabelKeyManagedBy.
	LabelValueManagedBy = containerruntime.LabelValueManagedBy

	// LabelKeySession identifies the run that created a container. Orphaned
	// containers outlive their session by design; the label records which run
	// created them.
	LabelKeySession = labelDomain + "/session"

	// LabelKeyCleanup records the effective cleanup policy a container was
	// created with.
	LabelKeyCleanup = labelDomain + "/cleanup"
)

// Server is a running diff server for one provider package.
type Server struct {
	Image       string
	Address     string
	ContainerID string
	GroupKinds  []schema.GroupKind

	conn    *grpc.ClientConn
	client  diffv1alpha1.PlanServiceClient
	running *containerruntime.Running
	cleanup Cleanup
}

// Skipped records a provider image that does not run a diff server this run,
// and why. Resources it would have served fall back to the approximate
// client-side diff.
type Skipped struct {
	Image  string
	Reason string
}

// Input describes one provider package to run as a diff server. The zero
// values mean the manager's defaults apply; set them to override Docker
// behavior for this provider only, the way render's per-function runtime
// annotations do.
type Input struct {
	Image          string
	PublishAddress netip.Addr
	Target         string
	Cleanup        Cleanup
}

// Manager starts diff server containers for provider packages and routes
// resources to them.
type Manager struct {
	pkgs         runtimexpkg.Client
	cli          containerruntime.Client
	log          logging.Logger
	readyTimeout time.Duration
	publishAddr  netip.Addr
	session      string

	mu      sync.Mutex
	routes  map[schema.GroupKind]*Server
	servers []*Server
}

// Option configures a Manager.
type Option func(*Manager)

// WithDockerClient replaces the Docker client the manager runs containers
// with, for testing. Nil (the default) means a real client built from the
// environment.
func WithDockerClient(cli containerruntime.Client) Option {
	return func(m *Manager) { m.cli = cli }
}

// WithReadyTimeout sets how long to wait for a started diff server to answer.
func WithReadyTimeout(d time.Duration) Option {
	return func(m *Manager) { m.readyTimeout = d }
}

// WithSessionID sets the session label value for the containers the manager
// creates, so callers orchestrating a larger run can scope cleanup to it.
// Empty means a random per-process ID, matching render's session labels.
func WithSessionID(session string) Option {
	return func(m *Manager) { m.session = session }
}

// NewManager returns a Manager that fetches provider packages with pkgs and
// runs their images as local diff server containers.
func NewManager(pkgs runtimexpkg.Client, log logging.Logger, opts ...Option) *Manager {
	m := &Manager{
		pkgs:         pkgs,
		log:          log,
		readyTimeout: defaultReadyTimeout,
		publishAddr:  netip.MustParseAddr("127.0.0.1"),
		routes:       map[schema.GroupKind]*Server{},
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Start inspects each provider image and runs a diff server container for
// every package that declares the DiffServer capability, in parallel and best
// effort: an image that cannot serve (no capability, fetch or startup
// failure) is returned as skipped rather than failing the run, since its
// resources degrade to the approximate client-side diff.
func (m *Manager) Start(ctx context.Context, inputs []Input) ([]Skipped, error) {
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		skipped []Skipped
	)
	for _, in := range inputs {
		wg.Add(1)
		go func(in Input) {
			defer wg.Done()
			s, skip := m.startOne(ctx, in)
			mu.Lock()
			defer mu.Unlock()
			if skip != nil {
				skipped = append(skipped, *skip)
				return
			}
			m.register(s)
		}(in)
	}
	wg.Wait()
	return skipped, ctx.Err()
}

// Images wraps plain image references into Inputs with no per-provider
// overrides.
func Images(images []string) []Input {
	ins := make([]Input, 0, len(images))
	for _, img := range images {
		ins = append(ins, Input{Image: img})
	}
	return ins
}

func (m *Manager) register(s *Server) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.servers = append(m.servers, s)
	for _, gk := range s.GroupKinds {
		if other, ok := m.routes[gk]; ok {
			m.log.Info("Multiple diff servers serve the same kind; keeping the first",
				"groupKind", gk.String(), "kept", other.Image, "ignored", s.Image)
			continue
		}
		m.routes[gk] = s
	}
}

func (m *Manager) startOne(ctx context.Context, in Input) (*Server, *Skipped) {
	image := in.Image

	pkg, err := m.pkgs.Get(ctx, image)
	if err != nil {
		return nil, &Skipped{Image: image, Reason: fmt.Sprintf("cannot fetch package: %s", err)}
	}

	meta := pkg.GetMeta()
	if meta == nil || !pkgmetav1.CapabilitiesContainFuzzyMatch(meta.GetCapabilities(), CapabilityDiffServer) {
		return nil, &Skipped{Image: image, Reason: "package does not declare the DiffServer capability"}
	}

	gks := clixpkg.GroupKinds(pkg.Package)
	if len(gks) == 0 {
		return nil, &Skipped{Image: image, Reason: "package contains no CRDs to route"}
	}

	// Run the container from the reference the package client resolved, so
	// ImageConfig path rewriting applies to the container image exactly as it
	// applied to the package fetch.
	runImage := pkg.ResolvedRef()
	if pkg.ResolvedSource == "" {
		runImage = image
	}

	// The manager's defaults are overridable per provider: Input carries the
	// publish and target addresses and the cleanup policy from the provider
	// manifest's runtime annotations, the way render's per-function
	// annotations work.
	publishAddr := m.publishAddr
	if in.PublishAddress.IsValid() {
		publishAddr = in.PublishAddress
	}
	cleanup := in.Cleanup
	if cleanup == "" {
		cleanup = CleanupRemove
	}

	ctr := &containerruntime.Container{
		Image: runImage,
		Name:  containerName(pkg.Digest),
		// --debug precedes the subcommand: it is the provider's global flag.
		Cmd:         []string{"--debug", "internal", "diff-server", "--address", fmt.Sprintf(":%d", containerPort)},
		Port:        containerPort,
		Labels:      containerruntime.ContainerLabels(labelDomain, m.session, cleanup),
		BindAddress: publishAddr.String(),
		Target:      in.Target,
		Log:         m.log,
		Client:      m.cli,
	}

	running, err := ctr.Start(ctx)
	if err != nil {
		return nil, &Skipped{Image: image, Reason: fmt.Sprintf("cannot start diff server container: %s", err)}
	}

	server, err := m.connect(ctx, running, image)
	if err != nil {
		// An orphaned container is left behind even on failure, for debugging.
		if !running.Reused && cleanup != CleanupOrphan {
			if serr := running.Stop(ctx, CleanupRemove); serr != nil {
				m.log.Info("Cannot clean up failed diff server container", "container", running.ID, "err", serr)
			}
		}
		return nil, &Skipped{Image: image, Reason: err.Error()}
	}

	server.GroupKinds = gks
	server.cleanup = cleanup
	m.log.Debug("Diff server ready", "image", image, "address", server.Address, "reused", running.Reused, "cleanup", cleanup)
	return server, nil
}

func (m *Manager) connect(ctx context.Context, running *containerruntime.Running, image string) (*Server, error) {
	conn, err := grpc.NewClient(running.Addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, errors.Wrap(err, "cannot create gRPC client")
	}

	client := diffv1alpha1.NewPlanServiceClient(conn)
	if err := waitReady(ctx, client, m.readyTimeout); err != nil {
		_ = conn.Close()
		return nil, err
	}

	return &Server{
		Image:       image,
		Address:     running.Addr,
		ContainerID: running.ID,
		conn:        conn,
		client:      client,
		running:     running,
	}, nil
}

// waitReady polls the server with an empty Plan request until it answers.
// Docker publishes the host port before the server inside the container binds
// its own, so a successful TCP connect proves nothing; any gRPC status from
// the server itself (an empty request draws INVALID_ARGUMENT) means it is
// serving.
func waitReady(ctx context.Context, c diffv1alpha1.PlanServiceClient, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var last error
	for {
		_, err := c.Plan(ctx, &diffv1alpha1.PlanRequest{})
		if err == nil {
			return nil
		}
		if s, ok := status.FromError(err); ok && s.Code() != codes.Unavailable && s.Code() != codes.DeadlineExceeded && s.Code() != codes.Canceled {
			return nil
		}
		last = err

		select {
		case <-ctx.Done():
			return errors.Wrap(last, "timed out waiting for diff server to become ready")
		case <-time.After(readyPollInterval):
		}
	}
}

// Route returns the diff server client that serves the given group and kind,
// if any.
func (m *Manager) Route(gk schema.GroupKind) (diffv1alpha1.PlanServiceClient, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.routes[gk]
	if !ok {
		return nil, false
	}
	return s.client, true
}

// Servers returns the running diff servers.
func (m *Manager) Servers() []*Server {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]*Server(nil), m.servers...)
}

// Stop closes connections and tears down containers according to each
// server's cleanup policy: Remove tears the container down, Orphan leaves it
// running so the next run finds it by name and skips startup.
func (m *Manager) Stop(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []error
	for _, s := range m.servers {
		if s.conn != nil {
			_ = s.conn.Close()
		}
		if err := s.running.Stop(ctx, s.cleanup); err != nil {
			errs = append(errs, errors.Wrapf(err, "cannot stop diff server container %s", s.ContainerID))
		}
	}
	return errors.Join(errs...)
}

func containerName(digest string) string {
	d := strings.TrimPrefix(digest, "sha256:")
	if len(d) > 12 {
		d = d[:12]
	}
	return containerPrefix + d
}
