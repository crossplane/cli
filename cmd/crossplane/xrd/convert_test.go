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
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/google/go-cmp/cmp"
	"github.com/invopop/jsonschema"
	"github.com/spf13/afero"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	apiextensionsv1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1"
)

func TestToCRDs(t *testing.T) {
	claimNames := &extv1.CustomResourceDefinitionNames{
		Kind:   "TestApp",
		Plural: "testapps",
	}

	cases := map[string]struct {
		reason     string
		xrd        *apiextensionsv1.CompositeResourceDefinition
		wantKinds  []string              // expected Spec.Names.Kind for each returned CRD, in order
		wantScopes []extv1.ResourceScope // expected Spec.Scope for each returned CRD, in order
	}{
		"Namespaced": {
			reason:     "A namespaced XRD without claimNames should produce one namespaced XR CRD.",
			xrd:        minimalXRD(apiextensionsv1.CompositeResourceScopeNamespaced, nil),
			wantKinds:  []string{"XTestApp"},
			wantScopes: []extv1.ResourceScope{extv1.NamespaceScoped},
		},
		"ClusterScope": {
			reason:     "A v2 cluster-scoped XRD should produce one cluster-scoped XR CRD.",
			xrd:        minimalXRD(apiextensionsv1.CompositeResourceScopeCluster, nil),
			wantKinds:  []string{"XTestApp"},
			wantScopes: []extv1.ResourceScope{extv1.ClusterScoped},
		},
		"LegacyWithoutClaim": {
			reason:     "A legacy XRD without claimNames should produce one cluster-scoped XR CRD.",
			xrd:        minimalXRD(apiextensionsv1.CompositeResourceScopeLegacyCluster, nil),
			wantKinds:  []string{"XTestApp"},
			wantScopes: []extv1.ResourceScope{extv1.ClusterScoped},
		},
		"LegacyOffersClaim": {
			reason:     "A legacy XRD offering a Claim should produce a cluster-scoped XR CRD and a namespaced Claim CRD, in that order.",
			xrd:        minimalXRD(apiextensionsv1.CompositeResourceScopeLegacyCluster, claimNames),
			wantKinds:  []string{"XTestApp", "TestApp"},
			wantScopes: []extv1.ResourceScope{extv1.ClusterScoped, extv1.NamespaceScoped},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			crds, err := toCRDs(tc.xrd)
			if err != nil {
				t.Fatalf("\n%s\ntoCRDs(): unexpected error: %v", tc.reason, err)
			}

			gotKinds := make([]string, len(crds))
			gotScopes := make([]extv1.ResourceScope, len(crds))
			for i, crd := range crds {
				gotKinds[i] = crd.Spec.Names.Kind
				gotScopes[i] = crd.Spec.Scope
			}

			if diff := cmp.Diff(tc.wantKinds, gotKinds); diff != "" {
				t.Errorf("\n%s\ntoCRDs() kinds: -want, +got:\n%s", tc.reason, diff)
			}

			if diff := cmp.Diff(tc.wantScopes, gotScopes); diff != "" {
				t.Errorf("\n%s\ntoCRDs() scopes: -want, +got:\n%s", tc.reason, diff)
			}

			for i, crd := range crds {
				if crd.APIVersion != "apiextensions.k8s.io/v1" {
					t.Errorf("\n%s\ncrds[%d].APIVersion = %q, want apiextensions.k8s.io/v1", tc.reason, i, crd.APIVersion)
				}

				if crd.Kind != "CustomResourceDefinition" {
					t.Errorf("\n%s\ncrds[%d].Kind = %q, want CustomResourceDefinition", tc.reason, i, crd.Kind)
				}
			}
		})
	}
}

func TestConvertJSONSchema(t *testing.T) {
	xrd := minimalXRD(apiextensionsv1.CompositeResourceScopeNamespaced, nil)
	crds, err := toCRDs(xrd)
	if err != nil {
		t.Fatalf("toCRDs(): unexpected error: %v", err)
	}

	wantFile := "example.org_v1alpha1_xtestapp.json"

	cases := map[string]struct {
		reason    string
		cmd       convertCmd
		wantFile  string
		wantStdio bool
	}{
		"Stdout": {
			reason:    "With no output flags, JSON Schema should be written to stdout.",
			cmd:       convertCmd{JSONSchema: true},
			wantStdio: true,
		},
		"OutputFile": {
			reason:   "With -o, JSON Schema should be written to the specified file.",
			cmd:      convertCmd{JSONSchema: true, OutputFile: "/out/schema.json"},
			wantFile: "/out/schema.json",
		},
		"OutputDir": {
			reason:   "With --output-dir, JSON Schema should be written as a named file in the directory.",
			cmd:      convertCmd{JSONSchema: true, OutputDir: "/schemas"},
			wantFile: filepath.Join("/schemas", wantFile),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			tc.cmd.fs = fs

			buf := &bytes.Buffer{}
			app, err := kong.New(&struct{}{})
			if err != nil {
				t.Fatalf("cannot create kong app: %v", err)
			}
			k, err := app.Parse([]string{})
			if err != nil {
				t.Fatalf("cannot parse kong: %v", err)
			}
			k.Stdout = buf

			outputs, err := toJSONSchemaOutputs(crds)
			if err != nil {
				t.Fatalf("\n%s\ntoJSONSchemaOutputs(): unexpected error: %v", tc.reason, err)
			}

			if err := tc.cmd.writeOutputs(k, outputs); err != nil {
				t.Fatalf("\n%s\nwriteOutputs(): unexpected error: %v", tc.reason, err)
			}

			var raw []byte
			if tc.wantStdio {
				if buf.Len() == 0 {
					t.Fatalf("\n%s\nexpected stdout output, got nothing", tc.reason)
				}
				raw = buf.Bytes()
			} else {
				raw, err = afero.ReadFile(fs, tc.wantFile)
				if err != nil {
					t.Fatalf("\n%s\nexpected file %s to exist: %v", tc.reason, tc.wantFile, err)
				}
			}

			var s jsonschema.Schema
			if err := json.Unmarshal(raw, &s); err != nil {
				t.Errorf("\n%s\noutput is not valid JSON Schema: %v", tc.reason, err)
			}

			if s.ID == "" {
				t.Errorf("\n%s\nexpected JSON Schema $id to be set", tc.reason)
			}
		})
	}
}

// minimalXRD returns a minimal valid XRD with the given scope and (optional)
// claim names. All other fields are populated with defaults sufficient to
// satisfy xcrd.ForCompositeResource / xcrd.ForCompositeResourceClaim.
func minimalXRD(scope apiextensionsv1.CompositeResourceScope, claimNames *extv1.CustomResourceDefinitionNames) *apiextensionsv1.CompositeResourceDefinition {
	return &apiextensionsv1.CompositeResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "xtestapps.example.org"},
		Spec: apiextensionsv1.CompositeResourceDefinitionSpec{
			Group: "example.org",
			Names: extv1.CustomResourceDefinitionNames{
				Kind:   "XTestApp",
				Plural: "xtestapps",
			},
			Scope:      &scope,
			ClaimNames: claimNames,
			Versions: []apiextensionsv1.CompositeResourceDefinitionVersion{{
				Name:          "v1alpha1",
				Served:        true,
				Referenceable: true,
				Schema: &apiextensionsv1.CompositeResourceValidation{
					OpenAPIV3Schema: runtime.RawExtension{Raw: []byte(`{"type":"object"}`)},
				},
			}},
		},
	}
}

