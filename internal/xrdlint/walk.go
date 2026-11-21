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

package xrdlint

import (
	"encoding/json"
	"slices"

	"gopkg.in/yaml.v3"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

// anchors are the YAML nodes on the path from the document to a schema node.
// A disable comment on any of them covers the schema node.
type anchors []*yaml.Node

func (a anchors) with(n ...*yaml.Node) anchors {
	out := make(anchors, 0, len(a)+len(n))
	out = append(out, a...)
	for _, node := range n {
		if node != nil {
			out = append(out, node)
		}
	}
	return out
}

// A Property is one node of a served version's openAPIV3Schema.
type Property struct {
	// Version is the name of the XRD version the property belongs to.
	Version string

	// Path is the property's data path: property names joined by dots from
	// the schema root, with [*] for array items and .* for the values of an
	// additionalProperties map, for example spec.subnets[*].cidr. The schema
	// root has the path "".
	Path string

	// Schema is the property's schema, including everything beneath it.
	Schema *extv1.JSONSchemaProps

	// InStatus is true for status and everything beneath it.
	InStatus bool

	// Required is true if the property's parent lists it as required.
	Required bool

	// node is the YAML node that positions the property: the key that
	// introduces it.
	node    *yaml.Node
	anchors anchors
}

// Line returns the line the property starts on.
func (p Property) Line() int {
	l, _ := p.position()
	return l
}

func (p Property) position() (int, int) {
	if p.node == nil {
		return 0, 0
	}
	return p.node.Line, p.node.Column
}

// Walk calls fn for every property of every served version's schema, parents
// before children and in the order they appear in the YAML. Walk stops at the
// first error fn returns.
func (x *XRD) Walk(fn func(p Property) error) error {
	specKey, specVal := mapEntry(x.root(), "spec")
	versionsKey, versions := mapEntry(specVal, "versions")

	for i, v := range x.Object.Spec.Versions {
		if !v.Served || v.Schema == nil || len(v.Schema.OpenAPIV3Schema.Raw) == 0 {
			continue
		}
		s := &extv1.JSONSchemaProps{}
		if err := json.Unmarshal(v.Schema.OpenAPIV3Schema.Raw, s); err != nil {
			return errors.Wrapf(err, "cannot decode the schema of version %q", v.Name)
		}

		var item *yaml.Node
		if versions != nil && versions.Kind == yaml.SequenceNode && i < len(versions.Content) {
			item = versions.Content[i]
		}
		schemaKey, schemaVal := mapEntry(item, "schema")
		rootKey, rootVal := mapEntry(schemaVal, "openAPIV3Schema")

		a := x.rootAnchors().with(specKey, versionsKey, item, schemaKey, rootKey)

		w := walker{version: v.Name, fn: fn}
		if err := w.walk(s, firstNonNil(rootKey, item), rootVal, a, "", false, false); err != nil {
			return err
		}
	}
	return nil
}

type walker struct {
	version string
	fn      func(p Property) error
}

// walk visits s, positioned at key, whose YAML value is val.
func (w walker) walk(s *extv1.JSONSchemaProps, key, val *yaml.Node, a anchors, path string, inStatus, required bool) error {
	if err := w.fn(Property{Version: w.version, Path: path, Schema: s, InStatus: inStatus, Required: required, node: key, anchors: a}); err != nil {
		return err
	}

	propsKey, propsVal := mapEntry(val, "properties")
	for _, name := range propertyOrder(s, propsVal) {
		child := s.Properties[name]
		k, v := mapEntry(propsVal, name)
		childPath := name
		if path != "" {
			childPath = path + "." + name
		}
		childInStatus := inStatus || (path == "" && name == "status")
		if err := w.walk(&child, firstNonNil(k, key), v, a.with(propsKey, k), childPath, childInStatus, slices.Contains(s.Required, name)); err != nil {
			return err
		}
	}

	if s.Items != nil && s.Items.Schema != nil {
		k, v := mapEntry(val, "items")
		if err := w.walk(s.Items.Schema, firstNonNil(k, key), v, a.with(k), path+"[*]", inStatus, false); err != nil {
			return err
		}
	}

	if s.AdditionalProperties != nil && s.AdditionalProperties.Schema != nil {
		k, v := mapEntry(val, "additionalProperties")
		if err := w.walk(s.AdditionalProperties.Schema, firstNonNil(k, key), v, a.with(k), path+".*", inStatus, false); err != nil {
			return err
		}
	}
	return nil
}

// propertyOrder returns the names of s's properties in the order the YAML
// lists them, followed by any the YAML does not show, sorted.
func propertyOrder(s *extv1.JSONSchemaProps, props *yaml.Node) []string {
	names := make([]string, 0, len(s.Properties))
	seen := map[string]bool{}
	if props != nil && props.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(props.Content); i += 2 {
			n := props.Content[i].Value
			if _, ok := s.Properties[n]; ok && !seen[n] {
				names = append(names, n)
				seen[n] = true
			}
		}
	}
	var rest []string
	for n := range s.Properties {
		if !seen[n] {
			rest = append(rest, n)
		}
	}
	slices.Sort(rest)
	return append(names, rest...)
}

func firstNonNil(nodes ...*yaml.Node) *yaml.Node {
	for _, n := range nodes {
		if n != nil {
			return n
		}
	}
	return nil
}
