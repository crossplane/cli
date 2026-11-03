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
// A provider can instead point at a diff server already running outside
// Docker (the Development runtime, like render's); nothing is fetched for it,
// so the provider declares the API groups its server serves.
package diffserver

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
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

	"github.com/crossplane/cli/v2/internal/async"
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

	// runSuffixLength is the length of the random suffix that makes a
	// container with the Remove policy this run's alone.
	runSuffixLength = 8

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
	// its deterministic name and reuses it without paying startup. Only an
	// Orphan container gets that name: a Remove one's name is unique to its
	// run, so no other run uses it, and its run can remove it. Render's
	// function runtimes support a third policy, Stop; a stopped diff server
	// serves nothing and restarting costs nearly as much as starting, so
	// simulate omits it.
	CleanupOrphan = containerruntime.CleanupOrphan
)

// Runtime is how a provider's diff server is run, following render's
// per-function runtime annotation.
type Runtime string

const (
	// RuntimeDocker runs the provider's package image as a local container.
	// It is the default.
	RuntimeDocker Runtime = "Docker"

	// RuntimeDevelopment connects to a diff server the user runs themselves,
	// from source or under a debugger. No package is fetched and no
	// container starts, so the provider declares the API groups it serves.
	RuntimeDevelopment Runtime = "Development"
)

// labelDomain prefixes the labels simulate applies to the Docker containers
// it creates.
const labelDomain = containerruntime.DomainSimulate

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
	// created them. A later run reclaims the containers of a run that died
	// without cleaning up by this label, unless they are Orphan.
	LabelKeySession = labelDomain + "/session"

	// LabelKeyCleanup records the effective cleanup policy a container was
	// created with.
	LabelKeyCleanup = labelDomain + "/cleanup"
)

// Server is a running diff server for one provider.
type Server struct {
	// Image identifies the provider: its package image, or the manifest
	// name of a Development provider, which has no image to run.
	Image   string
	Address string
	// ContainerID is empty for a Development provider: its server runs
	// outside Docker.
	ContainerID string
	// GroupKinds are the kinds the provider's package ships CRDs for.
	GroupKinds []schema.GroupKind
	// APIGroups are the groups a Development provider declared it serves.
	APIGroups []string

	conn   *grpc.ClientConn
	client diffv1alpha1.PlanServiceClient
	// stop tears down whatever Start created. Nil when nothing was created.
	stop func(context.Context) error
}

// Skipped records a provider that does not run a diff server this run, and
// why. Resources it would have served fall back to the approximate
// client-side diff. Image identifies the provider the way Server.Image does.
type Skipped struct {
	Image  string
	Reason string
}

// Input describes one provider to run, or connect to, as a diff server. The
// zero values mean the manager's defaults apply; set them to override Docker
// behavior for this provider only, the way render's per-function runtime
// annotations do.
type Input struct {
	// Image is the provider package to run. A Development provider needs
	// none: its server is already running.
	Image          string
	PublishAddress netip.Addr
	Target         string
	Cleanup        Cleanup

	// Runtime selects how the diff server is run. Empty means Docker.
	Runtime Runtime
	// Name identifies the provider in progress and skip messages when there
	// is no image to name it by, e.g. the Provider manifest's name.
	Name string
	// DevelopmentTarget is the gRPC target of a Development provider's
	// running diff server.
	DevelopmentTarget string
	// APIGroups are the API groups a Development provider serves. Resources
	// in these groups route to it unless a package-run server ships a CRD
	// for their exact group and kind.
	APIGroups []string
}

// Manager starts diff server containers for provider packages, connects to
// diff servers run outside Docker, and routes resources to them.
type Manager struct {
	pkgs         runtimexpkg.Client
	cli          DockerClient
	log          logging.Logger
	readyTimeout time.Duration
	publishAddr  netip.Addr
	session      string
	leaseDir     string

	// run leases this run's containers, and reclaims what dead runs left.
	run *containerruntime.Run

	mu sync.Mutex
	// leaked is set when a container Start may have created was not
	// removed, so Stop keeps the run's lease for a later run to reclaim it.
	leaked bool
	// containers maps each container this run starts to the image of the
	// first input that asked for it, so inputs whose packages share a digest
	// share one diff server.
	containers map[containerKey]string
	routes     map[schema.GroupKind]*Server
	groups     map[string]*Server
	servers    []*Server
}

// Option configures a Manager.
type Option func(*Manager)

// DockerClient is the Docker client a Manager runs its containers and
// reclaims dead runs' containers with.
type DockerClient interface {
	containerruntime.Client
	containerruntime.RunClient
}

// WithDockerClient makes the manager do everything it does on Docker through
// cli: run and clean up its containers, and reclaim what dead runs left
// behind. The manager never closes cli. Nil (the default) means clients
// built from the environment.
func WithDockerClient(cli DockerClient) Option {
	return func(m *Manager) { m.cli = cli }
}

// WithReadyTimeout sets how long to wait for a started diff server to answer.
func WithReadyTimeout(d time.Duration) Option {
	return func(m *Manager) { m.readyTimeout = d }
}

// WithSessionID sets the session label value for the containers the manager
// creates, so callers orchestrating a larger run can scope cleanup to it.
// Empty means a random ID generated for this manager, which it leases (see
// WithLeaseDir). A supplied ID is never leased, since other processes may
// share it, so what a run with one leaves after a crash is not reclaimed.
func WithSessionID(session string) Option {
	return func(m *Manager) { m.session = session }
}

// WithLeaseDir makes the manager lease its run in dir, and reclaim what dead
// runs leased there left behind, before it starts its first container. The
// default, "", disables both. See containerruntime.Run.
func WithLeaseDir(dir string) Option {
	return func(m *Manager) { m.leaseDir = dir }
}

// NewManager returns a Manager that fetches provider packages with pkgs and
// runs their images as local diff server containers. NewManager does no I/O.
func NewManager(pkgs runtimexpkg.Client, log logging.Logger, opts ...Option) *Manager {
	m := &Manager{
		pkgs:         pkgs,
		log:          log,
		readyTimeout: defaultReadyTimeout,
		publishAddr:  netip.MustParseAddr("127.0.0.1"),
		containers:   map[containerKey]string{},
		routes:       map[schema.GroupKind]*Server{},
		groups:       map[string]*Server{},
	}
	for _, opt := range opts {
		opt(m)
	}

	leased := m.session == ""
	if leased {
		m.session = containerruntime.NewSessionID()
	}
	var runOpts []containerruntime.RunOption
	if m.cli != nil {
		runOpts = append(runOpts, containerruntime.WithRunClient(func() (containerruntime.RunClient, error) {
			return sharedRunClient{m.cli}, nil
		}))
	}
	m.run = containerruntime.NewRun(m.leaseDir, m.session, leased, log, runOpts...)
	return m
}

// sharedRunClient is the manager's Docker client as its Run uses it. It hides
// the client's Close, so the Run, which closes the clients it gets, leaves the
// caller's open.
type sharedRunClient struct {
	containerruntime.RunClient
}

// Start inspects each provider image and runs a diff server container for
// every package that declares the DiffServer capability, and connects to the
// already running server of every Development provider, in parallel and best
// effort: a provider that cannot serve (no capability, fetch or startup
// failure, nothing answering at a development target) is returned as skipped
// rather than failing the run, since its resources degrade to the approximate
// client-side diff. Progress is reported per provider on events (nil means no
// reporting), with the package pull and the server start as separate steps
// because the pull dominates a cold start.
func (m *Manager) Start(ctx context.Context, inputs []Input, events async.EventChannel) ([]Skipped, error) {
	// Only a run that starts containers touches Docker: a Development
	// provider's server is the user's.
	if slices.ContainsFunc(inputs, func(in Input) bool { return in.Runtime != RuntimeDevelopment }) {
		// Idempotent: only the first call does anything. Best effort: a
		// failure only risks leaking this run's containers if it dies. It
		// holds mu, so Servers and Stop wait for it, up to the reap timeout,
		// on purpose: reclaiming must finish before any container starts.
		m.mu.Lock()
		err := m.run.Acquire(ctx)
		m.mu.Unlock()
		if err != nil {
			m.log.Info("Problem managing this run's diff server containers", "error", err)
		}
	}

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		skipped []Skipped
	)
	for _, in := range inputs {
		wg.Add(1)
		go func(in Input) {
			defer wg.Done()
			s, skip := m.startOne(ctx, in, events)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case skip != nil:
				skipped = append(skipped, *skip)
			case s != nil:
				m.register(s)
			}
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
	for _, g := range s.APIGroups {
		if other, ok := m.groups[g]; ok {
			m.log.Info("Multiple development diff servers declare the same API group; keeping the first",
				"group", g, "kept", other.Image, "ignored", s.Image)
			continue
		}
		m.groups[g] = s
	}
}

// startOne returns neither a Server nor a Skipped for an input whose diff
// server another input of this run already starts.
func (m *Manager) startOne(ctx context.Context, in Input, events async.EventChannel) (*Server, *Skipped) {
	if in.Runtime == RuntimeDevelopment {
		return m.connectDevelopment(ctx, in, events)
	}

	image := in.Image

	// The package pull is its own reported step: on a cold cache it downloads
	// the full provider package and dwarfs everything that follows.
	pull := "Pulling provider package " + image
	events.SendEvent(pull, async.EventStatusStarted)
	pkg, err := m.pkgs.Get(ctx, image)
	if err != nil {
		events.SendEvent(pull, async.EventStatusFailure)
		return nil, &Skipped{Image: image, Reason: fmt.Sprintf("cannot fetch package: %s", err)}
	}
	events.SendEvent(pull, async.EventStatusSuccess)

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

	// Two inputs can name one package, e.g. by tag and by digest. Its
	// server serves both: a second would only be started and torn down.
	// Without a digest the packages cannot be told apart, so each starts.
	if pkg.Digest != "" {
		if first, ok := m.claim(containerKey{digest: pkg.Digest, cleanup: cleanup, publishAddr: publishAddr, target: in.Target}, image); !ok {
			m.log.Debug("Sharing the diff server of another provider with the same package", "image", image, "shared-with", first)
			return nil, nil
		}
	}

	ctr := &containerruntime.Container{
		Image: runImage,
		Name:  containerName(pkg.Digest, cleanup),
		// --debug precedes the subcommand: it is the provider's global flag.
		Cmd:    []string{"--debug", "internal", "diff-server", "--address", fmt.Sprintf(":%d", containerPort)},
		Port:   containerPort,
		Labels: containerruntime.ContainerLabels(labelDomain, m.run.ID(), cleanup),
		// With a Domain, a name conflict with a concurrent run's container
		// is judged by its labels, so concurrent Orphan runs share one.
		Domain:      labelDomain,
		BindAddress: publishAddr.String(),
		Target:      in.Target,
		Log:         m.log,
		Client:      m.cli,
		// The container reports its own image pull on the same channel, as a
		// separate step: Docker pulls the image again even when the package
		// cache is warm, and only when the daemon does not have it yet.
		Events: events,
	}

	start := "Starting diff server for " + image
	events.SendEvent(start, async.EventStatusStarted)
	running, err := ctr.Start(ctx)
	if err != nil {
		events.SendEvent(start, async.EventStatusFailure)
		// Start may have created the container before failing, and no run
		// finds a Remove container by its name again.
		if cleanup != CleanupOrphan {
			m.markLeaked()
		}
		return nil, &Skipped{Image: image, Reason: fmt.Sprintf("cannot start diff server container: %s", err)}
	}

	server, err := m.connect(ctx, running, image, cleanup)
	if err != nil {
		events.SendEvent(start, async.EventStatusFailure)
		// An orphaned container is left behind even on failure, for debugging.
		if !running.Reused && cleanup != CleanupOrphan {
			if serr := running.Stop(ctx, CleanupRemove); serr != nil {
				m.log.Info("Cannot clean up failed diff server container", "container", running.ID, "err", serr)
				m.markLeaked()
			}
		}
		return nil, &Skipped{Image: image, Reason: err.Error()}
	}
	events.SendEvent(start, async.EventStatusSuccess)

	server.GroupKinds = gks
	m.log.Debug("Diff server ready", "image", image, "address", server.Address, "reused", running.Reused, "cleanup", cleanup)
	return server, nil
}

// containerKey is what makes two inputs' diff server containers the same.
type containerKey struct {
	digest      string
	cleanup     Cleanup
	publishAddr netip.Addr
	target      string
}

// claim records that image's input starts the container for k, and reports
// true, unless another input already did, in which case it returns that
// input's image and false.
func (m *Manager) claim(k containerKey, image string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if first, ok := m.containers[k]; ok {
		return first, false
	}
	m.containers[k] = image
	return image, true
}

// markLeaked records that this run may have left a container behind.
func (m *Manager) markLeaked() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.leaked = true
}

// connectDevelopment connects to a diff server the user already runs, from
// source or under a debugger. Nothing is fetched and nothing starts, so the
// routes come from the API groups the provider declared.
func (m *Manager) connectDevelopment(ctx context.Context, in Input, events async.EventChannel) (*Server, *Skipped) {
	name := in.Name
	if name == "" {
		name = in.Image
	}

	step := fmt.Sprintf("Connecting to diff server for %s at %s", name, in.DevelopmentTarget)
	events.SendEvent(step, async.EventStatusStarted)
	conn, client, err := m.dial(ctx, in.DevelopmentTarget)
	if err != nil {
		events.SendEvent(step, async.EventStatusFailure)
		return nil, &Skipped{Image: name, Reason: fmt.Sprintf("no diff server answered at %s; start the provider's internal diff-server command before simulating: %s", in.DevelopmentTarget, err)}
	}
	events.SendEvent(step, async.EventStatusSuccess)

	m.log.Debug("Development diff server ready", "provider", name, "target", in.DevelopmentTarget, "apiGroups", in.APIGroups)
	return &Server{
		Image:     name,
		Address:   in.DevelopmentTarget,
		APIGroups: in.APIGroups,
		conn:      conn,
		client:    client,
	}, nil
}

func (m *Manager) connect(ctx context.Context, running *containerruntime.Running, image string, cleanup Cleanup) (*Server, error) {
	conn, client, err := m.dial(ctx, running.Addr)
	if err != nil {
		return nil, err
	}

	return &Server{
		Image:       image,
		Address:     running.Addr,
		ContainerID: running.ID,
		conn:        conn,
		client:      client,
		stop:        func(ctx context.Context) error { return running.Stop(ctx, cleanup) },
	}, nil
}

// dial connects to the diff server at target and waits for it to answer.
func (m *Manager) dial(ctx context.Context, target string) (*grpc.ClientConn, diffv1alpha1.PlanServiceClient, error) {
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, errors.Wrap(err, "cannot create gRPC client")
	}

	client := diffv1alpha1.NewPlanServiceClient(conn)
	if err := waitReady(ctx, client, m.readyTimeout); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	return conn, client, nil
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
// if any. A package-run server that ships a CRD for the exact group and kind
// wins over a development server that declared the group.
func (m *Manager) Route(gk schema.GroupKind) (diffv1alpha1.PlanServiceClient, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.routes[gk]; ok {
		return s.client, true
	}
	if s, ok := m.groups[gk.Group]; ok {
		return s.client, true
	}
	return nil, false
}

// Servers returns the running diff servers.
func (m *Manager) Servers() []*Server {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]*Server(nil), m.servers...)
}

// Stop closes connections and tears down containers according to each
// server's cleanup policy: Remove tears the container down, Orphan leaves it
// running so the next run finds it by name and skips startup. A development
// server is the user's to stop, so only its connection is closed. Stop then
// gives up the run's lease, keeping its file for a later run to reclaim what
// is left when a container could not be torn down.
func (m *Manager) Stop(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []error
	for _, s := range m.servers {
		if s.conn != nil {
			_ = s.conn.Close()
		}
		if s.stop == nil {
			continue
		}
		if err := s.stop(ctx); err != nil {
			errs = append(errs, errors.Wrapf(err, "cannot stop diff server container %s", s.ContainerID))
		}
	}
	// Only render creates networks, so there are none to sweep.
	errs = append(errs, m.run.Release(ctx, len(errs) == 0 && !m.leaked, ""))
	return errors.Join(errs...)
}

// containerName names the container for the package with digest. An Orphan
// container's name is the digest's alone, so later runs find and reuse it.
// Any other's has a random suffix, so no other run ever finds it by name and
// uses a container this run removes when it exits.
func containerName(digest string, cleanup Cleanup) string {
	d := strings.TrimPrefix(digest, "sha256:")
	if len(d) > 12 {
		d = d[:12]
	}
	if cleanup == CleanupOrphan {
		return containerPrefix + d
	}
	return containerPrefix + d + "-" + containerruntime.NewSessionID()[:runSuffixLength]
}
