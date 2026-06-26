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

// options holds configurable behavior shared across generators.
type options struct {
	goRuntimeObjects bool
}

// Option configures the generators returned by AllLanguages.
type Option func(*options)

// WithGoRuntimeObjects enables generation of runtime.Object methods (DeepCopy,
// GetObjectKind, DeepCopyObject) and per-package AddToScheme helpers on the
// generated Go models. Disabled by default; gated behind the
// features.generateGoRuntimeObjects config flag.
func WithGoRuntimeObjects(enabled bool) Option {
	return func(o *options) { o.goRuntimeObjects = enabled }
}

// AllLanguages returns generators for all supported languages. The set of
// supported language identifiers is defined by
// devv1alpha1.SupportedSchemaLanguages.
func AllLanguages(opts ...Option) []Interface {
	o := &options{}
	for _, opt := range opts {
		opt(o)
	}
	return []Interface{
		&goGenerator{runtimeObjects: o.goRuntimeObjects},
		&jsonGenerator{},
		&kclGenerator{},
		&pythonGenerator{},
	}
}

// Filter returns the subset of generators whose language identifier appears
// in langs. The order of generators in the result matches the order of all.
// If langs is empty, all generators are returned unchanged.
func Filter(all []Interface, langs []string) []Interface {
	if len(langs) == 0 {
		return all
	}
	out := make([]Interface, 0, len(all))
	for _, g := range all {
		if slices.Contains(langs, g.Language()) {
			out = append(out, g)
		}
	}
	return out
}
