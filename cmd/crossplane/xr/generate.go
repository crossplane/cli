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
	"github.com/alecthomas/kong"
	"github.com/spf13/afero"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	apiextensionsv1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1"

	commonIO "github.com/crossplane/cli/v2/cmd/crossplane/convert/io"
	"github.com/crossplane/cli/v2/pkg/render"
	xrpkg "github.com/crossplane/cli/v2/pkg/xr"

	_ "embed"
)

//go:embed help/generate.md
var generateHelp string

type generateCmd struct {
	// Arguments.
	InputFile string `arg:"" default:"-" help:"The Claim YAML file to convert, or '-' for stdin." optional:"" predictor:"file" type:"path"`

	// Flags.
	OutputFile string `help:"The file to write the generated XR YAML to. Defaults to stdout."                                                                                             placeholder:"PATH" predictor:"file"   short:"o"        type:"path"`
	Name       string `help:"The name to use for the XR. If empty, defaults to the Claim's name (direct mode) or the Claim's name with a random suffix (non-direct)."                     placeholder:"NAME" type:"string"`
	Kind       string `help:"The kind to use for the XR. Mutually exclusive with --xrd. If neither is set, defaults to 'X' prepended to the Claim's kind (for example, Infra -> XInfra)." placeholder:"KIND" type:"string"      xor:"kind-xrd"`
	XRD        string `help:"A YAML file specifying the CompositeResourceDefinition (XRD) that owns the Claim, used to resolve the XR's kind. Mutually exclusive with --kind."            name:"xrd"         placeholder:"PATH" predictor:"file" type:"path" xor:"kind-xrd"`
	Direct     bool   `help:"Create a direct XR without Claim references and suffix."                                                                                                     name:"direct"      negatable:""`
	GenUID     bool   `help:"Set a fresh random metadata.uid on the generated XR."                                                                                                        name:"gen-uid"`

	fs afero.Fs
}

func (c *generateCmd) Help() string {
	return generateHelp
}

// AfterApply implements kong.AfterApply.
func (c *generateCmd) AfterApply() error {
	c.fs = afero.NewOsFs()
	return nil
}

// Run runs the generate command.
func (c *generateCmd) Run(k *kong.Context) error {
	claimData, err := commonIO.Read(c.fs, c.InputFile)
	if err != nil {
		return err
	}

	claim := &unstructured.Unstructured{}
	if err := yaml.Unmarshal(claimData, claim); err != nil {
		return errors.Wrap(err, "cannot unmarshal Claim")
	}

	var xrd *apiextensionsv1.CompositeResourceDefinition

	if c.XRD != "" {
		xrd, err = render.LoadXRD(c.fs, c.XRD)
		if err != nil {
			return errors.Wrapf(err, "cannot load XRD from %q", c.XRD)
		}
	}

	// Convert to XR
	xr, err := xrpkg.ConvertClaimToXR(claim, xrpkg.Options{
		Name:        c.Name,
		Kind:        c.Kind,
		XRD:         xrd,
		Direct:      c.Direct,
		GenerateUID: c.GenUID,
	})
	if err != nil {
		return errors.Wrap(err, "cannot convert Claim to XR")
	}

	b, err := yaml.Marshal(xr)
	if err != nil {
		return errors.Wrap(err, "cannot marshal XR")
	}

	data := append([]byte("---\n"), b...)

	if c.OutputFile != "" {
		if err := afero.WriteFile(c.fs, c.OutputFile, data, 0o644); err != nil {
			return errors.Wrapf(err, "cannot write output file %q", c.OutputFile)
		}

		return nil
	}

	if _, err := k.Stdout.Write(data); err != nil {
		return errors.Wrap(err, "cannot write output")
	}

	return nil
}
