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
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/spf13/afero"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xcrd"

	apiextensionsv1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1"
	apiextensionsv2 "github.com/crossplane/crossplane/apis/v2/apiextensions/v2"

	commonIO "github.com/crossplane/cli/v2/cmd/crossplane/convert/io"
	"github.com/crossplane/cli/v2/internal/schemas/generator"

	_ "embed"
)

//go:embed help/convert.md
var convertHelp string

type convertOutput struct {
	output string
	data   []byte
}

type convertCmd struct {
	// Arguments.
	InputFile string `arg:"" default:"-" help:"The XRD YAML file to convert, or '-' for stdin." optional:"" predictor:"file" type:"path"`

	// Output flags. OutputFile and OutputDir are mutually exclusive; when
	// neither is set the converted CRDs are emitted on stdout as a multi-doc
	// YAML stream.
	OutputFile string `help:"The file to write the generated CRD YAML to. Legacy XRDs produce a multi-doc YAML stream (XR CRD + Claim CRD)." placeholder:"PATH" predictor:"file"      short:"o"   type:"path"  xor:"output"`
	OutputDir  string `help:"A directory to write the generated CRDs to. Each CRD gets a separate file named after the CRD."                 placeholder:"DIR"  predictor:"directory" type:"path" xor:"output"`

	// Format flags.
	JSONSchema bool `help:"Write JSON Schema files instead of CRDs. Useful for YAML language server integration." name:"json-schema"`

	fs afero.Fs
}

func (c *convertCmd) Help() string {
	return convertHelp
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

	xrdGVKs := []schema.GroupVersionKind{
		apiextensionsv1.CompositeResourceDefinitionGroupVersionKind,
		apiextensionsv2.CompositeResourceDefinitionGroupVersionKind,
	}

	if !slices.Contains(xrdGVKs, xrd.GroupVersionKind()) {
		return errors.Errorf("input is not one of %v; got %s", xrdGVKs, xrd.GroupVersionKind())
	}

	crds, err := toCRDs(xrd)
	if err != nil {
		return errors.Wrapf(err, "cannot derive CRDs from XRD %q", xrd.GetName())
	}

	var outputs []convertOutput
	if c.JSONSchema {
		outputs, err = toJSONSchemaOutputs(crds)
	} else {
		outputs, err = toCRDOutputs(crds)
	}
	if err != nil {
		return err
	}

	return c.writeOutputs(k, outputs)
}

func toCRDOutputs(crds []*extv1.CustomResourceDefinition) ([]convertOutput, error) {
	outputs := make([]convertOutput, 0, len(crds))
	for _, crd := range crds {
		b, err := yaml.Marshal(crd)
		if err != nil {
			return nil, errors.Wrapf(err, "cannot marshal CRD %q", crd.GetName())
		}

		outputs = append(outputs, convertOutput{
			output: crd.GetName() + ".yaml",
			data:   append([]byte("---\n"), b...),
		})
	}
	return outputs, nil
}

func toJSONSchemaOutputs(crds []*extv1.CustomResourceDefinition) ([]convertOutput, error) {
	schemas, err := generator.CRDsToJSONSchemas(crds)
	if err != nil {
		return nil, err
	}

	outputs := make([]convertOutput, 0, len(schemas))
	for _, s := range schemas {
		outputs = append(outputs, convertOutput{
			output: fmt.Sprintf("%s_%s_%s.json", s.Group, s.Version, strings.ToLower(s.Kind)),
			data:   s.Data,
		})
	}
	return outputs, nil
}

func (c *convertCmd) writeOutputs(k *kong.Context, outputs []convertOutput) error {
	if c.OutputDir != "" {
		if err := c.fs.MkdirAll(c.OutputDir, 0o755); err != nil {
			return errors.Wrapf(err, "cannot create output directory %q", c.OutputDir)
		}

		for _, o := range outputs {
			path := filepath.Join(c.OutputDir, o.output)
			if err := afero.WriteFile(c.fs, path, o.data, 0o644); err != nil {
				return errors.Wrapf(err, "cannot write output file %q", path)
			}
		}

		return nil
	}

	if c.JSONSchema && len(outputs) > 1 {
		return errors.Errorf("cannot write %d JSON Schemas to a single output; use --output-dir to write one file per schema", len(outputs))
	}

	var buf bytes.Buffer
	for _, o := range outputs {
		buf.Write(o.data)
	}

	if c.OutputFile != "" {
		if err := afero.WriteFile(c.fs, c.OutputFile, buf.Bytes(), 0o644); err != nil {
			return errors.Wrapf(err, "cannot write output file %q", c.OutputFile)
		}

		return nil
	}

	if _, err := k.Stdout.Write(buf.Bytes()); err != nil {
		return errors.Wrap(err, "cannot write output")
	}

	return nil
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
