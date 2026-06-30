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

// Package generator generates language-specific schemas for Crossplane and
// Kubernetes resources.
package generator

import (
	"context"
	"slices"

	"github.com/spf13/afero"

	"github.com/crossplane/cli/v2/internal/schemas/runner"
)

// Interface generates schemas for a specific language.
type Interface interface {
	Language() string
	GenerateFromCRD(ctx context.Context, fs afero.Fs, runner runner.SchemaRunner) (afero.Fs, error)
	GenerateFromOpenAPI(ctx context.Context, fs afero.Fs, runner runner.SchemaRunner) (afero.Fs, error)
}

// DefaultLanguages returns generators for the default set of languages.
// TypeScript is excluded by default because it requires Node.js and npm,
// which adds significant build time. Users can enable it by explicitly
// listing "typescript" in schemas.languages.
func DefaultLanguages() []Interface {
	return []Interface{
		&goGenerator{},
		&jsonGenerator{},
		&kclGenerator{},
		&pythonGenerator{},
	}
}

// AllLanguages returns generators for all supported languages, including
// those that are not enabled by default. The set of supported language
// identifiers is defined by devv1alpha1.SupportedSchemaLanguages.
func AllLanguages() []Interface {
	return []Interface{
		&goGenerator{},
		&jsonGenerator{},
		&kclGenerator{},
		&pythonGenerator{},
		&typescriptGenerator{},
	}
}

// Filter returns the subset of generators whose language identifier appears
// in langs. The order of generators in the result matches the order of all.
// If langs is empty, the default generators are returned (excluding TypeScript).
func Filter(all []Interface, langs []string) []Interface {
	if len(langs) == 0 {
		return DefaultLanguages()
	}
	out := make([]Interface, 0, len(all))
	for _, g := range all {
		if slices.Contains(langs, g.Language()) {
			out = append(out, g)
		}
	}
	return out
}
