/*
Copyright 2025 The Crossplane Authors.

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

// Package op implements operation rendering using operation functions.
package op

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/alecthomas/kong"
	"github.com/spf13/afero"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kjson "k8s.io/apimachinery/pkg/runtime/serializer/json"
	"k8s.io/kube-openapi/pkg/spec3"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	opsv1alpha1 "github.com/crossplane/crossplane/apis/v2/ops/v1alpha1"
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
)

// Cmd arguments and flags for alpha render op subcommand.
type Cmd struct {
	render.EngineFlags `prefix:""`

	// Arguments.
	Operation string `arg:"" help:"A YAML file specifying the Operation to render."                                                                                                     predictor:"yaml_file" type:"existingfile"`
	Functions string `arg:"" help:"A YAML file or directory of YAML files specifying the operation functions to use to render the Operation. May be omitted when running in a project." optional:""           predictor:"yaml_file_or_directory" type:"path"`

	// Flags. Keep them in alphabetical order.
	ContextFiles           map[string]string `help:"Comma-separated context key-value pairs to pass to the function pipeline. Values must be files containing JSON."                           mapsep:""               predictor:"file"`
	ContextValues          map[string]string `help:"Comma-separated context key-value pairs to pass to the function pipeline. Values must be JSON. Keys take precedence over --context-files." mapsep:""`
	FunctionCredentials    string            `help:"A YAML file or directory of YAML files specifying credentials to use for functions."                                                       placeholder:"PATH"      predictor:"yaml_file_or_directory" type:"path"`
	FunctionAnnotations    []string          `help:"Override function annotations for all functions. Can be repeated."                                                                         placeholder:"KEY=VALUE" short:"a"`
	IncludeContext         bool              `help:"Include the context in the rendered output as a resource of kind: Context."                                                                short:"c"`
	IncludeFullOperation   bool              `help:"Include a direct copy of the input Operation's spec and metadata fields in the rendered output."                                           short:"o"`
	IncludeFunctionResults bool              `help:"Include informational and warning messages from functions in the rendered output as resources of kind: Result."                            short:"r"`
	RequiredResources      string            `help:"A YAML file or directory of YAML files specifying required resources to pass to the function pipeline."                                    placeholder:"PATH"      predictor:"yaml_file_or_directory" short:"e"   type:"path"`
	RequiredSchemas        string            `help:"A directory of JSON files specifying OpenAPI schemas to pass to the function pipeline."                                                    placeholder:"DIR"       predictor:"directory"              type:"path"`
	WatchedResource        string            `help:"A YAML file specifying the watched resource for WatchOperation rendering. The resource is also added to required resources."               placeholder:"PATH"      predictor:"yaml_file"              short:"w"   type:"existingfile"`

	CacheDir       string        `env:"CROSSPLANE_XPKG_CACHE"       help:"Directory for cached xpkg package contents."          name:"cache-dir"`
	MaxConcurrency uint          `default:"8"                       help:"Maximum concurrency for building embedded functions."`
	ProjectFile    string        `default:"crossplane-project.yaml" help:"Path to the project file. Optional."                  optional:""      predictor:"yaml_file" short:"f" type:"path"`
	Timeout        time.Duration `default:"1m"                      help:"How long to run before timing out."`

	fs afero.Fs

	// newEngine constructs the render Engine.
	newEngine func(*render.EngineFlags, logging.Logger) render.Engine
}

// Help prints out the help for the alpha render op command.
func (c *Cmd) Help() string {
	return `
This command shows you what resources an Operation would create or mutate by
printing them to stdout. It runs the Crossplane render engine to produce
high-fidelity output that matches what the real reconciler would produce.

For Operations, it runs the operation function pipeline and shows what
resources the operation would mutate.

Functions are pulled and run using Docker by default. You can add
the following annotations to each function to change how they're run:

  render.crossplane.io/runtime: "Development"

    Connect to a function that is already running, instead of using Docker. This
	is useful to develop and debug new functions. The function must be listening
	at localhost:9443 and running with the --insecure flag.

  render.crossplane.io/runtime-development-target: "dns:///example.org:7443"

    Connect to a function running somewhere other than localhost:9443. The
	target uses gRPC target syntax.

  render.crossplane.io/runtime-docker-cleanup: "Orphan"

    Don't stop the function's Docker container after rendering.

  render.crossplane.io/runtime-docker-name: "<name>"

    create a container with that name and also reuse it as long as it is running or can be restarted.

  render.crossplane.io/runtime-docker-pull-policy: "Always"

    Always pull the function's package, even if it already exists locally.
	Other supported values are Never, or IfNotPresent.

Use the standard DOCKER_HOST, DOCKER_API_VERSION, DOCKER_CERT_PATH, and
DOCKER_TLS_VERIFY environment variables to configure how this command connects
to the Docker daemon.

Examples:

  # Render an Operation.
  crossplane operation render operation.yaml functions.yaml

  # Pin the Crossplane version used for rendering.
  crossplane operation render operation.yaml functions.yaml \
    --crossplane-version=v2.2.1

  # Use a local crossplane binary instead of Docker.
  crossplane operation render operation.yaml functions.yaml \
    --crossplane-binary=/usr/local/bin/crossplane

  # Pass context values to the function pipeline.
  crossplane operation render operation.yaml functions.yaml \
    --context-values=apiextensions.crossplane.io/environment='{"key": "value"}'

  # Pass required resources functions can request.
  crossplane operation render operation.yaml functions.yaml \
	--required-resources=required-resources.yaml

  # Pass OpenAPI schemas for functions that need them.
  crossplane operation render operation.yaml functions.yaml \
	--required-schemas=schemas/

  # Render a WatchOperation with a watched resource.
  crossplane operation render watchoperation.yaml functions.yaml \
	--watched-resource=watched-configmap.yaml

  # Pass credentials to functions that need them.
  crossplane operation render operation.yaml functions.yaml \
	--function-credentials=credentials.yaml

  # Include function results and context in output.
  crossplane operation render operation.yaml functions.yaml -r -c

  # Include the full Operation with original spec and metadata.
  crossplane operation render operation.yaml functions.yaml -o

  # Override function annotations for remote Docker daemon.
  crossplane operation render operation.yaml functions.yaml \
	-a render.crossplane.io/runtime-docker-publish-address=0.0.0.0 \
	-a render.crossplane.io/runtime-docker-target=192.168.1.100

  # Use development runtime with custom target.
  crossplane operation render operation.yaml functions.yaml \
	-a render.crossplane.io/runtime=Development \
	-a render.crossplane.io/runtime-development-target=localhost:9444
`
}

// AfterApply implements kong.AfterApply.
func (c *Cmd) AfterApply() error {
	c.fs = afero.NewOsFs()
	c.newEngine = render.NewEngineFromFlags

	return nil
}

// Run alpha render op.
func (c *Cmd) Run(k *kong.Context, log logging.Logger, sp terminal.SpinnerPrinter) error { //nolint:gocognit // Orchestration is inherently complex.
	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()

	// Load operation (extracts Operation template from CronOperation/WatchOperation)
	op, err := LoadOperation(c.fs, c.Operation)
	if err != nil {
		return err
	}

	// Load required resources
	rrs := []unstructured.Unstructured{}
	if c.RequiredResources != "" {
		rrs, err = render.LoadRequiredResources(c.fs, c.RequiredResources)
		if err != nil {
			return errors.Wrapf(err, "cannot load required resources from %q", c.RequiredResources)
		}
	}

	// Load required schemas
	rsc := []spec3.OpenAPI{}
	if c.RequiredSchemas != "" {
		rsc, err = render.LoadRequiredSchemas(c.fs, c.RequiredSchemas)
		if err != nil {
			return errors.Wrapf(err, "cannot load required schemas from %q", c.RequiredSchemas)
		}
	}

	// Handle watched resource for WatchOperation rendering
	if c.WatchedResource != "" {
		watched, err := render.LoadRequiredResources(c.fs, c.WatchedResource)
		if err != nil {
			return errors.Wrapf(err, "cannot load watched resource from %q", c.WatchedResource)
		}

		if len(watched) != 1 {
			return errors.Errorf("--watched-resource must contain exactly one resource, got %d", len(watched))
		}

		// Inject selector into all pipeline steps (replicates WatchOperation controller behavior)
		InjectWatchedResource(op, &watched[0])

		// Add to required resources so it can be fetched by functions
		rrs = append(rrs, watched[0])
	}

	// Load functions
	fns, err := c.loadFunctions(ctx, log, sp)
	if err != nil {
		return err
	}

	// Apply global annotation overrides to each function
	if err := render.OverrideFunctionAnnotations(fns, c.FunctionAnnotations); err != nil {
		return errors.Wrap(err, "cannot apply function annotation overrides")
	}

	// Load function credentials
	fcreds := []corev1.Secret{}
	if c.FunctionCredentials != "" {
		fcreds, err = render.LoadCredentials(c.fs, c.FunctionCredentials)
		if err != nil {
			return errors.Wrapf(err, "cannot load function credentials from %q", c.FunctionCredentials)
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
			op.Spec.Pipeline = append([]opsv1alpha1.PipelineStep{ctxHandle.OperationSeedStep()}, op.Spec.Pipeline...)
		}
		if captureCtx {
			op.Spec.Pipeline = append(op.Spec.Pipeline, ctxHandle.OperationCaptureStep())
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
	in := render.OperationInputs{
		Operation:           op,
		FunctionAddrs:       addrs,
		RequiredResources:   rrs,
		RequiredSchemas:     rsc,
		FunctionCredentials: fcreds,
	}
	req, err := render.BuildOperationRequest(in)
	if err != nil {
		return errors.Wrap(err, "cannot build render request")
	}

	rsp, err := engine.Render(ctx, req)
	if err != nil {
		return errors.Wrap(err, "cannot render operation")
	}

	operationOut := rsp.GetOperation()
	if operationOut == nil {
		return errors.New("render response does not contain an operation output")
	}

	out, err := render.ParseOperationResponse(operationOut)
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

	// Output results
	s := kjson.NewSerializerWithOptions(kjson.DefaultMetaFactory, nil, nil, kjson.SerializerOptions{Yaml: true})

	// Only include spec when IncludeFullOperation flag is set
	if c.IncludeFullOperation && out.Operation != nil {
		out.Operation.Spec = *op.Spec.DeepCopy()
	}

	// Always output the Operation (with metadata and status, optionally with spec)
	if out.Operation != nil {
		_, _ = fmt.Fprintln(k.Stdout, "---")
		if err := s.Encode(out.Operation, k.Stdout); err != nil {
			return errors.Wrapf(err, "cannot marshal operation %q to YAML", op.GetName())
		}
	}

	// Output applied resources
	for _, res := range out.AppliedResources {
		_, _ = fmt.Fprintln(k.Stdout, "---")
		if err := s.Encode(&res, k.Stdout); err != nil {
			return errors.Wrap(err, "cannot marshal applied resource to YAML")
		}
	}

	// Output results if requested
	if c.IncludeFunctionResults {
		for _, res := range out.Results {
			_, _ = fmt.Fprintln(k.Stdout, "---")
			if err := s.Encode(&res, k.Stdout); err != nil {
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
