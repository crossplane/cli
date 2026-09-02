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
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apiserver/pkg/storage/names"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/fieldpath"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource/unstructured/composite"

	commonIO "github.com/crossplane/cli/v2/cmd/crossplane/convert/io"

	_ "embed"
)

// Common field names for unstructured objects.
const (
	fieldAPIVersion = "apiVersion"
	fieldKind       = "kind"
	fieldName       = "name"
	fieldNamespace  = "namespace"
)

//go:embed help/generate.md
var generateHelp string

type generateCmd struct {
	// Arguments.
	InputFile string `arg:"" default:"-" help:"The Claim YAML file to convert, or '-' for stdin." optional:"" predictor:"file" type:"path"`

	// Flags.
	OutputFile string `help:"The file to write the generated XR YAML to. Defaults to stdout."                                                                         placeholder:"PATH" predictor:"file" short:"o" type:"path"`
	Name       string `help:"The name to use for the XR. If empty, defaults to the Claim's name (direct mode) or the Claim's name with a random suffix (non-direct)." placeholder:"NAME" type:"string"`
	Kind       string `help:"The kind to use for the XR. Defaults to 'X' prepended to the Claim's kind (for example, Infra -> XInfra)."                               placeholder:"KIND" type:"string"`
	Direct     bool   `help:"Create a direct XR without Claim references and suffix."                                                                                 name:"direct"      negatable:""`
	GenUID     bool   `help:"Set a fresh random metadata.uid on the generated XR."                                                                                    name:"gen-uid"`

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

	// Convert to XR
	xr, err := ConvertClaimToXR(claim, Options{
		Name:        c.Name,
		Kind:        c.Kind,
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

const (
	labelClaimName      = "crossplane.io/claim-name"
	labelClaimNamespace = "crossplane.io/claim-namespace"
	labelComposite      = "crossplane.io/composite"
)

// Options configures ConvertClaimToXR.
type Options struct {
	// Name is the XR name. Empty falls back to:
	//   - claim.Name when Direct is true
	//   - claim.Name with a random suffix when Direct is false
	// A non-empty Name overrides both fallbacks.
	Name string

	// Kind is the XR kind. Empty defaults to "X" + claim.Kind.
	Kind string

	// Direct controls XR linkage to the claim:
	//   - true:  no spec.claimRef; no claim-name/claim-namespace labels
	//   - false: spec.claimRef is set; claim-name/claim-namespace labels added
	Direct bool

	// GenerateUID, when true, sets metadata.uid to a fresh random UUID.
	GenerateUID bool
}

// ConvertClaimToXR converts a Crossplane Claim to a Composite Resource (XR).
func ConvertClaimToXR(claim *unstructured.Unstructured, opts Options) (*composite.Unstructured, error) {
	if claim == nil {
		return nil, errors.New("input is nil")
	}

	if claim.Object == nil {
		return nil, errors.New("invalid Claim YAML: parsed object is empty")
	}

	// Get Claim's properties
	claimName := claim.GetName()

	claimKind := claim.GetKind()
	if claimKind == "" {
		return nil, errors.New("no kind section in Claim")
	}

	apiVersion := claim.GetAPIVersion()
	if apiVersion == "" {
		return nil, errors.New("no apiVersion in Claim")
	}

	if _, err := schema.ParseGroupVersion(apiVersion); err != nil {
		return nil, errors.Wrap(err, "cannot parse Claim APIVersion")
	}

	annotations := claim.GetAnnotations()

	labels := claim.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}

	claimSpec, ok := claim.Object["spec"].(map[string]any)
	if !ok || claimSpec == nil {
		return nil, errors.New("no spec section in Claim")
	}

	// Create a new XR and pave it for manipulation
	xr := composite.New()

	xrPaved, err := fieldpath.PaveObject(xr)
	if err != nil {
		return nil, errors.Wrap(err, "cannot pave object")
	}

	if err := xrPaved.SetString("apiVersion", apiVersion); err != nil {
		return nil, errors.Wrap(err, "cannot set apiVersion")
	}

	// Set XR kind - either from opts or by prepending X to Claim's kind
	kind := opts.Kind
	if kind == "" {
		kind = "X" + claimKind
	}

	if err := xrPaved.SetString("kind", kind); err != nil {
		return nil, errors.Wrap(err, "cannot set kind")
	}

	if len(annotations) > 0 {
		if err := xrPaved.SetValue("metadata.annotations", annotations); err != nil {
			return nil, errors.Wrap(err, "cannot set annotations")
		}
	}

	if err := xrPaved.SetValue("spec", claimSpec); err != nil {
		return nil, errors.Wrap(err, "cannot set spec")
	}

	xrName := claimName

	if !opts.Direct {
		xrName = names.SimpleNameGenerator.GenerateName(claimName + "-")
		labels[labelClaimName] = claim.GetName()

		labels[labelClaimNamespace] = claim.GetNamespace()
		if err := xrPaved.SetValue("spec.claimRef", map[string]any{
			fieldAPIVersion: apiVersion,
			fieldKind:       claimKind,
			fieldName:       claimName,
			fieldNamespace:  claim.GetNamespace(),
		}); err != nil {
			return nil, errors.Wrap(err, "cannot set claimRef")
		}
	}

	// Explicit Name overrides both Direct's claim-name default and the generated suffix.
	if opts.Name != "" {
		xrName = opts.Name
	}

	if err := xrPaved.SetString("metadata.name", xrName); err != nil {
		return nil, errors.Wrap(err, "cannot set name")
	}

	if len(labels) > 0 {
		delete(labels, labelComposite)

		if err := xrPaved.SetValue("metadata.labels", labels); err != nil {
			return nil, errors.Wrap(err, "cannot set labels")
		}
	}

	if opts.GenerateUID {
		xr.SetUID(uuid.NewUUID())
	}

	return xr, nil
}
