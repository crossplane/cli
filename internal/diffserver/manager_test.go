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
	diffv1alpha1 "github.com/crossplane/upjet/v2/proto/diff/v1alpha1"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	runtimexpkg "github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xpkg/parser"

	"github.com/crossplane/cli/v2/internal/containerruntime"
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
	existing map[string]string // name -> id of containers, pre-existing or created
	known    map[string]bool   // ids inspect resolves port bindings for
	created  []string          // names passed to ContainerCreate
	stopped  []string          // ids passed to ContainerStop
	removed  []string          // ids passed to ContainerRemove
	// labels holds each created container's labels, by name.
	labels map[string]map[string]string
	// listed are the containers ContainerList matches label filters against,
	// as left behind by dead runs.
	listed []container.Summary
	// removeErr fails ContainerRemove; startErr fails ContainerStart.
	removeErr, startErr error
	// conflict names a container another run creates, with an Orphan
	// policy, just before this run's ContainerCreate for that name.
	conflict string
}

func newFakeDocker(addr string, existing map[string]string) *fakeDocker {
	if existing == nil {
		existing = map[string]string{}
	}
	f := &fakeDocker{addr: addr, existing: existing, known: map[string]bool{}, labels: map[string]map[string]string{}}
	for _, id := range existing {
		f.known[id] = true
	}
	return f
}

// session returns the session label of the container created with a name
// starting with prefix.
func (f *fakeDocker) session(t *testing.T, prefix string) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for name, l := range f.labels {
		if strings.HasPrefix(name, prefix) {
			return l[LabelKeySession]
		}
	}
	t.Fatalf("no container created with a name starting with %q, got %v", prefix, f.created)
	return ""
}

func (f *fakeDocker) client() *fake.Client {
	return &fake.Client{
		MockContainerInspect: func(_ context.Context, ref string, _ client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if id, ok := f.existing[ref]; ok {
				return client.ContainerInspectResult{Container: container.InspectResponse{
					ID:     id,
					Config: &container.Config{Labels: f.labels[ref]},
				}}, nil
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
			if options.Name == f.conflict {
				f.existing[options.Name] = "winner-id"
				f.known["winner-id"] = true
				f.labels[options.Name] = containerruntime.ContainerLabels(labelDomain, "0000000000000001", CleanupOrphan)
				return client.ContainerCreateResult{}, errdefs.ErrConflict
			}
			id := "new-" + options.Name
			f.created = append(f.created, options.Name)
			f.existing[options.Name] = id
			f.labels[options.Name] = options.Config.Labels
			f.known[id] = true
			return client.ContainerCreateResult{ID: id}, nil
		},
		MockContainerStart: func(_ context.Context, _ string, _ client.ContainerStartOptions) (client.ContainerStartResult, error) {
			return client.ContainerStartResult{}, f.startErr
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
			if f.removeErr != nil {
				return client.ContainerRemoveResult{}, f.removeErr
			}
			f.removed = append(f.removed, id)
			return client.ContainerRemoveResult{}, nil
		},
		MockContainerList: func(_ context.Context, options client.ContainerListOptions) (client.ContainerListResult, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			var res client.ContainerListResult
			for _, c := range f.listed {
				if matchesLabels(options.Filters, c.Labels) {
					res.Items = append(res.Items, c)
				}
			}
			return res, nil
		},
		MockNetworkList: func(context.Context, client.NetworkListOptions) (client.NetworkListResult, error) {
			return client.NetworkListResult{}, nil
		},
	}
}

// matchesLabels reports whether labels carry every label filter, as Docker
// matches them.
func matchesLabels(filters client.Filters, labels map[string]string) bool {
	for v := range filters["label"] {
		k, want, _ := strings.Cut(v, "=")
		if labels[k] != want {
			return false
		}
	}
	return true
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

		skipped, err := m.Start(ctx, Images([]string{"xpkg.example.org/provider-aws-s3:v2.9.0"}), nil)
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

		skipped, err := m.Start(ctx, Images([]string{"xpkg.example.org/provider-aws-ec2:v2.9.0"}), nil)
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

		if len(fd.created) != 1 || !runName.MatchString(fd.created[0]) {
			t.Errorf("Start(...): want one container named like %s started, got %v", runName, fd.created)
		}

		if err := m.Stop(ctx); err != nil {
			t.Fatalf("Stop(...): unexpected error: %v", err)
		}
		if len(fd.removed) != 1 {
			t.Errorf("Stop(...): want the container removed, got %v", fd.removed)
		}
	})

	t.Run("OrphanReusesContainerByName", func(t *testing.T) {
		fd := newFakeDocker(servePlanService(t), map[string]string{
			containerPrefix + "0123456789ab": "existing-id",
		})
		m := NewManager(pkgs, log, WithDockerClient(fd.client()))

		skipped, err := m.Start(ctx, []Input{{Image: "xpkg.example.org/provider-aws-ec2:v2.9.0", Cleanup: CleanupOrphan}}, nil)
		if err != nil || len(skipped) != 0 {
			t.Fatalf("Start(...): unexpected error or skips: %v %+v", err, skipped)
		}
		if len(fd.created) != 0 {
			t.Errorf("Start(...): want the existing container reused, got new containers %v", fd.created)
		}
	})

	t.Run("RemoveNeverReusesContainerByName", func(t *testing.T) {
		fd := newFakeDocker(servePlanService(t), map[string]string{
			containerPrefix + "0123456789ab": "kept-id",
		})
		m := NewManager(pkgs, log, WithDockerClient(fd.client()))

		if _, err := m.Start(ctx, Images([]string{"xpkg.example.org/provider-aws-ec2:v2.9.0"}), nil); err != nil {
			t.Fatalf("Start(...): unexpected error: %v", err)
		}
		if len(fd.created) != 1 || !runName.MatchString(fd.created[0]) {
			t.Errorf("Start(...): want a container of this run's own, named like %s, got %v", runName, fd.created)
		}
		if err := m.Stop(ctx); err != nil {
			t.Fatalf("Stop(...): unexpected error: %v", err)
		}
		if slices.Contains(fd.removed, "kept-id") {
			t.Errorf("Stop(...): removed the kept container another run left, got removes %v", fd.removed)
		}
	})

	t.Run("ConcurrentRemoveRunsDoNotShareAServer", func(t *testing.T) {
		// Two runs on one Docker daemon serving the same provider: the
		// first to exit must not remove the server the other still uses.
		fd := newFakeDocker(servePlanService(t), nil)
		img := Images([]string{"xpkg.example.org/provider-aws-ec2:v2.9.0"})
		m1 := NewManager(pkgs, log, WithDockerClient(fd.client()))
		m2 := NewManager(pkgs, log, WithDockerClient(fd.client()))

		if _, err := m1.Start(ctx, img, nil); err != nil {
			t.Fatalf("m1.Start(...): unexpected error: %v", err)
		}
		if _, err := m2.Start(ctx, img, nil); err != nil {
			t.Fatalf("m2.Start(...): unexpected error: %v", err)
		}
		if len(fd.created) != 2 || fd.created[0] == fd.created[1] {
			t.Fatalf("Start(...): want each run to create its own container, got %v", fd.created)
		}

		if err := m1.Stop(ctx); err != nil {
			t.Fatalf("m1.Stop(...): unexpected error: %v", err)
		}
		if want := []string{m1.Servers()[0].ContainerID}; !slices.Equal(fd.removed, want) {
			t.Errorf("m1.Stop(...): want only m1's container removed %v, got %v", want, fd.removed)
		}
	})

	t.Run("InputsOfOnePackageShareAServer", func(t *testing.T) {
		// Two Provider manifests name one package, by tag and by digest.
		const byTag, byDigest = "xpkg.example.org/provider-aws-ec2:v2.9.0", "xpkg.example.org/provider-aws-ec2@sha256:0123456789abcdef"
		pkgs := &fakePkgClient{pkgs: map[string]*runtimexpkg.Package{
			byTag:    pkgs.pkgs[byTag],
			byDigest: pkgs.pkgs[byTag],
		}}
		fd := newFakeDocker(servePlanService(t), nil)
		m := NewManager(pkgs, log, WithDockerClient(fd.client()))

		skipped, err := m.Start(ctx, Images([]string{byTag, byDigest}), nil)
		if err != nil || len(skipped) != 0 {
			t.Fatalf("Start(...): unexpected error or skips: %v %+v", err, skipped)
		}
		if _, ok := m.Route(schema.GroupKind{Group: "ec2.aws.upbound.io", Kind: "VPC"}); !ok {
			t.Errorf("Route(VPC.ec2): want a route from the shared server")
		}
		if err := m.Stop(ctx); err != nil {
			t.Fatalf("Stop(...): unexpected error: %v", err)
		}
		if len(fd.created) != 1 || !slices.Equal(fd.removed, []string{"new-" + fd.created[0]}) {
			t.Errorf("Start and Stop: want one container started and removed, got created %v, removed %v", fd.created, fd.removed)
		}
	})

	t.Run("ConcurrentOrphanRunsShareTheWinnersServer", func(t *testing.T) {
		// Another run creates the Orphan container between this run's
		// lookup and its create.
		fd := newFakeDocker(servePlanService(t), nil)
		fd.conflict = containerPrefix + "0123456789ab"
		m := NewManager(pkgs, log, WithDockerClient(fd.client()))

		skipped, err := m.Start(ctx, []Input{{Image: "xpkg.example.org/provider-aws-ec2:v2.9.0", Cleanup: CleanupOrphan}}, nil)
		if err != nil || len(skipped) != 0 {
			t.Fatalf("Start(...): unexpected error or skips: %v %+v", err, skipped)
		}
		if got := m.Servers()[0].ContainerID; got != "winner-id" {
			t.Errorf("Start(...): want the winner's container shared, got %q", got)
		}
		if err := m.Stop(ctx); err != nil {
			t.Fatalf("Stop(...): unexpected error: %v", err)
		}
		if len(fd.stopped) != 0 || len(fd.removed) != 0 {
			t.Errorf("Stop(...): want the shared Orphan container left running, got stops %v removes %v", fd.stopped, fd.removed)
		}
	})

	t.Run("OrphanCleanupLeavesContainersRunning", func(t *testing.T) {
		fd := newFakeDocker(servePlanService(t), nil)
		m := NewManager(pkgs, log, WithDockerClient(fd.client()))

		if _, err := m.Start(ctx, []Input{{Image: "xpkg.example.org/provider-aws-ec2:v2.9.0", Cleanup: CleanupOrphan}}, nil); err != nil {
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

		skipped, err := m.Start(ctx, Images([]string{"xpkg.example.org/provider-aws-ec2:v2.9.0"}), nil)
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

	t.Run("DevelopmentRoutesDeclaredGroupsWithoutDocker", func(t *testing.T) {
		// An empty package client and a Docker fake with nothing published:
		// a fetch or a container start would surface as a skip.
		fd := newFakeDocker("", nil)
		m := NewManager(&fakePkgClient{}, log, WithDockerClient(fd.client()))

		in := Input{
			Runtime:           RuntimeDevelopment,
			Name:              "provider-aws-ec2",
			DevelopmentTarget: servePlanService(t),
			APIGroups:         []string{"ec2.aws.upbound.io", "ec2.aws.m.upbound.io"},
		}
		skipped, err := m.Start(ctx, []Input{in}, nil)
		if err != nil {
			t.Fatalf("Start(...): unexpected error: %v", err)
		}
		if len(skipped) != 0 {
			t.Fatalf("Start(...): unexpected skips: %+v", skipped)
		}

		for _, gk := range []schema.GroupKind{
			{Group: "ec2.aws.upbound.io", Kind: "VPC"},
			{Group: "ec2.aws.m.upbound.io", Kind: "Subnet"},
		} {
			if _, ok := m.Route(gk); !ok {
				t.Errorf("Route(%s): want a route from the declared API groups", gk)
			}
		}
		if _, ok := m.Route(schema.GroupKind{Group: "s3.aws.upbound.io", Kind: "Bucket"}); ok {
			t.Errorf("Route(Bucket.s3): want no route for a group the provider did not declare")
		}
		if len(fd.created) != 0 {
			t.Errorf("Start(...): a development diff server must not start a container, got %v", fd.created)
		}

		if err := m.Stop(ctx); err != nil {
			t.Fatalf("Stop(...): unexpected error: %v", err)
		}
		if len(fd.stopped) != 0 || len(fd.removed) != 0 {
			t.Errorf("Stop(...): nothing to clean up for a development diff server, got stops %v removes %v", fd.stopped, fd.removed)
		}
	})

	t.Run("PackageRoutesWinOverDeclaredGroups", func(t *testing.T) {
		fd := newFakeDocker(servePlanService(t), nil)
		m := NewManager(pkgs, log, WithDockerClient(fd.client()))

		const pkgImage, devName = "xpkg.example.org/provider-aws-ec2:v2.9.0", "provider-aws-ec2-dev"
		dev := Input{
			Runtime:           RuntimeDevelopment,
			Name:              devName,
			DevelopmentTarget: servePlanService(t),
			APIGroups:         []string{"ec2.aws.upbound.io"},
		}
		skipped, err := m.Start(ctx, []Input{{Image: pkgImage}, dev}, nil)
		if err != nil || len(skipped) != 0 {
			t.Fatalf("Start(...): unexpected error or skips: %v %+v", err, skipped)
		}

		clients := map[string]diffv1alpha1.PlanServiceClient{}
		for _, s := range m.Servers() {
			clients[s.Image] = s.client
		}

		// The package ships a CRD for VPC, so its server serves VPC even
		// though the development server declared the whole group. Subnet
		// ships in no package, so the declared group routes it.
		if c, ok := m.Route(schema.GroupKind{Group: "ec2.aws.upbound.io", Kind: "VPC"}); !ok || c != clients[pkgImage] {
			t.Errorf("Route(VPC.ec2): want the package-run server, got %v (ok %t)", c, ok)
		}
		if c, ok := m.Route(schema.GroupKind{Group: "ec2.aws.upbound.io", Kind: "Subnet"}); !ok || c != clients[devName] {
			t.Errorf("Route(Subnet.ec2): want the development server, got %v (ok %t)", c, ok)
		}

		if err := m.Stop(ctx); err != nil {
			t.Fatalf("Stop(...): unexpected error: %v", err)
		}
		if len(fd.removed) != 1 {
			t.Errorf("Stop(...): want only the package-run container removed, got %v", fd.removed)
		}
	})

	t.Run("DevelopmentTargetNotReadySkips", func(t *testing.T) {
		// A listener that accepts no gRPC: readiness never succeeds. No
		// Docker client at all: a development provider never needs one.
		lis, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("cannot listen: %v", err)
		}
		t.Cleanup(func() { _ = lis.Close() })

		m := NewManager(&fakePkgClient{}, log, WithReadyTimeout(700*time.Millisecond))

		target := lis.Addr().String()
		skipped, err := m.Start(ctx, []Input{{
			Runtime:           RuntimeDevelopment,
			Name:              "provider-aws-ec2",
			DevelopmentTarget: target,
			APIGroups:         []string{"ec2.aws.upbound.io"},
		}}, nil)
		if err != nil {
			t.Fatalf("Start(...): unexpected error: %v", err)
		}
		if len(skipped) != 1 || !strings.Contains(skipped[0].Reason, target) {
			t.Errorf("Start(...): want a skip naming the unreachable target %s, got %+v", target, skipped)
		}
		if _, ok := m.Route(schema.GroupKind{Group: "ec2.aws.upbound.io", Kind: "VPC"}); ok {
			t.Errorf("Route(VPC.ec2): want no route from a skipped development server")
		}
	})
}

// runName matches the name of a diff server container that is one run's
// alone: the package digest, then a random suffix.
var runName = regexp.MustCompile(`^` + containerPrefix + `0123456789ab-[0-9a-f]{8}$`)

func TestManagerLease(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("run leases are not supported on Windows")
	}

	ctx := context.Background()
	log := logging.NewNopLogger()
	const (
		img    = "xpkg.example.org/provider-aws-ec2:v2.9.0"
		deadID = "dead00000000000a"
		liveID = "1111111111111111"
	)
	pkgs := &fakePkgClient{pkgs: map[string]*runtimexpkg.Package{
		img: parsePackage(t, pkgWithCapability, "sha256:0123456789abcdef"),
	}}
	leases := func(t *testing.T, dir string) []string {
		t.Helper()
		got, err := filepath.Glob(filepath.Join(dir, "*.lock"))
		if err != nil {
			t.Fatal(err)
		}
		for i := range got {
			got[i] = strings.TrimSuffix(filepath.Base(got[i]), ".lock")
		}
		return got
	}

	t.Run("LeasesTheRunUntilACleanStop", func(t *testing.T) {
		dir := t.TempDir()
		fd := newFakeDocker(servePlanService(t), nil)
		m := NewManager(pkgs, log, WithDockerClient(fd.client()), WithLeaseDir(dir))

		if _, err := m.Start(ctx, Images([]string{img}), nil); err != nil {
			t.Fatalf("Start(...): unexpected error: %v", err)
		}
		if got, want := leases(t, dir), []string{fd.session(t, containerPrefix)}; !slices.Equal(got, want) {
			t.Errorf("Start(...): want a lease for the run its containers are labelled with %v, got %v", want, got)
		}
		if err := m.Stop(ctx); err != nil {
			t.Fatalf("Stop(...): unexpected error: %v", err)
		}
		if got := leases(t, dir); len(got) != 0 {
			t.Errorf("Stop(...): want the lease removed after a clean stop, got %v", got)
		}
	})

	t.Run("KeepsTheLeaseWhenAContainerIsLeft", func(t *testing.T) {
		cases := map[string]struct {
			removeErr, startErr error
			wantSkips           int
			wantStopErr         bool
		}{
			// Stop cannot remove the run's container.
			"RemoveFails": {removeErr: errors.New("daemon busy"), wantStopErr: true},
			// The container was created, then failed to start: nothing in
			// this run knows its ID to remove it.
			"StartFails": {startErr: errors.New("port is already allocated"), wantSkips: 1},
		}
		for name, tc := range cases {
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				fd := newFakeDocker(servePlanService(t), nil)
				fd.removeErr, fd.startErr = tc.removeErr, tc.startErr
				m := NewManager(pkgs, log, WithDockerClient(fd.client()), WithLeaseDir(dir))

				if skipped, err := m.Start(ctx, Images([]string{img}), nil); err != nil || len(skipped) != tc.wantSkips {
					t.Fatalf("Start(...): want %d skips and no error, got %+v, %v", tc.wantSkips, skipped, err)
				}
				if err := m.Stop(ctx); (err != nil) != tc.wantStopErr {
					t.Fatalf("Stop(...): want error %t, got %v", tc.wantStopErr, err)
				}
				if got := leases(t, dir); len(got) != 1 {
					t.Errorf("Stop(...): want the lease kept for a later run to reclaim, got %v", got)
				}
			})
		}
	})

	t.Run("ReclaimsDeadRunsBeforeStarting", func(t *testing.T) {
		dir := t.TempDir()
		// A lease no process holds: its run died.
		if err := os.WriteFile(filepath.Join(dir, deadID+".lock"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		fd := newFakeDocker(servePlanService(t), nil)
		fd.listed = []container.Summary{
			{ID: "dead-server", Labels: containerruntime.ContainerLabels(labelDomain, deadID, CleanupRemove)},
			// A run with no dead lease here, such as a live one: left alone.
			{ID: "live-server", Labels: containerruntime.ContainerLabels(labelDomain, liveID, CleanupRemove)},
			{ID: "kept-server", Labels: containerruntime.ContainerLabels(labelDomain, deadID, CleanupOrphan)},
		}
		m := NewManager(pkgs, log, WithDockerClient(fd.client()), WithLeaseDir(dir))

		if _, err := m.Start(ctx, Images([]string{img}), nil); err != nil {
			t.Fatalf("Start(...): unexpected error: %v", err)
		}
		if want := []string{"dead-server"}; !slices.Equal(fd.removed, want) {
			t.Errorf("Start(...): want only the dead run's Remove container removed %v, got %v", want, fd.removed)
		}
		if slices.Contains(leases(t, dir), deadID) {
			t.Errorf("Start(...): want the dead run's lease removed once reclaimed")
		}
	})

	t.Run("SuppliedIDIsNotLeased", func(t *testing.T) {
		dir := t.TempDir()
		fd := newFakeDocker(servePlanService(t), nil)
		m := NewManager(pkgs, log, WithDockerClient(fd.client()), WithLeaseDir(dir), WithSessionID("0123456789abcdef"))

		if _, err := m.Start(ctx, Images([]string{img}), nil); err != nil {
			t.Fatalf("Start(...): unexpected error: %v", err)
		}
		if got := leases(t, dir); len(got) != 0 {
			t.Errorf("Start(...): want no lease for a supplied session ID, got %v", got)
		}
		if got := fd.session(t, containerPrefix); got != "0123456789abcdef" {
			t.Errorf("Start(...): want the supplied session label, got %q", got)
		}
	})

	t.Run("DevelopmentOnlyRunTakesNoLease", func(t *testing.T) {
		dir := t.TempDir()
		m := NewManager(&fakePkgClient{}, log, WithLeaseDir(dir))

		_, err := m.Start(ctx, []Input{{
			Runtime:           RuntimeDevelopment,
			Name:              "provider-aws-ec2",
			DevelopmentTarget: servePlanService(t),
			APIGroups:         []string{"ec2.aws.upbound.io"},
		}}, nil)
		if err != nil {
			t.Fatalf("Start(...): unexpected error: %v", err)
		}
		if got := leases(t, dir); len(got) != 0 {
			t.Errorf("Start(...): want no lease for a run that starts no containers, got %v", got)
		}
	})
}

func TestManagerStartIdempotent(t *testing.T) {
	ctx := context.Background()
	log := logging.NewNopLogger()
	const img = "xpkg.example.org/provider-aws-ec2:v2.9.0"
	pkgs := &fakePkgClient{pkgs: map[string]*runtimexpkg.Package{
		img: parsePackage(t, pkgWithCapability, "sha256:0123456789abcdef"),
	}}
	vpc := schema.GroupKind{Group: "ec2.aws.upbound.io", Kind: "VPC"}

	t.Run("RepeatStartsNothing", func(t *testing.T) {
		fd := newFakeDocker(servePlanService(t), nil)
		m := NewManager(pkgs, log, WithDockerClient(fd.client()))
		for i := range 2 {
			skipped, err := m.Start(ctx, Images([]string{img, img}), nil)
			if err != nil || len(skipped) != 0 {
				t.Fatalf("Start #%d: unexpected error or skips: %v %+v", i, err, skipped)
			}
		}
		if len(fd.created) != 1 || len(m.Servers()) != 1 {
			t.Errorf("Start twice: want one container and one server, got containers %v servers %d", fd.created, len(m.Servers()))
		}
	})

	t.Run("CancelledStartIsRetried", func(t *testing.T) {
		fd := newFakeDocker(servePlanService(t), nil)
		m := NewManager(pkgs, log, WithDockerClient(fd.client()))
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := m.Start(cctx, Images([]string{img}), nil); err == nil {
			t.Fatal("Start(cancelled): want the context error")
		}
		skipped, err := m.Start(ctx, Images([]string{img}), nil)
		if err != nil || len(skipped) != 0 {
			t.Fatalf("Start(retry): unexpected error or skips: %v %+v", err, skipped)
		}
		if _, ok := m.Route(vpc); !ok {
			t.Errorf("Start(retry): want the input started after a cancelled Start")
		}
	})

	t.Run("ConcurrentStartsStartOnce", func(t *testing.T) {
		fd := newFakeDocker(servePlanService(t), nil)
		m := NewManager(pkgs, log, WithDockerClient(fd.client()))
		var wg sync.WaitGroup
		for range 4 {
			wg.Go(func() {
				if _, err := m.Start(ctx, Images([]string{img}), nil); err != nil {
					t.Errorf("Start(...): unexpected error: %v", err)
				}
			})
		}
		wg.Wait()
		if len(fd.created) != 1 || len(m.Servers()) != 1 {
			t.Errorf("concurrent Starts: want one container and one server, got containers %v servers %d", fd.created, len(m.Servers()))
		}
	})

	t.Run("StopHonoursDeadline", func(t *testing.T) {
		fd := newFakeDocker(servePlanService(t), nil)
		cli := fd.client()
		cli.MockContainerStop = func(ctx context.Context, _ string, _ client.ContainerStopOptions) (client.ContainerStopResult, error) {
			<-ctx.Done()
			return client.ContainerStopResult{}, ctx.Err()
		}
		m := NewManager(pkgs, log, WithDockerClient(cli))
		if _, err := m.Start(ctx, Images([]string{img}), nil); err != nil {
			t.Fatalf("Start(...): unexpected error: %v", err)
		}
		sctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		done := make(chan struct{})
		go func() {
			_ = m.Stop(sctx)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("Stop(...): did not return after its deadline while Docker blocked")
		}
	})
}
