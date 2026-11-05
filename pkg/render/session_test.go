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
	"io"
	"maps"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"

	"github.com/crossplane/cli/v2/internal/containerruntime"
	renderv1alpha1 "github.com/crossplane/cli/v2/proto/render/v1alpha1"
)

func TestManagedLabelsSession(t *testing.T) {
	cases := map[string]struct {
		reason  string
		session string
		want    string
	}{
		"Supplied": {
			reason:  "A caller-supplied session ID should be used verbatim.",
			session: "my-session",
			want:    "my-session",
		},
		"Default": {
			reason: "An empty session should fall back to the per-process session ID.",
			want:   sessionID(),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := managedLabels(tc.session)[LabelKeySession]
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("\n%s\nmanagedLabels(...): -want session, +got session:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestSessionIDDefaultIsStable(t *testing.T) {
	first, second := sessionID(), sessionID()
	if first == "" || first != second {
		t.Errorf("sessionID(): want a non-empty, stable per-process ID, got %q then %q", first, second)
	}
}

func TestInjectSessionAnnotation(t *testing.T) {
	fn := func(a map[string]string) pkgv1.Function {
		f := pkgv1.Function{}
		f.SetAnnotations(a)
		return f
	}

	cases := map[string]struct {
		reason  string
		session string
		fns     []pkgv1.Function
		want    []pkgv1.Function
	}{
		"Injects": {
			reason:  "Functions without the annotation should receive the session.",
			session: "my-session",
			fns:     []pkgv1.Function{fn(nil), fn(map[string]string{"other": "x"})},
			want: []pkgv1.Function{
				fn(map[string]string{AnnotationKeyRuntimeDockerSession: "my-session"}),
				fn(map[string]string{"other": "x", AnnotationKeyRuntimeDockerSession: "my-session"}),
			},
		},
		"DoesNotOverwrite": {
			reason:  "A session annotation already on the Function must be preserved.",
			session: "my-session",
			fns:     []pkgv1.Function{fn(map[string]string{AnnotationKeyRuntimeDockerSession: "pinned"})},
			want:    []pkgv1.Function{fn(map[string]string{AnnotationKeyRuntimeDockerSession: "pinned"})},
		},
		"EmptySessionIsNoop": {
			reason: "An empty session must leave Functions untouched so they use the per-process default.",
			fns:    []pkgv1.Function{fn(nil)},
			want:   []pkgv1.Function{fn(nil)},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			injectSessionAnnotation(tc.fns, tc.session)
			if diff := cmp.Diff(tc.want, tc.fns); diff != "" {
				t.Errorf("\n%s\ninjectSessionAnnotation(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestEngineSetupInjectsSession(t *testing.T) {
	cases := map[string]struct {
		reason string
		engine Engine
	}{
		"Docker": {
			reason: "The Docker engine should annotate Functions with its session even when the network is preset.",
			engine: &dockerRenderEngine{network: "preset", session: "my-session", log: logging.NewNopLogger()},
		},
		"Local": {
			reason: "The local engine should annotate Functions with its session, since their containers are still Docker.",
			engine: &localRenderEngine{session: "my-session"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fns := []pkgv1.Function{{}}
			if _, err := tc.engine.Setup(t.Context(), fns); err != nil {
				t.Fatalf("\n%s\nSetup(...): unexpected error: %v", tc.reason, err)
			}
			if got := fns[0].GetAnnotations()[AnnotationKeyRuntimeDockerSession]; got != "my-session" {
				t.Errorf("\n%s\nSetup(...): session annotation: want %q, got %q", tc.reason, "my-session", got)
			}
		})
	}
}

func TestGetRuntimeDockerSession(t *testing.T) {
	fn := pkgv1.Function{}
	fn.SetAnnotations(map[string]string{AnnotationKeyRuntimeDockerSession: "my-session"})

	rd, err := GetRuntimeDocker(fn, logging.NewNopLogger())
	if err != nil {
		t.Fatalf("GetRuntimeDocker(...): unexpected error: %v", err)
	}
	if rd.Session != "my-session" {
		t.Errorf("GetRuntimeDocker(...): Session: want %q, got %q", "my-session", rd.Session)
	}
}

// recorder records events from concurrent goroutines.
type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) add(e string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recorder) get() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

func namedFn(name string, annotations map[string]string) pkgv1.Function {
	fn := pkgv1.Function{}
	fn.SetName(name)
	fn.SetAnnotations(annotations)
	return fn
}

// devFn returns a Function named name on the Development runtime, which
// starts nothing and listens on name:9443.
func devFn(name string, annotations map[string]string) pkgv1.Function {
	a := map[string]string{
		AnnotationKeyRuntime:                  string(AnnotationValueRuntimeDevelopment),
		AnnotationKeyRuntimeDevelopmentTarget: name + ":9443",
	}
	maps.Copy(a, annotations)
	return namedFn(name, a)
}

// starts counts, by function name, the runtimes s started.
func starts(s *Session) map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := make(map[string]int)
	for _, rt := range s.runtimes {
		n[rt.name]++
	}
	return n
}

// recordStops makes each runtime s started record its stop in rec, then
// return err.
func recordStops(s *Session, rec *recorder, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.runtimes {
		name := s.runtimes[i].name
		s.runtimes[i].Stop = func(context.Context) error {
			rec.add("stop:" + name)
			return err
		}
	}
}

// newTestSession returns a Session on engine, leasing in a temporary
// directory.
func newTestSession(t *testing.T, engine Engine, opts ...SessionOption) (*Session, string) {
	t.Helper()
	dir := t.TempDir()
	opts = append([]SessionOption{
		WithLeaseDir(dir),
		WithEngineFactory(func(*EngineFlags, logging.Logger) Engine { return engine }),
		WithWarn(func(err error) { t.Errorf("unexpected warning: %v", err) }),
	}, opts...)
	s := NewSession(EngineFlags{}, logging.NewNopLogger(), opts...)
	return s, dir
}

func TestSessionStartsEachFunctionOnceConcurrently(t *testing.T) {
	s, _ := newTestSession(t, &MockEngine{})

	names := []string{"a", "b", "c", "d", "e"}

	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			fns := []pkgv1.Function{devFn(names[i%5], nil), devFn(names[(i+1)%5], nil)}
			addrs, err := s.Start(t.Context(), fns)
			if err != nil {
				t.Errorf("Start(...): unexpected error: %v", err)
				return
			}
			want := map[string]string{names[i%5]: names[i%5] + ":9443", names[(i+1)%5]: names[(i+1)%5] + ":9443"}
			if diff := cmp.Diff(want, addrs); diff != "" {
				t.Errorf("Start(...): -want addresses, +got addresses:\n%s", diff)
			}
			if _, err := s.Render(t.Context(), &renderv1alpha1.RenderRequest{}); err != nil {
				t.Errorf("Render(...): unexpected error: %v", err)
			}
		})
	}
	wg.Wait()

	got := starts(s)
	for _, n := range names {
		if got[n] != 1 {
			t.Errorf("function %q: want 1 start, got %d", n, got[n])
		}
	}
}

func TestSessionSetupOnceBeforeRender(t *testing.T) {
	cases := map[string]struct {
		reason string
		start  bool
	}{
		"StartWithNoFunctions": {
			reason: "A Start with no functions should still run Setup, once, before the first Render.",
			start:  true,
		},
		"RenderWithoutStart": {
			reason: "A Render with no Start before it should run Setup itself, once.",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec := &recorder{}
			engine := &MockEngine{
				MockSetup: func(context.Context, []pkgv1.Function) (func(), error) {
					rec.add("setup")
					return func() {}, nil
				},
				MockRender: func(context.Context, *renderv1alpha1.RenderRequest) (*renderv1alpha1.RenderResponse, error) {
					rec.add("render")
					return &renderv1alpha1.RenderResponse{}, nil
				},
			}
			s, _ := newTestSession(t, engine)

			if tc.start {
				if _, err := s.Start(t.Context(), nil); err != nil {
					t.Fatalf("\n%s\nStart(...): unexpected error: %v", tc.reason, err)
				}
				if _, err := s.Start(t.Context(), nil); err != nil {
					t.Fatalf("\n%s\nStart(...): unexpected error: %v", tc.reason, err)
				}
			}
			for range 2 {
				if _, err := s.Render(t.Context(), &renderv1alpha1.RenderRequest{}); err != nil {
					t.Fatalf("\n%s\nRender(...): unexpected error: %v", tc.reason, err)
				}
			}

			if diff := cmp.Diff([]string{"setup", "render", "render"}, rec.get()); diff != "" {
				t.Errorf("\n%s\n-want events, +got events:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestSessionClose(t *testing.T) {
	rec := &recorder{}
	var dir string

	leaseHeld := func() string {
		matches, _ := filepath.Glob(filepath.Join(dir, "*.lock"))
		return fmt.Sprintf("lease held: %t", len(matches) == 1)
	}

	engine := &MockEngine{
		MockSetup: func(_ context.Context, fns []pkgv1.Function) (func(), error) {
			n := fns[0].GetName()
			return func() { rec.add(fmt.Sprintf("cleanup %s (%s)", n, leaseHeld())) }, nil
		},
	}
	s, dir := newTestSession(t, engine)

	if _, err := s.Start(t.Context(), []pkgv1.Function{devFn("a", nil)}); err != nil {
		t.Fatalf("Start(...): unexpected error: %v", err)
	}
	// Two functions named b in one Start both start, and both stop.
	if _, err := s.Start(t.Context(), []pkgv1.Function{devFn("a", nil), devFn("b", nil), devFn("b", nil)}); err != nil {
		t.Fatalf("Start(...): unexpected error: %v", err)
	}
	if diff := cmp.Diff(map[string]int{"a": 1, "b": 2}, starts(s)); diff != "" {
		t.Errorf("Start(...): want each function started once per Start that names it first: -want starts, +got starts:\n%s", diff)
	}
	recordStops(s, rec, nil)

	if err := s.Close(t.Context()); err != nil {
		t.Fatalf("Close(...): unexpected error: %v", err)
	}
	rec.add(leaseHeld())

	got := rec.get()
	// Runtimes stop concurrently, so their order is not fixed.
	slices.Sort(got[0:3])
	want := []string{
		"stop:a", "stop:b", "stop:b",
		"cleanup b (lease held: true)", "cleanup a (lease held: true)",
		"lease held: false",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Close(...): want runtimes stopped, then Setup cleanups LIFO, then the lease released: -want events, +got events:\n%s", diff)
	}

	if err := s.Close(t.Context()); err != nil {
		t.Errorf("Close(...): a second Close: unexpected error: %v", err)
	}
	if diff := cmp.Diff(want, rec.get(), cmpopts.SortSlices(func(a, b string) bool { return a < b })); diff != "" {
		t.Errorf("Close(...): a second Close should do nothing: -want events, +got events:\n%s", diff)
	}

	if _, err := s.Start(t.Context(), nil); !errors.Is(err, errSessionClosed) {
		t.Errorf("Start(...) after Close: want %v, got %v", errSessionClosed, err)
	}
}

func TestSessionStartFailureKeepsSetupCleanup(t *testing.T) {
	rec := &recorder{}
	engine := &MockEngine{
		MockSetup: func(context.Context, []pkgv1.Function) (func(), error) {
			return func() { rec.add("cleanup") }, nil
		},
	}
	s, dir := newTestSession(t, engine)

	// An unknown runtime cannot be started.
	fn := namedFn("a", map[string]string{AnnotationKeyRuntime: "Unknown"})
	_, err := s.Start(t.Context(), []pkgv1.Function{fn})
	if err == nil || !strings.Contains(err.Error(), "cannot start function runtimes") || !strings.Contains(err.Error(), "unknown runtime") {
		t.Fatalf("Start(...): want a failure to start the unknown runtime, got %v", err)
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatalf("Close(...): unexpected error: %v", err)
	}

	if diff := cmp.Diff([]string{"cleanup"}, rec.get()); diff != "" {
		t.Errorf("Close(...): a failed start should keep the Setup cleanup: -want events, +got events:\n%s", diff)
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "*.lock")); len(matches) != 0 {
		t.Errorf("Close(...): want the lease released, got lease files %v", matches)
	}
}

func TestSessionCloseStopFailureKeepsLease(t *testing.T) {
	errBoom := errors.New("boom")
	s, dir := newTestSession(t, &MockEngine{})

	if _, err := s.Start(t.Context(), []pkgv1.Function{devFn("a", nil)}); err != nil {
		t.Fatalf("Start(...): unexpected error: %v", err)
	}
	recordStops(s, &recorder{}, errBoom)
	if err := s.Close(t.Context()); !errors.Is(err, errBoom) {
		t.Errorf("Close(...): want an error wrapping %v, got %v", errBoom, err)
	}

	if matches, _ := filepath.Glob(filepath.Join(dir, "*.lock")); len(matches) != 1 {
		t.Errorf("Close(...): a runtime that cannot be stopped should keep the lease for a later run to reclaim, got lease files %v", matches)
	}
}

// TestSessionDockerStartFailureKeepsLease checks that a container created by a
// failed Start, which no runtime stops, keeps the lease for a later run.
func TestSessionDockerStartFailureKeepsLease(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no run leases")
	}
	t.Setenv("DOCKER_CONFIG", t.TempDir()) // No credential helpers.

	errBoom := errors.New("boom")
	cli := &fakeDocker{host: "unix:///var/run/docker.sock", startErr: errBoom}
	s, dir := newTestSession(t, &MockEngine{}, WithDockerClient(cli))

	fn := namedFn("fn", nil)
	fn.Spec.Package = "fn-image"
	if _, err := s.Start(t.Context(), []pkgv1.Function{fn}); !errors.Is(err, errBoom) {
		t.Fatalf("Start(...): want an error wrapping %v, got %v", errBoom, err)
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatalf("Close(...): unexpected error: %v", err)
	}

	type state struct {
		Containers []string
		Leases     int
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*.lock"))
	got := state{Containers: cli.left().Containers, Leases: len(matches)}
	want := state{Containers: []string{"id-fn-image"}, Leases: 1}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Start and Close: want the created container left and its lease kept for a later run: -want, +got:\n%s", diff)
	}
}

// TestSessionSweepsOnlyItsOwnNetworks checks that Close removes the networks
// labelled with its session only when its session ID is its own. The seeded
// network is not the engine's own, so only the sweep can remove it.
func TestSessionSweepsOnlyItsOwnNetworks(t *testing.T) {
	cases := map[string]struct {
		reason string
		flags  EngineFlags
		want   []string
	}{
		"OwnNetwork": {
			reason: "A Docker engine on a network it created, under a generated ID, should sweep its session's networks.",
		},
		"SuppliedID": {
			reason: "A supplied ID may be shared with another process, so its networks should not be swept.",
			flags:  EngineFlags{SessionID: "0123456789abcdef"},
			want:   []string{"stray"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cli := &fakeDocker{}
			s := NewSession(tc.flags, logging.NewNopLogger(), WithDockerClient(cli))
			cli.networks = []network.Summary{{Network: network.Network{ID: "stray", Labels: managedLabels(s.ID())}}}

			if _, err := s.Start(t.Context(), nil); err != nil {
				t.Fatalf("\n%s\nStart(...): unexpected error: %v", tc.reason, err)
			}
			if err := s.Close(t.Context()); err != nil {
				t.Fatalf("\n%s\nClose(...): unexpected error: %v", tc.reason, err)
			}

			if diff := cmp.Diff(tc.want, cli.left().Networks, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("\n%s\nClose(...): -want networks left, +got networks left:\n%s", tc.reason, diff)
			}
		})
	}
}

// TestSessionWithDockerClient checks that a Session reclaims a dead run, runs
// a function and the render container, and cleans up after itself through the
// client WithDockerClient supplies, and that it never closes that client. The
// environment points at no daemon, so a path that builds its own client fails.
// CheckContextSupport judges the supplied client's daemon too.
func TestSessionWithDockerClient(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no run leases, so no dead run to reclaim")
	}
	t.Setenv("DOCKER_HOST", "unix://"+filepath.Join(t.TempDir(), "no-docker.sock"))
	t.Setenv("DOCKER_CONFIG", t.TempDir()) // No credential helpers.

	dir := t.TempDir()
	deadID := containerruntime.NewSessionID()
	if err := os.WriteFile(filepath.Join(dir, deadID+".lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	cli := &fakeDocker{
		host:       "tcp://remote:2376",
		containers: []container.Summary{{ID: "dead-fn", State: container.StateRunning, Labels: managedLabels(deadID)}},
		networks:   []network.Summary{{Network: network.Network{ID: "dead-net", Labels: managedLabels(deadID)}}},
	}
	s := NewSession(EngineFlags{CrossplaneImage: "render-image"}, logging.NewNopLogger(),
		WithDockerClient(cli),
		WithLeaseDir(dir),
		WithWarn(func(err error) { t.Errorf("unexpected warning: %v", err) }),
	)

	// The environment's daemon is local, the supplied client's remote.
	if err := s.CheckContextSupport(); err == nil {
		t.Errorf("CheckContextSupport(): want an error for the supplied client's remote daemon, got nil")
	}

	fn := namedFn("fn", nil)
	fn.Spec.Package = "fn-image"

	if _, err := s.Start(t.Context(), []pkgv1.Function{fn}); err != nil {
		t.Fatalf("Start(...): unexpected error: %v", err)
	}
	if _, err := s.Render(t.Context(), &renderv1alpha1.RenderRequest{}); err != nil {
		t.Fatalf("Render(...): unexpected error: %v", err)
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatalf("Close(...): unexpected error: %v", err)
	}

	if diff := cmp.Diff(leftover{}, cli.left(), cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("Start, Render and Close: want the dead run and the Session's own resources gone, and the client not closed: -want, +got:\n%s", diff)
	}
}

// fakeDocker is a Docker client that keeps the containers and networks it
// serves, for the Docker calls a Session makes. Any other call panics.
type fakeDocker struct {
	client.APIClient

	host string
	// startErr, if set, is what ContainerStart returns.
	startErr error

	mu         sync.Mutex
	closed     bool
	network    string
	networks   []network.Summary
	containers []container.Summary
}

// leftover is what a fakeDocker still holds.
type leftover struct {
	Containers []string
	Networks   []string
	Closed     bool
}

func (f *fakeDocker) left() leftover {
	f.mu.Lock()
	defer f.mu.Unlock()
	l := leftover{Closed: f.closed}
	for _, c := range f.containers {
		l.Containers = append(l.Containers, c.ID)
	}
	for _, n := range f.networks {
		l.Networks = append(l.Networks, n.ID)
	}
	return l
}

func (f *fakeDocker) DaemonHost() string { return f.host }

func (f *fakeDocker) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

// hasLabels reports whether labels has every key=value label filters selects
// on.
func hasLabels(filters client.Filters, labels map[string]string) bool {
	for l := range filters["label"] {
		k, v, _ := strings.Cut(l, "=")
		if labels[k] != v {
			return false
		}
	}
	return true
}

// ContainerList lists the containers the fake created and has not removed.
func (f *fakeDocker) ContainerList(_ context.Context, o client.ContainerListOptions) (client.ContainerListResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var res client.ContainerListResult
	for _, c := range f.containers {
		if hasLabels(o.Filters, c.Labels) {
			res.Items = append(res.Items, c)
		}
	}
	return res, nil
}

// NetworkList lists the networks the fake created and has not removed.
func (f *fakeDocker) NetworkList(_ context.Context, o client.NetworkListOptions) (client.NetworkListResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var res client.NetworkListResult
	for _, n := range f.networks {
		if hasLabels(o.Filters, n.Labels) {
			res.Items = append(res.Items, n)
		}
	}
	return res, nil
}

func (f *fakeDocker) NetworkCreate(_ context.Context, name string, o client.NetworkCreateOptions) (client.NetworkCreateResult, error) {
	f.mu.Lock()
	f.network = name
	f.networks = append(f.networks, network.Summary{Network: network.Network{ID: "net-id", Name: name, Labels: o.Labels}})
	f.mu.Unlock()
	return client.NetworkCreateResult{ID: "net-id"}, nil
}

func (f *fakeDocker) NetworkRemove(_ context.Context, id string, _ client.NetworkRemoveOptions) (client.NetworkRemoveResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.networks = slices.DeleteFunc(f.networks, func(n network.Summary) bool { return n.ID == id })
	return client.NetworkRemoveResult{}, nil
}

func (f *fakeDocker) ImageInspect(_ context.Context, _ string, _ ...client.ImageInspectOption) (client.ImageInspectResult, error) {
	return client.ImageInspectResult{}, nil
}

// ContainerCreate records the container, running and attached to the networks
// it was created on, so ContainerList lists it until it is removed.
func (f *fakeDocker) ContainerCreate(_ context.Context, o client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	id := "id-" + o.Config.Image
	c := container.Summary{ID: id, State: container.StateRunning, Labels: o.Config.Labels, NetworkSettings: &container.NetworkSettingsSummary{
		Networks: map[string]*network.EndpointSettings{},
	}}
	if o.NetworkingConfig != nil {
		for n := range o.NetworkingConfig.EndpointsConfig {
			c.NetworkSettings.Networks[n] = &network.EndpointSettings{}
		}
	}
	f.mu.Lock()
	f.containers = append(f.containers, c)
	f.mu.Unlock()
	return client.ContainerCreateResult{ID: id}, nil
}

func (f *fakeDocker) ContainerStart(_ context.Context, _ string, _ client.ContainerStartOptions) (client.ContainerStartResult, error) {
	return client.ContainerStartResult{}, f.startErr
}

// ContainerInspect reports the container attached to the network the fake
// created last.
func (f *fakeDocker) ContainerInspect(_ context.Context, id string, _ client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return client.ContainerInspectResult{Container: container.InspectResponse{
		Name:            "/" + id,
		NetworkSettings: &container.NetworkSettings{Networks: map[string]*network.EndpointSettings{f.network: {}}},
	}}, nil
}

func (f *fakeDocker) ContainerStop(_ context.Context, _ string, _ client.ContainerStopOptions) (client.ContainerStopResult, error) {
	return client.ContainerStopResult{}, nil
}

func (f *fakeDocker) ContainerRemove(_ context.Context, id string, _ client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.containers = slices.DeleteFunc(f.containers, func(c container.Summary) bool { return c.ID == id })
	return client.ContainerRemoveResult{}, nil
}

// ContainerAttach returns a connection that discards stdin and has no output.
func (f *fakeDocker) ContainerAttach(_ context.Context, _ string, _ client.ContainerAttachOptions) (client.ContainerAttachResult, error) {
	return client.ContainerAttachResult{HijackedResponse: client.NewHijackedResponse(discardConn{}, "")}, nil
}

// ContainerWait reports that the container exited successfully.
func (f *fakeDocker) ContainerWait(_ context.Context, _ string, _ client.ContainerWaitOptions) client.ContainerWaitResult {
	res := make(chan container.WaitResponse, 1)
	res <- container.WaitResponse{}
	return client.ContainerWaitResult{Result: res}
}

// discardConn is a connection that discards writes and reads nothing.
type discardConn struct {
	net.Conn
}

func (discardConn) Read([]byte) (int, error)    { return 0, io.EOF }
func (discardConn) Write(b []byte) (int, error) { return len(b), nil }
func (discardConn) Close() error                { return nil }
