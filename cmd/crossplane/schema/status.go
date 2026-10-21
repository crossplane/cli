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

	"github.com/crossplane/cli/v2/internal/config"
	"github.com/crossplane/cli/v2/internal/schemas/manager"

	_ "embed"
)

//go:embed help/status.md
var statusHelp string

// statusCmd reports generated schemas that differ from the schema lock.
type statusCmd struct {
	ProjectFlags
}

func (c *statusCmd) Help() string {
	return statusHelp
}

// Run executes the status command.
func (c *statusCmd) Run(k *kong.Context, cfg *config.Config) error {
	e, err := newEnv(c.ProjectFile, cfg, nil)
	if err != nil {
		return err
	}

	statuses, err := e.schemas.Status(context.Background())
	if err != nil {
		return err
	}

	for _, s := range statuses {
		for _, d := range s.Drift {
			fmt.Fprintf(k.Stdout, "%s: %s (%s)\n", s.Key, d.Path, d.Kind) //nolint:errcheck // Best-effort output.
		}
	}
	if drifted := countDrift(statuses); drifted > 0 {
		return errors.Errorf("%d generated files differ from the schema lock; run `crossplane schema generate` to restore them", drifted)
	}

	fmt.Fprintln(k.Stdout, "Schemas match the schema lock") //nolint:errcheck // Best-effort output.
	return nil
}

func countDrift(statuses []manager.SourceStatus) int {
	n := 0
	for _, s := range statuses {
		n += len(s.Drift)
	}
	return n
}
