//go:build integration && unix

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
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	cr "github.com/crossplane/cli/v2/internal/containerruntime"
)

const (
	// busybox is pinned, on the GCR mirror, which avoids Docker Hub's
	// unauthenticated pull limits.
	busybox = "mirror.gcr.io/library/busybox:1.37"

	// labelTest marks every resource this test creates, with a value unique
	// to the test, so its cleanup removes those and nothing else. If the test
	// dies without running its cleanups (a -timeout panic, a SIGKILL), remove
	// what it leaked with
	//
	//	docker rm -f $(docker ps -aq --filter label=containerruntime.crossplane.io/integration-test)
	//	docker network rm $(docker network ls -q --filter label=containerruntime.crossplane.io/integration-test)
	labelTest = "containerruntime.crossplane.io/integration-test"
)

// TestRunReclaimsADeadRunOnDocker pins, against a real Docker daemon, what
// Acquire does to the resources of a run that was SIGKILLed: the containers it
// would have removed are removed, a Stop container is stopped and kept, an
// Orphan container is kept running, a network holding only a stopped
// container is removed, the network of the kept Orphan container is kept, and
// the dead run's lease is dropped. While the run lives, its lease keeps all of
// it. The resources carry the child's session ID but are created here: the
// reaper finds them by label, whichever process created them.
//
// It fails, rather than skips, without a Docker daemon: the integration job
// must have one, and a skip would hide a broken runner. So it fails on a dev
// machine without Docker too.
func TestRunReclaimsADeadRunOnDocker(t *testing.T) {
	ctx := t.Context()

	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatalf("client.New(...): %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })

	if _, err := cli.Ping(ctx, client.PingOptions{}); err != nil {
		t.Fatalf("this test needs a Docker daemon: Ping(...): %v", err)
	}

	pullIfMissing(t, cli, busybox)

	testID := cr.NewSessionID()
	t.Cleanup(func() { removeTestResources(t, cli, testID) })

	dir, id := t.TempDir(), cr.NewSessionID()
	child := startLeaseHelper(t, dir, id)

	render := func(c cr.Cleanup) map[string]string { return cr.ContainerLabels(cr.DomainRender, id, c) }

	net := createNetwork(t, cli, testID, cr.ManagedLabels(cr.DomainRender, id))
	orphanNet := createNetwork(t, cli, testID, cr.ManagedLabels(cr.DomainRender, id))

	remove := runContainer(t, cli, testID, busybox, render(cr.CleanupRemove), net)
	renderer := runContainer(t, cli, testID, busybox, cr.ManagedLabels(cr.DomainRender, id), net)
	stop := runContainer(t, cli, testID, busybox, render(cr.CleanupStop), net)
	simulate := runContainer(t, cli, testID, busybox, cr.ContainerLabels(cr.DomainSimulate, id, cr.CleanupRemove), "")
	orphan := runContainer(t, cli, testID, busybox, render(cr.CleanupOrphan), orphanNet)

	all := []string{remove, renderer, stop, simulate, orphan}

	// While the child lives, its lease keeps everything.
	acquire(t, dir)

	for _, c := range all {
		if !containerRunning(t, cli, c) {
			t.Errorf("container %s of a live run: not running after another run's Acquire, want it untouched", c)
		}
	}

	for _, n := range []string{net, orphanNet} {
		if !networkExists(t, cli, n) {
			t.Errorf("network %s of a live run: removed by another run's Acquire, want it untouched", n)
		}
	}

	if err := child.Process.Kill(); err != nil {
		t.Fatalf("Kill(): %v", err)
	}
	_ = child.Wait()

	acquire(t, dir)

	for _, c := range []string{remove, renderer, simulate} {
		if containerExists(t, cli, c) {
			t.Errorf("container %s of the dead run: still exists after Acquire, want it removed", c)
		}
	}

	switch {
	case !containerExists(t, cli, stop):
		t.Errorf("Stop container %s of the dead run: removed by Acquire, want it stopped and kept", stop)
	case containerRunning(t, cli, stop):
		t.Errorf("Stop container %s of the dead run: still running after Acquire, want it stopped", stop)
	}

	if !containerRunning(t, cli, orphan) {
		t.Errorf("Orphan container %s of the dead run: not running after Acquire, want it untouched", orphan)
	}

	if networkExists(t, cli, net) {
		t.Errorf("network %s of the dead run, holding only a stopped container: still exists after Acquire, want it removed", net)
	}

	if !networkExists(t, cli, orphanNet) {
		t.Errorf("network %s of the dead run's Orphan container: removed by Acquire, want it kept", orphanNet)
	}

	if _, err := os.Stat(cr.LeasePath(dir, id)); !os.IsNotExist(err) {
		t.Errorf("lease file of the dead run: Stat(...) = %v, want it removed", err)
	}
}

// acquire runs a new run's Acquire on the lease directory, then releases it.
func acquire(t *testing.T, dir string) {
	t.Helper()

	r := cr.NewRun(dir, cr.NewSessionID(), true, logging.NewNopLogger())
	if err := r.Acquire(t.Context()); err != nil {
		t.Errorf("Acquire(...): unexpected error: %v", err)
	}

	if err := r.Release(context.Background(), true, ""); err != nil {
		t.Errorf("Release(...): unexpected error: %v", err)
	}
}

// pullIfMissing pulls image ref unless it is on the daemon, so local runs
// stay offline.
func pullIfMissing(t *testing.T, cli *client.Client, ref string) {
	t.Helper()

	_, err := cli.ImageInspect(t.Context(), ref)
	if !errdefs.IsNotFound(err) {
		if err != nil {
			t.Fatalf("ImageInspect(%q): %v", ref, err)
		}
		return
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()

	if err := cr.PullImage(ctx, cli, ref, client.ImagePullOptions{}); err != nil {
		t.Fatalf("PullImage(%q): %v", ref, err)
	}
}

// createNetwork creates a bridge network with labels, marked as the test's.
func createNetwork(t *testing.T, cli *client.Client, testID string, labels map[string]string) string {
	t.Helper()

	name := "crossplane-cli-test-" + cr.NewSessionID()

	l := maps.Clone(labels)
	l[labelTest] = testID

	if _, err := cli.NetworkCreate(t.Context(), name, client.NetworkCreateOptions{Driver: "bridge", Labels: l}); err != nil {
		t.Fatalf("NetworkCreate(%q): %v", name, err)
	}

	return name
}

// runContainer starts a container of image with labels, marked as the test's,
// on network net when it is not empty, and returns its ID.
func runContainer(t *testing.T, cli *client.Client, testID, image string, labels map[string]string, net string) string {
	t.Helper()

	l := maps.Clone(labels)
	l[labelTest] = testID

	init := true
	opts := client.ContainerCreateOptions{
		Config:     &container.Config{Image: image, Cmd: []string{"sleep", "300"}, Labels: l},
		HostConfig: &container.HostConfig{Init: &init},
	}
	if net != "" {
		opts.NetworkingConfig = &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{net: {}}}
	}

	rsp, err := cli.ContainerCreate(t.Context(), opts)
	if err != nil {
		t.Fatalf("ContainerCreate(...): %v", err)
	}

	if _, err := cli.ContainerStart(t.Context(), rsp.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("ContainerStart(%s): %v", rsp.ID, err)
	}

	return rsp.ID
}

// containerExists reports whether container id exists, in any state.
func containerExists(t *testing.T, cli *client.Client, id string) bool {
	t.Helper()

	_, err := cli.ContainerInspect(t.Context(), id, client.ContainerInspectOptions{})
	switch {
	case err == nil:
		return true
	case errdefs.IsNotFound(err):
		return false
	}

	t.Fatalf("ContainerInspect(%s): %v", id, err)

	return false
}

// containerRunning reports whether container id exists and is running.
func containerRunning(t *testing.T, cli *client.Client, id string) bool {
	t.Helper()

	res, err := cli.ContainerInspect(t.Context(), id, client.ContainerInspectOptions{})
	switch {
	case err == nil:
		return res.Container.State != nil && res.Container.State.Running
	case errdefs.IsNotFound(err):
		return false
	}

	t.Fatalf("ContainerInspect(%s): %v", id, err)

	return false
}

// networkExists reports whether network name exists.
func networkExists(t *testing.T, cli *client.Client, name string) bool {
	t.Helper()

	_, err := cli.NetworkInspect(t.Context(), name, client.NetworkInspectOptions{})
	switch {
	case err == nil:
		return true
	case errdefs.IsNotFound(err):
		return false
	}

	t.Fatalf("NetworkInspect(%q): %v", name, err)

	return false
}

// removeTestResources force removes every container, then every network,
// marked as test testID's.
func removeTestResources(t *testing.T, cli *client.Client, testID string) {
	t.Helper()

	ctx := context.Background()
	f := make(client.Filters).Add("label", labelTest+"="+testID)

	cs, err := cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: f})
	if err != nil {
		t.Errorf("cleanup: ContainerList(...): %v", err)
	}

	for _, c := range cs.Items {
		if _, err := cli.ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
			t.Errorf("cleanup: ContainerRemove(%s): %v", c.ID, err)
		}
	}

	ns, err := cli.NetworkList(ctx, client.NetworkListOptions{Filters: f})
	if err != nil {
		t.Errorf("cleanup: NetworkList(...): %v", err)
	}

	for _, n := range ns.Items {
		if _, err := cli.NetworkRemove(ctx, n.ID, client.NetworkRemoveOptions{}); err != nil && !errdefs.IsNotFound(err) {
			t.Errorf("cleanup: NetworkRemove(%s): %v", n.Name, err)
		}
	}
}
