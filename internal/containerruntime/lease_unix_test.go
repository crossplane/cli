//go:build unix

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
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	cr "github.com/crossplane/cli/v2/internal/containerruntime"
)

const (
	envLeaseHelperDir = "CONTAINERRUNTIME_LEASE_HELPER_DIR"
	envLeaseHelperID  = "CONTAINERRUNTIME_LEASE_HELPER_ID"
	leaseHelperReady  = "leased"
)

// TestLeaseHelper is not a test: it runs as startLeaseHelper's child process,
// where it takes a lease, says so on stdout, and holds the lease until its
// stdin closes. The parent holds the other end of stdin, and the kernel
// closes it however the parent dies, so the child never outlives it.
func TestLeaseHelper(t *testing.T) {
	dir, id := os.Getenv(envLeaseHelperDir), os.Getenv(envLeaseHelperID)
	if dir == "" {
		t.Skip("only runs as startLeaseHelper's child process")
	}

	h := &host{}
	r := cr.NewRun(dir, id, true, logging.NewNopLogger(), cr.WithRunClient(h.newClient))
	if err := r.Acquire(t.Context()); err != nil {
		t.Fatalf("Acquire(...): unexpected error: %v", err)
	}

	_, _ = fmt.Fprintln(os.Stdout, leaseHelperReady)

	_, _ = io.Copy(io.Discard, os.Stdin)
}

// TestLeaseAcrossProcesses pins that a lease protects a run from reapers in
// other processes while it lives, and that the kernel gives the lease up when
// the process dies, however it dies. A fake host can't span processes, so this
// process's host is seeded with resources labelled with the child's ID.
func TestLeaseAcrossProcesses(t *testing.T) {
	dir, id := t.TempDir(), cr.NewSessionID()

	cmd := startLeaseHelper(t, dir, id)

	if cr.TryLease(dir, id) {
		t.Fatal("TryLease(...): took the lease of a run that is alive in another process")
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("Kill(): %v", err)
	}

	_ = cmd.Wait()

	h := &host{
		containers: []container.Summary{ctr("fn", running, cr.ContainerLabels(cr.DomainRender, id, cr.CleanupRemove), "net")},
		networks:   []network.Summary{net("net", cr.ManagedLabels(cr.DomainRender, id))},
	}

	r := cr.NewRun(dir, cr.NewSessionID(), true, logging.NewNopLogger(), cr.WithRunClient(h.newClient))
	if err := r.Acquire(t.Context()); err != nil {
		t.Fatalf("Acquire(...) after the child died: unexpected error: %v", err)
	}

	t.Cleanup(func() { _ = r.Release(context.Background(), false, "") })

	cs, ns := h.left()
	if len(cs) != 0 || len(ns) != 0 {
		t.Errorf("Acquire(...) after the child died: left containers %v and networks %v, want none", cs, ns)
	}

	if _, err := os.Stat(cr.LeasePath(dir, id)); !os.IsNotExist(err) {
		t.Errorf("lease file of the dead child: Stat(...) = %v, want it removed", err)
	}
}

// startLeaseHelper runs TestLeaseHelper in a child process that leases run id
// in dir, and returns once the child holds the lease. The child is killed
// when the test ends.
func startLeaseHelper(t *testing.T, dir, id string) *exec.Cmd {
	t.Helper()

	cmd := exec.Command(os.Args[0], "-test.run=^TestLeaseHelper$")
	cmd.Env = append(os.Environ(), envLeaseHelperDir+"="+dir, envLeaseHelperID+"="+id)
	cmd.Stderr = os.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe(): %v", err)
	}

	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe(): %v", err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("Start(): %v", err)
	}

	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	ready := make(chan string, 1)
	go func() {
		s := bufio.NewScanner(out)
		s.Scan()
		ready <- s.Text()
	}()

	select {
	case got := <-ready:
		if got != leaseHelperReady {
			t.Fatalf("child process: got %q before stdout ended, want %q", got, leaseHelperReady)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("child process: did not print %q within 30s", leaseHelperReady)
	}

	return cmd
}
