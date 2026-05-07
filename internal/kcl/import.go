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

// Package kcl contains helpers for KCL function generation.
package kcl

import (
	"path/filepath"
	"strings"
)

// FormatKclImportPaths converts a set of schema directory paths under
// schemas/kcl/ to KCL import paths prefixed with "models." and unqiue aliases.
//
// For example, given a path like "kcl/io.example.platform.v1alpha1" (relative
// to the schemas root), this produces:
//
//	map[string]string{"platformv1alpha1": "models.io.example.platform.v1alpha1"}
//
// The "models." prefix matches the kcl.mod dependency name (models = { path =
// "./model" }) and the symlink created at function generation time.
func FormatKclImportPaths(paths []string) map[string]string {
	imports := make(map[string]string, len(paths))

	for _, path := range paths {
		path = filepath.ToSlash(path)

		// Strip the leading "kcl/" prefix to get the schema-relative path.
		const prefix = "kcl/"
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		schemaPath := path[len(prefix):]
		if schemaPath == "" {
			continue
		}

		// The import path is "models." + the schema path with slashes converted to
		// dots and hyphens to underscores.
		importPath := "models." + strings.ReplaceAll(schemaPath, "/", ".")
		importPath = strings.ReplaceAll(importPath, "-", "_")

		// Split into components for alias generation.
		parts := strings.Split(importPath, ".")
		if len(parts) < 2 {
			continue
		}

		// Default alias is the last two components joined, e.g. "ec2v1beta1".
		alias := parts[len(parts)-2] + parts[len(parts)-1]

		// Resolve collisions by adding more context from earlier components.
		if _, ok := imports[alias]; ok {
			for i := 3; i <= len(parts); i++ {
				alias = strings.Join(parts[len(parts)-i:], "")
				if _, ok := imports[alias]; !ok {
					break
				}
			}
		}

		imports[alias] = importPath
	}

	return imports
}
