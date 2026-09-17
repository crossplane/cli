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
	"context"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/spf13/afero"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/xcrd"
	runtimexpkg "github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xpkg/parser"
)

// providerPackageYAML is a Provider-style package bundling a raw
// CustomResourceDefinition, the way `crdFilename` has always handled it.
const providerPackageYAML = `apiVersion: meta.pkg.crossplane.io/v1
kind: Provider
metadata:
  name: example
spec:
  crossplane:
    version: ">=v1.14.0"
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: things.example.com
spec:
  group: example.com
  names:
    plural: things
    kind: Thing
    listKind: ThingList
    singular: thing
  scope: Namespaced
  versions:
  - name: v1
    served: true
    storage: true
    schema:
      openAPIV3Schema:
        type: object
`

// providerV1beta1PackageYAML bundles a v1beta1 CRD, the other branch of the
// existing CRD-only handling.
const providerV1beta1PackageYAML = `apiVersion: meta.pkg.crossplane.io/v1
kind: Provider
metadata:
  name: example
spec:
  crossplane:
    version: ">=v1.14.0"
---
apiVersion: apiextensions.k8s.io/v1beta1
kind: CustomResourceDefinition
metadata:
  name: widgets.example.com
spec:
  group: example.com
  names:
    plural: widgets
    kind: Widget
    listKind: WidgetList
    singular: widget
  scope: Namespaced
  version: v1beta1
`

// configurationXRDNoClaimPackageYAML bundles a single XRD with no claim
// names - the repro for the bug this package fixes.
const configurationXRDNoClaimPackageYAML = `apiVersion: meta.pkg.crossplane.io/v1
kind: Configuration
metadata:
  name: example
spec:
  crossplane:
    version: ">=v1.14.0"
---
apiVersion: apiextensions.crossplane.io/v1
kind: CompositeResourceDefinition
metadata:
  name: xdatabases.acme.example.com
spec:
  group: acme.example.com
  names:
    kind: XDatabase
    plural: xdatabases
    singular: xdatabase
    listKind: XDatabaseList
  scope: Cluster
  versions:
  - name: v1alpha1
    served: true
    referenceable: true
    schema:
      openAPIV3Schema:
        type: object
        properties:
          spec:
            type: object
`

// configurationXRDWithClaimPackageYAML bundles a single XRD that offers a
// claim.
const configurationXRDWithClaimPackageYAML = `apiVersion: meta.pkg.crossplane.io/v1
kind: Configuration
metadata:
  name: example
spec:
  crossplane:
    version: ">=v1.14.0"
---
apiVersion: apiextensions.crossplane.io/v1
kind: CompositeResourceDefinition
metadata:
  name: xdatabases.acme.example.com
spec:
  group: acme.example.com
  names:
    kind: XDatabase
    plural: xdatabases
    singular: xdatabase
    listKind: XDatabaseList
  claimNames:
    kind: Database
    plural: databases
    singular: database
    listKind: DatabaseList
  scope: LegacyCluster
  versions:
  - name: v1alpha1
    served: true
    referenceable: true
    schema:
      openAPIV3Schema:
        type: object
        properties:
          spec:
            type: object
`

// configurationXRDv2PackageYAML bundles a single apiextensions.crossplane.io/v2
// XRD (no claim support in v2).
const configurationXRDv2PackageYAML = `apiVersion: meta.pkg.crossplane.io/v1
kind: Configuration
metadata:
  name: example
spec:
  crossplane:
    version: ">=v2.0.0"
---
apiVersion: apiextensions.crossplane.io/v2
kind: CompositeResourceDefinition
metadata:
  name: xdatabases.acme.example.com
spec:
  group: acme.example.com
  names:
    kind: XDatabase
    plural: xdatabases
    singular: xdatabase
    listKind: XDatabaseList
  scope: Namespaced
  versions:
  - name: v1alpha1
    served: true
    referenceable: true
    schema:
      openAPIV3Schema:
        type: object
        properties:
          spec:
            type: object
`

// configurationCRDAndXRDPackageYAML bundles both a raw CRD and an XRD in the
// same package.
const configurationCRDAndXRDPackageYAML = `apiVersion: meta.pkg.crossplane.io/v1
kind: Configuration
metadata:
  name: example
spec:
  crossplane:
    version: ">=v1.14.0"
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: things.example.com
spec:
  group: example.com
  names:
    plural: things
    kind: Thing
    listKind: ThingList
    singular: thing
  scope: Namespaced
  versions:
  - name: v1
    served: true
    storage: true
    schema:
      openAPIV3Schema:
        type: object
---
apiVersion: apiextensions.crossplane.io/v1
kind: CompositeResourceDefinition
metadata:
  name: xdatabases.acme.example.com
spec:
  group: acme.example.com
  names:
    kind: XDatabase
    plural: xdatabases
    singular: xdatabase
    listKind: XDatabaseList
  scope: Cluster
  versions:
  - name: v1alpha1
    served: true
    referenceable: true
    schema:
      openAPIV3Schema:
        type: object
        properties:
          spec:
            type: object
`

// parseTestPackage parses body into a *parser.Package using the real runtime
// schemes, the same way the xpkg client parses a fetched package.
func parseTestPackage(t *testing.T, body string) *parser.Package {
	t.Helper()
	metaScheme, err := runtimexpkg.BuildMetaScheme()
	if err != nil {
		t.Fatalf("build meta scheme: %v", err)
	}
	objScheme, err := runtimexpkg.BuildObjectScheme()
	if err != nil {
		t.Fatalf("build object scheme: %v", err)
	}
	pkg, err := parser.New(metaScheme, objScheme).Parse(context.Background(), io.NopCloser(strings.NewReader(body)))
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	return pkg
}

// lsFS returns the sorted names of all files in fs.
func lsFS(t *testing.T, fs afero.Fs) []string {
	t.Helper()
	infos, err := afero.ReadDir(fs, "/")
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		names = append(names, info.Name())
	}
	slices.Sort(names)
	return names
}

func readCRD(t *testing.T, fs afero.Fs, name string) *extv1.CustomResourceDefinition {
	t.Helper()
	bs, err := afero.ReadFile(fs, name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	crd := &extv1.CustomResourceDefinition{}
	if err := yaml.Unmarshal(bs, crd); err != nil {
		t.Fatalf("unmarshal %s: %v", name, err)
	}
	// Downstream schema generators identify CRD documents by apiVersion/kind
	// (see internal/schemas/generator's goCollectOpenAPIs), so every CRD
	// CRDFilesystem writes - including ones derived from an XRD - must carry
	// them, even though xcrd.ForCompositeResource/ForCompositeResourceClaim
	// don't set them on the object they return.
	if crd.APIVersion != extv1.SchemeGroupVersion.String() || crd.Kind != "CustomResourceDefinition" {
		t.Errorf("%s: apiVersion/kind = %q/%q, want %q/%q", name, crd.APIVersion, crd.Kind, extv1.SchemeGroupVersion.String(), "CustomResourceDefinition")
	}
	return crd
}

func TestCRDFilesystem(t *testing.T) {
	type args struct {
		pkgYAML string
	}

	type wantCRD struct {
		group      string
		kind       string
		categories []string
	}

	type want struct {
		files []string
		crds  map[string]wantCRD // filename -> expected content, for files worth inspecting
		err   error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"ProviderCRDOnly": {
			reason: "A Provider package bundling a raw CustomResourceDefinition should pass it through as its own file.",
			args:   args{pkgYAML: providerPackageYAML},
			want: want{
				files: []string{"things.example.com.yaml"},
				crds: map[string]wantCRD{
					"things.example.com.yaml": {group: "example.com", kind: "Thing"},
				},
			},
		},
		"ProviderCRDv1beta1": {
			reason: "A Provider package bundling a v1beta1 CustomResourceDefinition should also pass it through as its own file.",
			args:   args{pkgYAML: providerV1beta1PackageYAML},
			want:   want{files: []string{"widgets.example.com.yaml"}},
		},
		"XRDOnlyNoClaim": {
			reason: "An XRD with no claim names should produce only the composite CRD, tagged with the composite category.",
			args:   args{pkgYAML: configurationXRDNoClaimPackageYAML},
			want: want{
				files: []string{"xdatabases.acme.example.com.yaml"},
				crds: map[string]wantCRD{
					"xdatabases.acme.example.com.yaml": {group: "acme.example.com", kind: "XDatabase", categories: []string{xcrd.CategoryComposite}},
				},
			},
		},
		"XRDWithClaim": {
			reason: "An XRD offering a claim should produce both the composite and claim CRDs, each tagged with its own category.",
			args:   args{pkgYAML: configurationXRDWithClaimPackageYAML},
			want: want{
				files: []string{"databases.acme.example.com.yaml", "xdatabases.acme.example.com.yaml"},
				crds: map[string]wantCRD{
					"xdatabases.acme.example.com.yaml": {group: "acme.example.com", kind: "XDatabase", categories: []string{xcrd.CategoryComposite}},
					"databases.acme.example.com.yaml":  {group: "acme.example.com", kind: "Database", categories: []string{xcrd.CategoryClaim}},
				},
			},
		},
		"XRDv2": {
			reason: "A v2 XRD, which has no claim support, should produce only the composite CRD.",
			args:   args{pkgYAML: configurationXRDv2PackageYAML},
			want: want{
				files: []string{"xdatabases.acme.example.com.yaml"},
				crds: map[string]wantCRD{
					"xdatabases.acme.example.com.yaml": {group: "acme.example.com", kind: "XDatabase", categories: []string{xcrd.CategoryComposite}},
				},
			},
		},
		"MixedCRDAndXRD": {
			reason: "A package bundling both a raw CRD and an XRD should produce a file for each.",
			args:   args{pkgYAML: configurationCRDAndXRDPackageYAML},
			want:   want{files: []string{"things.example.com.yaml", "xdatabases.acme.example.com.yaml"}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			pkg := parseTestPackage(t, tc.args.pkgYAML)
			fs, err := CRDFilesystem(pkg)
			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nCRDFilesystem(...): -want err, +got err:\n%s", tc.reason, diff)
			}
			if err != nil {
				return
			}

			if diff := cmp.Diff(tc.want.files, lsFS(t, fs)); diff != "" {
				t.Errorf("\n%s\nCRDFilesystem(...): -want files, +got files:\n%s", tc.reason, diff)
			}

			for filename, wc := range tc.want.crds {
				crd := readCRD(t, fs, filename)
				if diff := cmp.Diff(wc.group, crd.Spec.Group); diff != "" {
					t.Errorf("\n%s\nCRD %s group: -want, +got:\n%s", tc.reason, filename, diff)
				}
				if diff := cmp.Diff(wc.kind, crd.Spec.Names.Kind); diff != "" {
					t.Errorf("\n%s\nCRD %s kind: -want, +got:\n%s", tc.reason, filename, diff)
				}
				if diff := cmp.Diff(wc.categories, crd.Spec.Names.Categories); diff != "" {
					t.Errorf("\n%s\nCRD %s categories: -want, +got:\n%s", tc.reason, filename, diff)
				}
			}
		})
	}
}
