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

package function

import (
	"github.com/spf13/afero"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	apiextv1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1"
)

func addStepToComposition(fs afero.Fs, path, stepName, functionRef string) error {
	comp, err := readAndUnmarshalComposition(fs, path)
	if err != nil {
		return err
	}

	if err := addCompositionStep(comp, stepName, functionRef); err != nil {
		return err
	}

	data, err := marshalComposition(comp)
	if err != nil {
		return errors.Wrap(err, "cannot marshal composition")
	}

	return afero.WriteFile(fs, path, data, 0o644)
}

func addCompositionStep(comp *apiextv1.Composition, stepName, functionRef string) error {
	for _, step := range comp.Spec.Pipeline {
		if step.Step != stepName {
			continue
		}
		if step.FunctionRef.Name == functionRef {
			return nil // already exists
		}
		// Step names must be unique within a pipeline, so we can't add this
		// one alongside the existing step. Rewriting the existing step to
		// point somewhere else isn't ours to decide either.
		return errors.Errorf("composition already has a step named %q referencing function %q; rename the function or edit the pipeline by hand", stepName, step.FunctionRef.Name)
	}

	step := apiextv1.PipelineStep{
		Step: stepName,
		FunctionRef: apiextv1.FunctionReference{
			Name: functionRef,
		},
	}

	// Prepend, so the generated function runs before steps that observe what
	// it composes, such as function-auto-ready.
	comp.Spec.Pipeline = append([]apiextv1.PipelineStep{step}, comp.Spec.Pipeline...)
	return nil
}

func readAndUnmarshalComposition(fs afero.Fs, path string) (*apiextv1.Composition, error) {
	data, err := afero.ReadFile(fs, path)
	if err != nil {
		return nil, errors.Wrapf(err, "cannot read composition at %q", path)
	}

	var comp apiextv1.Composition
	if err := yaml.Unmarshal(data, &comp); err != nil {
		return nil, errors.Wrap(err, "cannot unmarshal composition")
	}
	return &comp, nil
}

func marshalComposition(obj any) ([]byte, error) {
	unst, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, err
	}

	unstructured.RemoveNestedField(unst, "status")
	unstructured.RemoveNestedField(unst, "metadata", "creationTimestamp")

	return yaml.Marshal(unst)
}
