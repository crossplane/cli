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

// Package xr provides library functions for working with XRs.
package xr

import (
	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	schema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	structuraldefaulting "k8s.io/apiextensions-apiserver/pkg/apiserver/schema/defaulting"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xcrd"

	apiextensionsv1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1"
)

// ApplyXRDDefaults applies default values from an XRD's openAPIV3Schema to an XR. The XR is mutated in place.
// This is the canonical XRD-defaulting entry point for the cli; downstream
// commands and tools (e.g. `crossplane render xr --xrd`) call into this
// function rather than re-implementing the schema-defaulting routine.
func ApplyXRDDefaults(xr *unstructured.Unstructured, xrdef *apiextensionsv1.CompositeResourceDefinition) error {
	crd, err := xcrd.ForCompositeResource(xrdef)
	if err != nil {
		return errors.Wrapf(err, "cannot derive CRD from XRD %q", xrdef.GetName())
	}

	return ApplyCRDDefaults(xr.UnstructuredContent(), xr.GetAPIVersion(), *crd)
}

// ApplyCRDDefaults sets default values on the XR based on the CRD schema.
// Callers starting from an XRD should prefer ApplyXRDDefaults; this is the
// lower-level routine for callers that already have a CRD in hand.
func ApplyCRDDefaults(xr map[string]any, apiVersion string, crd extv1.CustomResourceDefinition) error {
	var (
		k       apiextensions.JSONSchemaProps
		version *extv1.CustomResourceDefinitionVersion
	)

	for _, vr := range crd.Spec.Versions {
		checkAPIVersion := crd.Spec.Group + "/" + vr.Name
		if checkAPIVersion == apiVersion {
			version = &vr
			break
		}
	}

	if version == nil {
		return errors.Errorf("the specified API version '%s' does not exist in the XRD", apiVersion)
	}

	if err := extv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(version.Schema.OpenAPIV3Schema, &k, nil); err != nil {
		return err
	}

	crdWithDefaults, err := schema.NewStructural(&k)
	if err != nil {
		return err
	}

	structuraldefaulting.Default(xr, crdWithDefaults)

	return nil
}
