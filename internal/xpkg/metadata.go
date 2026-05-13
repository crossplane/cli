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

package xpkg

import (
	"fmt"

	"github.com/spf13/afero"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xpkg/parser"
)

// CRDFilesystem writes each CRD object in the package to a separate
// YAML file in an in-memory filesystem. Files are named
// <plural>.<group>.yaml so the schema generator sees per-CRD inputs.
// Non-CRD objects in the package are skipped.
func CRDFilesystem(pkg *parser.Package) (afero.Fs, error) {
	fs := afero.NewMemMapFs()
	for _, obj := range pkg.GetObjects() {
		name, ok := crdFilename(obj)
		if !ok {
			continue
		}
		bs, err := yaml.Marshal(obj)
		if err != nil {
			return nil, errors.Wrapf(err, "cannot marshal CRD %s", name)
		}
		if err := afero.WriteFile(fs, name, bs, 0o644); err != nil {
			return nil, errors.Wrapf(err, "cannot write CRD %s", name)
		}
	}
	return fs, nil
}

func crdFilename(obj runtime.Object) (string, bool) {
	switch c := obj.(type) {
	case *apiextv1.CustomResourceDefinition:
		return fmt.Sprintf("%s.%s.yaml", c.Spec.Names.Plural, c.Spec.Group), true
	case *apiextv1beta1.CustomResourceDefinition:
		return fmt.Sprintf("%s.%s.yaml", c.Spec.Names.Plural, c.Spec.Group), true
	default:
		return "", false
	}
}
