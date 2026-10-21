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
	"fmt"

	"github.com/alecthomas/kong"

	"github.com/crossplane/cli/v2/internal/config"

	_ "embed"
)

//go:embed help/clean.md
var cleanHelp string

// cleanCmd removes generated schemas and the schema lock.
type cleanCmd struct {
	ProjectFlags
}

func (c *cleanCmd) Help() string {
	return cleanHelp
}

// Run executes the clean command.
func (c *cleanCmd) Run(k *kong.Context, cfg *config.Config) error {
	e, err := newEnv(c.ProjectFile, cfg, nil)
	if err != nil {
		return err
	}
	if err := e.schemas.Clean(); err != nil {
		return err
	}

	fmt.Fprintln(k.Stdout, "Schemas cleaned") //nolint:errcheck // Best-effort output.
	return nil
}
