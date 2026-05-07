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

package composition

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"

	apiextv1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1"
	v2 "github.com/crossplane/crossplane/apis/v2/apiextensions/v2"
	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"

	"github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/internal/dependency"
	"github.com/crossplane/cli/v2/internal/project/projectfile"
	"github.com/crossplane/cli/v2/internal/terminal"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"
)

const (
	functionAutoReadyName    = "crossplane-contrib-function-auto-ready"
	functionAutoReadyPackage = "xpkg.crossplane.io/crossplane-contrib/function-auto-ready"
)

type generateCmd struct {
	XRD         string `arg:""                                              help:"Path to the CompositeResourceDefinition (XRD) file."`
	Name        string `help:"Name prefix for the composition."             optional:""`
	Plural      string `help:"Custom plural for the CompositeTypeRef.Kind." optional:""`
	Path        string `help:"Output file path override."                   optional:""`
	ProjectFile string `default:"crossplane-project.yaml"                   help:"Path to project definition file."                    short:"f"`
	CacheDir    string `env:"CROSSPLANE_XPKG_CACHE"                         help:"Directory for cached xpkg package contents."         name:"cache-dir"`

	projFS     afero.Fs
	apisFS     afero.Fs
	proj       *v1alpha1.Project
	depManager *dependency.Manager
}

// AfterApply sets up the project filesystem.
func (c *generateCmd) AfterApply() error {
	projFilePath, err := filepath.Abs(c.ProjectFile)
	if err != nil {
		return err
	}
	projDirPath := filepath.Dir(projFilePath)
	c.projFS = afero.NewBasePathFs(afero.NewOsFs(), projDirPath)

	proj, err := projectfile.Parse(c.projFS, filepath.Base(c.ProjectFile))
	if err != nil {
		return err
	}

	c.proj = proj
	c.apisFS = afero.NewBasePathFs(c.projFS, proj.Spec.Paths.APIs)
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

	c.depManager = dependency.NewManager(proj, c.projFS,
		dependency.WithProjectFile(filepath.Base(c.ProjectFile)),
		dependency.WithXpkgClient(client),
		dependency.WithResolver(resolver),
	)
	return nil
}

func (c *generateCmd) Run(sp terminal.SpinnerPrinter) error {
	ctx := context.Background()

	if err := sp.WrapWithSuccessSpinner("Ensuring function-auto-ready dependency", func() error {
		return c.ensureFunctionAutoReady(ctx)
	}); err != nil {
		return errors.Wrap(err, "failed to ensure function-auto-ready dependency")
	}

	return sp.WrapWithSuccessSpinner("Writing Composition", func() error {
		comp, plural, err := c.newComposition()
		if err != nil {
			return errors.Wrap(err, "failed to create Composition")
		}

		compYAML, err := marshalComposition(comp)
		if err != nil {
			return errors.Wrap(err, "failed to marshal Composition to YAML")
		}

		filePath := c.Path
		if filePath == "" {
			if c.Name != "" {
				filePath = fmt.Sprintf("%s/composition-%s.yaml", strings.ToLower(plural), c.Name)
			} else {
				filePath = fmt.Sprintf("%s/composition.yaml", strings.ToLower(plural))
			}
		}

		exists, err := afero.Exists(c.apisFS, filePath)
		if err != nil {
			return errors.Wrap(err, "failed to check if file exists")
		}
		if exists {
			return errors.Errorf("file %q already exists, use --path to specify a different output path or delete the existing file", filePath)
		}

		if err := c.apisFS.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
			return errors.Wrap(err, "failed to create directories for the specified output path")
		}

		return afero.WriteFile(c.apisFS, filePath, compYAML, 0o644)
	})
}

func (c *generateCmd) ensureFunctionAutoReady(ctx context.Context) error {
	for _, dep := range c.proj.Spec.Dependencies {
		if dep.Type == v1alpha1.DependencyTypeXpkg && dep.Xpkg != nil && dep.Xpkg.Package == functionAutoReadyPackage {
			return nil
		}
	}

	return c.depManager.AddDependency(ctx, &v1alpha1.Dependency{
		Type: v1alpha1.DependencyTypeXpkg,
		Xpkg: &v1alpha1.XpkgDependency{
			APIVersion: pkgv1.FunctionGroupVersionKind.GroupVersion().String(),
			Kind:       pkgv1.FunctionKind,
			Package:    functionAutoReadyPackage,
			Version:    ">=v0.0.0",
		},
	})
}

func (c *generateCmd) newComposition() (*apiextv1.Composition, string, error) {
	group, version, kind, plural, err := c.processXRD()
	if err != nil {
		return nil, "", errors.Wrap(err, "failed to load XRD")
	}

	name := strings.ToLower(fmt.Sprintf("%s.%s", plural, group))
	if c.Name != "" {
		name = strings.ToLower(fmt.Sprintf("%s.%s.%s", c.Name, plural, group))
	}

	comp := &apiextv1.Composition{
		TypeMeta: metav1.TypeMeta{
			APIVersion: apiextv1.CompositionGroupVersionKind.GroupVersion().String(),
			Kind:       apiextv1.CompositionGroupVersionKind.Kind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: apiextv1.CompositionSpec{
			CompositeTypeRef: apiextv1.TypeReference{
				APIVersion: fmt.Sprintf("%s/%s", group, version),
				Kind:       kind,
			},
			Mode: apiextv1.CompositionModePipeline,
			Pipeline: []apiextv1.PipelineStep{
				{
					Step: functionAutoReadyName,
					FunctionRef: apiextv1.FunctionReference{
						Name: xpkg.ToDNSLabel(functionAutoReadyName),
					},
				},
			},
		},
	}

	return comp, plural, nil
}

func (c *generateCmd) processXRD() (group, version, kind, plural string, err error) {
	raw, err := afero.ReadFile(c.projFS, c.XRD)
	if err != nil {
		return "", "", "", "", errors.Wrapf(err, "failed to read XRD file %s", c.XRD)
	}

	var xrd v2.CompositeResourceDefinition
	if err := yaml.Unmarshal(raw, &xrd); err != nil {
		return "", "", "", "", errors.Wrap(err, "failed to unmarshal XRD")
	}

	if xrd.Spec.Group == "" {
		return "", "", "", "", errors.New("XRD spec.group is required")
	}
	if xrd.Spec.Names.Kind == "" {
		return "", "", "", "", errors.New("XRD spec.names.kind is required")
	}

	group = xrd.Spec.Group
	kind = xrd.Spec.Names.Kind
	plural = xrd.Spec.Names.Plural
	if c.Plural != "" {
		plural = c.Plural
	}

	// Find the version that is served and referenceable.
	for _, v := range xrd.Spec.Versions {
		if v.Served && v.Referenceable {
			version = v.Name
			break
		}
	}
	if version == "" {
		return "", "", "", "", errors.New("no served and referenceable version found in XRD")
	}

	return group, version, kind, plural, nil
}

// marshalComposition marshals a Composition to YAML, removing creationTimestamp and status.
func marshalComposition(obj any) ([]byte, error) {
	unst, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, err
	}

	unstructured.RemoveNestedField(unst, "status")
	unstructured.RemoveNestedField(unst, "metadata", "creationTimestamp")

	return yaml.Marshal(unst)
}
