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

// Package xrd contains utilities for working with CompositeResourceDefinitions.
package xrd

import (
	"fmt"
	"maps"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

// InferProperties infers JSON schema properties from a map of values.
func InferProperties(spec map[string]any) (map[string]extv1.JSONSchemaProps, error) {
	properties := make(map[string]extv1.JSONSchemaProps)

	for key, value := range spec {
		strKey := fmt.Sprintf("%v", key)
		inferredProp, err := inferProperty(value)
		if err != nil {
			return nil, errors.Wrapf(err, "error inferring property for key '%s'", strKey)
		}
		properties[strKey] = inferredProp
	}

	return properties, nil
}

// inferArrayProperty handles array type inference with property merging for objects.
func inferArrayProperty(v []any) (extv1.JSONSchemaProps, error) {
	if len(v) == 0 {
		return extv1.JSONSchemaProps{
			Type: "array",
			Items: &extv1.JSONSchemaPropsOrArray{
				Schema: &extv1.JSONSchemaProps{
					Type: "object",
				},
			},
		}, nil
	}

	firstElemSchema, err := inferProperty(v[0])
	if err != nil {
		return extv1.JSONSchemaProps{}, err
	}

	mergedProperties := make(map[string]extv1.JSONSchemaProps)
	if firstElemSchema.Type == "object" {
		maps.Copy(mergedProperties, firstElemSchema.Properties)
	}

	for _, elem := range v {
		elemSchema, err := inferProperty(elem)
		if err != nil {
			return extv1.JSONSchemaProps{}, err
		}
		if elemSchema.Type != firstElemSchema.Type {
			return extv1.JSONSchemaProps{}, errors.New("mixed types detected in array")
		}
		if elemSchema.Type == "object" {
			maps.Copy(mergedProperties, elemSchema.Properties)
		}
	}

	resultSchema := firstElemSchema
	if firstElemSchema.Type == "object" && len(mergedProperties) > 0 {
		resultSchema.Properties = mergedProperties
	}

	return extv1.JSONSchemaProps{
		Type: "array",
		Items: &extv1.JSONSchemaPropsOrArray{
			Schema: &resultSchema,
		},
	}, nil
}

func inferProperty(value any) (extv1.JSONSchemaProps, error) {
	if value == nil {
		return extv1.JSONSchemaProps{
			Type: "string",
		}, nil
	}

	switch v := value.(type) {
	case string:
		return extv1.JSONSchemaProps{
			Type: "string",
		}, nil
	case int, int32, int64:
		return extv1.JSONSchemaProps{
			Type: "integer",
		}, nil
	case float32, float64:
		return extv1.JSONSchemaProps{
			Type: "number",
		}, nil
	case bool:
		return extv1.JSONSchemaProps{
			Type: "boolean",
		}, nil
	case map[string]any:
		inferredProps, err := InferProperties(v)
		if err != nil {
			return extv1.JSONSchemaProps{}, errors.Wrap(err, "error inferring properties for object")
		}
		return extv1.JSONSchemaProps{
			Type:       "object",
			Properties: inferredProps,
		}, nil
	case []any:
		return inferArrayProperty(v)
	default:
		return extv1.JSONSchemaProps{}, errors.Errorf("unknown type: %T", value)
	}
}
