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
	"io"
	"os"
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
Convert a Crossplane CompositeResourceDefinition (XRD) into the Kubernetes
CustomResourceDefinition (CRD) that describes the composite resource type.

The output CRD(s) are what Crossplane derives internally from the XRD. This is
useful for inspecting the CRD shape, feeding it into kubectl-based tooling that
doesn't understand XRDs, or as a debugging aid.

For legacy XRDs that offer a Claim (spec.claimNames set, typically with
spec.scope: LegacyCluster) two CRDs are produced: one cluster-scoped CRD for
the XR and one namespaced CRD for the Claim. For namespaced XRDs only the XR
CRD is produced. The detection is automatic; no flag is needed.

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

	crds, err := ToCRDs(xrd)
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
		return writeCRDs(k.Stdout, crds)
	}
}

// writeFile writes the given CRDs to a file as a multi-doc YAML stream.
func (c *convertCmd) writeFile(path string, crds []*extv1.CustomResourceDefinition) error {
	f, err := c.fs.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return errors.Wrapf(err, "cannot open output file %q", path)
	}

	defer func() { _ = f.Close() }()

	return writeCRDs(f, crds)
}

// writeCRDs writes a multi-doc YAML stream of CRDs to w.
func writeCRDs(output io.Writer, crds []*extv1.CustomResourceDefinition) error {
	outputW := bufio.NewWriter(output)

	for _, crd := range crds {
		b, err := yaml.Marshal(crd)
		if err != nil {
			return errors.Wrapf(err, "cannot marshal CRD %q", crd.GetName())
		}

		if _, err := outputW.WriteString("---\n"); err != nil {
			return errors.Wrap(err, "cannot write YAML file header")
		}

		if _, err := outputW.Write(b); err != nil {
			return errors.Wrap(err, "cannot write YAML file content")
		}
	}

	if err := outputW.Flush(); err != nil {
		return errors.Wrap(err, "cannot flush output")
	}

	return nil
}

// ToCRDs converts a Crossplane XRD into the Kubernetes CRDs that describe the
// composite resource type, ready to be serialized. The returned CRDs have
// their TypeMeta populated so YAML/JSON marshaling produces well-formed
// `kind: CustomResourceDefinition` documents, which is something that the
// underlying xcrd helpers do not do on their own.
//
// For legacy XRDs that offer a Claim the result is a two-element slice:
// the CRD for the XR followed by the CRD for the Claim. For namespaced XRDs
// the result is a single-element slice containing only the XR CRD.
//
// Callers that consume the CRD in-memory should call xcrd.ForCompositeResource
// (and, for legacy XRDs, xcrd.ForCompositeResourceClaim) directly.
func ToCRDs(xrd *apiextensionsv1.CompositeResourceDefinition) ([]*extv1.CustomResourceDefinition, error) {
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
