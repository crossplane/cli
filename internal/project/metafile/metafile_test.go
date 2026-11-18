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

package metafile

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/afero"

	"github.com/crossplane/cli/v2/apis/dev/v1alpha1"
)

const testMeta = `apiVersion: meta.pkg.crossplane.io/v1
kind: Configuration
metadata:
  name: my-config
spec:
  dependsOn:
  - apiVersion: pkg.crossplane.io/v1
    kind: Provider
    package: xpkg.crossplane.io/crossplane-contrib/provider-nop
    version: ">=v0.2.0"
  - function: xpkg.crossplane.io/crossplane-contrib/function-auto-ready
    version: v0.5.0
  - configuration: xpkg.crossplane.io/crossplane-contrib/configuration-foo
`

func TestUpsertDependency(t *testing.T) {
	t.Parallel()

	fs := afero.NewMemMapFs()
	if err := afero.WriteFile(fs, "crossplane.yaml", []byte(testMeta), 0o644); err != nil {
		t.Fatal(err)
	}

	// Replaces the deprecated-style function entry in place.
	if err := upsertDependency(fs, "crossplane.yaml", v1alpha1.XpkgDependency{
		APIVersion: "pkg.crossplane.io/v1", Kind: "Function",
		Package: "xpkg.crossplane.io/crossplane-contrib/function-auto-ready", Version: "v0.6.0",
	}); err != nil {
		t.Fatal(err)
	}
	// Appends a new entry.
	if err := upsertDependency(fs, "crossplane.yaml", v1alpha1.XpkgDependency{
		APIVersion: "pkg.crossplane.io/v1", Kind: "Provider",
		Package: "xpkg.crossplane.io/crossplane-contrib/provider-new", Version: "v1.0.0",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := afero.ReadFile(fs, "crossplane.yaml")
	if err != nil {
		t.Fatal(err)
	}

	want := `apiVersion: meta.pkg.crossplane.io/v1
kind: Configuration
metadata:
  name: my-config
spec:
  dependsOn:
  - apiVersion: pkg.crossplane.io/v1
    kind: Provider
    package: xpkg.crossplane.io/crossplane-contrib/provider-nop
    version: '>=v0.2.0'
  - apiVersion: pkg.crossplane.io/v1
    kind: Function
    package: xpkg.crossplane.io/crossplane-contrib/function-auto-ready
    version: v0.6.0
  - configuration: xpkg.crossplane.io/crossplane-contrib/configuration-foo
  - apiVersion: pkg.crossplane.io/v1
    kind: Provider
    package: xpkg.crossplane.io/crossplane-contrib/provider-new
    version: v1.0.0
`
	if diff := cmp.Diff(want, string(got)); diff != "" {
		t.Errorf("on-disk contents (-want +got):\n%s", diff)
	}
}
