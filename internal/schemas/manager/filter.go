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

package manager

import (
	"bufio"
	"bytes"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

// openAPIGroupPrefix starts the file name of a Kubernetes OpenAPI spec for a
// named API group, e.g. apis__apps__v1_openapi.json. Specs for the core group
// (api__v1_openapi.json) and files with other names are never filtered.
const openAPIGroupPrefix = "apis__"

// filterAPIGroups returns a filesystem containing only the resources of from
// whose API group matches one of the globs. With no globs it returns from
// unchanged. CRD and XRD files are filtered per YAML document, so a file that
// bundles several groups keeps only the matching ones.
func filterAPIGroups(from afero.Fs, sourceType SourceType, globs []string) (afero.Fs, error) {
	if len(globs) == 0 || from == nil {
		return from, nil
	}

	out := afero.NewMemMapFs()
	err := afero.Walk(from, ".", func(p string, info fs.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		content, err := afero.ReadFile(from, p)
		if err != nil {
			return err
		}

		switch {
		case sourceType == SourceTypeOpenAPI:
			group, ok := openAPIGroup(filepath.Base(p))
			if ok && !matchesAny(globs, group) {
				return nil
			}
		case isYAML(p):
			content, err = filterYAMLDocuments(content, globs)
			if err != nil {
				return errors.Wrapf(err, "cannot filter %s", p)
			}
			if content == nil {
				return nil
			}
		}

		if err := out.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			return err
		}
		return afero.WriteFile(out, p, content, 0o644)
	})
	return out, err
}

// filterYAMLDocuments keeps the documents of a YAML stream that are not
// resources with an API group, or whose group matches. It returns nil if no
// document is left, and the input unchanged if none was dropped.
func filterYAMLDocuments(content []byte, globs []string) ([]byte, error) {
	var kept [][]byte
	dropped := false
	err := eachYAMLDocument(content, func(doc []byte) error {
		// Documents that don't parse or have no spec.group aren't API
		// resources. Keep them and leave complaining to the generators.
		var res struct {
			Spec struct {
				Group string `json:"group"`
			} `json:"spec"`
		}
		if err := yaml.Unmarshal(doc, &res); err == nil && res.Spec.Group != "" && !matchesAny(globs, res.Spec.Group) {
			dropped = true
			return nil
		}
		kept = append(kept, doc)
		return nil
	})
	if err != nil {
		return nil, err
	}

	switch {
	case !dropped:
		return content, nil
	case len(kept) == 0:
		return nil, nil
	default:
		return bytes.Join(kept, []byte("---\n")), nil
	}
}

// eachYAMLDocument calls fn with every document of a YAML stream, in order. It
// stops at the first error.
func eachYAMLDocument(content []byte, fn func(doc []byte) error) error {
	reader := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(content)))
	for {
		doc, err := reader.Read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := fn(doc); err != nil {
			return err
		}
	}
}

// openAPIGroup returns the API group encoded in an OpenAPI spec file name, and
// whether the name has one.
func openAPIGroup(name string) (string, bool) {
	rest, ok := strings.CutPrefix(name, openAPIGroupPrefix)
	if !ok {
		return "", false
	}
	group, _, _ := strings.Cut(rest, "__")
	return group, true
}

func matchesAny(globs []string, group string) bool {
	for _, g := range globs {
		if ok, err := path.Match(g, group); err == nil && ok {
			return true
		}
	}
	return false
}

func isYAML(p string) bool {
	ext := strings.ToLower(filepath.Ext(p))
	return ext == ".yaml" || ext == ".yml"
}
