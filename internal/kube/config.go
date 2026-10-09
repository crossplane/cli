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

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

// ConfigFlags are the kubectl-compatible flags that select the kubeconfig
// used to reach the cluster. Embed it into a command's Kong flag struct with
// the `embed:""` tag.
type ConfigFlags struct {
	Kubeconfig string `help:"Path to the kubeconfig file to use for CLI requests." name:"kubeconfig" placeholder:"PATH" predictor:"file"`
}

// ClientConfig returns a client config for the selected kubeconfig. An
// explicit --kubeconfig wins over the KUBECONFIG environment variable and the
// default ~/.kube/config location.
func (f ConfigFlags) ClientConfig(overrides *clientcmd.ConfigOverrides) clientcmd.ClientConfig {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	rules.ExplicitPath = f.Kubeconfig

	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides)
}

// RESTConfig returns a rest.Config with the precedence of controller-runtime's
// config.GetConfig: the --kubeconfig flag, then the KUBECONFIG environment
// variable, then the in-cluster config, then ~/.kube/config. Client-side rate
// limiting is disabled unless the kubeconfig sets it.
func (f ConfigFlags) RESTConfig() (*rest.Config, error) {
	if f.Kubeconfig == "" && os.Getenv(clientcmd.RecommendedConfigPathEnvVar) == "" {
		if cfg, err := rest.InClusterConfig(); err == nil {
			return withDefaultQPS(cfg), nil
		}
	}

	cfg, err := f.ClientConfig(&clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, errors.Wrap(err, "cannot load a kubeconfig, use --kubeconfig to select one")
	}

	return withDefaultQPS(cfg), nil
}

func withDefaultQPS(cfg *rest.Config) *rest.Config {
	if cfg.QPS == 0 {
		cfg.QPS = -1
	}

	return cfg
}
