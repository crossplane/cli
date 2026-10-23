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

package kube

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"k8s.io/client-go/rest"
)

const testKubeconfig = `apiVersion: v1
kind: Config
current-context: one
clusters:
- name: c1
  cluster: {server: "https://one.example.com"}
- name: c2
  cluster: {server: "https://two.example.com"}
contexts:
- name: one
  context: {cluster: c1, user: u, namespace: ns-one}
- name: two
  context: {cluster: c2, user: u, namespace: ns-two}
users:
- name: u
  user: {token: abc}
`

func TestRESTConfig(t *testing.T) {
	type args struct {
		kubeContext string
		imp         ImpersonationFlags
	}
	type want struct {
		host        string
		namespace   string
		impersonate rest.ImpersonationConfig
		qps         float32
		burst       int
		err         bool
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"CurrentContext": {
			reason: "An empty context should use the kubeconfig's current context and default QPS/Burst.",
			want:   want{host: "https://one.example.com", namespace: "ns-one", qps: 20, burst: 30},
		},
		"ContextOverride": {
			reason: "A named context should override the current context.",
			args:   args{kubeContext: "two"},
			want:   want{host: "https://two.example.com", namespace: "ns-two", qps: 20, burst: 30},
		},
		"UnknownContext": {
			reason: "A context missing from the kubeconfig should error.",
			args:   args{kubeContext: "nope"},
			want:   want{err: true},
		},
		"Impersonation": {
			reason: "Impersonation flags should be applied to the config.",
			args:   args{imp: ImpersonationFlags{As: "jane", AsGroup: []string{"admins"}, AsUID: "u1"}},
			want: want{
				host: "https://one.example.com", namespace: "ns-one", qps: 20, burst: 30,
				impersonate: rest.ImpersonationConfig{UserName: "jane", Groups: []string{"admins"}, UID: "u1"},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config")
			if err := os.WriteFile(path, []byte(testKubeconfig), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("KUBECONFIG", path)

			cfg, cc, err := RESTConfig(tc.args.kubeContext, tc.args.imp)
			if gotErr := err != nil; gotErr != tc.want.err {
				t.Fatalf("\n%s\nRESTConfig(...): want error %t, got %v", tc.reason, tc.want.err, err)
			}
			if tc.want.err {
				return
			}

			ns, _, err := cc.Namespace()
			if err != nil {
				t.Fatalf("Namespace(): %v", err)
			}
			got := want{host: cfg.Host, namespace: ns, impersonate: cfg.Impersonate, qps: cfg.QPS, burst: cfg.Burst}
			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(want{})); diff != "" {
				t.Errorf("\n%s\nRESTConfig(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestRESTConfigMissingKubeconfig(t *testing.T) {
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "absent"))
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KUBERNETES_SERVICE_HOST", "")

	if _, _, err := RESTConfig("", ImpersonationFlags{}); err == nil {
		t.Error("RESTConfig(...): want error for a missing kubeconfig, got nil")
	}
}
