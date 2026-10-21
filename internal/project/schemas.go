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
	"context"

	"github.com/spf13/afero"
	"golang.org/x/sync/errgroup"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	"github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/internal/async"
	"github.com/crossplane/cli/v2/internal/config"
	"github.com/crossplane/cli/v2/internal/dependency"
	"github.com/crossplane/cli/v2/internal/schemas/generator"
	"github.com/crossplane/cli/v2/internal/schemas/manager"
	"github.com/crossplane/cli/v2/internal/schemas/runner"
)

// NewSchemaManager returns the schema manager for a project. It writes to the
// project's schemas path and generates the languages the project asks for.
func NewSchemaManager(proj *v1alpha1.Project, projFS afero.Fs, features config.Features) *manager.Manager {
	generators := generator.Filter(
		generator.AllLanguages(
			generator.WithGoModelAccessors(features.GenerateGoModelAccessors),
			generator.WithGoRuntimeObjects(features.GenerateGoRuntimeObjects),
		),
		proj.Spec.Schemas.GetLanguages(),
	)
	return manager.New(
		afero.NewBasePathFs(projFS, proj.Spec.Paths.Schemas),
		generators,
		runner.NewRealSchemaRunner(runner.WithImageConfig(proj.Spec.ImageConfigs)),
		manager.WithFlags(manager.FlagsFor(proj.Spec.Schemas, features.GenerateGoRuntimeObjects)),
	)
}

// apisFS returns the filesystem holding the project's own APIs.
func apisFS(proj *v1alpha1.Project, projFS afero.Fs) afero.Fs {
	if proj.Spec.Paths.APIs == "/" {
		return projFS
	}
	return afero.NewBasePathFs(projFS, proj.Spec.Paths.APIs)
}

// APISchemaSource returns the schema source for the project's own APIs, i.e.
// the XRDs under the project's APIs path.
func APISchemaSource(proj *v1alpha1.Project, projFS afero.Fs) manager.Source {
	return manager.NewFSSource(proj.Spec.Paths.APIs, apisFS(proj, projFS))
}

// UpdateResult describes what UpdateSchemas found and removed.
type UpdateResult struct {
	// Warnings are the requirements that the resolved dependencies do not meet.
	Warnings []dependency.Warning
	// Removed are the keys of locked sources that are not used any more.
	Removed []string
}

// UpdateSchemas resolves the project's dependencies, generates schemas for them
// and the project's own APIs, and records the result in the schema lock. It is
// the only place where versions are resolved. Unless refresh is set, versions
// in the lock are kept while they still satisfy the dependencies' constraints.
// An event for each source is sent to ch.
func UpdateSchemas(ctx context.Context, sm *manager.Manager, dm *dependency.Manager, proj *v1alpha1.Project, projFS afero.Fs, ch async.EventChannel, refresh bool) (*UpdateResult, error) {
	if dm == nil {
		return nil, errors.New("dependency manager is not configured")
	}

	locked, err := sm.Locked()
	if err != nil {
		return nil, err
	}

	ch.SendEvent("Resolving dependencies", async.EventStatusStarted)
	sources, warnings, err := dm.Resolve(ctx, dependency.ResolveOptions{Refresh: refresh, Locked: locked})
	if err != nil {
		ch.SendEvent("Resolving dependencies", async.EventStatusFailure)
		return nil, err
	}
	ch.SendEvent("Resolving dependencies", async.EventStatusSuccess)
	sources = append(sources, APISchemaSource(proj, projFS))

	if err := addAll(ctx, sm, ch, sources); err != nil {
		return nil, err
	}

	// Anything in the lock that is not a live source any more is left over from
	// a removed dependency or XRD.
	res, err := sm.Reconcile(ctx, sources)
	if err != nil {
		return nil, err
	}

	depsHash, err := dependency.DependenciesHash(proj)
	if err != nil {
		return nil, err
	}
	if err := sm.SetDependenciesHash(depsHash); err != nil {
		return nil, err
	}
	return &UpdateResult{Warnings: warnings, Removed: res.Sources}, nil
}

// addAll generates schemas for the given sources concurrently, sending an event
// for each to ch.
func addAll(ctx context.Context, sm *manager.Manager, ch async.EventChannel, sources []manager.Source) error {
	eg, egCtx := errgroup.WithContext(ctx)
	for _, src := range sources {
		desc := "Generating schemas for " + src.Key()
		eg.Go(func() error {
			ch.SendEvent(desc, async.EventStatusStarted)
			if err := sm.Add(egCtx, src); err != nil {
				ch.SendEvent(desc, async.EventStatusFailure)
				return errors.Wrapf(err, "cannot generate schemas for %s", src.Key())
			}
			ch.SendEvent(desc, async.EventStatusSuccess)
			return nil
		})
	}
	return eg.Wait()
}

// VerifySchemas checks that the generated schemas match the schema lock,
// recreating files that are missing from the exact pins the lock records. It
// never resolves anything. If repair is set, files that were modified are
// restored too. dm is only used to recreate files, and may be nil.
func VerifySchemas(ctx context.Context, sm *manager.Manager, dm *dependency.Manager, proj *v1alpha1.Project, projFS afero.Fs, repair bool) error {
	depsHash, err := dependency.DependenciesHash(proj)
	if err != nil {
		return err
	}

	fetch := func(context.Context, string, manager.LockedSource) (manager.Source, error) {
		return nil, errors.New("no dependency manager to fetch the locked sources")
	}
	if dm != nil {
		fetch = dm.FetchPinned
	}

	local := []manager.Source{APISchemaSource(proj, projFS)}
	if repair {
		return sm.Generate(ctx, local, depsHash, fetch)
	}
	return sm.Verify(ctx, local, depsHash, fetch)
}
