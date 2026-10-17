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

package diffserver

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	diffv1alpha1 "github.com/crossplane/upjet/v2/proto/diff/v1alpha1"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	runtimexpkg "github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xpkg/parser"

	"github.com/crossplane/cli/v2/internal/containerruntime/fake"
)

const (
	pkgWithCapability = `---
apiVersion: meta.pkg.crossplane.io/v1
kind: Provider
metadata:
  name: provider-aws-ec2
spec:
  capabilities:
  - SafeStart
  - DiffServer
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: vpcs.ec2.aws.upbound.io
spec:
  group: ec2.aws.upbound.io
  names:
    kind: VPC
    plural: vpcs
  scope: Cluster
  versions:
  - name: v1beta1
    served: true
    storage: true
    schema:
      openAPIV3Schema:
        type: object
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: vpcs.ec2.aws.m.upbound.io
spec:
  group: ec2.aws.m.upbound.io
  names:
    kind: VPC
    plural: vpcs
  scope: Namespaced
  versions:
  - name: v1beta1
    served: true
    storage: true
    schema:
      openAPIV3Schema:
        type: object
`

	pkgWithoutCapability = `---
apiVersion: meta.pkg.crossplane.io/v1
kind: Provider
metadata:
  name: provider-aws-s3
spec:
  capabilities:
  - SafeStart
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: buckets.s3.aws.upbound.io
spec:
  group: s3.aws.upbound.io
  names:
    kind: Bucket
    plural: buckets
  scope: Cluster
  versions:
  - name: v1beta1
    served: true
    storage: true
    schema:
      openAPIV3Schema:
        type: object
`
)

func parsePackage(t *testing.T, yamlDoc, digest string) *runtimexpkg.Package {
	t.Helper()

	metaScheme, err := runtimexpkg.BuildMetaScheme()
	if err != nil {
		t.Fatalf("cannot build meta scheme: %v", err)
	}
	objScheme, err := runtimexpkg.BuildObjectScheme()
	if err != nil {
		t.Fatalf("cannot build object scheme: %v", err)
	}

	pkg, err := parser.New(metaScheme, objScheme).Parse(context.Background(), io.NopCloser(strings.NewReader(yamlDoc)))
	if err != nil {
		t.Fatalf("cannot parse test package: %v", err)
	}
	return &runtimexpkg.Package{Package: pkg, Digest: digest}
}

// fakePkgClient implements the runtime xpkg.Client interface with canned
// packages.
type fakePkgClient struct {
	pkgs map[string]*runtimexpkg.Package
}

func (f *fakePkgClient) Get(_ context.Context, ref string, _ ...runtimexpkg.GetOption) (*runtimexpkg.Package, error) {
	p, ok := f.pkgs[ref]
	if !ok {
		return nil, status.Error(codes.NotFound, "no such package")
	}
	return p, nil
}

func (f *fakePkgClient) ListVersions(_ context.Context, _ string, _ ...runtimexpkg.GetOption) ([]string, error) {
	return nil, nil
}

// fakeDocker fakes container lifecycle at the Docker client level, publishing
// every container's diff server port on addr.
type fakeDocker struct {
	mu       sync.Mutex
	addr     string            // host:port every container's port binds to
	existing map[string]string // name -> id of pre-existing containers
	known    map[string]bool   // ids inspect resolves port bindings for
	created  []string          // names passed to ContainerCreate
	stopped  []string          // ids passed to ContainerStop
	removed  []string          // ids passed to ContainerRemove
}

func newFakeDocker(addr string, existing map[string]string) *fakeDocker {
	f := &fakeDocker{addr: addr, existing: existing, known: map[string]bool{}}
	for _, id := range existing {
		f.known[id] = true
	}
	return f
}

func (f *fakeDocker) client() *fake.Client {
	return &fake.Client{
		MockContainerInspect: func(_ context.Context, ref string, _ client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if id, ok := f.existing[ref]; ok {
				return client.ContainerInspectResult{Container: container.InspectResponse{ID: id}}, nil
			}
			if f.known[ref] {
				host, port, err := net.SplitHostPort(f.addr)
				if err != nil {
					return client.ContainerInspectResult{}, err
				}
				p := network.MustParsePort(fmt.Sprintf("%d/tcp", containerPort))
				return client.ContainerInspectResult{Container: container.InspectResponse{
					ID: ref,
					NetworkSettings: &container.NetworkSettings{
						Ports: network.PortMap{p: []network.PortBinding{{
							HostIP:   netip.MustParseAddr(host),
							HostPort: port,
						}}},
					},
				}}, nil
			}
			return client.ContainerInspectResult{}, errdefs.ErrNotFound
		},
		MockContainerCreate: func(_ context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			id := "new-" + options.Name
			f.created = append(f.created, options.Name)
			f.known[id] = true
			return client.ContainerCreateResult{ID: id}, nil
		},
		MockContainerStart: func(_ context.Context, _ string, _ client.ContainerStartOptions) (client.ContainerStartResult, error) {
			return client.ContainerStartResult{}, nil
		},
		MockContainerStop: func(_ context.Context, id string, _ client.ContainerStopOptions) (client.ContainerStopResult, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.stopped = append(f.stopped, id)
			return client.ContainerStopResult{}, nil
		},
		MockContainerRemove: func(_ context.Context, id string, _ client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.removed = append(f.removed, id)
			return client.ContainerRemoveResult{}, nil
		},
	}
}

// readyServer serves a real PlanService on a loopback listener that answers
// every request with INVALID_ARGUMENT, which is all readiness needs.
type readyServer struct {
	diffv1alpha1.UnimplementedPlanServiceServer
}

func (readyServer) Plan(context.Context, *diffv1alpha1.PlanRequest) (*diffv1alpha1.PlanResponse, error) {
	return nil, status.Error(codes.InvalidArgument, "empty request")
}

func servePlanService(t *testing.T) string {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cannot listen: %v", err)
	}
	s := grpc.NewServer()
	diffv1alpha1.RegisterPlanServiceServer(s, readyServer{})
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)
	return lis.Addr().String()
}

func TestManagerStart(t *testing.T) {
	ctx := context.Background()
	log := logging.NewNopLogger()

	pkgs := &fakePkgClient{pkgs: map[string]*runtimexpkg.Package{
		"xpkg.example.org/provider-aws-ec2:v2.9.0": parsePackage(t, pkgWithCapability, "sha256:0123456789abcdef"),
		"xpkg.example.org/provider-aws-s3:v2.9.0":  parsePackage(t, pkgWithoutCapability, "sha256:fedcba9876543210"),
	}}

	t.Run("NoCapabilitySkipsWithoutStarting", func(t *testing.T) {
		fd := newFakeDocker("", nil)
		m := NewManager(pkgs, log, WithDockerClient(fd.client()))

		skipped, err := m.Start(ctx, Images([]string{"xpkg.example.org/provider-aws-s3:v2.9.0"}))
		if err != nil {
			t.Fatalf("Start(...): unexpected error: %v", err)
		}
		if len(skipped) != 1 || !strings.Contains(skipped[0].Reason, "DiffServer capability") {
			t.Errorf("Start(...): want one capability skip, got %+v", skipped)
		}
		if len(fd.created) != 0 {
			t.Errorf("Start(...): no container should start for a package without the capability")
		}
	})

	t.Run("RoutesBuiltFromPackageCRDs", func(t *testing.T) {
		fd := newFakeDocker(servePlanService(t), nil)
		m := NewManager(pkgs, log, WithDockerClient(fd.client()))

		skipped, err := m.Start(ctx, Images([]string{"xpkg.example.org/provider-aws-ec2:v2.9.0"}))
		if err != nil {
			t.Fatalf("Start(...): unexpected error: %v", err)
		}
		if len(skipped) != 0 {
			t.Fatalf("Start(...): unexpected skips: %+v", skipped)
		}

		for _, gk := range []schema.GroupKind{
			{Group: "ec2.aws.upbound.io", Kind: "VPC"},
			{Group: "ec2.aws.m.upbound.io", Kind: "VPC"},
		} {
			if _, ok := m.Route(gk); !ok {
				t.Errorf("Route(%s): want a route from the package CRDs", gk)
			}
		}
		if _, ok := m.Route(schema.GroupKind{Group: "s3.aws.upbound.io", Kind: "Bucket"}); ok {
			t.Errorf("Route(Bucket.s3): want no route for a kind the package does not ship")
		}

		wantName := containerPrefix + "0123456789ab"
		if len(fd.created) != 1 || fd.created[0] != wantName {
			t.Errorf("Start(...): want container %q started, got %v", wantName, fd.created)
		}

		if err := m.Stop(ctx); err != nil {
			t.Fatalf("Stop(...): unexpected error: %v", err)
		}
		if len(fd.removed) != 1 {
			t.Errorf("Stop(...): want the container removed, got %v", fd.removed)
		}
	})

	t.Run("ReusesContainerByName", func(t *testing.T) {
		fd := newFakeDocker(servePlanService(t), map[string]string{
			containerPrefix + "0123456789ab": "existing-id",
		})
		m := NewManager(pkgs, log, WithDockerClient(fd.client()))

		skipped, err := m.Start(ctx, Images([]string{"xpkg.example.org/provider-aws-ec2:v2.9.0"}))
		if err != nil || len(skipped) != 0 {
			t.Fatalf("Start(...): unexpected error or skips: %v %+v", err, skipped)
		}
		if len(fd.created) != 0 {
			t.Errorf("Start(...): want the existing container reused, got new containers %v", fd.created)
		}
	})

	t.Run("OrphanCleanupLeavesContainersRunning", func(t *testing.T) {
		fd := newFakeDocker(servePlanService(t), nil)
		m := NewManager(pkgs, log, WithDockerClient(fd.client()))

		if _, err := m.Start(ctx, []Input{{Image: "xpkg.example.org/provider-aws-ec2:v2.9.0", Cleanup: CleanupOrphan}}); err != nil {
			t.Fatalf("Start(...): unexpected error: %v", err)
		}
		if err := m.Stop(ctx); err != nil {
			t.Fatalf("Stop(...): unexpected error: %v", err)
		}
		if len(fd.stopped) != 0 || len(fd.removed) != 0 {
			t.Errorf("Stop(...): want containers left running with the Orphan cleanup policy, got stops %v removes %v", fd.stopped, fd.removed)
		}
	})

	t.Run("NotReadyStopsContainerAndSkips", func(t *testing.T) {
		// A listener that accepts no gRPC: readiness never succeeds.
		lis, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("cannot listen: %v", err)
		}
		t.Cleanup(func() { _ = lis.Close() })

		fd := newFakeDocker(lis.Addr().String(), nil)
		m := NewManager(pkgs, log, WithDockerClient(fd.client()), WithReadyTimeout(700*time.Millisecond))

		skipped, err := m.Start(ctx, Images([]string{"xpkg.example.org/provider-aws-ec2:v2.9.0"}))
		if err != nil {
			t.Fatalf("Start(...): unexpected error: %v", err)
		}
		if len(skipped) != 1 || !strings.Contains(skipped[0].Reason, "ready") {
			t.Errorf("Start(...): want a readiness skip, got %+v", skipped)
		}
		if len(fd.removed) != 1 {
			t.Errorf("Start(...): want the unready container cleaned up, got removes %v", fd.removed)
		}
	})
}
