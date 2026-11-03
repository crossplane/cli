//go:build integration

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
	"testing"

	"github.com/moby/moby/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	"github.com/crossplane/cli/v2/internal/containerruntime"
)

// TestSessionNetworkOnDocker pins, against a real Docker daemon, that a
// Session's Start with no functions creates its network on the daemon and its
// Close removes it. Both the engine's cleanup and Close's sweep remove it, so
// this does not tell which one did. It needs no image. Like the
// containerruntime integration test, it fails rather than skips without a
// Docker daemon.
//
// If the test dies without its cleanups (a -timeout panic, a SIGKILL), remove
// the network it leaked, using the session ID it logged, with
//
//	docker network rm $(docker network ls -q --filter name=crossplane-render- --filter label=render.crossplane.io/session=<id>)
func TestSessionNetworkOnDocker(t *testing.T) {
	ctx := t.Context()

	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatalf("client.New(...): %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })

	if _, err := cli.Ping(ctx, client.PingOptions{}); err != nil {
		t.Fatalf("this test needs a Docker daemon: Ping(...): %v", err)
	}

	s := NewSession(EngineFlags{}, logging.NewNopLogger(), WithLeaseDir(t.TempDir()), WithDockerClient(cli))
	t.Cleanup(func() { _ = s.Close(ctx) })
	t.Logf("session %s", s.ID())

	if _, err := s.Start(ctx, nil); err != nil {
		t.Fatalf("Start(ctx, nil): %v", err)
	}

	f := make(client.Filters)
	for k, v := range containerruntime.ManagedLabels(containerruntime.DomainRender, s.ID()) {
		f = f.Add("label", k+"="+v)
	}

	ns, err := cli.NetworkList(ctx, client.NetworkListOptions{Filters: f})
	if err != nil {
		t.Fatalf("NetworkList(...): %v", err)
	}
	if len(ns.Items) != 1 {
		t.Fatalf("networks labelled with the session's ID after Start: got %d, want 1", len(ns.Items))
	}

	if err := s.Close(ctx); err != nil {
		t.Errorf("Close(...): %v", err)
	}

	ns, err = cli.NetworkList(ctx, client.NetworkListOptions{Filters: f})
	if err != nil {
		t.Fatalf("NetworkList(...): %v", err)
	}
	if len(ns.Items) != 0 {
		t.Errorf("networks labelled with the session's ID after Close: got %d, want 0", len(ns.Items))
	}
}
