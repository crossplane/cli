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
	"maps"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/moby/moby/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"

	"github.com/crossplane/cli/v2/internal/containerruntime"
	renderv1alpha1 "github.com/crossplane/cli/v2/proto/render/v1alpha1"
)

// errSessionClosed is returned by a Session used after Close.
var errSessionClosed = errors.New("render session is closed")

// A Session owns everything one run of render creates: a render engine, the
// environment its Setup creates (such as a Docker network), every function
// runtime it starts, and the run's lease. It starts each function once and
// renders any number of times against them.
//
// Order: the first Start or Render takes the run's lease, reclaims what dead
// runs left behind, then runs the engine's Setup, all before any Render
// reaches the engine. Close stops every runtime, then runs the Setup
// cleanups, then releases the lease.
//
// Concurrency: Start and Render are safe for concurrent use. Starting is
// serialized, so concurrent Starts that need the same function start it once.
// Rendering is not: once the first Setup has run, Render calls the engine
// without waiting for any Start. Close must not run while a Render is in
// flight; a Start or Render that begins after Close returns an error.
//
// Only a Session with a generated ID is leased. A supplied ID may be shared
// with other processes, so its Session neither leases it nor, at Close,
// removes networks labelled with it.
type Session struct {
	flags  EngineFlags
	log    logging.Logger
	engine Engine
	run    *containerruntime.Run
	warn   func(error)

	// sweep is whether Close removes networks left labelled with this
	// session: only when the engine creates its own network and the session
	// ID belongs to this run alone.
	sweep bool

	leaseDir  string
	newEngine func(*EngineFlags, logging.Logger) Engine
	// docker, if set, is the Docker client for everything the Session does
	// on Docker. When nil, each piece builds its own from the environment.
	docker client.APIClient

	// mu guards everything below, and serializes Setup and starting runtimes.
	mu sync.Mutex
	// addrs maps the name of every started function to its address.
	addrs map[string]string
	// runtimes holds every started runtime, so Close stops each of them.
	runtimes []startedRuntime
	cleanups []func()
	// leaked is whether a failed Start may have left a Docker container
	// behind, so Close keeps the lease for a later run to reclaim.
	leaked bool

	// closed is set, under mu, by Close. Render reads it without mu.
	closed atomic.Bool

	// setUp is set, under mu, once the engine's first Setup succeeded. Render
	// reads it without mu, which orders what Setup wrote before the render.
	setUp atomic.Bool
}

// A SessionOption configures a Session.
type SessionOption func(*Session)

// WithLeaseDir makes the Session lease its run in dir and reclaim what dead
// runs leased there left behind. The default, "", disables both. A Session
// with a supplied ID still reclaims dead runs, but takes no lease.
func WithLeaseDir(dir string) SessionOption {
	return func(s *Session) {
		s.leaseDir = dir
	}
}

// WithEngineFactory makes the Session build its engine with f. The default
// is NewEngineFromFlags.
func WithEngineFactory(f func(*EngineFlags, logging.Logger) Engine) SessionOption {
	return func(s *Session) {
		s.newEngine = f
	}
}

// WithWarn makes the Session report lease and reclaim failures to f. They
// never stop the Session. The default logs them at Info. f is called with
// the Session's lock held, so f must not call the Session.
func WithWarn(f func(error)) SessionOption {
	return func(s *Session) {
		s.warn = f
	}
}

// WithDockerClient makes the Session do everything it does on Docker through
// c, instead of clients built from the environment: create and remove the
// render network, run the render container, start and stop every function
// container, and reclaim what dead runs left behind. The render network and
// container are the Docker engine's, as NewEngineFromFlags builds it; another
// engine from WithEngineFactory keeps its own clients: if it uses Docker, it
// must use c's daemon too, or its network and the function containers end up
// on different daemons. Leases don't record their daemon, so a Session can
// retire the lease of a dead run against another daemon that shares its lease
// directory. The Session never closes c; the caller does, after Close. A nil
// c is the default.
func WithDockerClient(c client.APIClient) SessionOption {
	return func(s *Session) {
		s.docker = c
	}
}

// NewSession returns a Session that renders with an engine built from a copy
// of f. When f.SessionID is empty, the Session generates an ID, and leases it
// only when WithLeaseDir is set; a supplied ID is used as is and never
// leased, since other processes may share it. NewSession does no I/O.
func NewSession(f EngineFlags, log logging.Logger, opts ...SessionOption) *Session {
	if log == nil {
		log = logging.NewNopLogger()
	}

	s := &Session{
		flags:     f,
		log:       log,
		newEngine: NewEngineFromFlags,
		addrs:     make(map[string]string),
	}
	s.warn = func(err error) {
		s.log.Info("Problem managing this run's Docker resources", "error", err)
	}

	for _, o := range opts {
		o(s)
	}

	leased := s.flags.SessionID == ""
	if leased {
		s.flags.SessionID = containerruntime.NewSessionID()
	}

	var runOpts []containerruntime.RunOption
	if s.docker != nil {
		runOpts = append(runOpts, containerruntime.WithRunClient(func() (containerruntime.RunClient, error) {
			return sharedRunClient{s.docker}, nil
		}))
	}
	s.run = containerruntime.NewRun(s.leaseDir, s.flags.SessionID, leased, log, runOpts...)
	s.engine = s.newEngine(&s.flags, log)

	e, isDocker := s.engine.(*dockerRenderEngine)
	if isDocker && s.docker != nil {
		e.networks = s.docker
		e.dockerClient = s.docker
	}
	s.sweep = isDocker && s.flags.CrossplaneDockerNetwork == "" && leased

	return s
}

// ID is the session ID the Session labels its Docker resources with.
func (s *Session) ID() string {
	return s.flags.SessionID
}

// CheckContextSupport reports whether the Session's engine supports context
// injection and collection.
func (s *Session) CheckContextSupport() error {
	return s.engine.CheckContextSupport()
}

// Start ensures each of fns is running, starting the ones this Session has
// not started yet, and returns the addresses of fns only. Functions are
// identified by name. The first call, even with no fns, runs the engine's
// Setup. Start copies each function's annotations before Setup adds to them,
// so the caller's are untouched.
func (s *Session) Start(ctx context.Context, fns []pkgv1.Function) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed.Load() {
		return nil, errSessionClosed
	}

	// Functions sharing a name within fns all start, and the last one's
	// address is used, as with StartFunctionRuntimes.
	batch := make([]pkgv1.Function, 0, len(fns))
	for _, fn := range fns {
		if _, ok := s.addrs[fn.GetName()]; ok {
			continue
		}

		// fn is a copy, but shares its annotation map with the caller's.
		fn.SetAnnotations(maps.Clone(fn.GetAnnotations()))
		batch = append(batch, fn)
	}

	if err := s.setupLocked(ctx, batch); err != nil {
		return nil, err
	}

	if len(batch) > 0 {
		docker := false
		fa, err := startFunctionRuntimes(ctx, s.log, batch, func(fn pkgv1.Function, log logging.Logger) (Runtime, error) {
			rt, err := s.getRuntime(fn, log)
			if _, ok := rt.(*RuntimeDocker); ok {
				docker = true
			}
			return rt, err
		})
		if err != nil {
			// A Docker start can fail after creating its container, which
			// then is in no runtime Close stops.
			s.leaked = s.leaked || docker
			// The Setup cleanup is kept: Close still runs it.
			return nil, errors.Wrap(err, "cannot start function runtimes")
		}

		maps.Copy(s.addrs, fa.addrs)
		s.runtimes = append(s.runtimes, fa.runtimes...)
	}

	// Copied, because the caller reads them after mu is released.
	out := make(map[string]string, len(fns))
	for _, fn := range fns {
		if a, ok := s.addrs[fn.GetName()]; ok {
			out[fn.GetName()] = a
		}
	}

	return out, nil
}

// getRuntime gets fn's runtime as GetRuntime does, on the Session's Docker
// client when it has one.
func (s *Session) getRuntime(fn pkgv1.Function, log logging.Logger) (Runtime, error) {
	rt, err := GetRuntime(fn, log)
	if err != nil {
		return nil, err
	}
	if rd, ok := rt.(*RuntimeDocker); ok && s.docker != nil {
		rd.dockerClient = s.docker
	}
	return rt, nil
}

// Render runs req through the Session's engine. It waits for the first
// Setup, running it if no Start has, then renders without holding the
// Session's lock. After Close it returns an error without rendering. See
// Engine.Render for the partial response it may return with an error.
func (s *Session) Render(ctx context.Context, req *renderv1alpha1.RenderRequest) (*renderv1alpha1.RenderResponse, error) {
	if s.closed.Load() {
		return nil, errSessionClosed
	}

	if !s.setUp.Load() {
		s.mu.Lock()
		err := s.setupLocked(ctx, nil)
		s.mu.Unlock()

		if err != nil {
			return nil, err
		}
	}

	return s.engine.Render(ctx, req)
}

// setupLocked takes the run's lease and reclaims dead runs, the first time,
// then integrates fns into the engine's environment. Once the first Setup
// succeeded, it does nothing for no fns. The caller holds mu.
func (s *Session) setupLocked(ctx context.Context, fns []pkgv1.Function) error {
	if s.closed.Load() {
		return errSessionClosed
	}

	if s.setUp.Load() && len(fns) == 0 {
		return nil
	}

	// Idempotent: only the first call does anything.
	if err := s.run.Acquire(ctx); err != nil {
		s.warn(err)
	}

	cleanup, err := s.engine.Setup(ctx, fns)
	if err != nil {
		// Returned as is, as before Sessions: the engine's errors say what
		// failed.
		return err
	}

	s.cleanups = append(s.cleanups, cleanup)
	s.setUp.Store(true)

	return nil
}

// Close stops every runtime the Session started, each per its own cleanup
// policy and concurrently, then runs the engine's Setup cleanups in reverse
// order, then releases the run's lease. Teardown is detached from ctx's
// cancellation, so it can follow an interrupted render. The lease is kept,
// so a later run reclaims what is left, when a runtime cannot be stopped or
// a failed Start may have left a container.
// Close is idempotent; Start or Render after Close returns an error. Close
// must not run while a Render is in flight.
func (s *Session) Close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed.Swap(true) {
		return nil
	}

	// No deadline here: a Docker runtime's Stop bounds each of its requests
	// (containerruntime.StopTimeout, then RemoveTimeout), as the network
	// cleanup and the sweep bound theirs, so a hung daemon cannot hold Close.
	// The other runtimes' Stops do nothing.
	ctx = context.WithoutCancel(ctx)

	errs := make([]error, len(s.runtimes))

	var wg sync.WaitGroup
	for i, rt := range s.runtimes {
		wg.Go(func() {
			if err := rt.Stop(ctx); err != nil {
				errs[i] = errors.Wrapf(err, "cannot stop function %q runtime (target %q)", rt.name, rt.Target)
			}
		})
	}
	wg.Wait()

	for _, c := range slices.Backward(s.cleanups) {
		c()
	}

	stopErr := errors.Join(errs...)

	// Without a Setup, the engine created no network to sweep.
	sweep := ""
	if s.sweep && s.setUp.Load() {
		sweep = labelDomain
	}
	relErr := s.run.Release(ctx, stopErr == nil && !s.leaked, sweep)

	return errors.Join(stopErr, relErr)
}

// sharedRunClient is the Session's Docker client as the Run uses it. It hides
// the client's Close, so the Run, which closes the clients it gets, leaves the
// caller's open.
type sharedRunClient struct {
	containerruntime.RunClient
}
