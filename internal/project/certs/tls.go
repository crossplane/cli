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
	"context"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
)

const (
	// RootCACertSecretName is the name of the secret that will store CA
	// certificates. The rest of the certificates created per entity will be
	// signed by this CA.
	RootCACertSecretName = "crossplane-ca"

	// SecretKeyCACert is the secret key of CA certificate.
	SecretKeyCACert = "ca.crt"
)

// TLSCertificateGenerator generates TLS certificate bundles and stores them
// in k8s secrets.
type TLSCertificateGenerator struct {
	namespace           string
	caSecretName        string
	tlsServerSecretName *string
	tlsServerDNSNames   []string
	certificate         CertificateGenerator
	log                 logging.Logger
}

// TLSCertificateGeneratorOption configures TLSCertificateGenerator behavior.
type TLSCertificateGeneratorOption func(*TLSCertificateGenerator)

// TLSCertificateGeneratorWithLogger configures the logger.
func TLSCertificateGeneratorWithLogger(log logging.Logger) TLSCertificateGeneratorOption {
	return func(g *TLSCertificateGenerator) {
		g.log = log
	}
}

// TLSCertificateGeneratorWithServerSecretName sets the server secret name and
// DNS names.
func TLSCertificateGeneratorWithServerSecretName(s string, dnsNames []string) TLSCertificateGeneratorOption {
	return func(g *TLSCertificateGenerator) {
		g.tlsServerSecretName = &s
		g.tlsServerDNSNames = dnsNames
	}
}

// NewTLSCertificateGenerator returns a new TLSCertificateGenerator.
func NewTLSCertificateGenerator(ns, caSecret string, opts ...TLSCertificateGeneratorOption) *TLSCertificateGenerator {
	e := &TLSCertificateGenerator{
		namespace:    ns,
		caSecretName: caSecret,
		certificate:  NewCertGenerator(),
		log:          logging.NewNopLogger(),
	}

	for _, f := range opts {
		f(e)
	}
	return e
}

func (e *TLSCertificateGenerator) loadOrGenerateCA(ctx context.Context, kube client.Client, nn types.NamespacedName) (*CertificateSigner, error) {
	caSecret := &corev1.Secret{}

	err := kube.Get(ctx, nn, caSecret)
	if resource.IgnoreNotFound(err) != nil {
		return nil, errors.Wrapf(err, "cannot get TLS secret: %s", nn.Name)
	}

	create := true
	if err == nil {
		create = false
		kd := caSecret.Data[corev1.TLSPrivateKeyKey]
		cd := caSecret.Data[corev1.TLSCertKey]
		if len(kd) != 0 && len(cd) != 0 {
			e.log.Info("TLS CA secret is complete.")
			return parseCertificateSigner(kd, cd)
		}
	}
	e.log.Info("TLS CA secret is empty or not complete, generating a new CA...")

	a := &x509.Certificate{
		SerialNumber:          big.NewInt(2022),
		Subject:               pkixName,
		Issuer:                pkixName,
		DNSNames:              []string{RootCACertSecretName},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCRLSign | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}

	caKeyByte, caCrtByte, err := e.certificate.Generate(a, nil)
	if err != nil {
		return nil, errors.Wrap(err, "cannot generate CA certificate")
	}

	caSecret.Name = nn.Name
	caSecret.Namespace = nn.Namespace
	caSecret.Data = map[string][]byte{
		corev1.TLSCertKey:       caCrtByte,
		corev1.TLSPrivateKeyKey: caKeyByte,
	}
	if create {
		err = kube.Create(ctx, caSecret)
	} else {
		err = kube.Update(ctx, caSecret)
	}
	if err != nil {
		return nil, errors.Wrapf(err, "cannot create or update secret: %s", nn.Name)
	}

	return parseCertificateSigner(caKeyByte, caCrtByte)
}

func (e *TLSCertificateGenerator) ensureServerCertificate(ctx context.Context, kube client.Client, nn types.NamespacedName, signer *CertificateSigner) error {
	sec := &corev1.Secret{}

	err := kube.Get(ctx, nn, sec)
	if resource.IgnoreNotFound(err) != nil {
		return errors.Wrapf(err, "cannot get TLS secret: %s", nn.Name)
	}

	create := true
	if err == nil {
		create = false
		if len(sec.Data[corev1.TLSCertKey]) != 0 || len(sec.Data[corev1.TLSPrivateKeyKey]) != 0 || len(sec.Data[SecretKeyCACert]) != 0 {
			e.log.Info("TLS secret contains server certificate.", "secret", nn.Name)
			return nil
		}
	}
	e.log.Info("Server certificates are empty or not complete, generating a new pair...", "secret", nn.Name)
	dnsNames := e.tlsServerDNSNames
	if len(dnsNames) == 0 {
		return errors.New("server DNS names are empty, you must provide at least one DNS name")
	}

	cert := &x509.Certificate{
		SerialNumber:          big.NewInt(2022),
		Subject:               pkixName,
		DNSNames:              dnsNames,
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		IsCA:                  false,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageDataEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	keyData, certData, err := e.certificate.Generate(cert, signer)
	if err != nil {
		return errors.Wrap(err, "cannot generate tls certificate")
	}

	sec.Name = nn.Name
	sec.Namespace = nn.Namespace
	if sec.Data == nil {
		sec.Data = make(map[string][]byte)
	}
	sec.Data[corev1.TLSCertKey] = certData
	sec.Data[corev1.TLSPrivateKeyKey] = keyData
	sec.Data[SecretKeyCACert] = signer.certificatePEM

	if create {
		err = kube.Create(ctx, sec)
	} else {
		err = kube.Update(ctx, sec)
	}
	return errors.Wrapf(err, "cannot create or update secret: %s", nn.Name)
}

// Run generates the TLS certificate bundle and stores it in k8s secrets.
func (e *TLSCertificateGenerator) Run(ctx context.Context, kube client.Client) error {
	if e.tlsServerSecretName == nil {
		return nil
	}
	signer, err := e.loadOrGenerateCA(ctx, kube, types.NamespacedName{
		Name:      e.caSecretName,
		Namespace: e.namespace,
	})
	if err != nil {
		return errors.Wrap(err, "cannot load or generate certificate signer")
	}

	if e.tlsServerSecretName != nil {
		if err := e.ensureServerCertificate(ctx, kube, types.NamespacedName{
			Name:      *e.tlsServerSecretName,
			Namespace: e.namespace,
		}, signer); err != nil {
			return errors.Wrap(err, "could not generate server certificate")
		}
	}

	return nil
}

func parseCertificateSigner(key, cert []byte) (*CertificateSigner, error) {
	block, _ := pem.Decode(key)
	if block == nil {
		return nil, errors.New("cannot decode key")
	}

	sKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.Wrap(err, "cannot parse CA key")
	}

	block, _ = pem.Decode(cert)
	if block == nil {
		return nil, errors.New("cannot decode cert")
	}

	sCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, errors.Wrap(err, "cannot parse CA certificate")
	}

	return &CertificateSigner{
		key:            sKey,
		certificate:    sCert,
		certificatePEM: cert,
	}, nil
}
