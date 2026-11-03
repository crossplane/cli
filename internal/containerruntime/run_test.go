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

package containerruntime_test

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	cr "github.com/crossplane/cli/v2/internal/containerruntime"
	"github.com/crossplane/cli/v2/internal/containerruntime/fake"
)

const (
	selfID   = "00000000000000ff"
	deadID   = "dead00000000000a"
	dead2ID  = "dead00000000000b"
	orphanID = "dead00000000000c"
	futureID = "dead00000000000d"
	liveID   = "1111111111111111"
)

const (
	running = container.StateRunning
	exited  = container.StateExited
)

// host is an in-memory Docker daemon behind a fake.Client, holding containers,
// found by ID or name, and networks. It behaves as the daemon
// does wherever the difference would change what is left: create refuses a
// name in use; listing applies label filters (a resource matches only if it
// carries every one) and, without All, lists only running containers; a
// running container is only removed with Force; stopping a container leaves
// it exited; and a network is not removed while a running container is
// attached to it.
type host struct {
	containers []container.Summary
	networks   []network.Summary

	// raced, when set, is created under its name by another run just before
	// this run's first create, so that create finds the name taken.
	raced *container.Summary
	// cannotRemove names the container the daemon fails to remove.
	cannotRemove string
	// vanished holds IDs that are listed but gone by the time they are
	// stopped or removed, as when a concurrent reaper got there first.
	vanished map[string]bool
}

// client is a Docker client of the host.
func (h *host) client() *fake.Client {
	return &fake.Client{
		MockContainerInspect: h.containerInspect,
		MockContainerCreate:  h.containerCreate,
		MockContainerStart:   h.containerStart,
		MockContainerList:    h.containerList,
		MockContainerStop:    h.containerStop,
		MockContainerRemove:  h.containerRemove,
		MockNetworkList:      h.networkList,
		MockNetworkRemove:    h.networkRemove,
	}
}

// newClient is a RunClient factory for the host.
func (h *host) newClient() (cr.RunClient, error) {
	return h.client(), nil
}

// left is what is left on the host: each container's state, and the
// networks.
func (h *host) left() (map[string]container.ContainerState, []string) {
	cs := make(map[string]container.ContainerState, len(h.containers))
	for _, c := range h.containers {
		cs[c.ID] = c.State
	}

	ns := make([]string, 0, len(h.networks))
	for _, n := range h.networks {
		ns = append(ns, n.Name)
	}

	return cs, ns
}

// named reports whether container c is named name.
func named(c container.Summary, name string) bool {
	return slices.Contains(c.Names, "/"+name)
}

// container returns the index of the container with ID or name id.
func (h *host) container(id string) (int, error) {
	i := slices.IndexFunc(h.containers, func(c container.Summary) bool { return c.ID == id || named(c, id) })

	switch {
	case i < 0:
		return i, errors.Wrapf(errdefs.ErrNotFound, "no such container: %s", id)
	case h.vanished[id]:
		h.containers = slices.Delete(h.containers, i, i+1)
		return -1, errors.Wrapf(errdefs.ErrNotFound, "no such container: %s", id)
	}

	return i, nil
}

func (h *host) containerInspect(_ context.Context, id string, _ client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	i, err := h.container(id)
	if err != nil {
		return client.ContainerInspectResult{}, err
	}

	c := h.containers[i]

	return client.ContainerInspectResult{Container: container.InspectResponse{
		ID:              c.ID,
		Name:            c.Names[0],
		State:           &container.State{Running: c.State == running},
		Config:          &container.Config{Labels: c.Labels},
		NetworkSettings: &container.NetworkSettings{Networks: c.NetworkSettings.Networks},
	}}, nil
}

func (h *host) containerCreate(_ context.Context, o client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	if h.raced != nil {
		h.containers = append(h.containers, *h.raced)
		h.raced = nil
	}

	if slices.ContainsFunc(h.containers, func(c container.Summary) bool { return named(c, o.Name) }) {
		return client.ContainerCreateResult{}, errors.Wrapf(errdefs.ErrConflict, "container name %q is already in use", o.Name)
	}

	var nets []string
	if o.NetworkingConfig != nil {
		nets = slices.Collect(maps.Keys(o.NetworkingConfig.EndpointsConfig))
	}

	c := ctr("new-"+o.Name, container.StateCreated, o.Config.Labels, nets...)
	c.Names = []string{"/" + o.Name}
	h.containers = append(h.containers, c)

	return client.ContainerCreateResult{ID: c.ID}, nil
}

func (h *host) containerStart(_ context.Context, id string, _ client.ContainerStartOptions) (client.ContainerStartResult, error) {
	i, err := h.container(id)
	if err != nil {
		return client.ContainerStartResult{}, err
	}

	h.containers[i].State = running

	return client.ContainerStartResult{}, nil
}

func (h *host) containerList(_ context.Context, opts client.ContainerListOptions) (client.ContainerListResult, error) {
	var res client.ContainerListResult

	for _, c := range h.containers {
		ok, err := matches(opts.Filters, c.Labels)
		if err != nil {
			return client.ContainerListResult{}, err
		}

		if ok && (opts.All || c.State == running) {
			res.Items = append(res.Items, c)
		}
	}

	return res, nil
}

func (h *host) containerStop(_ context.Context, id string, _ client.ContainerStopOptions) (client.ContainerStopResult, error) {
	i, err := h.container(id)
	if err != nil {
		return client.ContainerStopResult{}, err
	}

	h.containers[i].State = exited

	return client.ContainerStopResult{}, nil
}

func (h *host) containerRemove(_ context.Context, id string, opts client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	if id == h.cannotRemove {
		return client.ContainerRemoveResult{}, errors.New("daemon says no")
	}

	i, err := h.container(id)
	if err != nil {
		return client.ContainerRemoveResult{}, err
	}

	if h.containers[i].State == running && !opts.Force {
		return client.ContainerRemoveResult{}, errors.Wrapf(errdefs.ErrConflict, "container %s is running: stop it or force the removal", id)
	}

	h.containers = slices.Delete(h.containers, i, i+1)

	return client.ContainerRemoveResult{}, nil
}

func (h *host) networkList(_ context.Context, opts client.NetworkListOptions) (client.NetworkListResult, error) {
	var res client.NetworkListResult

	for _, n := range h.networks {
		ok, err := matches(opts.Filters, n.Labels)
		if err != nil {
			return client.NetworkListResult{}, err
		}

		if ok {
			res.Items = append(res.Items, n)
		}
	}

	return res, nil
}

func (h *host) networkRemove(_ context.Context, id string, _ client.NetworkRemoveOptions) (client.NetworkRemoveResult, error) {
	i := slices.IndexFunc(h.networks, func(n network.Summary) bool { return n.ID == id || n.Name == id })
	if i < 0 {
		return client.NetworkRemoveResult{}, errors.Wrapf(errdefs.ErrNotFound, "network %s not found", id)
	}

	n := h.networks[i]

	switch {
	case h.vanished[n.ID]:
		h.networks = slices.Delete(h.networks, i, i+1)
		return client.NetworkRemoveResult{}, errors.Wrapf(errdefs.ErrNotFound, "network %s not found", id)
	case slices.ContainsFunc(h.containers, func(c container.Summary) bool {
		_, attached := c.NetworkSettings.Networks[n.Name]
		return attached && c.State == running
	}):
		return client.NetworkRemoveResult{}, errors.Wrapf(errdefs.ErrConflict, "error while removing network: network %s has active endpoints", n.Name)
	}

	h.networks = slices.Delete(h.networks, i, i+1)

	return client.NetworkRemoveResult{}, nil
}

// matches reports whether labels satisfy filters as the daemon's label filter
// does. It fails a list that uses any other filter term rather than ignore it.
func matches(filters client.Filters, labels map[string]string) (bool, error) {
	for term, values := range filters {
		if term != "label" {
			return false, errors.Errorf("fake Docker daemon does not support the %q list filter", term)
		}

		for v := range values {
			k, want, hasValue := strings.Cut(v, "=")
			if got, ok := labels[k]; !ok || (hasValue && got != want) {
				return false, nil
			}
		}
	}

	return true, nil
}

// ctr is a container named id.
func ctr(id string, state container.ContainerState, labels map[string]string, networks ...string) container.Summary {
	c := container.Summary{ID: id, Names: []string{"/" + id}, State: state, Labels: labels, NetworkSettings: &container.NetworkSettingsSummary{
		Networks: map[string]*network.EndpointSettings{},
	}}
	for _, n := range networks {
		c.NetworkSettings.Networks[n] = &network.EndpointSettings{}
	}

	return c
}

func net(name string, labels map[string]string) network.Summary {
	return network.Summary{Network: network.Network{ID: name, Name: name, Labels: labels}}
}

// hostContainers is a Docker host shared by three dead runs, a live run, this
// run, and things no run owns. Each run's render-domain containers are
// attached to its network.
func hostContainers() []container.Summary {
	return []container.Summary{
		// The dead run: a stopped Remove function, the render container that
		// was running when it died (no cleanup label), a running Stop
		// function, and a running Remove diff server (simulate domain).
		ctr("dead-fn", exited, cr.ContainerLabels(cr.DomainRender, deadID, cr.CleanupRemove), "dead-net"),
		ctr("dead-render", running, cr.ManagedLabels(cr.DomainRender, deadID), "dead-net"),
		ctr("dead-stop", running, cr.ContainerLabels(cr.DomainRender, deadID, cr.CleanupStop), "dead-net"),
		ctr("dead-server", running, cr.ContainerLabels(cr.DomainSimulate, deadID, cr.CleanupRemove)),

		// A second dead run.
		ctr("dead2-fn", exited, cr.ContainerLabels(cr.DomainRender, dead2ID, cr.CleanupRemove), "dead2-net"),

		// A dead run whose user asked to keep a function and a diff server.
		ctr("orphan-fn", running, cr.ContainerLabels(cr.DomainRender, orphanID, cr.CleanupOrphan), "orphan-net"),
		ctr("orphan-server", running, cr.ContainerLabels(cr.DomainSimulate, orphanID, cr.CleanupOrphan)),

		// A live run, and this run.
		ctr("live-fn", running, cr.ContainerLabels(cr.DomainRender, liveID, cr.CleanupRemove), "live-net"),
		ctr("self-fn", running, cr.ContainerLabels(cr.DomainRender, selfID, cr.CleanupRemove), "self-net"),

		// Not ours to judge: no labels, the dead run's session without
		// crossplane's management, its labels split across our two domains,
		// and its labels under a domain that isn't ours.
		ctr("unlabelled", running, nil),
		ctr("session-only", running, map[string]string{cr.DomainRender + "/session": deadID}),
		ctr("mixed-domains", running, map[string]string{
			cr.DomainRender + "/managed-by": cr.LabelValueManagedBy,
			cr.DomainSimulate + "/session":  deadID,
		}),
		ctr("other-domain", running, cr.ManagedLabels("example.org", deadID)),
	}
}

func hostNetworks() []network.Summary {
	return []network.Summary{
		net("dead-net", cr.ManagedLabels(cr.DomainRender, deadID)),
		net("dead2-net", cr.ManagedLabels(cr.DomainRender, dead2ID)),
		net("orphan-net", cr.ManagedLabels(cr.DomainRender, orphanID)),
		net("live-net", cr.ManagedLabels(cr.DomainRender, liveID)),
		net("self-net", cr.ManagedLabels(cr.DomainRender, selfID)),

		// Not ours to judge: one the user supplied with
		// CROSSPLANE_DOCKER_NETWORK (never labelled), and one with the dead
		// run's session but not crossplane's management.
		net("user-net", nil),
		net("session-only-net", map[string]string{cr.DomainRender + "/session": deadID}),
	}
}

// untouched is what reaping dead runs never changes.
var (
	untouchedContainers = map[string]container.ContainerState{
		"orphan-fn": running, "orphan-server": running,
		"live-fn": running, "self-fn": running,
		"unlabelled": running, "session-only": running, "mixed-domains": running, "other-domain": running,
	}
	untouchedNetworks = []string{"live-net", "self-net", "user-net", "session-only-net"}

	// deadLeft and dead2Left are what each dead run leaves while unreaped.
	deadLeft = map[string]container.ContainerState{
		"dead-fn": exited, "dead-render": running, "dead-stop": running, "dead-server": running,
	}
	dead2Left = map[string]container.ContainerState{"dead2-fn": exited}

	// deadReaped is what the dead run leaves once reaped: its Stop function,
	// stopped.
	deadReaped = map[string]container.ContainerState{"dead-stop": exited}
)

func union[K comparable, V any](ms ...map[K]V) map[K]V {
	out := map[K]V{}
	for _, m := range ms {
		maps.Copy(out, m)
	}

	return out
}

// startRun acquires a run the way a live process does, holding its lease
// until the test ends.
func startRun(t *testing.T, dir, id string, h *host) *cr.Run {
	t.Helper()

	r := cr.NewRun(dir, id, true, logging.NewNopLogger(), cr.WithRunClient(h.newClient))
	_ = r.Acquire(t.Context())

	t.Cleanup(func() { _ = r.Release(context.Background(), false, "") })

	return r
}

// leaveLease leaves the lease file of a run that died: it exists, but no
// process holds its lock.
func leaveLease(t *testing.T, dir, id string) {
	t.Helper()

	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, id+".lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("ReadDir(%q): %v", dir, err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}

	return names
}

var unordered = []cmp.Option{
	cmpopts.SortSlices(func(a, b string) bool { return a < b }),
	cmpopts.EquateEmpty(),
}

func skipWithoutLeases(t *testing.T) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("run leases are not supported on Windows")
	}
}

func TestNewSessionID(t *testing.T) {
	format := regexp.MustCompile(`^[0-9a-f]{16}$`)

	a, b := cr.NewSessionID(), cr.NewSessionID()
	for _, id := range []string{a, b} {
		if !format.MatchString(id) {
			t.Errorf("NewSessionID() = %q, want 16 lowercase hex characters: reapers skip any other lease name", id)
		}
	}

	if a == b {
		t.Errorf("NewSessionID() returned %q twice", a)
	}
}

func TestRunAcquire(t *testing.T) {
	skipWithoutLeases(t)

	type want struct {
		Containers map[string]container.ContainerState
		Networks   []string
		Files      []string
		Err        bool
	}

	tests := map[string]struct {
		reason string

		dead       []string // runs that died, leaving their lease behind
		live       []string // runs that are still running
		otherFiles []string // files in the lease directory that are not leases
		host       host     // resources to add to hostContainers and hostNetworks, and failures

		want want
	}{
		"NoDeadRunRemovesNothing": {
			reason: "With no dead run to reclaim, acquiring a run changes nothing.",
			live:   []string{liveID},
			want: want{
				Containers: union(untouchedContainers, deadLeft, dead2Left),
				Networks:   append(slices.Clone(untouchedNetworks), "dead-net", "dead2-net", "orphan-net"),
				Files:      []string{liveID + ".lock", selfID + ".lock"},
			},
		},
		"ReapsOnlyTheDeadRunsResourcesPerPolicy": {
			reason: "Every dead run's Remove and unlabelled containers, in both domains, are removed, its Stop one " +
				"is stopped, then its network and lease go. A live run's, this run's, and anything without both " +
				"of one domain's labels for a dead run are left alone.",
			dead: []string{deadID, dead2ID},
			live: []string{liveID},
			want: want{
				Containers: union(untouchedContainers, deadReaped),
				Networks:   append(slices.Clone(untouchedNetworks), "orphan-net"),
				Files:      []string{liveID + ".lock", selfID + ".lock"},
			},
		},
		"OrphanIsKeptWithItsNetwork": {
			reason: "A container the user asked to keep is left running, and its network with it. That counts as " +
				"reclaimed, so the lease is dropped.",
			dead: []string{orphanID},
			want: want{
				Containers: union(untouchedContainers, deadLeft, dead2Left),
				Networks:   append(slices.Clone(untouchedNetworks), "dead-net", "dead2-net", "orphan-net"),
				Files:      []string{selfID + ".lock"},
			},
		},
		"StoppedOrphanKeepsItsNetwork": {
			reason: "Docker would remove the network of a stopped Orphan container, which then could not start " +
				"again, so the network is left with it, and the lease is dropped.",
			dead: []string{futureID},
			host: host{
				containers: []container.Summary{
					ctr("stopped-orphan", exited, cr.ContainerLabels(cr.DomainRender, futureID, cr.CleanupOrphan), "future-net"),
				},
				networks: []network.Summary{net("future-net", cr.ManagedLabels(cr.DomainRender, futureID))},
			},
			want: want{
				Containers: union(untouchedContainers, deadLeft, dead2Left, map[string]container.ContainerState{"stopped-orphan": exited}),
				Networks:   append(slices.Clone(untouchedNetworks), "dead-net", "dead2-net", "orphan-net", "future-net"),
				Files:      []string{selfID + ".lock"},
			},
		},
		"UnknownCleanupPolicyIsKept": {
			reason: "A container whose cleanup label holds a value this version does not know may have been asked " +
				"to be kept, so it is left alone like Orphan, with its network, and the lease is dropped.",
			dead: []string{futureID},
			host: host{
				containers: []container.Summary{
					ctr("future-fn", running, cr.ContainerLabels(cr.DomainRender, futureID, "orphan"), "future-net"),
				},
				networks: []network.Summary{net("future-net", cr.ManagedLabels(cr.DomainRender, futureID))},
			},
			want: want{
				Containers: union(untouchedContainers, deadLeft, dead2Left, map[string]container.ContainerState{"future-fn": running}),
				Networks:   append(slices.Clone(untouchedNetworks), "dead-net", "dead2-net", "orphan-net", "future-net"),
				Files:      []string{selfID + ".lock"},
			},
		},
		"ContainerRemovalFailureKeepsNetworkAndLease": {
			reason: "A container that could not be removed may still hold its network, and the next run should " +
				"retry, so the network and lease are kept and Acquire reports it.",
			dead: []string{deadID},
			host: host{cannotRemove: "dead-render"},
			want: want{
				Containers: union(untouchedContainers, deadReaped, map[string]container.ContainerState{"dead-render": running}, dead2Left),
				Networks:   append(slices.Clone(untouchedNetworks), "dead-net", "dead2-net", "orphan-net"),
				Files:      []string{deadID + ".lock", selfID + ".lock"},
				Err:        true,
			},
		},
		"ResourcesAlreadyGoneCountAsReaped": {
			reason: "A container or network that no longer exists by the time it is removed, e.g. removed by a " +
				"concurrent reaper, is already reclaimed.",
			dead: []string{deadID},
			host: host{vanished: map[string]bool{"dead-fn": true, "dead-stop": true, "dead-net": true}},
			want: want{
				Containers: union(untouchedContainers, dead2Left),
				Networks:   append(slices.Clone(untouchedNetworks), "dead2-net", "orphan-net"),
				Files:      []string{selfID + ".lock"},
			},
		},
		"IgnoresFilesThatAreNotLeases": {
			reason: "Only <16 lowercase hex>.lock files are leases; anything else in the directory is never acted on.",
			otherFiles: []string{
				"notes.txt",
				"not-a-run-id.lock",
				strings.ToUpper(deadID) + ".lock",
				deadID + "-123.tmp",
			},
			want: want{
				Containers: union(untouchedContainers, deadLeft, dead2Left),
				Networks:   append(slices.Clone(untouchedNetworks), "dead-net", "dead2-net", "orphan-net"),
				Files: []string{
					deadID + "-123.tmp", strings.ToUpper(deadID) + ".lock", "not-a-run-id.lock", "notes.txt",
					selfID + ".lock",
				},
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()

			h := tt.host
			h.containers = append(hostContainers(), tt.host.containers...)
			h.networks = append(hostNetworks(), tt.host.networks...)

			// Live runs started before any run died, so had nothing to reclaim.
			for _, id := range tt.live {
				startRun(t, dir, id, &h)
			}

			for _, id := range tt.dead {
				leaveLease(t, dir, id)
			}

			for _, f := range tt.otherFiles {
				if err := os.WriteFile(filepath.Join(dir, f), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}

			r := cr.NewRun(dir, selfID, true, logging.NewNopLogger(), cr.WithRunClient(h.newClient))
			err := r.Acquire(t.Context())
			t.Cleanup(func() { _ = r.Release(context.Background(), false, "") })

			got := want{Files: dirEntries(t, dir), Err: err != nil}
			got.Containers, got.Networks = h.left()

			if diff := cmp.Diff(tt.want, got, unordered...); diff != "" {
				t.Errorf("\n%s\nAcquire(...): -want, +got:\n%s", tt.reason, diff)
			}
		})
	}
}

// TestRunRemovesStaleTempLeases checks that a reaper removes the temporary
// lease file a run that died while taking its lease left, once it is old
// enough that no live run can still be renaming it, and nothing else.
func TestRunRemovesStaleTempLeases(t *testing.T) {
	skipWithoutLeases(t)

	dir := t.TempDir()
	stale := time.Now().Add(-time.Hour)

	files := map[string]bool{ // name -> stale
		deadID + "-123.tmp":  true,
		dead2ID + "-456.tmp": false,
		"notes-123.tmp":      true,
	}
	for f, old := range files {
		p := filepath.Join(dir, f)
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if old {
			if err := os.Chtimes(p, stale, stale); err != nil {
				t.Fatal(err)
			}
		}
	}

	h := &host{}
	startRun(t, dir, selfID, h)

	want := []string{dead2ID + "-456.tmp", "notes-123.tmp", selfID + ".lock"}
	if diff := cmp.Diff(want, dirEntries(t, dir), unordered...); diff != "" {
		t.Errorf("Acquire(...): lease directory, -want, +got:\n%s", diff)
	}
}

// TestRunConcurrentReapersOfOneDeadRun checks that two runs reaping one dead
// run at once both succeed, leaving the dead run reclaimed once, by one of
// them, and its lease gone. A flock conflicts per open file, so two goroutines contend for
// a lease as two processes do. Repeated, since the interleaving varies.
func TestRunConcurrentReapersOfOneDeadRun(t *testing.T) {
	skipWithoutLeases(t)

	for range 50 {
		dir := t.TempDir()
		leaveLease(t, dir, deadID)

		h := &host{
			containers: []container.Summary{
				ctr("dead-fn", running, cr.ContainerLabels(cr.DomainRender, deadID, cr.CleanupRemove), "dead-net"),
				ctr("dead-stop", running, cr.ContainerLabels(cr.DomainRender, deadID, cr.CleanupStop), "dead-net"),
				ctr("dead-server", running, cr.ContainerLabels(cr.DomainSimulate, deadID, cr.CleanupRemove)),
			},
			networks: []network.Summary{net("dead-net", cr.ManagedLabels(cr.DomainRender, deadID))},
		}
		var mu sync.Mutex
		finished := map[string]int{}
		newClient := func() (cr.RunClient, error) { return serialized(&mu, h.client(), finished), nil }

		ids := []string{cr.NewSessionID(), cr.NewSessionID()}
		errs := make([]error, len(ids))
		var wg sync.WaitGroup
		for i, id := range ids {
			r := cr.NewRun(dir, id, true, logging.NewNopLogger(), cr.WithRunClient(newClient))
			t.Cleanup(func() { _ = r.Release(context.Background(), false, "") })
			wg.Go(func() { errs[i] = r.Acquire(t.Context()) })
		}
		wg.Wait()

		type state struct {
			Errs       []error
			Containers map[string]container.ContainerState
			Networks   []string
			Files      []string
			Finished   map[string]int
		}
		got := state{Errs: errs, Files: dirEntries(t, dir), Finished: finished}
		got.Containers, got.Networks = h.left()
		want := state{
			Errs:       []error{nil, nil},
			Containers: map[string]container.ContainerState{"dead-stop": exited},
			Files:      []string{ids[0] + ".lock", ids[1] + ".lock"},
			// Only one reaper takes the dead run's lease and reclaims it.
			Finished: map[string]int{"dead-fn": 1, "dead-stop": 1, "dead-server": 1, "dead-net": 1},
		}
		if diff := cmp.Diff(want, got, append(unordered, cmpopts.EquateErrors())...); diff != "" {
			t.Fatalf("two concurrent Acquire(...): -want, +got:\n%s", diff)
		}
	}
}

// serialized makes a fake client's calls run one at a time under mu, for a
// host shared by concurrent runs, and counts in finished each container or
// network a stop or remove succeeded on.
func serialized(mu *sync.Mutex, c *fake.Client, finished map[string]int) *fake.Client {
	count := func(id string, err error) error {
		if err == nil {
			finished[id]++
		}
		return err
	}

	return &fake.Client{
		MockContainerList: func(ctx context.Context, o client.ContainerListOptions) (client.ContainerListResult, error) {
			mu.Lock()
			defer mu.Unlock()
			return c.MockContainerList(ctx, o)
		},
		MockContainerStop: func(ctx context.Context, id string, o client.ContainerStopOptions) (client.ContainerStopResult, error) {
			mu.Lock()
			defer mu.Unlock()
			res, err := c.MockContainerStop(ctx, id, o)
			return res, count(id, err)
		},
		MockContainerRemove: func(ctx context.Context, id string, o client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
			mu.Lock()
			defer mu.Unlock()
			res, err := c.MockContainerRemove(ctx, id, o)
			return res, count(id, err)
		},
		MockNetworkList: func(ctx context.Context, o client.NetworkListOptions) (client.NetworkListResult, error) {
			mu.Lock()
			defer mu.Unlock()
			return c.MockNetworkList(ctx, o)
		},
		MockNetworkRemove: func(ctx context.Context, id string, o client.NetworkRemoveOptions) (client.NetworkRemoveResult, error) {
			mu.Lock()
			defer mu.Unlock()
			res, err := c.MockNetworkRemove(ctx, id, o)
			return res, count(id, err)
		},
	}
}

func TestRunWithoutLease(t *testing.T) {
	skipWithoutLeases(t)

	t.Run("SuppliedIDIsNeverLeased", func(t *testing.T) {
		dir := t.TempDir()
		h := &host{containers: hostContainers(), networks: hostNetworks()}
		leaveLease(t, dir, deadID)

		// A supplied ID that happens to be valid hex is still not leased.
		r := cr.NewRun(dir, selfID, false, logging.NewNopLogger(), cr.WithRunClient(h.newClient))
		if err := r.Acquire(t.Context()); err != nil {
			t.Fatalf("Acquire(...): unexpected error: %v", err)
		}

		if got := dirEntries(t, dir); len(got) != 0 {
			t.Errorf("Acquire(...) of a supplied ID: lease directory holds %v, want nothing (no lease of its own, dead lease reaped)", got)
		}

		if err := r.Release(t.Context(), true, ""); err != nil {
			t.Errorf("Release(...): unexpected error: %v", err)
		}
	})

	t.Run("NoLeaseDirDisablesLeaseAndReap", func(t *testing.T) {
		// A dead run's lease in the working directory, which an empty lease
		// directory would resolve to, is left alone, along with its resources.
		dir := t.TempDir()
		t.Chdir(dir)
		leaveLease(t, dir, deadID)
		h := &host{containers: hostContainers(), networks: hostNetworks()}

		r := cr.NewRun("", cr.NewSessionID(), true, logging.NewNopLogger(), cr.WithRunClient(h.newClient))
		if err := r.Acquire(t.Context()); err != nil {
			t.Fatalf("Acquire(...): unexpected error: %v", err)
		}

		if err := r.Release(t.Context(), true, ""); err != nil {
			t.Errorf("Release(...): unexpected error: %v", err)
		}

		if got := dirEntries(t, dir); !slices.Equal(got, []string{deadID + ".lock"}) {
			t.Errorf("Acquire/Release(...) without a lease dir: working directory holds %v, want only the dead lease", got)
		}

		if got, _ := h.left(); got["dead-fn"] != exited {
			t.Errorf("Acquire/Release(...) without a lease dir: reaped a dead run's container, left %v", got)
		}
	})
}

func TestRunRelease(t *testing.T) {
	skipWithoutLeases(t)

	type want struct {
		Networks []string
		Files    []string
		Err      bool
	}

	tests := map[string]struct {
		reason string
		clean  bool
		sweep  string
		host   host
		want   want
	}{
		"CleanDropsLease": {
			reason: "A run that removed everything drops its lease file, and leaves networks alone without a sweep.",
			clean:  true,
			want: want{
				Networks: []string{"live-net", "self-net", "self-net-2", "self-sim-net"},
			},
		},
		"NotCleanKeepsLease": {
			reason: "A run that left something behind keeps its lease file, unlocked, so the next run reaps it.",
			want: want{
				Networks: []string{"live-net", "self-net", "self-net-2", "self-sim-net"},
				Files:    []string{selfID + ".lock"},
			},
		},
		"SweepRemovesOnlyThisRunsNetworksInItsDomain": {
			reason: "A sweep removes this run's networks in the domain it is given, and no other run's or domain's.",
			clean:  true,
			sweep:  cr.DomainRender,
			want: want{
				Networks: []string{"live-net", "self-sim-net"},
			},
		},
		"SweepExcusesNetworkOfKeptContainer": {
			reason: "A network that this run's Orphan or unknown-policy container is attached to, running or stopped, is left, and does not keep the lease.",
			clean:  true,
			sweep:  cr.DomainRender,
			host: host{containers: []container.Summary{
				ctr("orphan", exited, cr.ContainerLabels(cr.DomainRender, selfID, cr.CleanupOrphan), "self-net"),
				ctr("future", running, cr.ContainerLabels(cr.DomainRender, selfID, cr.Cleanup("Future")), "self-net-2"),
			}},
			want: want{
				Networks: []string{"live-net", "self-net", "self-net-2", "self-sim-net"},
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()

			h := tt.host
			h.networks = []network.Summary{
				net("self-net", cr.ManagedLabels(cr.DomainRender, selfID)),
				net("self-net-2", cr.ManagedLabels(cr.DomainRender, selfID)),
				net("self-sim-net", cr.ManagedLabels(cr.DomainSimulate, selfID)),
				net("live-net", cr.ManagedLabels(cr.DomainRender, liveID)),
			}

			r := cr.NewRun(dir, selfID, true, logging.NewNopLogger(), cr.WithRunClient(h.newClient))
			if err := r.Acquire(t.Context()); err != nil {
				t.Fatalf("Acquire(...): unexpected error: %v", err)
			}

			err := r.Release(t.Context(), tt.clean, tt.sweep)

			_, nets := h.left()
			got := want{Networks: nets, Files: dirEntries(t, dir), Err: err != nil}

			if diff := cmp.Diff(tt.want, got, unordered...); diff != "" {
				t.Errorf("\n%s\nRelease(...): -want, +got:\n%s", tt.reason, diff)
			}
		})
	}
}

// TestRunReclaimsARunThatDied pins reaping end to end, with generated IDs and
// the leases runs really take: a later run leaves a run's resources alone
// while that run is alive, and removes them once its lock is gone.
func TestRunReclaimsARunThatDied(t *testing.T) {
	skipWithoutLeases(t)

	type left struct {
		Containers map[string]container.ContainerState
		Networks   []string
	}

	dir := t.TempDir()
	h := &host{}

	first := cr.NewRun(dir, cr.NewSessionID(), true, logging.NewNopLogger(), cr.WithRunClient(h.newClient))
	if err := first.Acquire(t.Context()); err != nil {
		t.Fatalf("first Acquire(...): unexpected error: %v", err)
	}

	h.containers = []container.Summary{ctr("fn", running, cr.ContainerLabels(cr.DomainRender, first.ID(), cr.CleanupRemove), "net")}
	h.networks = []network.Summary{net("net", cr.ManagedLabels(cr.DomainRender, first.ID()))}

	startRun(t, dir, cr.NewSessionID(), h)

	got := map[string]left{}
	got["WhileAlive"] = func() left { c, n := h.left(); return left{c, n} }()

	// The first run dies: its lock is dropped and its lease file stays.
	_ = first.Release(t.Context(), false, "")

	startRun(t, dir, cr.NewSessionID(), h)

	got["AfterItDied"] = func() left { c, n := h.left(); return left{c, n} }()

	want := map[string]left{
		"WhileAlive":  {Containers: map[string]container.ContainerState{"fn": running}, Networks: []string{"net"}},
		"AfterItDied": {},
	}
	if diff := cmp.Diff(want, got, unordered...); diff != "" {
		t.Errorf("what later runs left of a run, -want, +got:\n%s", diff)
	}
}

func TestRunReclaimsInAGlobLikeDir(t *testing.T) {
	skipWithoutLeases(t)

	type left struct {
		Containers map[string]container.ContainerState
		Networks   []string
	}

	// Glob syntax in the lease dir must not hide its leases.
	dir := filepath.Join(t.TempDir(), "[x]", "runs")
	h := &host{}

	dead := cr.NewRun(dir, cr.NewSessionID(), true, logging.NewNopLogger(), cr.WithRunClient(h.newClient))
	if err := dead.Acquire(t.Context()); err != nil {
		t.Fatalf("Acquire(...): unexpected error: %v", err)
	}

	h.containers = []container.Summary{ctr("fn", running, cr.ContainerLabels(cr.DomainRender, dead.ID(), cr.CleanupRemove), "net")}
	h.networks = []network.Summary{net("net", cr.ManagedLabels(cr.DomainRender, dead.ID()))}

	_ = dead.Release(t.Context(), false, "")

	startRun(t, dir, cr.NewSessionID(), h)

	c, n := h.left()
	if diff := cmp.Diff(left{}, left{c, n}, unordered...); diff != "" {
		t.Errorf("what a later run left of a dead run, -want, +got:\n%s", diff)
	}
}
