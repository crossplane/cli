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
	devv1alpha1 "github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/internal/config"
	"github.com/crossplane/cli/v2/internal/schemas/generator"
)

// SchemaGenerators returns the schema generators for a project, restricted to
// the languages configured in its spec.schemas.languages.
func SchemaGenerators(proj *devv1alpha1.Project, cfg *config.Config) []generator.Interface {
	return generator.Filter(
		generator.AllLanguages(
			generator.WithGoModelAccessors(cfg.Features.GenerateGoModelAccessors),
			generator.WithGoRuntimeObjects(cfg.Features.GenerateGoRuntimeObjects),
		),
		proj.Spec.Schemas.GetLanguages(),
	)
}
