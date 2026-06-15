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
	"math"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

const (
	schemaTypeArray   = "array"
	schemaTypeBoolean = "boolean"
	schemaTypeInteger = "integer"
	schemaTypeNumber  = "number"
	schemaTypeObject  = "object"
	schemaTypeString  = "string"
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
			Type: schemaTypeArray,
			Items: &extv1.JSONSchemaPropsOrArray{
				Schema: &extv1.JSONSchemaProps{
					Type: schemaTypeObject,
				},
			},
		}, nil
	}

	firstElemSchema, err := inferProperty(v[0])
	if err != nil {
		return extv1.JSONSchemaProps{}, err
	}

	mergedProperties := make(map[string]extv1.JSONSchemaProps)
	if firstElemSchema.Type == schemaTypeObject {
		maps.Copy(mergedProperties, firstElemSchema.Properties)
	}

	for _, elem := range v {
		elemSchema, err := inferProperty(elem)
		if err != nil {
			return extv1.JSONSchemaProps{}, err
		}

		// If an array contains a mix of numbers and integers, use number as the
		// array type.
		if elemSchema.Type == schemaTypeInteger && firstElemSchema.Type == schemaTypeNumber {
			continue
		}
		if elemSchema.Type == schemaTypeNumber && firstElemSchema.Type == schemaTypeInteger {
			firstElemSchema.Type = schemaTypeNumber
		}

		if elemSchema.Type != firstElemSchema.Type {
			return extv1.JSONSchemaProps{}, errors.New("mixed types detected in array")
		}
		if elemSchema.Type == schemaTypeObject {
			maps.Copy(mergedProperties, elemSchema.Properties)
		}
	}

	resultSchema := firstElemSchema
	if firstElemSchema.Type == schemaTypeObject && len(mergedProperties) > 0 {
		resultSchema.Properties = mergedProperties
	}

	return extv1.JSONSchemaProps{
		Type: schemaTypeArray,
		Items: &extv1.JSONSchemaPropsOrArray{
			Schema: &resultSchema,
		},
	}, nil
}

func inferProperty(value any) (extv1.JSONSchemaProps, error) {
	if value == nil {
		return extv1.JSONSchemaProps{
			Type: schemaTypeString,
		}, nil
	}

	switch v := value.(type) {
	case string:
		return extv1.JSONSchemaProps{
			Type: schemaTypeString,
		}, nil
	case int, int32, int64:
		return extv1.JSONSchemaProps{
			Type: schemaTypeInteger,
		}, nil
	case float32:
		// JSON doesn't have integers, so json.Unmarshal treats all numbers as
		// floats. Try to detect whehter the number is actually an integer, so
		// that we're more likely to infer the user's intent. This heuristic
		// isn't perfect since not all integers are representable as floats, but
		// it will work for common cases.
		t := schemaTypeNumber
		if math.Trunc(float64(v)) == float64(v) {
			t = schemaTypeInteger
		}

		return extv1.JSONSchemaProps{
			Type: t,
		}, nil
	case float64:
		t := schemaTypeNumber
		if math.Trunc(v) == v {
			t = schemaTypeInteger
		}

		return extv1.JSONSchemaProps{
			Type: t,
		}, nil
	case bool:
		return extv1.JSONSchemaProps{
			Type: schemaTypeBoolean,
		}, nil
	case map[string]any:
		inferredProps, err := InferProperties(v)
		if err != nil {
			return extv1.JSONSchemaProps{}, errors.Wrap(err, "error inferring properties for object")
		}
		return extv1.JSONSchemaProps{
			Type:       schemaTypeObject,
			Properties: inferredProps,
		}, nil
	case []any:
		return inferArrayProperty(v)
	default:
		return extv1.JSONSchemaProps{}, errors.Errorf("unknown type: %T", value)
	}
}
