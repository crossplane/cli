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
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

// RESTConfig loads the user's kubeconfig, optionally overriding the current
// context, applies impersonation, and raises unset QPS/Burst to 20/30. It also
// returns the ClientConfig so callers can resolve the context's namespace.
func RESTConfig(kubeContext string, imp ImpersonationFlags) (*rest.Config, clientcmd.ClientConfig, error) {
	clientconfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{CurrentContext: kubeContext},
	)

	cfg, err := clientconfig.ClientConfig()
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to get kubeconfig")
	}

	imp.Apply(cfg)

	// client-go defaults unset QPS/Burst to 5/10 with client-side throttling,
	// which is painfully slow for commands that do several reads per resource.
	if cfg.QPS == 0 {
		cfg.QPS = 20
	}
	if cfg.Burst == 0 {
		cfg.Burst = 30
	}

	return cfg, clientconfig, nil
}
