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
	"cmp"
	"context"
	"os"
	"path/filepath"

	"github.com/alecthomas/kong"
	"github.com/spf13/afero"
	un "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	apiextensionsv1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1"

	"github.com/crossplane/cli/v2/internal/config"
	"github.com/crossplane/cli/v2/internal/dependency"
	"github.com/crossplane/cli/v2/internal/project"
	"github.com/crossplane/cli/v2/internal/project/projectfile"
	"github.com/crossplane/cli/v2/internal/schemas/generator"
	"github.com/crossplane/cli/v2/internal/simulate"
	"github.com/crossplane/cli/v2/internal/terminal"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"
	ld "github.com/crossplane/cli/v2/pkg/load"

	_ "embed"
)

//go:embed help/simulate.md
var simulateHelp string

// simulateCmd previews how a project's compositions, or changed Composition
// or composite resource files, would change the composite resources in a
// cluster.
type simulateCmd struct {
	simulate.Flags `embed:""`
}

func (c *simulateCmd) Help() string {
	return simulateHelp
}

// Run previews the inputs -f names against the cluster: a project's
// compositions, with its functions built locally, or changed Composition or
// composite resource files, with functions from the cluster.
func (c *simulateCmd) Run(sigCtx context.Context, k *kong.Context, logger logging.Logger, sp terminal.SpinnerPrinter, cliCfg *config.Config) error {
	in, err := loadInputs(c.Input)
	if err != nil {
		return err
	}
	si := &simulate.Inputs{Mode: in.mode, Resources: in.resources}
	if in.mode == simulate.ProjectMode {
		si.Build = func(ctx context.Context) (*simulate.Built, error) {
			return c.buildProject(ctx, logger, sp, cliCfg, in.projectFile)
		}
	}
	return c.Flags.Run(sigCtx, k, logger, sp, si)
}

// buildProject returns the project's compositions, its locally built
// functions, and the packages its provider dependencies resolve through.
// --image-configs, when set, replaces the project's ImageConfigs for both.
func (c *simulateCmd) buildProject(ctx context.Context, logger logging.Logger, sp terminal.SpinnerPrinter, cliCfg *config.Config, projectFile string) (*simulate.Built, error) {
	projFilePath, err := filepath.Abs(projectFile)
	if err != nil {
		return nil, err
	}
	projFS := afero.NewBasePathFs(afero.NewOsFs(), filepath.Dir(projFilePath))
	projFileName := filepath.Base(projFilePath)

	proj, err := projectfile.Parse(projFS, projFileName)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to parse project file %q", projectFile)
	}

	comps, err := project.LoadCompositions(proj, projFS)
	if err != nil {
		return nil, errors.Wrap(err, "cannot load compositions from project")
	}
	if len(comps) == 0 {
		return nil, errors.Errorf("no Compositions found under the project's APIs path %q", proj.Spec.Paths.APIs)
	}

	ics := proj.Spec.ImageConfigs
	if c.ImageConfigs != "" {
		if ics, err = simulate.ImageConfigs(c.ImageConfigs, ""); err != nil {
			return nil, err
		}
	}
	client, err := clixpkg.NewClient(clixpkg.WithCacheDir(afero.NewOsFs(), cmp.Or(c.CacheDir, dependency.DefaultCacheDir())), clixpkg.WithImageConfigs(ics))
	if err != nil {
		return nil, errors.Wrap(err, "cannot create xpkg client")
	}
	generators := generator.AllLanguages(
		generator.WithGoModelAccessors(cliCfg.Features.GenerateGoModelAccessors),
		generator.WithGoRuntimeObjects(cliCfg.Features.GenerateGoRuntimeObjects),
	)
	depMgr := dependency.NewManager(proj, projFS,
		dependency.WithProjectFile(projFileName),
		dependency.WithSchemaGenerators(generators),
		dependency.WithXpkgClient(client),
	)

	logger.Debug("Loading functions from project", "project-file", projFilePath)
	fns, digests, err := project.LoadFunctions(ctx, proj, projFS, depMgr, project.LoadFunctionsOptions{
		Logger:         logger,
		Spinner:        sp,
		Generators:     generators,
		MaxConcurrency: c.MaxConcurrency,
		Digests:        true,
	})
	if err != nil {
		return nil, err
	}
	return &simulate.Built{Compositions: comps, Functions: fns, Digests: digests, Packages: &simulate.Packages{ICs: ics, Client: client, Proj: proj, DepMgr: depMgr}}, nil
}

// inputs are the loaded -f inputs.
type inputs struct {
	mode simulate.Mode

	// projectFile is the project file, in project mode.
	projectFile string

	// resources are the Compositions or composite resources to simulate,
	// in the other modes.
	resources []*un.Unstructured
}

// loadInputs loads and classifies the -f inputs. A single project file
// selects project mode. Anything else is loaded as resources: Compositions
// select composition mode, and resources with no Composition among them
// select XR mode. A project file among other inputs, or Compositions mixed
// with other resources, are rejected.
func loadInputs(paths []string) (*inputs, error) {
	if len(paths) == 1 && paths[0] != "-" {
		isProj, err := isProjectFile(paths[0])
		if err != nil {
			return nil, err
		}
		if isProj {
			return &inputs{mode: simulate.ProjectMode, projectFile: paths[0]}, nil
		}
	}

	// Pipeline step inputs are part of their Composition, not resources of
	// their own.
	loader, err := ld.NewExactCompositeLoader(paths, ld.WithoutPipelineInputs())
	if err != nil {
		return nil, errors.Wrap(err, "cannot load -f inputs")
	}
	resources, err := loader.Load()
	if err != nil {
		return nil, errors.Wrap(err, "cannot load -f inputs")
	}

	var comps, others []*un.Unstructured
	for _, r := range resources {
		gvk := r.GroupVersionKind()
		switch {
		case gvk.GroupVersion().String() == projectfile.APIVersion && gvk.Kind == projectfile.Kind:
			return nil, errors.New("a project file must be the only -f input, named by its path; pass it alone, or pass Composition or composite resource files without it")
		case gvk == apiextensionsv1.CompositionGroupVersionKind:
			comps = append(comps, r)
		default:
			others = append(others, r)
		}
	}

	switch {
	case len(comps) == 0:
		return &inputs{mode: simulate.XRMode, resources: others}, nil
	case len(others) == 0:
		return &inputs{mode: simulate.CompositionMode, resources: comps}, nil
	default:
		return nil, errors.Errorf("-f inputs mix Compositions with other resources (%s %q); simulate Compositions and composite resources in separate runs", others[0].GetKind(), others[0].GetName())
	}
}

// isProjectFile reports whether path is a project file. A directory is not.
func isProjectFile(path string) (bool, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return false, errors.Wrapf(err, "cannot read -f input %q", path)
	}
	if fi.IsDir() {
		return false, nil
	}
	isProj, err := projectfile.IsProjectFile(afero.NewOsFs(), path)
	return isProj, errors.Wrapf(err, "cannot read -f input %q", path)
}
