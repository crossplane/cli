/*
Copyright 2023 The Crossplane Authors.

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

// Package xr implements composite resource (XR) rendering.
package xr

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"dario.cat/mergo"
	"github.com/alecthomas/kong"
	"github.com/spf13/afero"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"
	"k8s.io/kube-openapi/pkg/spec3"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource/unstructured/composed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xcrd"

	apiextensionsv1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1"
	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"

	"github.com/crossplane/cli/v2/cmd/crossplane/render"
	"github.com/crossplane/cli/v2/cmd/crossplane/render/contextfn"
	"github.com/crossplane/cli/v2/internal/async"
	"github.com/crossplane/cli/v2/internal/dependency"
	"github.com/crossplane/cli/v2/internal/project"
	"github.com/crossplane/cli/v2/internal/project/functions"
	"github.com/crossplane/cli/v2/internal/project/projectfile"
	"github.com/crossplane/cli/v2/internal/schemas/generator"
	"github.com/crossplane/cli/v2/internal/schemas/manager"
	"github.com/crossplane/cli/v2/internal/schemas/runner"
	"github.com/crossplane/cli/v2/internal/terminal"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"

	_ "embed"
)

//go:embed help/render.md
var helpDetail string

// Cmd arguments and flags for the `render xr` subcommand.
type Cmd struct {
	render.EngineFlags `prefix:""`

	// Arguments.
	CompositeResource string `arg:"" help:"A YAML file specifying the composite resource (XR) to render."                                                                            predictor:"yaml_file" type:"existingfile"`
	Composition       string `arg:"" help:"A YAML file specifying the Composition to use to render the XR. Must be mode: Pipeline."                                                  predictor:"yaml_file" type:"existingfile"`
	Functions         string `arg:"" help:"A YAML file or directory of YAML files specifying the Composition Functions to use to render the XR. Optional when running in a project." optional:""           predictor:"yaml_file_or_directory" type:"path"`

	// Flags. Keep them in alphabetical order.
	ContextFiles           map[string]string `help:"Comma-separated context key-value pairs to pass to the Function pipeline. Values must be files containing JSON/YAML."                           mapsep:""               predictor:"file"`
	ContextValues          map[string]string `help:"Comma-separated context key-value pairs to pass to the Function pipeline. Values must be JSON/YAML. Keys take precedence over --context-files." mapsep:""`
	IncludeFunctionResults bool              `help:"Include informational and warning messages from Functions in the rendered output as resources of kind: Result."                                 short:"r"`
	IncludeFullXR          bool              `help:"Include a direct copy of the input XR's spec and metadata fields in the rendered output."                                                       short:"x"`
	ObservedResources      string            `help:"A YAML file or directory of YAML files specifying the observed state of composed resources."                                                    placeholder:"PATH"      predictor:"yaml_file_or_directory" short:"o"   type:"path"`
	ExtraResources         string            `help:"A YAML file or directory of YAML files specifying required resources (deprecated, use --required-resources)."                                   placeholder:"PATH"      predictor:"yaml_file_or_directory" type:"path"`
	RequiredResources      string            `help:"A YAML file or directory of YAML files specifying required resources to pass to the Function pipeline."                                         placeholder:"PATH"      predictor:"yaml_file_or_directory" short:"e"   type:"path"`
	RequiredSchemas        string            `help:"A directory of JSON files specifying OpenAPI v3 schemas (from kubectl get --raw /openapi/v3/<group-version>)."                                  placeholder:"DIR"       predictor:"directory"              short:"s"   type:"path"`
	IncludeContext         bool              `help:"Include the context in the rendered output as a resource of kind: Context."                                                                     short:"c"`
	FunctionCredentials    string            `help:"A YAML file or directory of YAML files specifying credentials to use for Functions to render the XR."                                           placeholder:"PATH"      predictor:"yaml_file_or_directory" type:"path"`
	FunctionAnnotations    []string          `help:"Override function annotations for all functions. Provide multiple annotations by repeating the argument."                                       placeholder:"KEY=VALUE" short:"a"`

	CacheDir       string        `env:"CROSSPLANE_XPKG_CACHE"                                                                                      help:"Directory for cached xpkg package contents."          name:"cache-dir"`
	MaxConcurrency uint          `default:"8"                                                                                                      help:"Maximum concurrency for building embedded functions."`
	ProjectFile    string        `default:"crossplane-project.yaml"                                                                                help:"Path to the project file. Optional."                  optional:""        predictor:"yaml_file" short:"f" type:"path"`
	Timeout        time.Duration `default:"1m"                                                                                                     help:"How long to run before timing out."`
	XRD            string        `help:"A YAML file specifying the CompositeResourceDefinition (XRD) that defines the XR's schema and properties." optional:""                                                 placeholder:"PATH" type:"existingfile"`

	fs afero.Fs

	// newEngine constructs the render Engine.
	newEngine func(*render.EngineFlags, logging.Logger) render.Engine
}

// Help prints out the help for the render command.
func (c *Cmd) Help() string {
	return helpDetail
}

// AfterApply implements kong.AfterApply.
func (c *Cmd) AfterApply() error {
	c.fs = afero.NewOsFs()
	c.newEngine = render.NewEngineFromFlags

	return nil
}

// Run render.
func (c *Cmd) Run(k *kong.Context, log logging.Logger, sp terminal.SpinnerPrinter) error { //nolint:gocognit // Orchestration is inherently complex.
	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()

	xr, err := render.LoadCompositeResource(c.fs, c.CompositeResource)
	if err != nil {
		return errors.Wrapf(err, "cannot load composite resource from %q", c.CompositeResource)
	}

	comp, err := render.LoadComposition(c.fs, c.Composition)
	if err != nil {
		return errors.Wrapf(err, "cannot load Composition from %q", c.Composition)
	}

	// Validate that Composition's compositeTypeRef matches the XR's GroupVersionKind.
	xrGVK := xr.GetObjectKind().GroupVersionKind()
	compRef := comp.Spec.CompositeTypeRef

	if compRef.Kind != xrGVK.Kind {
		return errors.Errorf("composition's compositeTypeRef.kind (%s) does not match XR's kind (%s)", compRef.Kind, xrGVK.Kind)
	}

	if compRef.APIVersion != xrGVK.GroupVersion().String() {
		return errors.Errorf("composition's compositeTypeRef.apiVersion (%s) does not match XR's apiVersion (%s)", compRef.APIVersion, xrGVK.GroupVersion().String())
	}

	// check if XR's matchLabels have corresponding label at composition
	xrSelector := xr.GetCompositionSelector()
	if xrSelector != nil {
		for key, value := range xrSelector.MatchLabels {
			compValue, exists := comp.Labels[key]
			if !exists {
				return fmt.Errorf("composition %q is missing required label %q", comp.GetName(), key)
			}

			if compValue != value {
				return fmt.Errorf("composition %q has incorrect value for label %q: want %q, got %q",
					comp.GetName(), key, value, compValue)
			}
		}
	}

	if comp.Spec.Mode != apiextensionsv1.CompositionModePipeline {
		return errors.Errorf("render only supports Composition Function pipelines: Composition %q must use spec.mode: Pipeline", comp.GetName())
	}

	fns, err := c.loadFunctions(ctx, log, sp)
	if err != nil {
		return err
	}

	// Apply global annotation overrides to each function
	if err := render.OverrideFunctionAnnotations(fns, c.FunctionAnnotations); err != nil {
		return errors.Wrap(err, "cannot apply function annotation overrides")
	}

	if c.XRD != "" {
		xrd, err := render.LoadXRD(c.fs, c.XRD)
		if err != nil {
			return errors.Wrapf(err, "cannot load XRD from %q", c.XRD)
		}

		crd, err := xcrd.ForCompositeResource(xrd)
		if err != nil {
			return errors.Wrapf(err, "cannot derive composite CRD from XRD %q", xrd.GetName())
		}

		if err := render.DefaultValues(xr.UnstructuredContent(), xr.GetAPIVersion(), *crd); err != nil {
			return errors.Wrapf(err, "cannot default values for XR %q", xr.GetName())
		}
	}

	fcreds := []corev1.Secret{}
	if c.FunctionCredentials != "" {
		fcreds, err = render.LoadCredentials(c.fs, c.FunctionCredentials)
		if err != nil {
			return errors.Wrapf(err, "cannot load secrets from %q", c.FunctionCredentials)
		}
	}

	ors := []composed.Unstructured{}
	if c.ObservedResources != "" {
		ors, err = render.LoadObservedResources(c.fs, c.ObservedResources)
		if err != nil {
			return errors.Wrapf(err, "cannot load observed composed resources from %q", c.ObservedResources)
		}
	}

	rrs := []unstructured.Unstructured{}
	if c.RequiredResources != "" {
		rrs, err = render.LoadRequiredResources(c.fs, c.RequiredResources)
		if err != nil {
			return errors.Wrapf(err, "cannot load required resources from %q", c.RequiredResources)
		}
	}

	if c.ExtraResources != "" {
		ers, err := render.LoadRequiredResources(c.fs, c.ExtraResources)
		if err != nil {
			return errors.Wrapf(err, "cannot load extra resources from %q", c.ExtraResources)
		}

		// Merge extra resources into required resources.
		rrs = append(rrs, ers...)
	}

	// Load required schemas
	rsc := []spec3.OpenAPI{}
	if c.RequiredSchemas != "" {
		rsc, err = render.LoadRequiredSchemas(c.fs, c.RequiredSchemas)
		if err != nil {
			return errors.Wrapf(err, "cannot load required schemas from %q", c.RequiredSchemas)
		}
	}

	engine := c.newEngine(&c.EngineFlags, log)

	seedCtx := len(c.ContextValues) > 0 || len(c.ContextFiles) > 0
	captureCtx := c.IncludeContext

	var ctxHandle *contextfn.Handle
	if seedCtx || captureCtx {
		if err := engine.CheckContextSupport(); err != nil {
			return err
		}

		raw, err := render.BuildContextData(c.fs, c.ContextFiles, c.ContextValues)
		if err != nil {
			return errors.Wrap(err, "cannot build context data")
		}

		parsed, err := render.ParseContextData(raw)
		if err != nil {
			return errors.Wrap(err, "cannot parse context data")
		}

		ctxHandle, err = contextfn.Start(ctx, log, parsed)
		if err != nil {
			return errors.Wrap(err, "cannot start context function")
		}
		defer ctxHandle.Stop()

		fns = append(fns, ctxHandle.Function())
		if seedCtx {
			comp.Spec.Pipeline = append([]apiextensionsv1.PipelineStep{ctxHandle.CompositeSeedStep()}, comp.Spec.Pipeline...)
		}
		if captureCtx {
			comp.Spec.Pipeline = append(comp.Spec.Pipeline, ctxHandle.CompositeCaptureStep())
		}
	}

	cleanup, err := engine.Setup(ctx, fns)
	if err != nil {
		return err
	}
	defer cleanup()

	// Start function runtimes to get their addresses.
	fnAddrs, err := render.StartFunctionRuntimes(ctx, log, fns)
	if err != nil {
		return errors.Wrap(err, "cannot start function runtimes")
	}
	defer render.StopFunctionRuntimes(log, fnAddrs)

	addrs := fnAddrs.Addresses()
	if ctxHandle != nil {
		addrs[contextfn.FunctionName] = ctxHandle.Target
	}

	// Build and execute the render request.
	in := render.CompositionInputs{
		CompositeResource:   xr,
		Composition:         comp,
		FunctionAddrs:       addrs,
		ObservedResources:   ors,
		RequiredResources:   rrs,
		RequiredSchemas:     rsc,
		FunctionCredentials: fcreds,
	}
	req, err := render.BuildCompositeRequest(in)
	if err != nil {
		return errors.Wrap(err, "cannot build render request")
	}

	rsp, err := engine.Render(ctx, req)
	if err != nil {
		return errors.Wrap(err, "cannot render composite resource")
	}

	compositeOut := rsp.GetComposite()
	if compositeOut == nil {
		return errors.New("render response does not contain a composite output")
	}

	out, err := render.ParseCompositeResponse(compositeOut)
	if err != nil {
		return errors.Wrap(err, "cannot parse render response")
	}

	if captureCtx && ctxHandle != nil {
		if s := ctxHandle.Captured(); s != nil {
			out.Context = &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "render.crossplane.io/v1beta1",
				"kind":       "Context",
				"fields":     s.AsMap(),
			}}
		}
	}

	s := json.NewSerializerWithOptions(json.DefaultMetaFactory, nil, nil, json.SerializerOptions{Yaml: true})

	if c.IncludeFullXR {
		// Make our best effort to merge the composition pipeline's changes into
		// the original XR. Note that this may not be 100% accurate, since we
		// don't know how the apiserver would merge lists.
		updatedXR := xr.DeepCopy()
		if err := mergo.Merge(&updatedXR.Object, out.CompositeResource.Object, mergo.WithOverride); err != nil {
			return errors.Wrap(err, "cannot merge updated XR")
		}
		out.CompositeResource = updatedXR
	}

	_, _ = fmt.Fprintln(k.Stdout, "---")
	if err := s.Encode(out.CompositeResource, k.Stdout); err != nil {
		return errors.Wrapf(err, "cannot marshal composite resource %q to YAML", xr.GetName())
	}

	for i := range out.ComposedResources {
		_, _ = fmt.Fprintln(k.Stdout, "---")
		if err := s.Encode(&out.ComposedResources[i], k.Stdout); err != nil {
			return errors.Wrapf(err, "cannot marshal composed resource %q to YAML", out.ComposedResources[i].GetAnnotations()[render.AnnotationKeyCompositionResourceName])
		}
	}

	if c.IncludeFunctionResults {
		for i := range out.Results {
			_, _ = fmt.Fprintln(k.Stdout, "---")
			if err := s.Encode(&out.Results[i], k.Stdout); err != nil {
				return errors.Wrap(err, "cannot marshal result to YAML")
			}
		}
	}

	if c.IncludeContext && out.Context != nil {
		_, _ = fmt.Fprintln(k.Stdout, "---")
		if err := s.Encode(out.Context, k.Stdout); err != nil {
			return errors.Wrap(err, "cannot marshal context to YAML")
		}
	}

	return nil
}

func (c *Cmd) loadFunctions(ctx context.Context, log logging.Logger, sp terminal.SpinnerPrinter) ([]pkgv1.Function, error) {
	if c.Functions != "" {
		fns, err := render.LoadFunctions(c.fs, c.Functions)
		if err != nil {
			return nil, errors.Wrapf(err, "cannot load functions from %q", c.Functions)
		}
		return fns, nil
	}

	projFilePath, err := filepath.Abs(c.ProjectFile)
	if err != nil {
		return nil, errors.Wrap(err, "cannot determine project file path")
	}
	projDir := filepath.Dir(projFilePath)

	if _, err := os.Stat(projFilePath); err != nil {
		return nil, errors.New("functions argument is required when not in a project")
	}

	log.Debug("Loading functions from project", "project-file", projFilePath)

	projFS := afero.NewBasePathFs(afero.NewOsFs(), projDir)
	proj, err := projectfile.Parse(projFS, filepath.Base(projFilePath))
	if err != nil {
		return nil, errors.Wrapf(err, "cannot parse project file %q", projFilePath)
	}

	cacheDir := c.CacheDir
	if cacheDir == "" {
		cacheDir = dependency.DefaultCacheDir()
	}

	xpkgClient, err := clixpkg.NewClient(
		clixpkg.NewRemoteFetcher(),
		clixpkg.WithCacheDir(afero.NewOsFs(), cacheDir),
		clixpkg.WithImageConfigs(proj.Spec.ImageConfigs),
	)
	if err != nil {
		return nil, errors.Wrap(err, "cannot create xpkg client")
	}
	resolver := clixpkg.NewResolver(xpkgClient)

	depMgr := dependency.NewManager(proj, projFS,
		dependency.WithProjectFile(filepath.Base(projFilePath)),
		dependency.WithXpkgClient(xpkgClient),
		dependency.WithResolver(resolver),
	)

	var fns []pkgv1.Function
	if err := sp.WrapWithSuccessSpinner("Resolving function dependencies", func() error {
		var err error
		fns, err = project.LoadFunctionDependencies(depMgr, proj)
		return err
	}); err != nil {
		return nil, errors.Wrap(err, "cannot load project functions")
	}

	if err := sp.WrapAsyncWithSuccessSpinners(func(ch async.EventChannel) error {
		schemasFS := afero.NewBasePathFs(projFS, proj.Spec.Paths.Schemas)
		generators := generator.AllLanguages()
		schemaRunner := runner.NewRealSchemaRunner(runner.WithImageConfig(proj.Spec.ImageConfigs))
		schemaMgr := manager.New(schemasFS, generators, schemaRunner)

		b := project.NewBuilder(
			project.BuildWithMaxConcurrency(c.MaxConcurrency),
			project.BuildWithFunctionIdentifier(functions.DefaultIdentifier),
			project.BuildWithSchemaManager(schemaMgr),
			project.BuildWithDependencyManager(depMgr),
		)

		imgMap, err := b.Build(ctx, proj, projFS,
			project.BuildWithLogger(log),
			project.BuildWithEventChannel(ch),
		)
		if err != nil {
			return err
		}

		embeddedFns, err := project.EmbeddedFunctionsToDaemon(ctx, imgMap)
		fns = append(fns, embeddedFns...)
		return err
	}); err != nil {
		return nil, errors.Wrap(err, "cannot build embedded functions")
	}

	return fns, nil
}
