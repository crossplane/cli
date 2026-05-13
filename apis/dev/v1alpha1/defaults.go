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

package v1alpha1

// Default sets default values for a Project.
func (p *Project) Default() {
	if p.Spec.Paths == nil {
		p.Spec.Paths = &ProjectPaths{}
	}
	p.Spec.Paths.Default()

	if len(p.Spec.Architectures) == 0 {
		p.Spec.Architectures = []string{"amd64", "arm64"}
	}
}

// Default sets default values for ProjectPaths.
func (p *ProjectPaths) Default() {
	if p.APIs == "" {
		p.APIs = "apis"
	}
	if p.Functions == "" {
		p.Functions = "functions"
	}
	if p.Examples == "" {
		p.Examples = "examples"
	}
	if p.Tests == "" {
		p.Tests = "tests"
	}
	if p.Operations == "" {
		p.Operations = "operations"
	}
	if p.Schemas == "" {
		p.Schemas = "schemas"
	}
}
