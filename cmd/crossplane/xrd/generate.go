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
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/gobuffalo/flect"
	"github.com/kubernetes-sigs/kro/pkg/simpleschema"
	"github.com/spf13/afero"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	v2 "github.com/crossplane/crossplane/apis/v2/apiextensions/v2"

	"github.com/crossplane/cli/v2/internal/project/projectfile"
	"github.com/crossplane/cli/v2/internal/xrd"
)

type generateCmd struct {
	File        string `arg:""                                        help:"Path to the XR or XRC YAML file."`
	From        string `default:"xr"                                  enum:"xr,simpleschema"                  help:"Input format: xr or simpleschema."`
	Path        string `help:"Output path within the APIs directory." optional:""`
	Plural      string `help:"Custom plural form for the XRD."        optional:""`
	ProjectFile string `default:"crossplane-project.yaml"             help:"Path to project definition."      short:"f"`

	projFS  afero.Fs
	apisFS  afero.Fs
	relFile string
}

// AfterApply sets up the project filesystem.
func (c *generateCmd) AfterApply() error {
	projFilePath, err := filepath.Abs(c.ProjectFile)
	if err != nil {
		return err
	}
	projDirPath := filepath.Dir(projFilePath)
	c.projFS = afero.NewBasePathFs(afero.NewOsFs(), projDirPath)

	proj, err := projectfile.Parse(c.projFS, filepath.Base(c.ProjectFile))
	if err != nil {
		return err
	}

	c.apisFS = afero.NewBasePathFs(c.projFS, proj.Spec.Paths.APIs)

	c.relFile = c.File
	if filepath.IsAbs(c.File) {
		relPath, err := filepath.Rel(afero.FullBaseFsPath(c.projFS.(*afero.BasePathFs), "."), c.File) //nolint:forcetypeassert // We know the type of projFS from above.
		if err != nil {
			return errors.Wrap(err, "failed to make file path relative to project filesystem")
		}
		if strings.HasPrefix(relPath, "..") || filepath.IsAbs(relPath) {
			return errors.New("file path is outside the project filesystem")
		}
		c.relFile = relPath
	}

	return nil
}

func (c *generateCmd) Run(k *kong.Context) error {
	yamlData, err := afero.ReadFile(c.projFS, c.relFile)
	if err != nil {
		return errors.Wrapf(err, "failed to read file %s", c.relFile)
	}

	var xrdObj *v2.CompositeResourceDefinition
	switch c.From {
	case "simpleschema":
		xrdObj, err = newXRDFromSimpleSchema(yamlData, c.Plural)
	default:
		xrdObj, err = newXRDFromExample(yamlData, c.Plural)
	}
	if err != nil {
		return errors.Wrap(err, "failed to create CompositeResourceDefinition (XRD)")
	}

	pluralName := xrdObj.Spec.Names.Plural

	xrdYAML, err := marshalXRD(xrdObj)
	if err != nil {
		return errors.Wrap(err, "failed to marshal XRD to YAML")
	}

	filePath := c.Path
	if filePath == "" {
		filePath = fmt.Sprintf("%s/definition.yaml", pluralName)
	}

	exists, err := afero.Exists(c.apisFS, filePath)
	if err != nil {
		return errors.Wrap(err, "failed to check if file exists")
	}
	if exists {
		return errors.Errorf("file %q already exists, use --path to specify a different output path or delete the existing file", filePath)
	}

	if err := c.apisFS.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return errors.Wrap(err, "failed to create directories for the specified output path")
	}

	if err := afero.WriteFile(c.apisFS, filePath, xrdYAML, 0o644); err != nil {
		return errors.Wrap(err, "failed to write CompositeResourceDefinition (XRD) to file")
	}

	_, err = fmt.Fprintf(k.Stdout, "Created CompositeResourceDefinition (XRD) at %s\n", filePath)
	return err
}

// marshalXRD marshals an XRD to YAML, removing creationTimestamp and status.
func marshalXRD(obj any) ([]byte, error) {
	unst, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, err
	}

	unstructured.RemoveNestedField(unst, "status")
	unstructured.RemoveNestedField(unst, "metadata", "creationTimestamp")

	return yaml.Marshal(unst)
}

func isCELExpression(value any) bool {
	if str, ok := value.(string); ok {
		return strings.HasPrefix(str, "${") && strings.HasSuffix(str, "}")
	}
	return false
}

// celFieldPath tracks paths to fields containing CEL expressions.
type celFieldPath []string

// findCELFields recursively finds all field paths that contain CEL expressions.
func findCELFields(data map[string]any, currentPath []string) []celFieldPath {
	var paths []celFieldPath

	for key, value := range data {
		fieldPath := make([]string, len(currentPath), len(currentPath)+1)
		copy(fieldPath, currentPath)
		fieldPath = append(fieldPath, key)

		if isCELExpression(value) {
			paths = append(paths, celFieldPath(fieldPath))
		} else if nestedMap, ok := value.(map[string]any); ok {
			paths = append(paths, findCELFields(nestedMap, fieldPath)...)
		}
	}

	return paths
}

// replaceCELWithPlaceholder replaces CEL expressions with "object" placeholder for simpleschema processing.
func replaceCELWithPlaceholder(data map[string]any) map[string]any {
	result := make(map[string]any)

	for key, value := range data {
		if isCELExpression(value) {
			result[key] = "object"
		} else if nestedMap, ok := value.(map[string]any); ok {
			result[key] = replaceCELWithPlaceholder(nestedMap)
		} else {
			result[key] = value
		}
	}

	return result
}

// markCELFieldsPreserveUnknown marks fields at the given paths with x-kubernetes-preserve-unknown-fields: true.
func markCELFieldsPreserveUnknown(schema *extv1.JSONSchemaProps, paths []celFieldPath) {
	if schema == nil || len(paths) == 0 {
		return
	}

	preserveTrue := true

	for _, path := range paths {
		current := schema
		for i, key := range path {
			if current.Properties == nil {
				break
			}

			if prop, exists := current.Properties[key]; exists {
				if i == len(path)-1 {
					prop.XPreserveUnknownFields = &preserveTrue
					prop.Type = ""
					prop.Properties = nil
					current.Properties[key] = prop
				} else {
					current = &prop
				}
			}
		}
	}
}

// newXRDFromSimpleSchema creates a new CompositeResourceDefinition v2 from a SimpleSchema definition.
func newXRDFromSimpleSchema(yamlData []byte, customPlural string) (*v2.CompositeResourceDefinition, error) {
	var simpleInput inputXR
	if err := yaml.Unmarshal(yamlData, &simpleInput); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal YAML")
	}

	gv, err := schema.ParseGroupVersion(simpleInput.APIVersion)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse API version")
	}

	kind := simpleInput.Kind
	plural := customPlural
	if plural == "" {
		plural = flect.Pluralize(kind)
	}

	specSchema, err := simpleschema.ToOpenAPISpec(simpleInput.Spec, nil)
	if err != nil {
		return nil, errors.Wrap(err, "failed to convert spec to OpenAPI schema")
	}

	statusSchema := &extv1.JSONSchemaProps{Type: "object", Properties: map[string]extv1.JSONSchemaProps{}}
	if len(simpleInput.Status) > 0 {
		celPaths := findCELFields(simpleInput.Status, nil)
		processedStatus := replaceCELWithPlaceholder(simpleInput.Status)

		statusSchema, err = simpleschema.ToOpenAPISpec(processedStatus, nil)
		if err != nil {
			return nil, errors.Wrap(err, "failed to convert status to OpenAPI schema")
		}

		markCELFieldsPreserveUnknown(statusSchema, celPaths)
	}

	openAPIV3Schema := &extv1.JSONSchemaProps{
		Description: fmt.Sprintf("%s is the Schema for the %s API.", kind, kind),
		Type:        "object",
		Properties: map[string]extv1.JSONSchemaProps{
			"spec":   *specSchema,
			"status": *statusSchema,
		},
		Required: []string{"spec"},
	}

	schemaBytes, err := json.Marshal(openAPIV3Schema)
	if err != nil {
		return nil, errors.Wrap(err, "failed to marshal OpenAPI schema")
	}

	return &v2.CompositeResourceDefinition{
		TypeMeta: metav1.TypeMeta{
			APIVersion: v2.CompositeResourceDefinitionGroupVersionKind.GroupVersion().String(),
			Kind:       v2.CompositeResourceDefinitionGroupVersionKind.Kind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: strings.ToLower(fmt.Sprintf("%s.%s", plural, gv.Group)),
		},
		Spec: v2.CompositeResourceDefinitionSpec{
			Group: gv.Group,
			Scope: v2.CompositeResourceScopeNamespaced,
			Names: extv1.CustomResourceDefinitionNames{
				Categories: []string{"crossplane"},
				Kind:       flect.Capitalize(kind),
				Plural:     strings.ToLower(plural),
			},
			Versions: []v2.CompositeResourceDefinitionVersion{
				{
					AdditionalPrinterColumns: simpleInput.AdditionalPrinterColumns,
					Name:                     gv.Version,
					Referenceable:            true,
					Served:                   true,
					Schema: &v2.CompositeResourceValidation{
						OpenAPIV3Schema: runtime.RawExtension{Raw: schemaBytes},
					},
				},
			},
		},
	}, nil
}

type inputXR struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`

	Spec                     map[string]any                         `json:"spec"`
	Status                   map[string]any                         `json:"status"`
	AdditionalPrinterColumns []extv1.CustomResourceColumnDefinition `json:"additionalPrinterColumns"`
}

// newXRDFromExample creates an XRD based on an example XR, ineferring property types
// heuristically based on the property values in the example.
func newXRDFromExample(yamlData []byte, customPlural string) (*v2.CompositeResourceDefinition, error) {
	var topLevelKeys map[string]any
	if err := yaml.Unmarshal(yamlData, &topLevelKeys); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal YAML to check top-level keys")
	}
	for key := range topLevelKeys {
		allowedKeys := []string{"apiVersion", "kind", "metadata", "spec", "status", "additionalPrinterColumns"}
		if !slices.Contains(allowedKeys, key) {
			return nil, errors.Errorf("invalid manifest: valid top-level keys are: %v", allowedKeys)
		}
	}

	var input inputXR
	if err := yaml.Unmarshal(yamlData, &input); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal YAML")
	}

	if input.APIVersion == "" {
		return nil, errors.New("invalid manifest: apiVersion is required")
	}
	if strings.Count(input.APIVersion, "/") != 1 {
		return nil, errors.New("invalid manifest: apiVersion must be in the format group/version")
	}
	if input.Kind == "" {
		return nil, errors.New("invalid manifest: kind is required")
	}
	if input.Name == "" {
		return nil, errors.New("invalid manifest: metadata.name is required")
	}
	if input.Spec == nil {
		return nil, errors.New("invalid manifest: spec is required")
	}

	fieldsToRemove := []string{
		"resourceRefs",
		"writeConnectionSecretToRef",
		"publishConnectionDetailsTo",
		"environmentConfigRefs",
		"compositionUpdatePolicy",
		"compositionRevisionRef",
		"compositionRevisionSelector",
		"compositionRef",
		"compositionSelector",
		"claimRef",
	}
	for _, field := range fieldsToRemove {
		delete(input.Spec, field)
	}

	gv, err := schema.ParseGroupVersion(input.APIVersion)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse API version")
	}

	kind := input.Kind

	plural := customPlural
	if plural == "" {
		plural = flect.Pluralize(kind)
	}

	description := fmt.Sprintf("%s is the Schema for the %s API.", kind, kind)

	specProps, err := xrd.InferProperties(input.Spec)
	if err != nil {
		return nil, errors.Wrap(err, "failed to infer properties for spec")
	}

	statusProps, err := xrd.InferProperties(input.Status)
	if err != nil {
		return nil, errors.Wrap(err, "failed to infer properties for status")
	}

	openAPIV3Schema := &extv1.JSONSchemaProps{
		Description: description,
		Type:        "object",
		Properties: map[string]extv1.JSONSchemaProps{
			"spec": {
				Description: fmt.Sprintf("%sSpec defines the desired state of %s.", kind, kind),
				Type:        "object",
				Properties:  specProps,
			},
			"status": {
				Description: fmt.Sprintf("%sStatus defines the observed state of %s.", kind, kind),
				Type:        "object",
				Properties:  statusProps,
			},
		},
		Required: []string{"spec"},
	}

	schemaBytes, err := json.Marshal(openAPIV3Schema)
	if err != nil {
		return nil, errors.Wrap(err, "failed to marshal OpenAPI v3 schema")
	}

	scope := v2.CompositeResourceScopeCluster
	if input.Namespace != "" {
		scope = v2.CompositeResourceScopeNamespaced
	}

	return &v2.CompositeResourceDefinition{
		TypeMeta: metav1.TypeMeta{
			APIVersion: v2.CompositeResourceDefinitionGroupVersionKind.GroupVersion().String(),
			Kind:       v2.CompositeResourceDefinitionGroupVersionKind.Kind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: strings.ToLower(fmt.Sprintf("%s.%s", plural, gv.Group)),
		},
		Spec: v2.CompositeResourceDefinitionSpec{
			Group: gv.Group,
			Scope: scope,
			Names: extv1.CustomResourceDefinitionNames{
				Categories: []string{"crossplane"},
				Kind:       flect.Capitalize(kind),
				Plural:     strings.ToLower(plural),
			},
			Versions: []v2.CompositeResourceDefinitionVersion{
				{
					Name:          gv.Version,
					Referenceable: true,
					Served:        true,
					Schema: &v2.CompositeResourceValidation{
						OpenAPIV3Schema: runtime.RawExtension{Raw: schemaBytes},
					},
				},
			},
		},
	}, nil
}
