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

func TestCRDFilesystem_ProviderCRDOnly(t *testing.T) {
	pkg := parseTestPackage(t, providerPackageYAML)

	fs, err := CRDFilesystem(pkg)
	if err != nil {
		t.Fatalf("CRDFilesystem: %v", err)
	}

	wantFiles := []string{"things.example.com.yaml"}
	if diff := cmpNames(wantFiles, lsFS(t, fs)); diff != "" {
		t.Errorf("files (-want +got):\n%s", diff)
	}

	crd := readCRD(t, fs, "things.example.com.yaml")
	if crd.Spec.Group != "example.com" || crd.Spec.Names.Plural != "things" || crd.Spec.Names.Kind != "Thing" {
		t.Errorf("unexpected CRD content: %+v", crd.Spec)
	}
}

func TestCRDFilesystem_ProviderCRDv1beta1(t *testing.T) {
	pkg := parseTestPackage(t, providerV1beta1PackageYAML)

	fs, err := CRDFilesystem(pkg)
	if err != nil {
		t.Fatalf("CRDFilesystem: %v", err)
	}

	wantFiles := []string{"widgets.example.com.yaml"}
	if diff := cmpNames(wantFiles, lsFS(t, fs)); diff != "" {
		t.Errorf("files (-want +got):\n%s", diff)
	}
}

func TestCRDFilesystem_XRDOnlyNoClaim(t *testing.T) {
	pkg := parseTestPackage(t, configurationXRDNoClaimPackageYAML)

	fs, err := CRDFilesystem(pkg)
	if err != nil {
		t.Fatalf("CRDFilesystem: %v", err)
	}

	wantFiles := []string{"xdatabases.acme.example.com.yaml"}
	if diff := cmpNames(wantFiles, lsFS(t, fs)); diff != "" {
		t.Errorf("files (-want +got):\n%s", diff)
	}

	crd := readCRD(t, fs, "xdatabases.acme.example.com.yaml")
	if crd.Spec.Group != "acme.example.com" || crd.Spec.Names.Kind != "XDatabase" {
		t.Errorf("unexpected CRD content: %+v", crd.Spec)
	}
	if !slices.Contains(crd.Spec.Names.Categories, xcrd.CategoryComposite) {
		t.Errorf("derived CRD missing composite category: %v", crd.Spec.Names.Categories)
	}
}

func TestCRDFilesystem_XRDWithClaim(t *testing.T) {
	pkg := parseTestPackage(t, configurationXRDWithClaimPackageYAML)

	fs, err := CRDFilesystem(pkg)
	if err != nil {
		t.Fatalf("CRDFilesystem: %v", err)
	}

	wantFiles := []string{"databases.acme.example.com.yaml", "xdatabases.acme.example.com.yaml"}
	if diff := cmpNames(wantFiles, lsFS(t, fs)); diff != "" {
		t.Errorf("files (-want +got):\n%s", diff)
	}

	composite := readCRD(t, fs, "xdatabases.acme.example.com.yaml")
	if !slices.Contains(composite.Spec.Names.Categories, xcrd.CategoryComposite) {
		t.Errorf("composite CRD missing composite category: %v", composite.Spec.Names.Categories)
	}

	claim := readCRD(t, fs, "databases.acme.example.com.yaml")
	if claim.Spec.Names.Kind != "Database" {
		t.Errorf("unexpected claim CRD kind: %s", claim.Spec.Names.Kind)
	}
	if !slices.Contains(claim.Spec.Names.Categories, xcrd.CategoryClaim) {
		t.Errorf("claim CRD missing claim category: %v", claim.Spec.Names.Categories)
	}
}

func TestCRDFilesystem_XRDv2(t *testing.T) {
	pkg := parseTestPackage(t, configurationXRDv2PackageYAML)

	fs, err := CRDFilesystem(pkg)
	if err != nil {
		t.Fatalf("CRDFilesystem: %v", err)
	}

	wantFiles := []string{"xdatabases.acme.example.com.yaml"}
	if diff := cmpNames(wantFiles, lsFS(t, fs)); diff != "" {
		t.Errorf("files (-want +got):\n%s", diff)
	}

	crd := readCRD(t, fs, "xdatabases.acme.example.com.yaml")
	if crd.Spec.Group != "acme.example.com" || crd.Spec.Names.Kind != "XDatabase" {
		t.Errorf("unexpected CRD content: %+v", crd.Spec)
	}
}

func TestCRDFilesystem_MixedCRDAndXRD(t *testing.T) {
	pkg := parseTestPackage(t, configurationCRDAndXRDPackageYAML)

	fs, err := CRDFilesystem(pkg)
	if err != nil {
		t.Fatalf("CRDFilesystem: %v", err)
	}

	wantFiles := []string{"things.example.com.yaml", "xdatabases.acme.example.com.yaml"}
	if diff := cmpNames(wantFiles, lsFS(t, fs)); diff != "" {
		t.Errorf("files (-want +got):\n%s", diff)
	}
}

// cmpNames returns a diff-style string if want and got differ, else "".
func cmpNames(want, got []string) string {
	if slices.Equal(want, got) {
		return ""
	}
	return "want: " + strings.Join(want, ", ") + "\ngot:  " + strings.Join(got, ", ")
}
