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

package xr

import (
	"context"

	"github.com/alecthomas/kong"
	un "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	apiextensionsv1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1"

	"github.com/crossplane/cli/v2/internal/project/projectfile"
	"github.com/crossplane/cli/v2/internal/simulate"
	"github.com/crossplane/cli/v2/internal/terminal"

	_ "embed"
)

var (
	//go:embed help/simulate.md
	simulateHelp string
	//go:embed help/simulate_examples.md
	simulateExamples string
)

// takes is what xr simulate takes, for simulate.RejectInput.
const takes = "xr simulate takes composite resource or claim files"

// simulateCmd previews how changed composite resources or claims would change
// a cluster.
type simulateCmd struct {
	simulate.Flags `embed:""`

	Input []string `help:"Composite resource or claim file or directory to simulate. Takes a comma-separated list of paths, and can be repeated. - reads standard input." placeholder:"PATH" required:"" short:"f"`
}

func (c *simulateCmd) Help() string {
	return simulateHelp + simulate.Help(false) + simulateExamples
}

// Run previews the composite resources or claims -f names against the
// cluster, with the cluster's Compositions and functions.
func (c *simulateCmd) Run(sigCtx context.Context, k *kong.Context, logger logging.Logger, sp terminal.SpinnerPrinter) error {
	// Anything but a Composition or project file is a candidate composite
	// resource or claim; checkComposites confirms it against the cluster's XRDs.
	xrs, err := simulate.LoadInputs(c.Input, func(r *un.Unstructured) error {
		gvk := r.GroupVersionKind()
		switch {
		case gvk == apiextensionsv1.CompositionGroupVersionKind:
			return simulate.RejectInput(r, takes+"; composition simulate takes Composition files")
		case gvk.GroupVersion().String() == projectfile.APIVersion && gvk.Kind == projectfile.Kind:
			return simulate.RejectInput(r, takes+"; project simulate takes a project file")
		case r.IsList() || gvk.Kind == "List":
			return simulate.RejectInput(r, takes)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return c.Flags.Run(sigCtx, k, logger, sp, &simulate.Inputs{Composites: xrs})
}
