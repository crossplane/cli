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
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	xpv1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1"
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

// Load reads XRDs from files, directories and, for the path "-", stdin.
// Directories are searched recursively for .yaml and .yml files. Documents
// that are not XRDs are skipped.
func Load(fs afero.Fs, stdin io.Reader, paths []string) ([]*XRD, error) {
	var out []*XRD
	for _, p := range paths {
		if p == "-" {
			if stdin == nil {
				return nil, errors.New("cannot read from stdin")
			}
			data, err := io.ReadAll(stdin)
			if err != nil {
				return nil, errors.Wrap(err, "cannot read stdin")
			}
			xrds, err := Parse(StdinName, data)
			if err != nil {
				return nil, err
			}
			out = append(out, xrds...)
			continue
		}

		files, err := yamlFiles(fs, p)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			data, err := afero.ReadFile(fs, f)
			if err != nil {
				return nil, errors.Wrapf(err, "cannot read %q", f)
			}
			xrds, err := Parse(f, data)
			if err != nil {
				return nil, err
			}
			out = append(out, xrds...)
		}
	}
	return out, nil
}

// yamlFiles returns p if it is a file, or the YAML files beneath it, in
// lexical order, if it is a directory.
func yamlFiles(fs afero.Fs, p string) ([]string, error) {
	info, err := fs.Stat(p)
	if err != nil {
		return nil, errors.Wrapf(err, "cannot read %q", p)
	}
	if !info.IsDir() {
		return []string{p}, nil
	}

	var files []string
	err = afero.Walk(fs, p, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".yaml", ".yml":
			files = append(files, path)
		}
		return nil
	})
	return files, errors.Wrapf(err, "cannot read directory %q", p)
}

// Parse reads the XRDs in a YAML stream. file names the stream in findings.
func Parse(file string, data []byte) ([]*XRD, error) {
	var out []*XRD
	dec := yaml.NewDecoder(bytes.NewReader(data))
	for {
		doc := &yaml.Node{}
		err := dec.Decode(doc)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, errors.Wrapf(err, "cannot parse %s", file)
		}

		x, err := parseDocument(file, doc)
		if err != nil {
			return nil, err
		}
		if x != nil {
			out = append(out, x)
		}
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
