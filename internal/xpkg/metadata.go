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

package xpkg

import (
	"fmt"

	"github.com/spf13/afero"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xcrd"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xpkg/parser"

	xrdv1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1"
	xrdv2 "github.com/crossplane/crossplane/apis/v2/apiextensions/v2"
)

// CRDFilesystem writes each CRD object in the package to a separate
// YAML file in an in-memory filesystem. Files are named
// <plural>.<group>.yaml so the schema generator sees per-CRD inputs.
// CompositeResourceDefinitions (XRDs) are converted to the
// CustomResourceDefinition(s) Crossplane derives from them - the composite
// resource CRD, and the claim CRD if the XRD offers one. Any other object in
// the package is skipped.
func CRDFilesystem(pkg *parser.Package) (afero.Fs, error) {
	fs := afero.NewMemMapFs()
	for _, obj := range pkg.GetObjects() {
		docs, err := crdDocuments(obj)
		if err != nil {
			return nil, err
		}
		for _, d := range docs {
			bs, err := yaml.Marshal(d.obj)
			if err != nil {
				return nil, errors.Wrapf(err, "cannot marshal CRD %s", d.name)
			}
			if err := afero.WriteFile(fs, d.name, bs, 0o644); err != nil {
				return nil, errors.Wrapf(err, "cannot write CRD %s", d.name)
			}
		}
	}
	return fs, nil
}

// crdDocument is a single CRD YAML file to write to the output filesystem,
// and the name to write it under.
type crdDocument struct {
	name string
	obj  runtime.Object
}

// crdDocuments returns the CRD documents obj represents: itself, if it's
// already a CustomResourceDefinition, or the CRD(s) Crossplane derives from
// it, if it's a CompositeResourceDefinition. It returns no documents (and no
// error) for any other kind of object.
func crdDocuments(obj runtime.Object) ([]crdDocument, error) {
	switch c := obj.(type) {
	case *apiextv1.CustomResourceDefinition:
		return []crdDocument{{name: crdFilename(c.Spec.Names.Plural, c.Spec.Group), obj: c}}, nil
	case *apiextv1beta1.CustomResourceDefinition:
		return []crdDocument{{name: crdFilename(c.Spec.Names.Plural, c.Spec.Group), obj: c}}, nil
	case *xrdv1.CompositeResourceDefinition:
		return xrdCRDDocuments(c)
	case *xrdv2.CompositeResourceDefinition:
		v1XRD, err := convertXRDv2ToV1(c)
		if err != nil {
			return nil, errors.Wrapf(err, "cannot convert XRD %s", c.GetName())
		}
		return xrdCRDDocuments(v1XRD)
	default:
		return nil, nil
	}
}

// xrdCRDDocuments derives the CustomResourceDefinition(s) Crossplane
// generates for an XRD: the composite resource CRD, and the claim CRD if
// the XRD offers one.
func xrdCRDDocuments(xrd *xrdv1.CompositeResourceDefinition) ([]crdDocument, error) {
	xr, err := xcrd.ForCompositeResource(xrd)
	if err != nil {
		return nil, errors.Wrapf(err, "cannot derive composite CRD from XRD %s", xrd.GetName())
	}
	setCRDTypeMeta(xr)
	docs := []crdDocument{{name: crdFilename(xr.Spec.Names.Plural, xr.Spec.Group), obj: xr}}

	if xrd.OffersClaim() {
		claim, err := xcrd.ForCompositeResourceClaim(xrd)
		if err != nil {
			return nil, errors.Wrapf(err, "cannot derive claim CRD from XRD %s", xrd.GetName())
		}
		setCRDTypeMeta(claim)
		docs = append(docs, crdDocument{name: crdFilename(claim.Spec.Names.Plural, claim.Spec.Group), obj: claim})
	}
	return docs, nil
}

// setCRDTypeMeta sets apiVersion/kind on a CRD derived via xcrd.ForCompositeResource
// or xcrd.ForCompositeResourceClaim, neither of which populates TypeMeta.
// Downstream consumers of CRDFilesystem's output (e.g. the schema
// generators) identify CRD YAML documents by their apiVersion/kind, the same
// way cmd/crossplane/xrd/convert.go's setTypeMeta does for its own derived
// CRDs.
func setCRDTypeMeta(crd *apiextv1.CustomResourceDefinition) {
	crd.APIVersion = apiextv1.SchemeGroupVersion.String()
	crd.Kind = "CustomResourceDefinition"
}

// convertXRDv2ToV1 converts an apiextensions.crossplane.io/v2 XRD to the v1
// shape xcrd.ForCompositeResource requires. The v2 spec is a same-named-field
// subset of the v1 spec (v2 dropped claim support), so a YAML round trip is a
// safe, lossless-for-this-purpose conversion - the same technique used
// elsewhere in this repo (cmd/crossplane/xrd/convert.go, validate/manager.go)
// to interpret an XRD payload against the v1 struct.
func convertXRDv2ToV1(xrd *xrdv2.CompositeResourceDefinition) (*xrdv1.CompositeResourceDefinition, error) {
	bs, err := yaml.Marshal(xrd)
	if err != nil {
		return nil, errors.Wrap(err, "cannot marshal v2 XRD")
	}
	v1XRD := &xrdv1.CompositeResourceDefinition{}
	if err := yaml.Unmarshal(bs, v1XRD); err != nil {
		return nil, errors.Wrap(err, "cannot unmarshal v2 XRD as v1")
	}
	return v1XRD, nil
}

func crdFilename(plural, group string) string {
	return fmt.Sprintf("%s.%s.yaml", plural, group)
}
