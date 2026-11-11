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

package xpkg

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/kong"
	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	v1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"

	"github.com/crossplane/cli/v2/internal/testutils/integration"
)

func TestMain(m *testing.M) {
	integration.ReapOrphanedEnvtest(func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) })
	os.Exit(m.Run())
}

// startInstallCluster starts a cluster serving Crossplane's CRDs and points KUBECONFIG, which install loads via
// ctrl.GetConfig, at it.
func startInstallCluster(t *testing.T) *integration.Cluster {
	t.Helper()

	cluster := integration.Start(t, integration.Setup{})

	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(kubeconfig, cluster.Kubeconfig, 0o600); err != nil {
		t.Fatalf("cannot write kubeconfig: %v", err)
	}

	t.Setenv("KUBECONFIG", kubeconfig)

	return cluster
}

// runInstall parses args as `crossplane xpkg install` would and runs it, returning its stdout.
func runInstall(t *testing.T, args ...string) (string, error) {
	t.Helper()

	var c installCmd

	parser, err := kong.New(&c)
	if err != nil {
		t.Fatalf("kong.New(): %v", err)
	}

	kongCtx, err := parser.Parse(args)
	if err != nil {
		t.Fatalf("Parse(%v): %v", args, err)
	}

	var stdout bytes.Buffer
	kongCtx.Stdout = &stdout

	err = c.Run(kongCtx, logging.NewNopLogger())

	return stdout.String(), err
}

// TestInstallIntegration installs packages into a real API server and checks the objects read back from it.
func TestInstallIntegration(t *testing.T) {
	cluster := startInstallCluster(t)

	type want struct {
		obj    v1.Package
		spec   v1.PackageSpec
		rcName string
		stdout string
		errSub string
	}

	auto, manual := v1.AutomaticActivation, v1.ManualActivation

	cases := map[string]struct {
		reason string
		args   []string
		want   want
	}{
		"Provider": {
			reason: "A provider is created with automatic activation, no pull secrets and a zero history limit by default.",
			args:   []string{"provider", "xpkg.crossplane.io/crossplane-contrib/provider-nop:v0.4.0", "provider-a"},
			want: want{
				obj: &v1.Provider{},
				spec: v1.PackageSpec{
					Package:                  "xpkg.crossplane.io/crossplane-contrib/provider-nop:v0.4.0",
					RevisionActivationPolicy: &auto,
					RevisionHistoryLimit:     ptr.To[int64](0),
				},
				rcName: "default", // The CRD defaults it.
				stdout: "provider/provider-a created\n",
			},
		},
		"ConfigurationWithFlags": {
			reason: "Manual activation, pull secrets and a history limit are set on a configuration's spec.",
			args: []string{
				"configuration", "xpkg.crossplane.io/acme/config:v1.0.0", "config-a",
				"--manual-activation", "--package-pull-secrets=s1,s2", "--revision-history-limit=3",
			},
			want: want{
				obj: &v1.Configuration{},
				spec: v1.PackageSpec{
					Package:                  "xpkg.crossplane.io/acme/config:v1.0.0",
					RevisionActivationPolicy: &manual,
					RevisionHistoryLimit:     ptr.To[int64](3),
					PackagePullSecrets:       []corev1.LocalObjectReference{{Name: "s1"}, {Name: "s2"}},
				},
				stdout: "configuration/config-a created\n",
			},
		},
		"FunctionWithRuntimeConfig": {
			reason: "A function references the runtime config it was installed with.",
			args:   []string{"function", "xpkg.crossplane.io/acme/fn:v1.0.0", "fn-a", "--runtime-config=rc"},
			want: want{
				obj: &v1.Function{},
				spec: v1.PackageSpec{
					Package:                  "xpkg.crossplane.io/acme/fn:v1.0.0",
					RevisionActivationPolicy: &auto,
					RevisionHistoryLimit:     ptr.To[int64](0),
				},
				rcName: "rc",
				stdout: "function/fn-a created\n",
			},
		},
		"ProviderWithRuntimeConfig": {
			reason: "A provider references the runtime config it was installed with.",
			args:   []string{"provider", "xpkg.crossplane.io/acme/prov:v1.0.0", "provider-rc", "--runtime-config=rc"},
			want: want{
				obj: &v1.Provider{},
				spec: v1.PackageSpec{
					Package:                  "xpkg.crossplane.io/acme/prov:v1.0.0",
					RevisionActivationPolicy: &auto,
					RevisionHistoryLimit:     ptr.To[int64](0),
				},
				rcName: "rc",
				stdout: "provider/provider-rc created\n",
			},
		},
		"ConfigurationWithRuntimeConfig": {
			reason: "A configuration has no runtime, so a runtime config is refused before anything is created.",
			args:   []string{"configuration", "xpkg.crossplane.io/acme/config:v1.0.0", "config-rc", "--runtime-config=rc"},
			want:   want{errSub: "does not support runtime configuration"},
		},
		"DefaultName": {
			reason: "Without a name the package is named for its repository, as a DNS label.",
			args:   []string{"function", "xpkg.crossplane.io/crossplane-contrib/function-patch-and-transform:v0.8.0"},
			want: want{
				obj: &v1.Function{ObjectMeta: metav1.ObjectMeta{Name: "crossplane-contrib-function-patch-and-transform"}},
				spec: v1.PackageSpec{
					Package:                  "xpkg.crossplane.io/crossplane-contrib/function-patch-and-transform:v0.8.0",
					RevisionActivationPolicy: &auto,
					RevisionHistoryLimit:     ptr.To[int64](0),
				},
				rcName: "default",
				stdout: "function/crossplane-contrib-function-patch-and-transform created\n",
			},
		},
		"AlreadyExists": {
			reason: "Installing a package under a name that is taken fails.",
			args:   []string{"provider", "xpkg.crossplane.io/crossplane-contrib/provider-nop:v0.4.0", "provider-a"},
			want:   want{errSub: "already exists"},
		},
	}

	// Cases share a cluster and AlreadyExists depends on Provider, so they run in order.
	for _, name := range []string{
		"Provider", "ConfigurationWithFlags", "FunctionWithRuntimeConfig", "ProviderWithRuntimeConfig",
		"ConfigurationWithRuntimeConfig", "DefaultName", "AlreadyExists",
	} {
		tc := cases[name]
		t.Run(name, func(t *testing.T) {
			stdout, err := runInstall(t, tc.args...)

			if tc.want.errSub != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want.errSub) {
					t.Fatalf("\n%s\nRun(): want error containing %q, got %v", tc.reason, tc.want.errSub, err)
				}

				return
			}

			if err != nil {
				t.Fatalf("\n%s\nRun(): %v", tc.reason, err)
			}

			if diff := cmp.Diff(tc.want.stdout, stdout); diff != "" {
				t.Errorf("\n%s\nstdout: -want, +got:\n%s", tc.reason, diff)
			}

			got := tc.want.obj
			key := client.ObjectKey{Name: got.GetName()}
			if key.Name == "" {
				key.Name = tc.args[2]
			}

			if err := cluster.Client.Get(cluster.Context, key, got); err != nil {
				t.Fatalf("\n%s\nGet(%s): %v", tc.reason, key.Name, err)
			}

			gotSpec := v1.PackageSpec{
				Package:                  got.GetSource(),
				RevisionActivationPolicy: got.GetActivationPolicy(),
				RevisionHistoryLimit:     got.GetRevisionHistoryLimit(),
				PackagePullSecrets:       got.GetPackagePullSecrets(),
			}
			if diff := cmp.Diff(tc.want.spec, gotSpec); diff != "" {
				t.Errorf("\n%s\nspec: -want, +got:\n%s", tc.reason, diff)
			}

			gotRC := ""
			if rp, ok := got.(v1.PackageWithRuntime); ok && rp.GetRuntimeConfigRef() != nil {
				gotRC = rp.GetRuntimeConfigRef().Name
			}

			if gotRC != tc.want.rcName {
				t.Errorf("\n%s\nruntimeConfigRef.name: want %q, got %q", tc.reason, tc.want.rcName, gotRC)
			}
		})
	}
}

// TestInstallWaitIntegration checks that --wait returns once the package reports Healthy, and fails when it never
// does.
func TestInstallWaitIntegration(t *testing.T) {
	cluster := startInstallCluster(t)

	cases := map[string]struct {
		reason  string
		name    string
		healthy bool
		wantErr bool
	}{
		"BecomesHealthy": {
			reason:  "Run returns success once the package's Healthy condition turns true.",
			name:    "provider-healthy",
			healthy: true,
		},
		"NeverHealthy": {
			reason:  "Run fails when the package isn't healthy before the wait times out.",
			name:    "provider-unhealthy",
			wantErr: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if tc.healthy {
				// No package manager runs under envtest, so mark the package healthy once Run has created it.
				go func() {
					p := &v1.Provider{}
					for cluster.Context.Err() == nil {
						if err := cluster.Client.Get(cluster.Context, client.ObjectKey{Name: tc.name}, p); err == nil {
							p.SetConditions(v1.Healthy())
							if err := cluster.Client.Status().Update(cluster.Context, p); err == nil {
								return
							}
						}

						time.Sleep(100 * time.Millisecond)
					}
				}()
			}

			start := time.Now()
			_, err := runInstall(t, "provider", "xpkg.crossplane.io/acme/prov:v1.0.0", tc.name, "--wait=5s")

			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "Package did not become ready") {
					t.Fatalf("\n%s\nRun(): want a not-ready error, got %v", tc.reason, err)
				}

				return
			}

			if err != nil {
				t.Fatalf("\n%s\nRun(): %v", tc.reason, err)
			}

			if elapsed := time.Since(start); elapsed >= 5*time.Second {
				t.Errorf("\n%s\nRun() returned after %s, want before the 5s wait elapsed", tc.reason, elapsed)
			}
		})
	}
}

// TestInstallMissingCRDIntegration checks the error install returns when the cluster doesn't serve the package
// kind, such as one without Crossplane installed.
func TestInstallMissingCRDIntegration(t *testing.T) {
	cluster := startInstallCluster(t)

	// Start always serves Crossplane's CRDs, so remove the one this case needs gone.
	crd := &extv1.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{Name: "providers.pkg.crossplane.io"}}
	if err := cluster.Client.Delete(cluster.Context, crd); err != nil {
		t.Fatalf("cannot delete the Provider CRD: %v", err)
	}

	for cluster.Client.Get(cluster.Context, client.ObjectKeyFromObject(crd), crd) == nil {
		time.Sleep(100 * time.Millisecond)
	}

	// The client's RESTMapper fails before any request, so the error is a no-match, not the 404 warnIfNotFound
	// annotates with a hint that the CLI might be out of date.
	_, err := runInstall(t, "provider", "xpkg.crossplane.io/acme/prov:v1.0.0", "provider-missing")
	if err == nil || !strings.Contains(err.Error(), `no matches for kind "Provider"`) {
		t.Fatalf("Run(): want a no-match error for the Provider kind, got %v", err)
	}

	if strings.Contains(err.Error(), "might be out of date") {
		t.Errorf("Run(): warnIfNotFound unexpectedly annotated a no-match error: %v", err)
	}
}
