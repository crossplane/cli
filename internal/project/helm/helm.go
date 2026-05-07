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

// Package helm implements a helm chart installer.
package helm

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/afero"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage/driver"
	"k8s.io/client-go/rest"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
)

const (
	helmDriverSecret = "secret"
	defaultCacheDir  = ".cache/crossplane/charts"
	allVersions      = ">0.0.0-0"
	waitTimeout      = 10 * time.Minute
)

// Manager installs and manages Helm charts in a Kubernetes cluster.
type Manager struct {
	repoURL     string
	chartRef    string
	chartName   string
	releaseName string
	namespace   string
	cacheDir    string
	wait        bool
	log         logging.Logger
	fs          afero.Fs

	pullClient    *puller
	getClient     helmGetter
	installClient helmInstaller
}

type helmGetter interface {
	Run(ref string) (*release.Release, error)
}

type helmInstaller interface {
	Run(ch *chart.Chart, values map[string]any) (*release.Release, error)
}

// ManagerOption configures a Manager.
type ManagerOption func(*Manager)

// Wait configures the manager to wait for operations to complete.
func Wait() ManagerOption {
	return func(m *Manager) {
		m.wait = true
	}
}

// WithLogger sets the logger for the manager.
func WithLogger(l logging.Logger) ManagerOption {
	return func(m *Manager) {
		m.log = l
	}
}

type puller struct {
	*action.Pull
}

// NewManager builds a helm install manager.
func NewManager(config *rest.Config, chartName, repoURL, namespace string, opts ...ManagerOption) (*Manager, error) {
	m := &Manager{
		repoURL:     repoURL,
		chartRef:    chartName,
		chartName:   chartName,
		releaseName: chartName,
		namespace:   namespace,
		log:         logging.NewNopLogger(),
		fs:          afero.NewOsFs(),
	}
	for _, o := range opts {
		o(m)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	m.cacheDir = filepath.Join(home, defaultCacheDir)

	actionConfig := new(action.Configuration)
	if err := actionConfig.Init(newRESTClientGetter(config, namespace), namespace, helmDriverSecret, func(format string, v ...any) {
		m.log.Debug(fmt.Sprintf(format, v...))
	}); err != nil {
		return nil, err
	}

	if _, err := m.fs.Stat(m.cacheDir); err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		if err := m.fs.MkdirAll(m.cacheDir, 0o755); err != nil {
			return nil, err
		}
	}

	// Pull Client
	p := action.NewPullWithOpts(action.WithConfig(&action.Configuration{}))
	p.DestDir = m.cacheDir
	p.Devel = true
	p.Settings = &cli.EnvSettings{}
	p.RepoURL = repoURL
	m.pullClient = &puller{Pull: p}

	// Get Client
	m.getClient = action.NewGet(actionConfig)

	// Install Client
	ic := action.NewInstall(actionConfig)
	ic.Namespace = namespace
	ic.CreateNamespace = false
	ic.ReleaseName = chartName
	ic.Wait = m.wait
	ic.Timeout = waitTimeout
	m.installClient = ic

	return m, nil
}

// GetCurrentVersion gets the current version of the chart in the cluster.
func (m *Manager) GetCurrentVersion() (string, error) {
	r, err := m.getClient.Run(m.chartName)
	if err != nil {
		return "", errors.Wrapf(err, "could not identify installed release for %s in namespace %s", m.chartName, m.namespace)
	}
	if r == nil || r.Chart == nil || r.Chart.Metadata == nil {
		return "", errors.New("could not identify current version")
	}
	return r.Chart.Metadata.Version, nil
}

// Install installs the chart in the cluster.
func (m *Manager) Install(version string, parameters map[string]any) error {
	// Make sure no version is already installed.
	current, err := m.GetCurrentVersion()
	if err == nil {
		return errors.Errorf("chart already installed with version %s", current)
	}
	if !errors.Is(err, driver.ErrReleaseNotFound) {
		// Some other error getting the current version - check if it's because
		// the release wasn't found.
		if !strings.Contains(err.Error(), "not found") {
			return errors.Wrap(err, "could not verify that chart is not already installed")
		}
	}

	helmChart, err := m.pullAndLoad(version)
	if err != nil {
		return err
	}

	_, err = m.installClient.Run(helmChart, parameters)
	return err
}

func (m *Manager) pullAndLoad(version string) (*chart.Chart, error) {
	if version != "" {
		fileName := filepath.Join(m.cacheDir, fmt.Sprintf("%s-%s.tgz", m.chartName, version))
		if _, err := m.fs.Stat(fileName); err != nil {
			m.pullClient.DestDir = m.cacheDir
			m.pullClient.Version = version
			if _, err := m.pullClient.Run(m.chartRef); err != nil {
				return nil, errors.Wrap(err, "could not pull chart")
			}
		}
		return loader.Load(fileName)
	}

	tmp, err := afero.TempDir(m.fs, m.cacheDir, "")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := m.fs.RemoveAll(tmp); err != nil {
			m.log.Debug("failed to clean up temporary directory", "error", err)
		}
	}()
	m.pullClient.DestDir = tmp
	m.pullClient.Version = allVersions
	if _, err := m.pullClient.Run(m.chartRef); err != nil {
		return nil, errors.Wrap(err, "could not pull chart")
	}
	files, err := afero.ReadDir(m.fs, tmp)
	if err != nil {
		return nil, errors.Wrap(err, "could not identify chart pulled as latest")
	}
	if len(files) != 1 {
		return nil, errors.Errorf("corrupt chart tmp directory, consider removing cache (%s)", m.cacheDir)
	}
	tmpFileName := filepath.Join(tmp, files[0].Name())
	c, err := loader.Load(tmpFileName)
	if err != nil {
		return nil, err
	}
	fileName := filepath.Join(m.cacheDir, fmt.Sprintf("%s-%s.tgz", m.chartName, c.Metadata.Version))
	if err := m.fs.Rename(tmpFileName, fileName); err != nil {
		return nil, errors.Wrap(err, "could not move latest pulled chart to cache")
	}
	return c, nil
}
