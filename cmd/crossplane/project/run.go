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

package project

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/spf13/afero"
	"golang.org/x/sync/errgroup"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	xpkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"
	xpkgv1beta1 "github.com/crossplane/crossplane/apis/v2/pkg/v1beta1"

	devv1alpha1 "github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/cmd/crossplane/render"
	"github.com/crossplane/cli/v2/internal/async"
	"github.com/crossplane/cli/v2/internal/dependency"
	"github.com/crossplane/cli/v2/internal/project"
	"github.com/crossplane/cli/v2/internal/project/controlplane"
	"github.com/crossplane/cli/v2/internal/project/functions"
	"github.com/crossplane/cli/v2/internal/project/projectfile"
	"github.com/crossplane/cli/v2/internal/schemas/generator"
	"github.com/crossplane/cli/v2/internal/schemas/manager"
	"github.com/crossplane/cli/v2/internal/schemas/runner"
	"github.com/crossplane/cli/v2/internal/terminal"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"
)

// runCmd builds a project and runs it in a local dev control plane.
type runCmd struct {
	ProjectFile    string `default:"crossplane-project.yaml" help:"Path to project definition."                 short:"f"`
	Repository     string `help:"Override the repository."   optional:""`
	MaxConcurrency uint   `default:"8"                       help:"Max concurrent builds."`
	CacheDir       string `env:"CROSSPLANE_XPKG_CACHE"       help:"Directory for cached xpkg package contents." name:"cache-dir"`

	ControlPlaneName  string        `help:"Name of the dev control plane. Defaults to project name."`
	CrossplaneVersion string        `help:"Version of Crossplane to install."`
	RegistryDir       string        `help:"Directory for local registry images."`
	ClusterAdmin      bool          `default:"true"                                                  help:"Allow Crossplane cluster admin." negatable:""`
	Timeout           time.Duration `default:"5m"                                                    help:"Max wait for project readiness."`
	InitResources     []string      `help:"Resources to apply before installing."                    type:"path"`
	ExtraResources    []string      `help:"Resources to apply after installing."                     type:"path"`

	proj   *devv1alpha1.Project
	projFS afero.Fs

	initResources  []runtime.RawExtension
	extraResources []runtime.RawExtension
}

// AfterApply parses flags and reads the project file.
func (c *runCmd) AfterApply() error {
	projFilePath, err := filepath.Abs(c.ProjectFile)
	if err != nil {
		return err
	}
	projDirPath := filepath.Dir(projFilePath)
	c.projFS = afero.NewBasePathFs(afero.NewOsFs(), projDirPath)

	projFileName := filepath.Base(c.ProjectFile)
	prj, err := projectfile.Parse(c.projFS, projFileName)
	if err != nil {
		return errors.New("this is not a project directory")
	}
	c.proj = prj

	for _, m := range c.InitResources {
		yamls, err := render.LoadYAMLStream(afero.NewOsFs(), m)
		if err != nil {
			return errors.Wrapf(err, "failed to read init resources from %s", m)
		}
		for _, bs := range yamls {
			var e runtime.RawExtension
			if err := yaml.Unmarshal(bs, &e); err != nil {
				return errors.Wrapf(err, "failed to unmarshal init resource from %s", m)
			}
			c.initResources = append(c.initResources, e)
		}
	}
	for _, m := range c.ExtraResources {
		yamls, err := render.LoadYAMLStream(afero.NewOsFs(), m)
		if err != nil {
			return errors.Wrapf(err, "failed to read extra resources from %s", m)
		}
		for _, bs := range yamls {
			var e runtime.RawExtension
			if err := yaml.Unmarshal(bs, &e); err != nil {
				return errors.Wrapf(err, "failed to unmarshal extra resource from %s", m)
			}
			c.extraResources = append(c.extraResources, e)
		}
	}

	return nil
}

// Run executes the run command.
func (c *runCmd) Run(logger logging.Logger, sp terminal.SpinnerPrinter) error { //nolint:gocyclo // Main command orchestration.
	ctx := context.Background()

	if c.Repository != "" {
		ref, err := name.NewRepository(c.Repository)
		if err != nil {
			return errors.Wrap(err, "failed to parse repository")
		}
		c.proj.Spec.Repository = ref.String()
	}

	if c.ControlPlaneName == "" {
		c.ControlPlaneName = "crossplane-" + c.proj.Name
	}

	concurrency := max(1, c.MaxConcurrency)

	schemasFS := afero.NewBasePathFs(c.projFS, c.proj.Spec.Paths.Schemas)
	generators := generator.AllLanguages()
	schemaRunner := runner.NewRealSchemaRunner(runner.WithImageConfig(c.proj.Spec.ImageConfigs))
	schemaMgr := manager.New(schemasFS, generators, schemaRunner)
	cacheDir := c.CacheDir
	if cacheDir == "" {
		cacheDir = dependency.DefaultCacheDir()
	}

	client, err := clixpkg.NewClient(
		clixpkg.NewRemoteFetcher(),
		clixpkg.WithCacheDir(afero.NewOsFs(), cacheDir),
	)
	if err != nil {
		return err
	}
	resolver := clixpkg.NewResolver(client)

	depMgr := dependency.NewManager(c.proj, c.projFS,
		dependency.WithProjectFile(c.ProjectFile),
		dependency.WithSchemaFS(schemasFS),
		dependency.WithSchemaGenerators(generators),
		dependency.WithSchemaRunner(schemaRunner),
		dependency.WithXpkgClient(client),
		dependency.WithResolver(resolver),
	)

	b := project.NewBuilder(
		project.BuildWithMaxConcurrency(concurrency),
		project.BuildWithFunctionIdentifier(functions.DefaultIdentifier),
		project.BuildWithSchemaManager(schemaMgr),
		project.BuildWithDependencyManager(depMgr),
	)

	var (
		imgMap project.ImageTagMap
		devCtp controlplane.DevControlPlane
	)

	// Parallel build + control plane setup with async spinners.
	err = sp.WrapAsyncWithSuccessSpinners(func(ch async.EventChannel) error {
		eg, egCtx := errgroup.WithContext(ctx)

		eg.Go(func() error {
			ch.SendEvent("Setting up control plane", async.EventStatusStarted)
			var ctpErr error
			devCtp, ctpErr = controlplane.EnsureLocalDevControlPlane(egCtx,
				controlplane.WithName(c.ControlPlaneName),
				controlplane.WithCrossplaneVersion(c.CrossplaneVersion),
				controlplane.WithRegistryDir(c.RegistryDir),
				controlplane.WithClusterAdmin(c.ClusterAdmin),
				controlplane.WithLogger(logger),
			)
			if ctpErr != nil {
				ch.SendEvent("Setting up control plane", async.EventStatusFailure)
				return ctpErr
			}

			ctpSchemeBuilders := []*scheme.Builder{
				xpkgv1.SchemeBuilder,
				xpkgv1beta1.SchemeBuilder,
			}
			for _, bld := range ctpSchemeBuilders {
				if err := bld.AddToScheme(devCtp.Client().Scheme()); err != nil {
					ch.SendEvent("Setting up control plane", async.EventStatusFailure)
					return err
				}
			}
			ch.SendEvent("Setting up control plane", async.EventStatusSuccess)
			return nil
		})

		eg.Go(func() error {
			var buildErr error
			imgMap, buildErr = b.Build(egCtx, c.proj, c.projFS,
				project.BuildWithLogger(logger),
				project.BuildWithEventChannel(ch),
			)
			return buildErr
		})

		return eg.Wait()
	})
	if err != nil {
		return err
	}

	// Sideload built images into the local registry.
	tagStr := fmt.Sprintf("%s:v0.0.0-%d", c.proj.Spec.Repository, time.Now().Unix())
	tag, err := name.NewTag(tagStr, name.StrictValidation)
	if err != nil {
		return errors.Wrap(err, "failed to construct image tag")
	}

	logger.Debug("Loading packages into control plane")
	if err := sp.WrapWithSuccessSpinner("Loading packages into control plane", func() error {
		return devCtp.Sideload(ctx, imgMap, tag)
	}); err != nil {
		return errors.Wrap(err, "failed to sideload packages")
	}

	// Apply init resources.
	if len(c.initResources) > 0 {
		logger.Debug("Applying init resources")
		if err := sp.WrapWithSuccessSpinner("Applying init resources", func() error {
			return project.ApplyResources(ctx, devCtp.Client(), c.initResources)
		}); err != nil {
			return errors.Wrap(err, "failed to apply init resources")
		}
	}

	// Install the configuration and wait for readiness.
	readyCtx := ctx
	if c.Timeout != 0 {
		timeoutCtx, cancel := context.WithTimeout(ctx, c.Timeout)
		defer cancel()
		readyCtx = timeoutCtx
	}

	logger.Debug("Installing configuration package")
	if err := sp.WrapWithSuccessSpinner("Installing configuration", func() error {
		return project.InstallConfiguration(readyCtx, devCtp.Client(), c.proj.Name, tag, logger)
	}); err != nil {
		return errors.Wrap(err, "failed to install configuration")
	}

	// Apply extra resources.
	if len(c.extraResources) > 0 {
		logger.Debug("Applying extra resources")
		if err := sp.WrapWithSuccessSpinner("Applying extra resources", func() error {
			return project.ApplyResources(ctx, devCtp.Client(), c.extraResources)
		}); err != nil {
			return errors.Wrap(err, "failed to apply extra resources")
		}
	}

	// Update kubeconfig.
	ctpKubeconfig, err := devCtp.Kubeconfig().RawConfig()
	if err != nil {
		return errors.Wrap(err, "failed to get kubeconfig")
	}

	if err := writeKubeconfig(ctpKubeconfig); err != nil {
		return errors.Wrap(err, "failed to update kubeconfig")
	}

	fmt.Println(devCtp.Info())                                                                   //nolint:forbidigo // CLI output.
	fmt.Printf("Kubeconfig updated. Current context is %q.\n", ctpKubeconfig.CurrentContext)     //nolint:forbidigo // CLI output.
	fmt.Printf("Run `kubectl get configurations` to see the installed project configuration.\n") //nolint:forbidigo // CLI output.
	fmt.Printf("Run `kind delete cluster --name %s` to clean up.\n", c.ControlPlaneName)         //nolint:forbidigo // CLI output.

	return nil
}

func writeKubeconfig(rawConfig clientcmdapi.Config) error {
	// Merge the control plane's kubeconfig into the user's default kubeconfig
	// and set it as the current context.
	defaultPath := clientcmd.RecommendedHomeFile

	existing, err := clientcmd.LoadFromFile(defaultPath)
	if err != nil {
		// If the file doesn't exist, start fresh.
		existing = clientcmdapi.NewConfig()
	}

	maps.Copy(existing.Clusters, rawConfig.Clusters)
	maps.Copy(existing.AuthInfos, rawConfig.AuthInfos)
	maps.Copy(existing.Contexts, rawConfig.Contexts)
	existing.CurrentContext = rawConfig.CurrentContext

	return clientcmd.WriteToFile(*existing, defaultPath)
}
