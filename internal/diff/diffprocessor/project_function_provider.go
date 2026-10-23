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

package diffprocessor

import (
	"context"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	apiextensionsv1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1"
	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"

	xp "github.com/crossplane/cli/v2/internal/diff/client/crossplane"
)

// ProjectFunctionProvider wraps another FunctionProvider and resolves each
// pipeline step whose function name matches one of a project's functions to
// that project function, instead of the function installed in the cluster.
// Steps naming any other function fall back to the wrapped provider, which is
// asked only for those steps, so a project function that is not installed in
// the cluster does not fail the lookup.
//
// The project functions are used as given: their images must already be
// resolvable by the Docker daemon (project.LoadFunctions loads embedded
// functions into it). Without a pull-policy annotation the docker runtime
// pulls only when the image is absent, so daemon-loaded tags run as loaded.
//
// It is safe for concurrent use, and every call returns its own copy of the
// functions, as CachedFunctionProvider does.
type ProjectFunctionProvider struct {
	inner   FunctionProvider
	project map[string]pkgv1.Function
	logger  logging.Logger
}

// NewProjectFunctionProvider wraps inner so that pipeline steps naming one of
// fns resolve to it. fns is copied. When two of fns share a name the later one
// wins; project.LoadFunctions lists embedded functions after dependencies, so
// an embedded function takes precedence.
func NewProjectFunctionProvider(inner FunctionProvider, fns []pkgv1.Function, logger logging.Logger) FunctionProvider {
	project := make(map[string]pkgv1.Function, len(fns))
	for i := range fns {
		project[fns[i].GetName()] = *fns[i].DeepCopy()
	}

	return &ProjectFunctionProvider{
		inner:   inner,
		project: project,
		logger:  logger,
	}
}

// NewProjectFunctionProviderFactory returns a factory for
// WithFunctionProviderFactory that overlays fns on the default
// CachedFunctionProvider.
func NewProjectFunctionProviderFactory(fns []pkgv1.Function) func(xp.FunctionClient, logging.Logger) FunctionProvider {
	return func(fnClient xp.FunctionClient, logger logging.Logger) FunctionProvider {
		return NewProjectFunctionProvider(NewCachedFunctionProvider(fnClient, logger), fns, logger)
	}
}

// GetFunctionsForComposition returns one function per pipeline step, in
// pipeline order: the project function when the step names one, otherwise the
// function the wrapped provider returns for that step.
func (p *ProjectFunctionProvider) GetFunctionsForComposition(ctx context.Context, comp *apiextensionsv1.Composition) ([]pkgv1.Function, error) {
	// The wrapped provider owns rejecting non-pipeline compositions.
	if comp.Spec.Mode != apiextensionsv1.CompositionModePipeline {
		return p.inner.GetFunctionsForComposition(ctx, comp)
	}

	clusterSteps := make([]apiextensionsv1.PipelineStep, 0, len(comp.Spec.Pipeline))
	for _, step := range comp.Spec.Pipeline {
		if _, ok := p.project[step.FunctionRef.Name]; !ok {
			clusterSteps = append(clusterSteps, step)
		}
	}

	if len(clusterSteps) == len(comp.Spec.Pipeline) {
		return p.inner.GetFunctionsForComposition(ctx, comp)
	}

	clusterFns := map[string]pkgv1.Function{}

	if len(clusterSteps) > 0 {
		// Same name, fewer steps: the wrapped provider caches by composition
		// name, and for a given project set a composition always narrows to
		// the same steps.
		narrowed := comp.DeepCopy()
		narrowed.Spec.Pipeline = clusterSteps

		fns, err := p.inner.GetFunctionsForComposition(ctx, narrowed)
		if err != nil {
			return nil, err
		}

		for i := range fns {
			clusterFns[fns[i].GetName()] = fns[i]
		}
	}

	out := make([]pkgv1.Function, 0, len(comp.Spec.Pipeline))
	for _, step := range comp.Spec.Pipeline {
		name := step.FunctionRef.Name

		if fn, ok := p.project[name]; ok {
			p.logger.Debug("Using project function", "composition", comp.GetName(), "step", step.Step, "function", name, "package", fn.Spec.Package)
			out = append(out, *fn.DeepCopy())

			continue
		}

		fn, ok := clusterFns[name]
		if !ok {
			return nil, errors.Errorf("function %q referenced in pipeline step %q not found", name, step.Step)
		}

		// The wrapped provider's functions are already this call's own copies,
		// but two steps may name the same function.
		out = append(out, *fn.DeepCopy())
	}

	return out, nil
}

// Cleanup delegates to the wrapped provider.
func (p *ProjectFunctionProvider) Cleanup(ctx context.Context) error {
	return p.inner.Cleanup(ctx)
}
