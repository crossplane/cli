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

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	"github.com/crossplane/cli/v2/internal/config"
	"github.com/crossplane/cli/v2/internal/project"
	"github.com/crossplane/cli/v2/internal/terminal"

	_ "embed"
)

//go:embed help/generate.md
var generateHelp string

// generateCmd recreates schemas from the schema lock without changing it.
type generateCmd struct {
	ProjectFlags
	SourceFlags
}

func (c *generateCmd) Help() string {
	return generateHelp
}

// Run executes the generate command.
func (c *generateCmd) Run(k *kong.Context, logger logging.Logger, sp terminal.SpinnerPrinter, cfg *config.Config) error {
	ctx := context.Background()

	e, err := newEnv(c.ProjectFile, cfg, &c.SourceFlags)
	if err != nil {
		return err
	}

	hasLock, err := e.schemas.HasLock()
	if err != nil {
		return err
	}
	if !hasLock {
		return errors.New("no schema lock; run `crossplane schema update`")
	}

	logger.Debug("Generating schemas from the schema lock")
	if err := sp.WrapWithSuccessSpinner("Generating schemas from the schema lock", func() error {
		return project.VerifySchemas(ctx, e.schemas, e.deps, e.proj, e.projFS, true)
	}); err != nil {
		return err
	}
	fmt.Fprintln(k.Stdout, "Schemas match the schema lock") //nolint:errcheck // Best-effort output.
	return nil
}
