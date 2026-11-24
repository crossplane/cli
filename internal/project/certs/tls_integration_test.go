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

package certs

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"

	"github.com/crossplane/cli/v2/internal/testutils/integration"
)

func TestMain(m *testing.M) {
	integration.ReapOrphanedEnvtest(func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) })
	os.Exit(m.Run())
}

// TestTLSCertificateGeneratorIntegration runs the generator against a real API server and checks the CA and server
// secrets it leaves there.
func TestTLSCertificateGeneratorIntegration(t *testing.T) {
	cluster := integration.Start(t, integration.Setup{})

	// A CA generated up front, for the cases where one already exists.
	existingCAKey, existingCACert, err := NewCertGenerator().Generate(&x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkixName,
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}, nil)
	if err != nil {
		t.Fatalf("cannot generate CA: %v", err)
	}

	type args struct {
		// existing are the secrets in the namespace before Run, keyed by name.
		existing map[string]map[string][]byte
		dnsNames []string
		// runs is how many times Run is called; zero means once.
		runs int
	}

	type want struct {
		err error
		// caCert is the CA certificate the secrets should hold; empty means any newly generated one.
		caCert []byte
		// serverData is the server secret's exact data, when it should be left as it was.
		serverData map[string][]byte
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"NoSecrets": {
			reason: "A CA and a server certificate it signs for the DNS names should be created.",
			args:   args{dnsNames: []string{"registry", "registry.ns.svc"}},
		},
		"Idempotent": {
			reason: "Running again should keep the CA and server certificate the first run created.",
			args:   args{dnsNames: []string{"registry"}, runs: 2},
		},
		"ExistingCA": {
			reason: "A complete CA secret should be reused to sign the server certificate.",
			args: args{
				existing: map[string]map[string][]byte{RootCACertSecretName: {
					corev1.TLSCertKey: existingCACert, corev1.TLSPrivateKeyKey: existingCAKey,
				}},
				dnsNames: []string{"registry"},
			},
			want: want{caCert: existingCACert},
		},
		"IncompleteCA": {
			reason: "A CA secret without a key should be replaced with a new CA.",
			args: args{
				existing: map[string]map[string][]byte{RootCACertSecretName: {corev1.TLSCertKey: existingCACert}},
				dnsNames: []string{"registry"},
			},
		},
		"MalformedCA": {
			reason: "A CA secret whose key isn't PEM should fail rather than be overwritten.",
			args: args{
				existing: map[string]map[string][]byte{RootCACertSecretName: {
					corev1.TLSCertKey: existingCACert, corev1.TLSPrivateKeyKey: []byte("not pem"),
				}},
				dnsNames: []string{"registry"},
			},
			want: want{err: errors.Wrap(errors.New("cannot decode key"), "cannot load or generate certificate signer")},
		},
		"ExistingServerSecret": {
			reason: "A server secret that already holds any certificate data should be left as it is.",
			args: args{
				existing: map[string]map[string][]byte{"server": {SecretKeyCACert: []byte("kept")}},
				dnsNames: []string{"registry"},
			},
			want: want{serverData: map[string][]byte{SecretKeyCACert: []byte("kept")}},
		},
		"EmptyServerSecret": {
			reason: "An empty server secret should be filled with a new certificate.",
			args: args{
				existing: map[string]map[string][]byte{"server": {}},
				dnsNames: []string{"registry"},
			},
		},
		"NoDNSNames": {
			reason: "Issuing a server certificate without DNS names should fail.",
			args:   args{},
			want: want{err: errors.Wrap(errors.New("server DNS names are empty, you must provide at least one DNS name"),
				"could not generate server certificate")},
		},
	}

	i := 0
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// Each case gets its own namespace, so the secrets one leaves don't affect another.
			i++
			ns := fmt.Sprintf("certs-%d", i)
			if err := cluster.Client.Create(cluster.Context, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
				t.Fatalf("cannot create namespace: %v", err)
			}

			for n, data := range tc.args.existing {
				s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: n, Namespace: ns}, Data: data}
				if err := cluster.Client.Create(cluster.Context, s); err != nil {
					t.Fatalf("cannot create secret %s: %v", n, err)
				}
			}

			g := NewTLSCertificateGenerator(ns, RootCACertSecretName, TLSCertificateGeneratorWithServerSecretName("server", tc.args.dnsNames))

			var firstServer map[string][]byte
			for run := range max(tc.args.runs, 1) {
				err := g.Run(cluster.Context, cluster.Client)
				if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
					t.Fatalf("\n%s\nRun(): -want error, +got error:\n%s", tc.reason, diff)
				}

				if tc.want.err != nil {
					return
				}

				if run == 0 {
					firstServer = getSecret(t, cluster, ns, "server").Data
				}
			}

			server := getSecret(t, cluster, ns, "server")

			if tc.want.serverData != nil {
				if diff := cmp.Diff(tc.want.serverData, server.Data); diff != "" {
					t.Errorf("\n%s\nserver secret data: -want, +got:\n%s", tc.reason, diff)
				}

				return
			}

			if diff := cmp.Diff(firstServer, server.Data); diff != "" {
				t.Errorf("\n%s\nserver secret changed after the first run: -first, +last:\n%s", tc.reason, diff)
			}

			ca := getSecret(t, cluster, ns, RootCACertSecretName)
			caCert := ca.Data[corev1.TLSCertKey]
			if tc.want.caCert != nil && !bytes.Equal(tc.want.caCert, caCert) {
				t.Errorf("\n%s\nCA secret: the existing CA certificate was replaced", tc.reason)
			}

			if !bytes.Equal(caCert, server.Data[SecretKeyCACert]) {
				t.Errorf("\n%s\nserver secret's %s isn't the CA secret's certificate", tc.reason, SecretKeyCACert)
			}

			// The server certificate should verify against the CA for each DNS name.
			roots := x509.NewCertPool()
			if !roots.AppendCertsFromPEM(caCert) {
				t.Fatalf("\n%s\nCA secret holds no PEM certificate", tc.reason)
			}

			block, _ := pem.Decode(server.Data[corev1.TLSCertKey])
			if block == nil {
				t.Fatalf("\n%s\nserver secret holds no PEM certificate", tc.reason)
			}

			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				t.Fatalf("\n%s\ncannot parse server certificate: %v", tc.reason, err)
			}

			if diff := cmp.Diff(tc.args.dnsNames, cert.DNSNames); diff != "" {
				t.Errorf("\n%s\nserver certificate DNS names: -want, +got:\n%s", tc.reason, diff)
			}

			for _, dns := range tc.args.dnsNames {
				if _, err := cert.Verify(x509.VerifyOptions{DNSName: dns, Roots: roots}); err != nil {
					t.Errorf("\n%s\nserver certificate doesn't verify for %s: %v", tc.reason, dns, err)
				}
			}

			// The key should match the certificate.
			if _, err := tls.X509KeyPair(server.Data[corev1.TLSCertKey], server.Data[corev1.TLSPrivateKeyKey]); err != nil {
				t.Errorf("\n%s\ncannot parse server key pair: %v", tc.reason, err)
			}
		})
	}
}

func getSecret(t *testing.T, cluster *integration.Cluster, ns, name string) *corev1.Secret {
	t.Helper()

	s := &corev1.Secret{}
	if err := cluster.Client.Get(cluster.Context, client.ObjectKey{Namespace: ns, Name: name}, s); err != nil {
		t.Fatalf("cannot get secret %s: %v", name, err)
	}

	return s
}
