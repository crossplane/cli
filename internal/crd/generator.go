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

package crd

import (
	"path/filepath"

	"github.com/spf13/afero"
	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xcrd"

	xpv1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1"
)

// createCRDFromXRD creates a xrCRD and claimCRD if possible from the XRD.
func createCRDFromXRD(xrd xpv1.CompositeResourceDefinition) (*apiextensionsv1.CustomResourceDefinition, *apiextensionsv1.CustomResourceDefinition, error) {
	var xrCrd, claimCrd *apiextensionsv1.CustomResourceDefinition

	crdGVK := apiextensionsv1.SchemeGroupVersion.WithKind("CustomResourceDefinition")

	xrCrd, err := xcrd.ForCompositeResource(&xrd)
	if err != nil {
		return nil, nil, errors.Wrapf(err, "cannot derive composite CRD from XRD %q for Composite Resource", xrd.GetName())
	}
	xrCrd.SetGroupVersionKind(crdGVK)
	if xrCrd.Spec.Names.ListKind == "" {
		xrCrd.Spec.Names.ListKind = xrCrd.Spec.Names.Kind + "List"
	}

	if err := validateStructural(xrCrd); err != nil {
		return nil, nil, errors.Wrapf(err, "composite CRD derived from XRD %q has an unusable schema", xrd.GetName())
	}

	if xrd.Spec.ClaimNames != nil {
		claimCrd, err = xcrd.ForCompositeResourceClaim(&xrd)
		if err != nil {
			return nil, nil, errors.Wrapf(err, "cannot derive composite CRD from XRD %q for Composite Resource Claim", xrd.GetName())
		}

		claimCrd.SetGroupVersionKind(crdGVK)
		if claimCrd.Spec.Names.ListKind == "" {
			claimCrd.Spec.Names.ListKind = claimCrd.Spec.Names.Kind + "List"
		}
	}

	return xrCrd, claimCrd, nil
}

// validateStructural reports schemas that Kubernetes would not accept as
// structural. Such a schema is silently reduced to an empty object when it is
// converted to OpenAPI, which leaves the generated language types with no
// fields at all rather than with the one bad field missing, so it is worth
// stopping on rather than passing through.
func validateStructural(crd *apiextensionsv1.CustomResourceDefinition) error {
	for _, ver := range crd.Spec.Versions {
		if ver.Schema == nil || ver.Schema.OpenAPIV3Schema == nil {
			continue
		}

		internal := &apiextensions.JSONSchemaProps{}
		if err := apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(ver.Schema.OpenAPIV3Schema, internal, nil); err != nil {
			return errors.Wrapf(err, "cannot read the schema of version %q", ver.Name)
		}

		s, err := structuralschema.NewStructural(internal)
		if err != nil {
			return errors.Wrapf(err, "version %q", ver.Name)
		}

		if err := structuralschema.ValidateStructural(nil, s).ToAggregate(); err != nil {
			return errors.Wrapf(err, "version %q", ver.Name)
		}
	}

	return nil
}

// ProcessXRD generates associated CRDs from an XRD.
func ProcessXRD(fs afero.Fs, bs []byte, path, baseFolder string) (string, string, error) {
	var xrd xpv1.CompositeResourceDefinition
	if err := yaml.Unmarshal(bs, &xrd); err != nil {
		return "", "", errors.Wrapf(err, "failed to unmarshal XRD file %q", path)
	}

	xrCRD, claimCRD, err := createCRDFromXRD(xrd)
	if err != nil {
		return "", "", err
	}

	var xrPath, claimPath string

	if xrCRD != nil {
		xrPath = filepath.Join(baseFolder, xrCRD.Name+".yaml")
		xrCRDBytes, err := yaml.Marshal(xrCRD)
		if err != nil {
			return "", "", errors.Wrap(err, "failed to marshal XR CRD to YAML")
		}
		if err := afero.WriteFile(fs, xrPath, xrCRDBytes, 0o644); err != nil {
			return "", "", err
		}
	}

	if claimCRD != nil {
		claimPath = filepath.Join(baseFolder, claimCRD.Name+".yaml")
		claimCRDBytes, err := yaml.Marshal(claimCRD)
		if err != nil {
			return "", "", errors.Wrap(err, "failed to marshal claim CRD to YAML")
		}
		if err := afero.WriteFile(fs, claimPath, claimCRDBytes, 0o644); err != nil {
			return "", "", err
		}
	}

	return xrPath, claimPath, nil
}
