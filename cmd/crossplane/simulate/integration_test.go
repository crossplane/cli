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

package simulate_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	diffv1alpha1 "github.com/crossplane/upjet/v2/proto/diff/v1alpha1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	"github.com/crossplane/cli/v2/cmd/crossplane/resource"
	tu "github.com/crossplane/cli/v2/internal/diff/testutils"
	"github.com/crossplane/cli/v2/internal/exitcode"
	"github.com/crossplane/cli/v2/internal/plan"
	isimulate "github.com/crossplane/cli/v2/internal/simulate"
	"github.com/crossplane/cli/v2/internal/terminal"
	"github.com/crossplane/cli/v2/internal/testutils/integration"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"
)

const crds = "../../../internal/testutils/integration/testdata/diff/crds"

// unrelatedDiffServer answers every Plan with InvalidArgument, which is all
// the readiness probe needs. It serves an API group no test resource is in,
// so every managed resource plans by the approximate fallback.
type unrelatedDiffServer struct {
	diffv1alpha1.UnimplementedPlanServiceServer
}

func (unrelatedDiffServer) Plan(context.Context, *diffv1alpha1.PlanRequest) (*diffv1alpha1.PlanResponse, error) {
	return nil, status.Error(codes.InvalidArgument, "unrelated")
}

// developmentProviders starts unrelatedDiffServer and returns a Provider
// manifest file that runs it as a Development diff server, so resource
// simulate needs no Docker.
func developmentProviders(t *testing.T) string {
	t.Helper()

	lis, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cannot listen: %v", err)
	}
	srv := grpc.NewServer()
	diffv1alpha1.RegisterPlanServiceServer(srv, unrelatedDiffServer{})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	path := filepath.Join(t.TempDir(), "providers.yaml")
	manifest := fmt.Sprintf(`apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: provider-unrelated
  annotations:
    simulate.crossplane.io/runtime: Development
    simulate.crossplane.io/runtime-development-target: %s
    simulate.crossplane.io/runtime-development-api-groups: unrelated.example.org
spec:
  package: xpkg.example.org/provider-unrelated:v0.1.0
`, lis.Addr())
	if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// runResourceSimulate runs `crossplane resource simulate` with argv against
// the cluster kubeconfig names, and returns the code main would exit with.
func runResourceSimulate(t *testing.T, kubeconfig []byte, argv []string, stdout, stderr *bytes.Buffer) int {
	t.Helper()

	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, kubeconfig, 0o600); err != nil {
		t.Fatal(err)
	}
	// The command loads the kubeconfig as kubectl does, so the cases run
	// serially.
	t.Setenv("KUBECONFIG", path)

	var cli struct {
		Resource resource.Cmd `cmd:""`
	}
	parser, err := kong.New(&cli,
		kong.Name("crossplane"),
		kong.Vars{"project_file": clixpkg.ProjectFile},
		kong.Writers(stdout, stderr),
		kong.Exit(func(int) { t.Fatal("kong tried to exit the test binary") }),
		kong.BindTo(t.Context(), (*context.Context)(nil)),
		kong.BindTo(tu.TestLogger(t, true), (*logging.Logger)(nil)),
		kong.BindTo(terminal.NewNopSpinnerPrinter(), (*terminal.SpinnerPrinter)(nil)),
	)
	if err != nil {
		t.Fatalf("cannot create kong parser: %v", err)
	}
	kctx, err := parser.Parse(append([]string{"resource", "simulate"}, argv...))
	if err != nil {
		t.Fatalf("cannot parse %v: %v", argv, err)
	}
	err = kctx.Run()
	if code, ok := exitcode.SilentExit(err); ok {
		return code
	}
	if err != nil {
		t.Errorf("resource simulate failed: %v", err)
		return 1
	}
	return 0
}

// TestResourceSimulateApplier pins which live-only fields the approximate
// fallback plans as removed for a user-supplied managed resource, by the
// applier resource simulate resolves from its flags and the live object.
func TestResourceSimulateApplier(t *testing.T) {
	type args struct {
		applies []integration.FieldManagerApply
		flags   []string
	}
	type want struct {
		// removed are the paths planned as removed; kept are live-only paths
		// that must not be.
		removed, kept []string
		// warning are substrings of stderr's undetermined-applier warning;
		// none means stderr must carry no warning.
		warning []string
		// exitCode is the status the command exits with: changes, or none.
		exitCode int
	}
	const (
		removable = "spec.forProvider.removableField"
		roleName  = "spec.forProvider.roleName"
	)
	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"InferredServerSideApplierRemovesItsDroppedField": {
			reason: "With one server-side applier and no flags, a field only it owns and the manifest drops is removed, with no warning.",
			args: args{applies: []integration.FieldManagerApply{
				{File: "testdata/existing-mr-with-removable-field.yaml", FieldManager: "argocd-controller"},
			}},
			want: want{removed: []string{removable}, exitCode: isimulate.ExitCodeChanges},
		},
		"FieldAnotherManagerAlsoOwnsIsKept": {
			reason: "A dropped field another manager also owns survives the apply, so is not removed.",
			args: args{
				applies: []integration.FieldManagerApply{
					{File: "testdata/existing-mr-with-removable-field.yaml", FieldManager: "argocd-controller"},
					{File: "testdata/existing-mr-other-manager-removable-field.yaml", FieldManager: "platform-operator"},
				},
				flags: []string{"--field-manager=argocd-controller"},
			},
			want: want{kept: []string{removable}, exitCode: 0},
		},
		"TwoServerSideAppliersWarn": {
			reason: "With two server-side appliers and no flags, the applier is undetermined: stderr warns naming the resource, its namespace, both candidates and the fix.",
			args: args{applies: []integration.FieldManagerApply{
				{File: "testdata/existing-mr-with-removable-field.yaml", FieldManager: "argocd-controller"},
				{File: "testdata/existing-mr-other-manager-role-name.yaml", FieldManager: "platform-operator"},
			}},
			want: want{
				// An undetermined applier keeps the union model, so each applier's
				// exclusive dropped field still plans removed.
				removed:  []string{removable, roleName},
				warning:  []string{"XDownstreamResource default/test-resource", "candidates: argocd-controller, platform-operator", "--field-manager"},
				exitCode: isimulate.ExitCodeChanges,
			},
		},
		"TwoServerSideAppliersWithFieldManager": {
			reason: "--field-manager names the applier: its exclusive dropped field is removed, the other manager's is kept, and nothing warns.",
			args: args{
				applies: []integration.FieldManagerApply{
					{File: "testdata/existing-mr-with-removable-field.yaml", FieldManager: "argocd-controller"},
					{File: "testdata/existing-mr-other-manager-role-name.yaml", FieldManager: "platform-operator"},
				},
				flags: []string{"--field-manager=argocd-controller"},
			},
			want: want{removed: []string{removable}, kept: []string{roleName}, exitCode: isimulate.ExitCodeChanges},
		},
		"ClientSideAppliedInferred": {
			reason: "A client-side applied object, inferred from its annotation, has the key the annotation records and the manifest drops removed.",
			args: args{applies: []integration.FieldManagerApply{
				{File: "testdata/existing-mr-client-side-applied-with-removable-field.yaml", FieldManager: "kubectl-client-side-apply", Create: true},
			}},
			want: want{removed: []string{removable}, exitCode: isimulate.ExitCodeChanges},
		},
		"ClientSideAppliedExplicit": {
			reason: "--server-side=false applies client-side: the key the annotation records and the manifest drops is removed.",
			args: args{
				applies: []integration.FieldManagerApply{
					{File: "testdata/existing-mr-client-side-applied-with-removable-field.yaml", FieldManager: "kubectl-client-side-apply", Create: true},
				},
				flags: []string{"--server-side=false"},
			},
			want: want{removed: []string{removable}, exitCode: isimulate.ExitCodeChanges},
		},
		"ClientSideWithoutAnnotationRemovesNothing": {
			reason: "Client-side apply of an object with no last-applied-configuration annotation removes nothing.",
			args: args{
				applies: []integration.FieldManagerApply{
					{File: "testdata/existing-mr-with-removable-field.yaml", FieldManager: "kubectl-client-side-apply", Create: true},
				},
				flags: []string{"--server-side=false"},
			},
			want: want{kept: []string{removable}, exitCode: 0},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cluster := integration.Start(t, integration.Setup{CRDDirs: []string{crds}, FieldManagerApplies: tc.args.applies})

			argv := append([]string{"-f", "testdata/mr-without-removable-field.yaml", "--providers", developmentProviders(t), "-o", "json"}, tc.args.flags...)
			var stdout, stderr bytes.Buffer
			code := runResourceSimulate(t, cluster.Kubeconfig, argv, &stdout, &stderr)

			var res plan.Result
			if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
				t.Fatalf("\n%s\nstdout is not a plan.Result: %v\n%s\nstderr:\n%s", tc.reason, err, stdout.String(), stderr.String())
			}
			if len(res.Plans) != 1 || !res.Plans[0].Approximate {
				t.Fatalf("\n%s\nwant one approximate plan, got exit %d: %s", tc.reason, code, stdout.String())
			}
			removed := map[string]bool{}
			if code != tc.want.exitCode {
				t.Errorf("\n%s\nwant exit %d, got %d\nstderr:\n%s", tc.reason, tc.want.exitCode, code, stderr.String())
			}
			for _, c := range res.Plans[0].Changes {
				if c.Planned == nil {
					removed[c.Path] = true
				}
			}
			for _, p := range tc.want.removed {
				if !removed[p] {
					t.Errorf("\n%s\n%s not planned removed: %s", tc.reason, p, stdout.String())
				}
			}
			for _, p := range tc.want.kept {
				if removed[p] {
					t.Errorf("\n%s\n%s planned removed: %s", tc.reason, p, stdout.String())
				}
			}
			if len(tc.want.warning) == 0 && strings.Contains(stderr.String(), "Warning") {
				t.Errorf("\n%s\nstderr warns:\n%s", tc.reason, stderr.String())
			}
			for _, w := range tc.want.warning {
				if !strings.Contains(stderr.String(), w) {
					t.Errorf("\n%s\nstderr lacks %q:\n%s", tc.reason, w, stderr.String())
				}
			}
		})
	}
}
