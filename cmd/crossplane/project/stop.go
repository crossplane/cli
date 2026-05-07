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
	"fmt"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	"github.com/crossplane/cli/v2/internal/project/controlplane"
	"github.com/crossplane/cli/v2/internal/project/projectfile"
	"github.com/crossplane/cli/v2/internal/terminal"
)

// stopCmd tears down a local dev control plane.
type stopCmd struct {
	ProjectFile      string `default:"crossplane-project.yaml"                               help:"Path to project definition." short:"f"`
	ControlPlaneName string `help:"Name of the dev control plane. Defaults to project name."`
	RegistryDir      string `help:"Directory for local registry images."`
}

// Run executes the stop command.
func (c *stopCmd) Run(logger logging.Logger, sp terminal.SpinnerPrinter) error {
	ctx := context.Background()

	name := c.ControlPlaneName
	if name == "" {
		projFilePath, err := filepath.Abs(c.ProjectFile)
		if err != nil {
			return err
		}
		projDirPath := filepath.Dir(projFilePath)
		projFS := afero.NewBasePathFs(afero.NewOsFs(), projDirPath)

		projFileName := filepath.Base(c.ProjectFile)
		prj, err := projectfile.Parse(projFS, projFileName)
		if err != nil {
			return errors.New("this is not a project directory; use --control-plane-name to specify the control plane name")
		}
		name = "crossplane-" + prj.Name
	}

	logger.Debug("Tearing down local dev control plane", "name", name)
	if err := sp.WrapWithSuccessSpinner("Tearing down control plane", func() error {
		return controlplane.TeardownLocalDevControlPlane(ctx, name, c.RegistryDir)
	}); err != nil {
		return err
	}

	fmt.Printf("Local dev control plane %q has been torn down.\n", name) //nolint:forbidigo // CLI output.
	return nil
}
