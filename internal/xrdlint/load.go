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
	"io"

	"gopkg.in/yaml.v3"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	xpv1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1"

	"github.com/crossplane/cli/v2/pkg/load"
)

// StdinName is the file name findings use for input read from stdin.
const StdinName = "<stdin>"

const xrdKind = "CompositeResourceDefinition"

// isXRDAPIVersion reports whether v is an XRD API version lint reads. Both
// have the same shape, so both decode into the v1 Go type.
func isXRDAPIVersion(v string) bool {
	switch v {
	case "apiextensions.crossplane.io/v1", "apiextensions.crossplane.io/v2":
		return true
	default:
		return false
	}
}

// An XRD is a CompositeResourceDefinition together with the YAML it was
// parsed from, which supplies line numbers and disable comments.
type XRD struct {
	// File is the file the XRD was read from.
	File string
	// Name is the XRD's metadata.name.
	Name string
	// Object is the decoded XRD.
	Object *xpv1.CompositeResourceDefinition

	doc *yaml.Node
}

// Load reads XRDs from files, directories and, for the path "-", stdin,
// through pkg/load's document loader. Each path may be a comma-separated list,
// as elsewhere in the CLI. Directories are searched recursively for .yaml and
// .yml files. The path "-" reads stdin, or os.Stdin if stdin is nil.
// Documents that are not XRDs are skipped.
func Load(paths []string, stdin io.Reader) ([]*XRD, error) {
	var out []*XRD
	for _, p := range paths {
		l, err := load.NewDocumentLoader(p, load.WithStdin(stdin))
		if err != nil {
			return nil, errors.Wrapf(err, "cannot read %q", p)
		}
		docs, err := l.Load()
		if err != nil {
			return nil, errors.Wrapf(err, "cannot read %q", p)
		}
		for _, d := range docs {
			x, err := parse(d)
			if err != nil {
				return nil, err
			}
			if x != nil {
				out = append(out, x)
			}
		}
	}
	return out, nil
}

// parse decodes one document, with line numbers counted from the start of
// its source rather than of the document.
func parse(d load.Document) (*XRD, error) {
	file := d.Source
	if file == load.StdinSource {
		file = StdinName
	}

	doc := &yaml.Node{}
	if err := yaml.Unmarshal(d.Raw, doc); err != nil {
		return nil, errors.Wrapf(err, "cannot parse %s (document starting at line %d)", file, d.StartLine)
	}
	if doc.Kind == 0 {
		return nil, nil //nolint:nilnil // An empty document is not an XRD.
	}
	offsetLines(doc, d.StartLine-1)
	return parseDocument(file, doc)
}

// offsetLines shifts the line of n and everything beneath it.
func offsetLines(n *yaml.Node, offset int) {
	n.Line += offset
	for _, c := range n.Content {
		offsetLines(c, offset)
	}
}

func parseDocument(file string, doc *yaml.Node) (*XRD, error) {
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, nil //nolint:nilnil // A document that is not an object is not an XRD.
	}

	header := struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
	}{}
	if err := doc.Decode(&header); err != nil {
		return nil, nil //nolint:nilerr,nilnil // A document without a readable header is not an XRD.
	}
	if header.Kind != xrdKind || !isXRDAPIVersion(header.APIVersion) {
		return nil, nil //nolint:nilnil // Not an XRD.
	}

	var m map[string]any
	if err := doc.Decode(&m); err != nil {
		return nil, errors.Wrapf(err, "cannot decode XRD in %s at line %d", file, doc.Line)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, errors.Wrapf(err, "cannot decode XRD in %s at line %d", file, doc.Line)
	}
	obj := &xpv1.CompositeResourceDefinition{}
	if err := json.Unmarshal(raw, obj); err != nil {
		return nil, errors.Wrapf(err, "cannot decode XRD in %s at line %d", file, doc.Line)
	}

	return &XRD{File: file, Name: obj.GetName(), Object: obj, doc: doc}, nil
}

// root returns the XRD's top-level mapping node.
func (x *XRD) root() *yaml.Node {
	return x.doc.Content[0]
}

// rootAnchors are the nodes a comment above the start of the document can
// attach to. Findings with no node of their own attach here.
func (x *XRD) rootAnchors() anchors {
	a := anchors{x.doc, x.root()}
	if len(x.root().Content) > 0 {
		a = append(a, x.root().Content[0])
	}
	return a
}

// rootPosition is the line and column of the start of the document.
func (x *XRD) rootPosition() (int, int) {
	r := x.root()
	if len(r.Content) > 0 {
		return r.Content[0].Line, r.Content[0].Column
	}
	return r.Line, r.Column
}

// mapEntry returns the key and value nodes for key in a mapping node, or nil
// nodes if n is not a mapping or has no such key.
func mapEntry(n *yaml.Node, key string) (*yaml.Node, *yaml.Node) {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i], n.Content[i+1]
		}
	}
	return nil, nil
}
