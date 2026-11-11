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

package version

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/crossplane/cli/v2/internal/kube"
	"github.com/crossplane/cli/v2/internal/testutils/integration"
)

func TestMain(m *testing.M) {
	integration.ReapOrphanedEnvtest(func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) })
	os.Exit(m.Run())
}

// deployment returns a Deployment in crossplane-system with the given labels and first container image.
func deployment(name string, labels map[string]string, image string) *appsv1.Deployment {
	sel := map[string]string{"pod": name}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "crossplane-system", Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: sel},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: sel},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: image}}},
			},
		},
	}
}

// TestFetchCrossplaneVersionIntegration reads the Crossplane version from Deployments in a real API server. Each
// case gets its own cluster, since FetchCrossplaneVersion lists Deployments in every namespace. Cases run in turn
// because each sets KUBECONFIG.
func TestFetchCrossplaneVersionIntegration(t *testing.T) {
	xp := map[string]string{"app": "crossplane"}
	withVersion := func(v string) map[string]string {
		return map[string]string{"app": "crossplane", "app.kubernetes.io/version": v}
	}

	type want struct {
		version string
		errSub  string
	}

	cases := map[string]struct {
		reason      string
		deployments []*appsv1.Deployment
		want        want
	}{
		"VersionLabel": {
			reason:      "A version label without a leading v is returned with one.",
			deployments: []*appsv1.Deployment{deployment("crossplane", withVersion("2.0.0"), "xpkg.crossplane.io/crossplane/crossplane:v9.9.9")},
			want:        want{version: "v2.0.0"},
		},
		"VersionLabelWithV": {
			reason:      "A version label that already has a leading v is returned unchanged.",
			deployments: []*appsv1.Deployment{deployment("crossplane", withVersion("v2.0.0"), "xpkg.crossplane.io/crossplane/crossplane:v9.9.9")},
			want:        want{version: "v2.0.0"},
		},
		"ImageTag": {
			reason:      "Without a version label, the image tag is returned with a leading v added.",
			deployments: []*appsv1.Deployment{deployment("crossplane", xp, "xpkg.crossplane.io/crossplane/crossplane:2.1.0")},
			want:        want{version: "v2.1.0"},
		},
		"ImageTagWithV": {
			reason:      "Without a version label, an image tag that has a leading v is returned unchanged.",
			deployments: []*appsv1.Deployment{deployment("crossplane", xp, "xpkg.crossplane.io/crossplane/crossplane:v2.1.0")},
			want:        want{version: "v2.1.0"},
		},
		"ImageDigest": {
			reason: "An image referenced by digest has no tag, so no version is found.",
			deployments: []*appsv1.Deployment{deployment("crossplane", xp,
				"xpkg.crossplane.io/crossplane/crossplane@sha256:"+strings.Repeat("a", 64))},
			want: want{errSub: "Crossplane version or image tag not found"},
		},
		"NoDeployments": {
			reason: "With no Deployments there is no version to find.",
			want:   want{errSub: "Crossplane version or image tag not found"},
		},
		"NoMatchingDeployments": {
			reason:      "Deployments without the app=crossplane label are ignored.",
			deployments: []*appsv1.Deployment{deployment("other", map[string]string{"app": "other", "app.kubernetes.io/version": "2.0.0"}, "example.com/other:v1.0.0")},
			want:        want{errSub: "Crossplane version or image tag not found"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cluster := integration.Start(t, integration.Setup{})

			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "crossplane-system"}}
			if err := cluster.Client.Create(cluster.Context, ns); err != nil {
				t.Fatalf("cannot create namespace: %v", err)
			}

			for _, d := range tc.deployments {
				if err := cluster.Client.Create(cluster.Context, d); err != nil {
					t.Fatalf("cannot create deployment %s: %v", d.GetName(), err)
				}
			}

			kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
			if err := os.WriteFile(kubeconfig, cluster.Kubeconfig, 0o600); err != nil {
				t.Fatalf("cannot write kubeconfig: %v", err)
			}

			// FetchCrossplaneVersion loads its config via ctrl.GetConfig.
			t.Setenv("KUBECONFIG", kubeconfig)

			got, err := FetchCrossplaneVersion(cluster.Context, kube.ImpersonationFlags{})

			if tc.want.errSub != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want.errSub) {
					t.Fatalf("\n%s\nFetchCrossplaneVersion(): want error containing %q, got %q, %v", tc.reason, tc.want.errSub, got, err)
				}

				return
			}

			if err != nil {
				t.Fatalf("\n%s\nFetchCrossplaneVersion(): %v", tc.reason, err)
			}

			if got != tc.want.version {
				t.Errorf("\n%s\nFetchCrossplaneVersion(): want %q, got %q", tc.reason, tc.want.version, got)
			}
		})
	}
}
