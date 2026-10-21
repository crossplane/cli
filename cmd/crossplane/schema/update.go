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

package schema

import (
	"context"
	"fmt"

	"github.com/alecthomas/kong"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	"github.com/crossplane/cli/v2/internal/async"
	"github.com/crossplane/cli/v2/internal/config"
	"github.com/crossplane/cli/v2/internal/project"
	"github.com/crossplane/cli/v2/internal/terminal"

	_ "embed"
)

//go:embed help/update.md
var updateHelp string

// updateCmd generates schemas for the project's APIs and dependencies and
// writes the schema lock.
type updateCmd struct {
	ProjectFlags
	SourceFlags

	Refresh bool `help:"Ignore the versions in the schema lock, resolve to the newest allowed versions and pull them again." name:"refresh"`
}

func (c *updateCmd) Help() string {
	return updateHelp
}

// Run executes the update command.
func (c *updateCmd) Run(k *kong.Context, logger logging.Logger, sp terminal.SpinnerPrinter, cfg *config.Config) error {
	ctx := context.Background()
	e, err := newEnv(c.ProjectFile, cfg, &c.SourceFlags)
	if err != nil {
		return err
	}

	logger.Debug("Updating schemas", "refresh", c.Refresh)
	var res *project.UpdateResult
	if err := sp.WrapAsyncWithSuccessSpinners(func(ch async.EventChannel) error {
		var err error
		res, err = project.UpdateSchemas(ctx, e.schemas, e.deps, e.proj, e.projFS, ch, c.Refresh)
		return err
	}); err != nil {
		return err
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(k.Stderr, "Warning: %s\n", w) //nolint:errcheck // Best-effort output.
	}
	for _, key := range res.Removed {
		fmt.Fprintf(k.Stdout, "Removed unused schemas of %s\n", key) //nolint:errcheck // Best-effort output.
	}

	// Add never repairs files, so tell the user if some are still off.
	statuses, err := e.schemas.Status(ctx)
	if err != nil {
		return err
	}
	if drifted := countDrift(statuses); drifted > 0 {
		fmt.Fprintf(k.Stdout, "%d generated files differ from the schema lock; run `crossplane schema generate` to restore them\n", drifted) //nolint:errcheck // Best-effort output.
	}
	return nil
}
