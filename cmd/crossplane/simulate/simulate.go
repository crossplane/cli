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

// Package simulate contains the resource simulate command: preview what
// applying managed resources would do, to the cluster and to the cloud
// behind it, by asking the providers' own diff servers.
package simulate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	"github.com/spf13/afero"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	"github.com/crossplane/cli/v2/cmd/crossplane/common/load"
	"github.com/crossplane/cli/v2/internal/diffserver"
	"github.com/crossplane/cli/v2/internal/docker"
	"github.com/crossplane/cli/v2/internal/kube"
	"github.com/crossplane/cli/v2/internal/plan"
	isimulate "github.com/crossplane/cli/v2/internal/simulate"
	"github.com/crossplane/cli/v2/internal/terminal"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"

	_ "embed"
)

//go:embed help/simulate.md
var helpDetail string

// Cmd previews what applying managed resources would do.
type Cmd struct {
	Filename string `help:"YAML files or directory with managed resources, or '-' for stdin (comma-separated)." predictor:"yaml_file_or_directory" required:"" short:"f"`

	Providers    string        `help:"YAML file or directory with Provider manifests to run as diff servers, overriding discovery from the cluster. Supports per-provider runtime annotations."       name:"providers"                                         optional:""                                     predictor:"yaml_file_or_directory" type:"path"`
	ImageConfigs string        `help:"YAML file with ImageConfig manifests that rewrite provider image paths for local pulls. Defaults to the project's imageConfigs when a project file is present." name:"image-configs"                                     optional:""                                     predictor:"yaml_file"              type:"existingfile"`
	ProjectFile  string        `help:"Path to a project definition file to read imageConfigs from. Defaults to ${project_file} when present."                                                         name:"project-file"                                      optional:""`
	SessionID    string        `help:"Session ID to apply as the simulate.crossplane.io/session label on diff server containers. Defaults to a random per-process ID."                                placeholder:"ID"`
	Context      string        `default:""                                                                                                                                                            help:"Kubernetes context."                               name:"context"                                  predictor:"context"                short:"c"`
	CacheDir     string        `default:"~/.crossplane/cache"                                                                                                                                         help:"Cache directory for downloaded provider packages." name:"cache-dir"                                predictor:"directory"`
	Timeout      time.Duration `default:"2m"                                                                                                                                                          help:"Per-resource plan timeout."`
	Output       string        `default:"plan"                                                                                                                                                        enum:"plan,json,yaml"                                    help:"Output format. One of: plan, json, yaml." name:"output"                      short:"o"`

	Impersonation kube.ImpersonationFlags `embed:""`
}

// Help returns the help message for the simulate command.
func (c *Cmd) Help() string {
	return helpDetail
}

// AfterApply expands the cache directory.
func (c *Cmd) AfterApply() error {
	if strings.HasPrefix(c.CacheDir, "~/") {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return errors.Wrap(err, "cannot determine home directory for cache dir")
		}
		c.CacheDir = filepath.Join(homeDir, c.CacheDir[2:])
	}
	return nil
}

// Run runs the simulate command. ctx is the signal-bound command context from
// main: cancellation on SIGINT/SIGTERM unwinds in-flight plans so the
// deferred diff server cleanup runs.
func (c *Cmd) Run(ctx context.Context, k *kong.Context, logger logging.Logger, sp terminal.SpinnerPrinter) error {
	loader, err := load.NewLoader(c.Filename)
	if err != nil {
		return errors.Wrapf(err, "cannot load resources from %q", c.Filename)
	}
	resources, err := loader.Load()
	if err != nil {
		return errors.Wrapf(err, "cannot load resources from %q", c.Filename)
	}
	if len(resources) == 0 {
		return errors.Errorf("no resources found in %q", c.Filename)
	}

	renderer, err := plan.NewRenderer(c.Output)
	if err != nil {
		return err
	}

	var inputs []diffserver.Input
	if c.Providers != "" {
		if inputs, err = isimulate.LoadProviderInputs(c.Providers); err != nil {
			return errors.Wrapf(err, "cannot load provider manifests from %q", c.Providers)
		}
	}
	// Only a provider whose package image runs as a container needs Docker;
	// a Development provider's server is already running. Discovery from the
	// cluster always yields the former.
	if c.Providers == "" || isimulate.NeedsDocker(inputs) {
		if err := docker.Check(ctx); err != nil {
			return errors.Wrap(err, "cannot connect to Docker, which simulate needs to run provider diff servers")
		}
	}

	kubeClient, rmapper, defaultNamespace, err := c.setupKubeClient(logger)
	if err != nil {
		return err
	}

	imageConfigs, err := isimulate.ImageConfigs(c.ImageConfigs, c.ProjectFile)
	if err != nil {
		return err
	}

	if c.Providers == "" {
		// With local ImageConfigs the rewrites apply to original package
		// references, so discovery must hand those out; without them the
		// cluster's resolved reference is the exact artifact it runs.
		images := isimulate.RelevantProviderImages(ctx, kubeClient, rmapper, resources, len(imageConfigs) > 0, isimulate.NewImageCache(), k.Stderr)
		inputs = diffserver.Images(images)
	}
	pkgClient, err := clixpkg.NewClient(clixpkg.NewRemoteFetcher(),
		clixpkg.WithCacheDir(afero.NewOsFs(), c.CacheDir),
		clixpkg.WithImageConfigs(imageConfigs),
	)
	if err != nil {
		return errors.Wrap(err, "cannot create package client")
	}

	mgr, stop, err := isimulate.StartDiffServers(ctx, sp, logger, k.Stderr, pkgClient, inputs, c.SessionID,
		"No providers found to run as diff servers; all results will be approximate.")
	if err != nil {
		return err
	}
	defer stop()

	plans := make([]*plan.ResourcePlan, 0, len(resources))
	planner := isimulate.NewPlanner(kubeClient, rmapper, defaultNamespace, mgr, c.Timeout)
	for _, desired := range resources {
		plans = append(plans, planner.Plan(ctx, desired))
	}

	return renderer.Render(k.Stdout, plan.NewResult(plans))
}

func (c *Cmd) setupKubeClient(logger logging.Logger) (client.Client, meta.RESTMapper, string, error) {
	kubeconfig, clientconfig, err := kube.RESTConfig(c.Context, c.Impersonation)
	if err != nil {
		return nil, nil, "", err
	}

	logger.Debug("Found kubeconfig")

	cl, rmapper, err := isimulate.NewKubeClient(kubeconfig)
	if err != nil {
		return nil, nil, "", err
	}

	namespace, _, err := clientconfig.Namespace()
	if err != nil {
		return nil, nil, "", errors.Wrap(err, "failed to get namespace from kubeconfig")
	}

	return cl, rmapper, namespace, nil
}
