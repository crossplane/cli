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

package client

import (
	"testing"

	"k8s.io/client-go/rest"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
)

func TestNew(t *testing.T) {
	cases := map[string]struct {
		reason string
		config *rest.Config
		err    bool
	}{
		"ValidConfig": {
			reason: "A valid config should build every client without contacting the cluster.",
			config: &rest.Config{Host: "https://example.com"},
		},
		"InvalidTLS": {
			reason: "A config with unusable TLS data should fail client creation.",
			config: &rest.Config{
				Host:            "https://example.com",
				TLSClientConfig: rest.TLSClientConfig{CAData: []byte("not a cert"), CertData: []byte("x")},
			},
			err: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := New(tc.config, logging.NewNopLogger())
			if gotErr := err != nil; gotErr != tc.err {
				t.Fatalf("\n%s\nNew(...): want error %t, got %v", tc.reason, tc.err, err)
			}
			if tc.err {
				return
			}
			if got == nil || got.K8sClients.Resource == nil || got.XpClients.Composition == nil || got.XpClients.Function == nil {
				t.Errorf("\n%s\nNew(...): want fully populated clients, got %+v", tc.reason, got)
			}
		})
	}
}
