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
	"bytes"
	"path/filepath"

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

	// Output flags. OutputFile and OutputDir are mutually exclusive; when
	// neither is set the converted CRDs are emitted on stdout as a multi-doc
	// YAML stream.
	OutputFile string `help:"The file to write the generated CRD YAML to. Legacy XRDs produce a multi-doc YAML stream (XR CRD + Claim CRD)." placeholder:"PATH" predictor:"file"      short:"o"   type:"path"  xor:"output"`
	OutputDir  string `help:"A directory to write the generated CRDs to. Each CRD is written to a separate file named <crd.Name>.yaml."      placeholder:"DIR"  predictor:"directory" type:"path" xor:"output"`

	fs afero.Fs
}

func (c *convertCmd) Help() string {
	return `
Convert a CompositeResourceDefinition (XRD) into the CustomResourceDefinition(s)
that Crossplane derives from it internally.

Useful for inspecting the generated CRD shape, feeding it into kubectl-based
tooling that doesn't understand XRDs, or debugging composition behavior.

Output depends on the XRD type, detected automatically:
  * Namespaced or Cluster-scoped XRD -> 1 CRD for the XR
  * Legacy XRD without claimNames    -> 1 CRD for the XR
  * Legacy XRD with claimNames       -> 2 CRDs: one for the XR and one for the Claim

Examples:

  # Convert an XRD file and print the CRD(s) to stdout (multi-doc YAML for legacy XRDs).
  crossplane xrd convert xrd.yaml

  # Convert and write to a single file (multi-doc YAML for legacy XRDs).
  crossplane xrd convert xrd.yaml -o crds.yaml

  # Split per-CRD files into a directory (each named <crd.Name>.yaml).
  crossplane xrd convert xrd.yaml --output-dir ./crds/

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

	crds, err := toCRDs(xrd)
	if err != nil {
		return errors.Wrapf(err, "cannot derive CRDs from XRD %q", xrd.GetName())
	}

	switch {
	case c.OutputDir != "":
		if err := c.fs.MkdirAll(c.OutputDir, 0o755); err != nil {
			return errors.Wrapf(err, "cannot create output directory %q", c.OutputDir)
		}

		for _, crd := range crds {
			path := filepath.Join(c.OutputDir, crd.GetName()+".yaml")
			if err := c.writeFile(path, []*extv1.CustomResourceDefinition{crd}); err != nil {
				return err
			}
		}

		return nil

	case c.OutputFile != "":
		return c.writeFile(c.OutputFile, crds)

	default:
		data, err := marshalCRDs(crds)
		if err != nil {
			return err
		}

		if _, err := k.Stdout.Write(data); err != nil {
			return errors.Wrap(err, "cannot write output")
		}

		return nil
	}
}

// writeFile marshals the given CRDs to a multi-doc YAML stream and writes it to path.
func (c *convertCmd) writeFile(path string, crds []*extv1.CustomResourceDefinition) error {
	data, err := marshalCRDs(crds)
	if err != nil {
		return err
	}

	if err := afero.WriteFile(c.fs, path, data, 0o644); err != nil {
		return errors.Wrapf(err, "cannot write output file %q", path)
	}

	return nil
}

// marshalCRDs returns the multi-doc YAML stream for the given CRDs.
func marshalCRDs(crds []*extv1.CustomResourceDefinition) ([]byte, error) {
	var buf bytes.Buffer

	for _, crd := range crds {
		b, err := yaml.Marshal(crd)
		if err != nil {
			return nil, errors.Wrapf(err, "cannot marshal CRD %q", crd.GetName())
		}

		buf.WriteString("---\n")
		buf.Write(b)
	}

	return buf.Bytes(), nil
}

// toCRDs converts a Crossplane XRD into the Kubernetes CRDs that describe
// the composite resource type, ready to be serialized. The returned CRDs
// have their TypeMeta populated so YAML/JSON marshaling produces well-formed
// `kind: CustomResourceDefinition` documents, which is something that the
// underlying xcrd helpers do not do on their own.
//
// When the XRD offers a Claim (Spec.ClaimNames set) the result is a two-
// element slice: the CRD for the XR followed by the CRD for the Claim.
// Otherwise the result is a single-element slice containing the XR CRD.
func toCRDs(xrd *apiextensionsv1.CompositeResourceDefinition) ([]*extv1.CustomResourceDefinition, error) {
	xrCRD, err := xcrd.ForCompositeResource(xrd)
	if err != nil {
		return nil, err
	}

	setTypeMeta(xrCRD)

	crds := []*extv1.CustomResourceDefinition{xrCRD}

	if xrd.OffersClaim() {
		claimCRD, err := xcrd.ForCompositeResourceClaim(xrd)
		if err != nil {
			return nil, err
		}

		setTypeMeta(claimCRD)
		crds = append(crds, claimCRD)
	}

	return crds, nil
}

func setTypeMeta(crd *extv1.CustomResourceDefinition) {
	crd.APIVersion = extv1.SchemeGroupVersion.String()
	crd.Kind = "CustomResourceDefinition"
}
