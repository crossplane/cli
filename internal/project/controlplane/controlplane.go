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

// Package controlplane manages local development control planes.
package controlplane

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/kind/pkg/apis/config/defaults"
	"sigs.k8s.io/kind/pkg/apis/config/v1alpha4"
	kind "sigs.k8s.io/kind/pkg/cluster"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	pkgv1beta1 "github.com/crossplane/crossplane/apis/v2/pkg/v1beta1"

	"github.com/crossplane/cli/v2/internal/docker"
	"github.com/crossplane/cli/v2/internal/project"
	"github.com/crossplane/cli/v2/internal/project/certs"
	"github.com/crossplane/cli/v2/internal/project/helm"
)

const (
	crossplaneNamespace = "crossplane-system"
)

// DevControlPlane is a local development control plane.
type DevControlPlane interface {
	// Info returns human-friendly information about the control plane.
	Info() string
	// Client returns a controller-runtime client for the control plane.
	Client() client.Client
	// Kubeconfig returns a kubeconfig for the control plane.
	Kubeconfig() clientcmd.ClientConfig
	// Teardown tears down the control plane, deleting any resources it may use.
	Teardown(ctx context.Context) error
	// Sideload sideloads packages into the control plane.
	Sideload(ctx context.Context, imgMap project.ImageTagMap, tag name.Tag) error
}

type localDevControlPlane struct {
	name                string
	kubeconfig          clientcmd.ClientConfig
	client              client.Client
	registryDir         string
	registryContainerID string
	registryHostname    string
}

func (l *localDevControlPlane) Info() string {
	return fmt.Sprintf("Local dev control plane running in kind cluster %q.", l.name)
}

func (l *localDevControlPlane) Client() client.Client {
	return l.client
}

func (l *localDevControlPlane) Kubeconfig() clientcmd.ClientConfig {
	return l.kubeconfig
}

func (l *localDevControlPlane) Teardown(ctx context.Context) error {
	provider := kind.NewProvider()

	if err := ctx.Err(); err != nil {
		return err
	}

	if err := provider.Delete(l.name, ""); err != nil {
		return errors.Wrap(err, "failed to delete the local control plane")
	}

	if err := teardownLocalRegistry(ctx, l.registryContainerID); err != nil {
		return errors.Wrap(err, "failed to tear down registry")
	}

	_ = os.RemoveAll(l.registryDir)

	return nil
}

func (l *localDevControlPlane) Sideload(ctx context.Context, imgMap project.ImageTagMap, tag name.Tag) error {
	cfgImage, fnImages, err := project.SortImages(imgMap, tag.Repository.Name())
	if err != nil {
		return err
	}

	for repo, images := range fnImages {
		p := filepath.Join(l.registryDir, repo.RepositoryStr())
		if err := os.MkdirAll(p, 0o750); err != nil {
			return err
		}

		idx, _, err := project.BuildIndex(images...)
		if err != nil {
			return err
		}

		lp, err := layout.Write(p, empty.Index)
		if err != nil {
			return err
		}

		if err := lp.AppendIndex(idx, layout.WithAnnotations(map[string]string{
			"org.opencontainers.image.ref.name": tag.TagStr(),
		})); err != nil {
			return err
		}
	}

	p := filepath.Join(l.registryDir, tag.RepositoryStr())
	if err := os.MkdirAll(p, 0o750); err != nil {
		return err
	}

	lpath, err := layout.Write(p, empty.Index)
	if err != nil {
		return err
	}

	if err := lpath.AppendImage(cfgImage, layout.WithAnnotations(map[string]string{
		"org.opencontainers.image.ref.name": tag.TagStr(),
	})); err != nil {
		return err
	}

	// Make everything world-readable for unprivileged container access.
	if err := filepath.WalkDir(l.registryDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			return os.Chmod(path, 0o755) //nolint:gosec // Container needs to read the dir.
		}

		return os.Chmod(path, 0o644) //nolint:gosec // Container needs to read the file.
	}); err != nil {
		return errors.Wrap(err, "failed to adjust permissions on sideloaded images")
	}

	rewrite := path.Join(l.registryHostname, tag.RepositoryStr())
	imgcfg := &pkgv1beta1.ImageConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "local-registry",
		},
		Spec: pkgv1beta1.ImageConfigSpec{
			MatchImages: []pkgv1beta1.ImageMatch{{
				Type:   pkgv1beta1.Prefix,
				Prefix: tag.Repository.Name(),
			}},
			RewriteImage: &pkgv1beta1.ImageRewrite{
				Prefix: rewrite,
			},
		},
	}

	if err := pkgv1beta1.AddToScheme(l.client.Scheme()); err != nil {
		return err
	}
	if err := l.client.Create(ctx, imgcfg); err != nil && !kerrors.IsAlreadyExists(err) {
		return errors.Wrap(err, "failed to create image config")
	}

	return nil
}

// Option configures EnsureLocalDevControlPlane.
type Option func(*config)

type config struct {
	name              string
	crossplaneVersion string
	registryDir       string
	clusterAdmin      bool
	log               logging.Logger
}

// WithName sets the name of the local dev control plane.
func WithName(n string) Option {
	return func(c *config) {
		c.name = n
	}
}

// WithCrossplaneVersion sets the Crossplane version to install.
func WithCrossplaneVersion(v string) Option {
	return func(c *config) {
		c.crossplaneVersion = v
	}
}

// WithRegistryDir sets the directory for local registry images.
func WithRegistryDir(d string) Option {
	return func(c *config) {
		c.registryDir = d
	}
}

// WithClusterAdmin sets whether to grant Crossplane cluster admin privileges.
func WithClusterAdmin(enabled bool) Option {
	return func(c *config) {
		c.clusterAdmin = enabled
	}
}

// WithLogger sets the logger for progress updates.
func WithLogger(l logging.Logger) Option {
	return func(c *config) {
		c.log = l
	}
}

// EnsureLocalDevControlPlane creates or reuses a local kind-based development
// control plane with Crossplane installed.
func EnsureLocalDevControlPlane(ctx context.Context, opts ...Option) (DevControlPlane, error) { //nolint:gocyclo // Main orchestration function.
	cfg := &config{
		clusterAdmin: true,
		log:          logging.NewNopLogger(),
	}
	for _, opt := range opts {
		opt(cfg)
	}

	cfg.log.Debug("Checking Docker connectivity")
	if err := docker.Check(ctx); err != nil {
		return nil, errors.Wrap(err, "failed to connect to Docker; local dev control planes require a Docker-compatible container runtime")
	}

	// kind creates a docker container named <name>-control-plane, and uses the
	// name as the container's hostname. Hostnames can be at most 63 characters.
	nameLen := len(cfg.name)
	nameLen = min(nameLen, 63-len("-control-plane"))
	cfg.name = cfg.name[:nameLen]

	cfg.log.Debug("Ensuring kind cluster", "name", cfg.name)
	kubeconfig, err := ensureKindCluster(cfg.name)
	if err != nil {
		return nil, err
	}

	restConfig, err := kubeconfig.ClientConfig()
	if err != nil {
		return nil, errors.Wrap(err, "cannot get rest config")
	}

	cl, err := client.New(restConfig, client.Options{})
	if err != nil {
		return nil, errors.Wrap(err, "cannot construct control plane client")
	}

	// Create the crossplane namespace.
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: crossplaneNamespace,
		},
	}
	if err := cl.Create(ctx, ns); err != nil && !kerrors.IsAlreadyExists(err) {
		return nil, errors.Wrap(err, "failed to create crossplane-system namespace")
	}

	// Generate a CA and certificate for the local registry.
	regName := cfg.name + "-registry"
	cfg.log.Debug("Ensuring local registry certificate")
	certSecret, ca, err := ensureLocalRegistryCertificate(ctx, cl, regName)
	if err != nil {
		return nil, errors.Wrap(err, "cannot generate certificate for registry")
	}

	// Create a directory to store sideloaded images and spin up a registry
	// container that uses it.
	registryDir := cfg.registryDir
	if registryDir == "" {
		registryDir = filepath.Join(os.TempDir(), "crossplane-local-registry")
	}
	registryDir = filepath.Join(registryDir, cfg.name)
	if err := os.MkdirAll(registryDir, 0o755); err != nil { //nolint:gosec // Container needs to read the dir.
		return nil, err
	}

	cfg.log.Debug("Ensuring local registry container")
	cid, err := ensureLocalRegistry(ctx, cl, regName, registryDir, certSecret)
	if err != nil {
		return nil, err
	}

	cfg.log.Debug("Ensuring Crossplane is installed")
	if err := ensureCrossplane(restConfig, cfg.crossplaneVersion, ca.Name, cfg.clusterAdmin); err != nil {
		return nil, err
	}

	cfg.log.Debug("Local dev control plane ready")
	return &localDevControlPlane{
		name:                cfg.name,
		kubeconfig:          kubeconfig,
		client:              cl,
		registryDir:         registryDir,
		registryContainerID: cid,
		registryHostname:    regName + ":5000",
	}, nil
}

// TeardownLocalDevControlPlane tears down a local dev control plane by name.
// It deletes the kind cluster, stops the registry container, and removes the
// registry data directory.
func TeardownLocalDevControlPlane(ctx context.Context, name string, registryDir string) error {
	// Truncate name the same way EnsureLocalDevControlPlane does.
	nameLen := len(name)
	nameLen = min(nameLen, 63-len("-control-plane"))
	name = name[:nameLen]

	provider := kind.NewProvider()

	existing, err := provider.List()
	if err != nil {
		return errors.Wrap(err, "failed to list kind clusters")
	}
	if !slices.Contains(existing, name) {
		return errors.Errorf("kind cluster %q not found", name)
	}

	if err := provider.Delete(name, ""); err != nil {
		return errors.Wrap(err, "failed to delete the local control plane")
	}

	// Stop and remove the registry container.
	regName := name + "-registry"
	cid, found, err := docker.GetContainerIDByName(ctx, regName, true)
	if err != nil {
		return errors.Wrap(err, "failed to look up registry container")
	}
	if found {
		if err := teardownLocalRegistry(ctx, cid); err != nil {
			return errors.Wrap(err, "failed to tear down registry")
		}
	}

	// Remove the registry data directory.
	if registryDir == "" {
		registryDir = filepath.Join(os.TempDir(), "crossplane-local-registry")
	}
	registryDir = filepath.Join(registryDir, name)
	_ = os.RemoveAll(registryDir)

	return nil
}

func ensureKindCluster(clusterName string) (clientcmd.ClientConfig, error) {
	provider := kind.NewProvider()

	kubeconfigFile, err := os.CreateTemp("", "crossplane-*.kubeconfig")
	if err != nil {
		return nil, errors.Wrap(err, "failed to create temporary kubeconfig")
	}
	_ = kubeconfigFile.Close()
	defer func() { _ = os.Remove(kubeconfigFile.Name()) }()

	existing, err := provider.List()
	if err != nil {
		return nil, errors.Wrap(err, "failed to list kind clusters")
	}

	if slices.Contains(existing, clusterName) {
		if err := provider.ExportKubeConfig(clusterName, kubeconfigFile.Name(), false); err != nil {
			return nil, errors.Wrap(err, "failed to get kubeconfig for kind cluster")
		}
	} else {
		if err := createNewKindCluster(provider, clusterName, kubeconfigFile.Name()); err != nil {
			return nil, err
		}
	}

	kubeconfigBytes, err := os.ReadFile(kubeconfigFile.Name())
	if err != nil {
		return nil, errors.Wrap(err, "failed to load kubeconfig")
	}

	kubeconfig, err := clientcmd.NewClientConfigFromBytes(kubeconfigBytes)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse kubeconfig")
	}

	return kubeconfig, nil
}

func createNewKindCluster(provider *kind.Provider, clusterName, kubeconfigPath string) error {
	cfg := createKindClusterConfig()

	cfgBytes, err := yaml.Marshal(cfg)
	if err != nil {
		return errors.Wrap(err, "failed to marshal kind config")
	}

	if err := provider.Create(
		clusterName,
		kind.CreateWithRawConfig(cfgBytes),
		kind.CreateWithNodeImage(defaults.Image),
		kind.CreateWithDisplayUsage(false),
		kind.CreateWithDisplaySalutation(false),
		kind.CreateWithKubeconfigPath(kubeconfigPath),
	); err != nil {
		return errors.Wrap(err, "failed to create kind cluster")
	}

	return nil
}

func createKindClusterConfig() *v1alpha4.Cluster {
	return &v1alpha4.Cluster{
		TypeMeta: v1alpha4.TypeMeta{
			APIVersion: "kind.x-k8s.io/v1alpha4",
			Kind:       "Cluster",
		},
		Nodes: []v1alpha4.Node{{
			Role: v1alpha4.ControlPlaneRole,
		}},
		ContainerdConfigPatches: []string{
			"[plugins.\"io.containerd.grpc.v1.cri\".registry]\nconfig_path = \"/etc/containerd/certs.d\"\n",
		},
	}
}

func ensureCrossplane(restConfig *rest.Config, version, caConfigMap string, clusterAdmin bool) error {
	mgr, err := helm.NewManager(restConfig,
		"crossplane",
		"https://charts.crossplane.io/stable",
		crossplaneNamespace,
		helm.Wait(),
	)
	if err != nil {
		return errors.Wrap(err, "failed to build new helm manager")
	}

	// If crossplane is already installed, check the version.
	if v, err := mgr.GetCurrentVersion(); err == nil {
		if version != "" && v != version {
			return errors.Errorf("existing cluster has wrong crossplane version installed: got %s, want %s", v, version)
		}
		return nil
	}

	values := map[string]any{
		"args": []string{
			"--enable-dependency-version-upgrades",
		},
		"registryCaBundleConfig": map[string]string{
			"name": caConfigMap,
			"key":  certs.SecretKeyCACert,
		},
		"rbac": map[string]any{
			"clusterAdmin": clusterAdmin,
		},
	}
	if err = mgr.Install(version, values); err != nil {
		return errors.Wrap(err, "failed to install crossplane")
	}

	return nil
}

func ensureLocalRegistry(ctx context.Context, cl client.Client, regName, dir string, certSecret *corev1.Secret) (string, error) {
	const regImage = "ghcr.io/olareg/olareg:edge"
	certDir := filepath.Join(dir, ".certs")

	// Check for existing registry container.
	existing, found, err := docker.GetContainerIDByName(ctx, regName, true)
	if err != nil {
		return "", errors.Wrap(err, "failed to look up existing registry container")
	}
	if found {
		//nolint:gosec // We don't do anything dangerous with the CA data.
		caData, err := os.ReadFile(filepath.Join(certDir, "ca.crt"))
		if err == nil && bytes.Equal(caData, certSecret.Data[certs.SecretKeyCACert]) {
			if err := docker.StartContainerByID(ctx, existing); err != nil {
				return "", errors.Wrap(err, "failed to start existing registry container")
			}
			return existing, nil
		}

		if err := teardownLocalRegistry(ctx, existing); err != nil {
			return "", errors.Wrap(err, "failed to tear down outdated registry")
		}
	}

	// Write the TLS cert and key files.
	if err := os.MkdirAll(certDir, 0o755); err != nil { //nolint:gosec // Container needs to read the dir.
		return "", errors.New("failed to create cert directory")
	}
	if err := os.WriteFile(filepath.Join(certDir, "ca.crt"), certSecret.Data[certs.SecretKeyCACert], 0o644); err != nil { //nolint:gosec // Container needs to read the file.
		return "", errors.New("failed to write ca cert")
	}
	if err := os.WriteFile(filepath.Join(certDir, "tls.crt"), certSecret.Data[corev1.TLSCertKey], 0o644); err != nil { //nolint:gosec // Container needs to read the file.
		return "", errors.New("failed to write tls cert")
	}
	if err := os.WriteFile(filepath.Join(certDir, "tls.key"), certSecret.Data[corev1.TLSPrivateKeyKey], 0o644); err != nil { //nolint:gosec // Container needs to read the file.
		return "", errors.New("failed to write tls key")
	}

	// Find kind's network.
	nid, found, err := docker.GetNetworkIDByName(ctx, "kind")
	if err != nil {
		return "", errors.Wrap(err, "failed to get kind network ID")
	}
	if !found {
		return "", errors.New("missing kind network")
	}

	// Start the registry container.
	cid, err := docker.StartContainer(ctx, regName, regImage,
		docker.StartWithCommand([]string{"serve", "--dir=/registry-data", "--api-push=false", "--store-ro", "--tls-cert=/registry-data/.certs/tls.crt", "--tls-key=/registry-data/.certs/tls.key"}),
		docker.StartWithBindMount(dir, "/registry-data"),
		docker.StartWithNetworkID(nid),
	)
	if err != nil {
		return "", errors.Wrap(err, "failed to start registry container")
	}

	// Configure containerd in the cluster to accept the local registry's CA
	// certificate.
	if err := configureContainerdLocalRegistry(ctx, cl, regName, string(certSecret.Data[certs.SecretKeyCACert])); err != nil {
		return "", errors.Wrap(err, "failed to configure registry in kind cluster")
	}

	return cid, nil
}

func teardownLocalRegistry(ctx context.Context, cid string) error {
	return errors.Wrap(docker.StopContainerByID(ctx, cid), "failed to stop registry container")
}

func ensureLocalRegistryCertificate(ctx context.Context, cl client.Client, hostname string) (*corev1.Secret, *corev1.ConfigMap, error) {
	const secretName = "local-registry-tls"

	gen := certs.NewTLSCertificateGenerator(crossplaneNamespace, certs.RootCACertSecretName,
		certs.TLSCertificateGeneratorWithServerSecretName(secretName, []string{hostname}),
	)

	if err := gen.Run(ctx, cl); err != nil {
		return nil, nil, errors.Wrap(err, "failed to generate local registry certificate")
	}

	var s corev1.Secret
	if err := cl.Get(ctx, types.NamespacedName{Namespace: crossplaneNamespace, Name: secretName}, &s); err != nil {
		return nil, nil, errors.Wrap(err, "failed to retrieve local registry certificate")
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "local-registry-cert",
			Namespace: crossplaneNamespace,
		},
		BinaryData: map[string][]byte{
			certs.SecretKeyCACert: s.Data[certs.SecretKeyCACert],
		},
	}
	if err := cl.Create(ctx, cm); err != nil && !kerrors.IsAlreadyExists(err) {
		return nil, nil, errors.Wrap(err, "failed to save local registry ca certificate")
	}

	return &s, cm, nil
}

func configureContainerdLocalRegistry(ctx context.Context, cl client.Client, regName, caCert string) error {
	hostsToml := fmt.Sprintf(`server = "https://%s:5000"

[host."https://%s:5000"]
  ca = "ca.crt"
`, regName, regName)
	cmd := fmt.Sprintf("mkdir -p /containerd-certs/%s:5000", regName)
	cmd += fmt.Sprintf("&& echo '%s' > /containerd-certs/%s:5000/ca.crt", caCert, regName)
	cmd += fmt.Sprintf("&& echo '%s' > /containerd-certs/%s:5000/hosts.toml", hostsToml, regName)
	j := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "configure-kind-registry",
			Namespace: "default",
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name: "configurator",
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "containerd-certs",
							MountPath: "/containerd-certs",
						}},
						Image:   "docker.io/library/alpine:3",
						Command: []string{"sh", "-c", cmd},
					}},
					Volumes: []corev1.Volume{{
						Name: "containerd-certs",
						VolumeSource: corev1.VolumeSource{
							HostPath: &corev1.HostPathVolumeSource{
								Path: "/etc/containerd/certs.d",
							},
						},
					}},
				},
			},
		},
	}

	if err := cl.Create(ctx, j); err != nil && !kerrors.IsAlreadyExists(err) {
		return err
	}

	return nil
}
