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
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func TestRESTConfig(t *testing.T) {
	dir := t.TempDir()
	flag := writeKubeconfig(t, filepath.Join(dir, "flag"), "https://flag.example.com")
	env := writeKubeconfig(t, filepath.Join(dir, "env"), "https://env.example.com")
	home := writeKubeconfig(t, filepath.Join(dir, "home", ".kube", "config"), "https://home.example.com")

	type args struct {
		env   string
		flags ConfigFlags
	}

	type want struct {
		host string
		qps  float32
		err  error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"FlagWinsOverEnv": {
			reason: "--kubeconfig takes precedence over KUBECONFIG.",
			args:   args{env: env, flags: ConfigFlags{Kubeconfig: flag}},
			want:   want{host: "https://flag.example.com", qps: -1},
		},
		"FlagOnly": {
			reason: "--kubeconfig is used when KUBECONFIG is unset.",
			args:   args{flags: ConfigFlags{Kubeconfig: flag}},
			want:   want{host: "https://flag.example.com", qps: -1},
		},
		"EnvWhenFlagUnset": {
			reason: "KUBECONFIG is used when --kubeconfig is unset.",
			args:   args{env: env},
			want:   want{host: "https://env.example.com", qps: -1},
		},
		"MissingFlagFile": {
			reason: "A --kubeconfig that does not exist is an error, even if KUBECONFIG is set.",
			args:   args{env: env, flags: ConfigFlags{Kubeconfig: filepath.Join(dir, "missing")}},
			want:   want{err: cmpopts.AnyError},
		},
		"HomeWhenFlagAndEnvUnset": {
			reason: "~/.kube/config is used outside a cluster when neither --kubeconfig nor KUBECONFIG is set.",
			want:   want{host: "https://home.example.com", qps: -1},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv(clientcmd.RecommendedConfigPathEnvVar, tc.args.env)
			t.Setenv("KUBERNETES_SERVICE_HOST", "")
			t.Setenv("KUBERNETES_SERVICE_PORT", "")
			recommended := clientcmd.RecommendedHomeFile
			clientcmd.RecommendedHomeFile = home
			t.Cleanup(func() { clientcmd.RecommendedHomeFile = recommended })

			cfg, err := tc.args.flags.RESTConfig()
			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("%s\nRESTConfig(): -want error, +got error:\n%s", tc.reason, diff)
			}
			if err != nil {
				return
			}
			if diff := cmp.Diff(tc.want.host, cfg.Host); diff != "" {
				t.Errorf("%s\nRESTConfig(): -want host, +got host:\n%s", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.qps, cfg.QPS); diff != "" {
				t.Errorf("%s\nRESTConfig(): -want QPS, +got QPS:\n%s", tc.reason, diff)
			}
		})
	}
}

func writeKubeconfig(t *testing.T, path, server string) string {
	t.Helper()

	cfg := clientcmdapi.NewConfig()
	cfg.Clusters["cluster"] = &clientcmdapi.Cluster{Server: server}
	cfg.AuthInfos["user"] = &clientcmdapi.AuthInfo{Token: "token"}
	cfg.Contexts["context"] = &clientcmdapi.Context{Cluster: "cluster", AuthInfo: "user"}
	cfg.CurrentContext = "context"

	if err := clientcmd.WriteToFile(*cfg, path); err != nil {
		t.Fatalf("WriteToFile(%q): unexpected error: %v", path, err)
	}

	return path
}
