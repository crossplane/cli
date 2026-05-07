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

// Package crd contains utilities for working with CRDs.
package crd

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/afero"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/controller/openapi/builder"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/kube-openapi/pkg/spec3"
	"k8s.io/kube-openapi/pkg/validation/spec"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

// ToOpenAPI converts the storage version of a CRD to an OpenAPI spec. The
// version is returned along with the OpenAPI spec.
func ToOpenAPI(crd *extv1.CustomResourceDefinition) (map[string]*spec3.OpenAPI, error) {
	modifyCRDManifestFields(crd)
	oapis := make(map[string]*spec3.OpenAPI, len(crd.Spec.Versions))

	if len(crd.Spec.Versions) == 0 {
		return nil, errors.New("crd has no versions")
	}

	for _, crdVersion := range crd.Spec.Versions {
		version := crdVersion.Name

		output, err := builder.BuildOpenAPIV3(crd, version, builder.Options{})
		if err != nil {
			return nil, errors.Wrapf(err, "failed to build OpenAPI v3 schema")
		}

		groupParts := strings.Split(crd.Spec.Group, ".")
		slices.Reverse(groupParts)
		reverseGroup := strings.Join(groupParts, ".")

		for name, s := range output.Components.Schemas {
			if !strings.HasPrefix(name, reverseGroup+".") {
				continue
			}

			if fmt.Sprintf("%s.%s.%s", reverseGroup, version, crd.Spec.Names.Kind) == name {
				addDefaultAPIVersionAndKind(s, schema.GroupVersionKind{
					Group:   crd.Spec.Group,
					Version: version,
					Kind:    crd.Spec.Names.Kind,
				})
			}
		}

		oapis[version] = output
	}

	return oapis, nil
}

// FilesToOpenAPI converts an on-disk CRD to an OpenAPI spec, and writes the
// OpenAPI spec to a file. The paths to the specs are returned.
func FilesToOpenAPI(fs afero.Fs, bs []byte, path string) ([]string, error) {
	var crd extv1.CustomResourceDefinition
	if err := yaml.Unmarshal(bs, &crd); err != nil {
		return nil, errors.Wrapf(err, "failed to unmarshal CRD file %q", path)
	}

	outputs, err := ToOpenAPI(&crd)
	if err != nil {
		return nil, err
	}

	paths := make([]string, 0, len(outputs))
	for version, output := range outputs {
		openAPIBytes, err := yaml.Marshal(output)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to marshal OpenAPI output to YAML")
		}

		groupFormatted := strings.ReplaceAll(crd.Spec.Group, ".", "_")
		kindFormatted := strings.ToLower(crd.Spec.Names.Kind)
		openAPIPath := fmt.Sprintf("%s_%s_%s.yaml", groupFormatted, version, kindFormatted)

		if err := afero.WriteFile(fs, openAPIPath, openAPIBytes, 0o644); err != nil {
			return nil, errors.Wrapf(err, "failed to write OpenAPI file")
		}

		paths = append(paths, openAPIPath)
	}

	return paths, nil
}

func addDefaultAPIVersionAndKind(s *spec.Schema, gvk schema.GroupVersionKind) {
	if prop, ok := s.Properties["apiVersion"]; ok {
		prop.Default = gvk.GroupVersion().String()
		prop.Enum = []any{gvk.GroupVersion().String()}
		s.Properties["apiVersion"] = prop
	}
	if prop, ok := s.Properties["kind"]; ok {
		prop.Default = gvk.Kind
		prop.Enum = []any{gvk.Kind}
		s.Properties["kind"] = prop
	}
}

func modifyCRDManifestFields(crd *extv1.CustomResourceDefinition) {
	for i, version := range crd.Spec.Versions {
		if version.Schema != nil && version.Schema.OpenAPIV3Schema != nil {
			updateSchemaPropertiesXEmbeddedResource(version.Schema.OpenAPIV3Schema)
			crd.Spec.Versions[i].Schema.OpenAPIV3Schema.Properties = version.Schema.OpenAPIV3Schema.Properties
		}
	}
}

// updateSchemaPropertiesXEmbeddedResource recursively traverses and updates
// schema properties at all depths.
func updateSchemaPropertiesXEmbeddedResource(s *extv1.JSONSchemaProps) {
	if s == nil {
		return
	}

	if s.XEmbeddedResource && s.XPreserveUnknownFields != nil && *s.XPreserveUnknownFields {
		s.XEmbeddedResource = false
		s.XPreserveUnknownFields = nil
		s.Type = "object"
		s.AdditionalProperties = &extv1.JSONSchemaPropsOrBool{
			Allows: true,
			Schema: nil,
		}
	}

	for key, property := range s.Properties {
		updateSchemaPropertiesXEmbeddedResource(&property)
		s.Properties[key] = property
	}

	if s.AdditionalProperties != nil && s.AdditionalProperties.Schema != nil {
		updateSchemaPropertiesXEmbeddedResource(s.AdditionalProperties.Schema)
	}

	if s.Items != nil {
		if s.Items.Schema != nil {
			updateSchemaPropertiesXEmbeddedResource(s.Items.Schema)
		} else if s.Items.JSONSchemas != nil {
			for i := range s.Items.JSONSchemas {
				updateSchemaPropertiesXEmbeddedResource(&s.Items.JSONSchemas[i])
			}
		}
	}
}
