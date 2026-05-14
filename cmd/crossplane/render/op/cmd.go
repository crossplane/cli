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

	"github.com/crossplane/cli/v2/cmd/crossplane/render"
	"github.com/crossplane/cli/v2/cmd/crossplane/render/contextfn"

	_ "embed"
)

//go:embed help/render.md
var helpDetail string

// Cmd arguments and flags for alpha render op subcommand.
type Cmd struct {
	render.EngineFlags `prefix:""`

	// Arguments.
	Operation string `arg:"" help:"A YAML file specifying the Operation to render."                                                           predictor:"yaml_file"              type:"existingfile"`
	Functions string `arg:"" help:"A YAML file or directory of YAML files specifying the operation functions to use to render the Operation." predictor:"yaml_file_or_directory" type:"path"`

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

	Timeout time.Duration `default:"1m" help:"How long to run before timing out."`

	fs afero.Fs

	// newEngine constructs the render Engine.
	newEngine func(*render.EngineFlags, logging.Logger) render.Engine
}

// Help prints out the help for the alpha render op command.
func (c *Cmd) Help() string {
	return helpDetail
}

// AfterApply implements kong.AfterApply.
func (c *Cmd) AfterApply() error {
	c.fs = afero.NewOsFs()
	c.newEngine = render.NewEngineFromFlags

	return nil
}

// Run alpha render op.
func (c *Cmd) Run(k *kong.Context, log logging.Logger) error { //nolint:gocognit // Orchestration is inherently complex.
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
	fns, err := render.LoadFunctions(c.fs, c.Functions)
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
