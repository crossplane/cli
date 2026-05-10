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

package xrd

import (
	"bufio"
	"os"

	"github.com/alecthomas/kong"
	"github.com/spf13/afero"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xcrd"

	apiextensionsv1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1"

	commonIO "github.com/crossplane/cli/v2/cmd/crossplane/convert/io"
)

type convertCmd struct {
	// Arguments.
	InputFile string `arg:"" default:"-" help:"The XRD YAML file to be converted. If not specified or '-', stdin will be used." optional:"" predictor:"file" type:"path"`

	// Flags.
	OutputFile string `help:"The file to write the generated CRD YAML to. If not specified, stdout will be used." placeholder:"PATH" predictor:"file" short:"o" type:"path"`

	fs afero.Fs
}

func (c *convertCmd) Help() string {
	return `
Convert a Crossplane CompositeResourceDefinition (XRD) into the Kubernetes
CustomResourceDefinition (CRD) that describes the composite resource type.

The output CRD is what Crossplane derives internally from the XRD. This is
useful for inspecting the CRD shape, feeding it into kubectl-based tooling
that doesn't understand XRDs, or as a debugging aid.

Examples:

  # Convert an XRD file and print the CRD to stdout.
  crossplane xrd convert xrd.yaml

  # Convert and write the CRD to a file.
  crossplane xrd convert xrd.yaml -o crd.yaml

  # Read the XRD from stdin.
  cat xrd.yaml | crossplane xrd convert -
`
}

// AfterApply implements kong.AfterApply.
func (c *convertCmd) AfterApply() error {
	c.fs = afero.NewOsFs()
	return nil
}

func (c *convertCmd) Run(k *kong.Context) error {
	data, err := commonIO.Read(c.fs, c.InputFile)
	if err != nil {
		return err
	}

	xrd := &apiextensionsv1.CompositeResourceDefinition{}
	if err := yaml.Unmarshal(data, xrd); err != nil {
		return errors.Wrap(err, "cannot unmarshal XRD")
	}

	if xrd.GroupVersionKind() != apiextensionsv1.CompositeResourceDefinitionGroupVersionKind {
		return errors.Errorf("input is not a %s; got %s", apiextensionsv1.CompositeResourceDefinitionGroupVersionKind, xrd.GroupVersionKind())
	}

	crd, err := ToCRD(xrd)
	if err != nil {
		return errors.Wrapf(err, "cannot derive CRD from XRD %q", xrd.GetName())
	}

	b, err := yaml.Marshal(crd)
	if err != nil {
		return errors.Wrap(err, "cannot marshal CRD")
	}

	output := k.Stdout

	if c.OutputFile != "" {
		f, err := c.fs.OpenFile(c.OutputFile, os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return errors.Wrap(err, "cannot open output file")
		}

		defer func() { _ = f.Close() }()

		output = f
	}

	outputW := bufio.NewWriter(output)
	if _, err := outputW.WriteString("---\n"); err != nil {
		return errors.Wrap(err, "cannot write YAML file header")
	}

	if _, err := outputW.Write(b); err != nil {
		return errors.Wrap(err, "cannot write YAML file content")
	}

	if err := outputW.Flush(); err != nil {
		return errors.Wrap(err, "cannot flush output")
	}

	return nil
}

// ToCRD converts a Crossplane XRD into a Kubernetes CRD that describes the
// Composite Resource type, ready to be serialized. The returned CRD's
// TypeMeta is populated so YAML/JSON marshaling produces a well-formed
// `kind: CustomResourceDefinition` document, which is something that
// the underlying xcrd.ForCompositeResource helper does not do on its own.
// Callers that consume the CRD in-memory should call xcrd.ForCompositeResource directly.
func ToCRD(xrd *apiextensionsv1.CompositeResourceDefinition) (*extv1.CustomResourceDefinition, error) {
	crd, err := xcrd.ForCompositeResource(xrd)
	if err != nil {
		return nil, err
	}

	crd.APIVersion = extv1.SchemeGroupVersion.String()
	crd.Kind = "CustomResourceDefinition"

	return crd, nil
}
