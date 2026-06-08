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

	commonIO "github.com/crossplane/cli/v2/cmd/crossplane/convert/io"
	"github.com/crossplane/cli/v2/cmd/crossplane/render"
	xrpkg "github.com/crossplane/cli/v2/pkg/xr"

	_ "embed"
)

//go:embed help/patch.md
var patchHelp string

type patchCmd struct {
	// Arguments.
	InputFile string `arg:"" default:"-" help:"The XR YAML file to patch, or '-' for stdin." optional:"" predictor:"file" type:"path"`

	// Output flags.
	OutputFile string `help:"The file to write the patched XR YAML to. Defaults to stdout." placeholder:"PATH" predictor:"file" short:"o" type:"path"`

	// Patching flags.
	XRD string `help:"A YAML file specifying the CompositeResourceDefinition (XRD) that provides schema defaults for the XR." name:"xrd" placeholder:"PATH" predictor:"file" type:"path"`

	fs afero.Fs
}

func (c *patchCmd) Help() string {
	return patchHelp
}

// AfterApply implements kong.AfterApply.
func (c *patchCmd) AfterApply() error {
	c.fs = afero.NewOsFs()
	return nil
}

// Run runs the patch command.
func (c *patchCmd) Run(k *kong.Context) error {
	if c.XRD == "" {
		return errors.New("no patching flag provided: at least one of --xrd must be set")
	}

	xrData, err := commonIO.Read(c.fs, c.InputFile)
	if err != nil {
		return err
	}

	xr := &unstructured.Unstructured{}
	if err := yaml.Unmarshal(xrData, xr); err != nil {
		return errors.Wrap(err, "cannot unmarshal XR")
	}

	xrd, err := render.LoadXRD(c.fs, c.XRD)
	if err != nil {
		return errors.Wrapf(err, "cannot load XRD from %q", c.XRD)
	}

	if err := xrpkg.ApplyXRDDefaults(xr, xrd); err != nil {
		return errors.Wrapf(err, "cannot apply XRD defaults to XR %q", xr.GetName())
	}

	b, err := yaml.Marshal(xr)
	if err != nil {
		return errors.Wrap(err, "cannot marshal patched XR")
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
