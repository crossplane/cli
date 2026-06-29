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

// Package manager implements a schema manager for use in Crossplane projects.
package manager

import (
	"context"
	"encoding/json"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"

	"github.com/invopop/jsonschema"
	"github.com/spf13/afero"
	"golang.org/x/sync/errgroup"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	devv1alpha1 "github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/internal/filesystem"
	"github.com/crossplane/cli/v2/internal/schemas/generator"
	"github.com/crossplane/cli/v2/internal/schemas/runner"
)

// Manager is a schema manager. It manages a directory of schemas, generating
// new schemas only when necessary.
type Manager struct {
	fs         afero.Fs
	generators []generator.Interface
	runner     runner.SchemaRunner

	lockMu sync.RWMutex
}

// Add ensures schemas for resources in the given source are present in the
// managed directory.
func (m *Manager) Add(ctx context.Context, source Source) error {
	version, err := source.Version(ctx)
	if err != nil {
		return err
	}

	existing, err := m.currentVersion(source.ID())
	if err != nil {
		return err
	}
	if existing == version {
		return nil
	}

	_, err = m.Generate(ctx, source)
	return err
}

// Generate generates and returns schemas using the manager's generators, and
// adds them to the manager. Unlike Add, Generate will always generate schemas,
// regardless of whether they're already present in the manager.
func (m *Manager) Generate(ctx context.Context, source Source) (map[string]afero.Fs, error) {
	version, err := source.Version(ctx)
	if err != nil {
		return nil, err
	}

	fromFS, err := source.Resources(ctx)
	if err != nil {
		return nil, err
	}

	schemas := make(map[string]afero.Fs)
	eg, egCtx := errgroup.WithContext(ctx)
	sourceType := source.Type()
	for _, gen := range m.generators {
		eg.Go(func() error {
			var schemaFS afero.Fs
			var err error

			switch sourceType {
			case SourceTypeCRD:
				schemaFS, err = gen.GenerateFromCRD(egCtx, fromFS, m.runner)
			case SourceTypeOpenAPI:
				schemaFS, err = gen.GenerateFromOpenAPI(egCtx, fromFS, m.runner)
			default:
				return errors.Errorf("unsupported source type %q", sourceType)
			}
			if err != nil {
				return err
			}

			if schemaFS != nil {
				schemas[gen.Language()] = schemaFS
			}

			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}

	// Copy generated schemas into our schema repository. Generators produce
	// output into models/ — we strip that prefix by copying from models/ into
	// the language directory.
	for lang, genFS := range schemas {
		langFS := afero.NewBasePathFs(m.fs, lang)

		// Try to copy from models/ subdirectory first (generators put output there).
		modelsFS := afero.NewBasePathFs(genFS, "models")
		hasModels := false
		if fi, err := modelsFS.Stat("."); err == nil && fi.IsDir() {
			hasModels = true
		}

		if hasModels {
			if err := filesystem.CopyFilesBetweenFs(modelsFS, langFS); err != nil {
				return nil, err
			}
		} else {
			if err := filesystem.CopyFilesBetweenFs(genFS, langFS); err != nil {
				return nil, err
			}
		}

		if err := postProcessForLanguage(lang, langFS); err != nil {
			return nil, err
		}
	}

	return schemas, m.updateVersion(source.ID(), version)
}

func postProcessForLanguage(language string, langFS afero.Fs) error {
	switch language {
	case devv1alpha1.SchemaLanguageJSON:
		if err := jsonBuildIndexSchema(langFS); err != nil {
			return errors.Wrap(err, "failed to build index schema for JSON")
		}
		return nil

	default:
		return nil
	}
}

func jsonBuildIndexSchema(langFS afero.Fs) error {
	schemas, err := afero.Glob(langFS, "*.schema.json")
	if err != nil {
		return err
	}

	metaFile := "index.schema.json"
	var metaSchema jsonschema.Schema
	for _, schema := range schemas {
		if schema == metaFile {
			continue
		}
		metaSchema.AnyOf = append(metaSchema.AnyOf, &jsonschema.Schema{
			Ref: filepath.Base(schema),
		})
	}
	bs, err := json.Marshal(metaSchema)
	if err != nil {
		return err
	}

	return afero.WriteFile(langFS, metaFile, bs, 0o644)
}

func (m *Manager) currentVersion(id string) (string, error) {
	m.lockMu.RLock()
	defer m.lockMu.RUnlock()

	l, err := m.getLock()
	if err != nil {
		return "", err
	}

	return l.Packages[id], nil
}

func (m *Manager) updateVersion(id, version string) error {
	m.lockMu.Lock()
	defer m.lockMu.Unlock()

	l, err := m.getLock()
	if err != nil {
		return err
	}

	l.Packages[id] = version

	return m.updateLock(l)
}

func (m *Manager) getLock() (*lock, error) {
	lf, err := m.fs.Open(lockFileName)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return newLock(), nil
		}
		return nil, err
	}
	defer func() { _ = lf.Close() }()

	var l lock
	if err := json.NewDecoder(lf).Decode(&l); err != nil {
		return nil, err
	}

	return &l, nil
}

func (m *Manager) updateLock(l *lock) error {
	if err := m.fs.MkdirAll("/", 0o750); err != nil {
		return errors.Wrap(err, "failed to ensure schema directory exists")
	}

	bs, err := json.Marshal(l)
	if err != nil {
		return errors.Wrap(err, "failed to serialize schema lock")
	}

	if err := afero.WriteFile(m.fs, lockFileName, bs, 0o600); err != nil {
		return errors.Wrap(err, "failed to write schema lock file")
	}

	return nil
}

// GenerateFromMultipleSources generates schemas from multiple sources at once.
// This is important for TypeScript generation where all CRDs should be processed
// together to generate proper cross-references and a unified index.js.
// Sources with the same SourceType are merged before generation.
func (m *Manager) GenerateFromMultipleSources(ctx context.Context, sources []Source) error {
	if len(sources) == 0 {
		return nil
	}

	// Group sources by type
	crdSources := make([]Source, 0)
	openAPISources := make([]Source, 0)
	for _, src := range sources {
		switch src.Type() {
		case SourceTypeCRD:
			crdSources = append(crdSources, src)
		case SourceTypeOpenAPI:
			openAPISources = append(openAPISources, src)
		}
	}

	// Generate from CRD sources (merged)
	if len(crdSources) > 0 {
		if err := m.generateFromMergedSources(ctx, crdSources, SourceTypeCRD); err != nil {
			return errors.Wrap(err, "failed to generate schemas from CRD sources")
		}
	}

	// Generate from OpenAPI sources (merged)
	if len(openAPISources) > 0 {
		if err := m.generateFromMergedSources(ctx, openAPISources, SourceTypeOpenAPI); err != nil {
			return errors.Wrap(err, "failed to generate schemas from OpenAPI sources")
		}
	}

	return nil
}

// generateFromMergedSources merges all source filesystems and generates schemas once.
func (m *Manager) generateFromMergedSources(ctx context.Context, sources []Source, sourceType SourceType) error {
	// Collect all resources into a merged filesystem
	mergedFS := afero.NewMemMapFs()
	sourceVersions := make(map[string]string)

	for _, src := range sources {
		version, err := src.Version(ctx)
		if err != nil {
			return errors.Wrapf(err, "failed to get version for source %s", src.ID())
		}

		// Check if this source is already up to date
		existing, err := m.currentVersion(src.ID())
		if err != nil {
			return err
		}
		if existing == version {
			// Source is up to date, but we still need to include its resources
			// for the merged generation to work correctly
		}

		srcFS, err := src.Resources(ctx)
		if err != nil {
			return errors.Wrapf(err, "failed to get resources for source %s", src.ID())
		}

		// Copy resources into merged filesystem under a unique prefix
		// to avoid file name collisions
		prefix := sanitizeSourceID(src.ID())
		prefixedFS := afero.NewBasePathFs(mergedFS, prefix)
		if err := filesystem.CopyFilesBetweenFs(srcFS, prefixedFS); err != nil {
			return errors.Wrapf(err, "failed to copy resources from source %s", src.ID())
		}

		sourceVersions[src.ID()] = version
	}

	// Run generators on the merged filesystem
	schemas := make(map[string]afero.Fs)
	var schemasMu sync.Mutex
	eg, egCtx := errgroup.WithContext(ctx)
	for _, gen := range m.generators {
		eg.Go(func() error {
			var schemaFS afero.Fs
			var err error

			switch sourceType {
			case SourceTypeCRD:
				schemaFS, err = gen.GenerateFromCRD(egCtx, mergedFS, m.runner)
			case SourceTypeOpenAPI:
				schemaFS, err = gen.GenerateFromOpenAPI(egCtx, mergedFS, m.runner)
			default:
				return errors.Errorf("unsupported source type %q", sourceType)
			}
			if err != nil {
				return err
			}

			if schemaFS != nil {
				schemasMu.Lock()
				schemas[gen.Language()] = schemaFS
				schemasMu.Unlock()
			}

			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return err
	}

	// Copy generated schemas into our schema repository
	for lang, genFS := range schemas {
		langFS := afero.NewBasePathFs(m.fs, lang)

		// Try to copy from models/ subdirectory first (generators put output there)
		modelsFS := afero.NewBasePathFs(genFS, "models")
		hasModels := false
		if fi, err := modelsFS.Stat("."); err == nil && fi.IsDir() {
			hasModels = true
		}

		if hasModels {
			if err := filesystem.CopyFilesBetweenFs(modelsFS, langFS); err != nil {
				return err
			}
		} else {
			if err := filesystem.CopyFilesBetweenFs(genFS, langFS); err != nil {
				return err
			}
		}

		if err := postProcessForLanguage(lang, langFS); err != nil {
			return err
		}
	}

	// Update version for all sources
	for id, version := range sourceVersions {
		if err := m.updateVersion(id, version); err != nil {
			return errors.Wrapf(err, "failed to update version for source %s", id)
		}
	}

	return nil
}

// sanitizeSourceID converts a source ID to a safe directory name.
func sanitizeSourceID(id string) string {
	// Replace characters that are problematic in filesystem paths
	result := id
	for _, c := range []string{"://", ":", "/", "@"} {
		result = strings.ReplaceAll(result, c, "_")
	}
	return result
}

// New returns an initialized manager.
func New(fs afero.Fs, gens []generator.Interface, r runner.SchemaRunner) *Manager {
	return &Manager{
		fs:         fs,
		generators: gens,
		runner:     r,
	}
}
