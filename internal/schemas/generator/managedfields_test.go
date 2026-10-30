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

package generator

import (
	"strings"
	"testing"

	"github.com/spf13/afero"
)

func TestAddManagedFieldsMarshalers(t *testing.T) {
	cases := map[string]struct {
		reason      string
		input       string
		wantMethods []string
		notMethods  []string
	}{
		"RootObject": {
			reason: "a root object with an ObjectMeta gets a MarshalJSON",
			input: `package v1alpha1

import metav1 "dev.crossplane.io/models/io/k8s/meta/v1"

type Foo struct {
	APIVersion *string             ` + "`json:\"apiVersion,omitempty\"`" + `
	Kind       *string             ` + "`json:\"kind,omitempty\"`" + `
	Metadata   *metav1.ObjectMeta  ` + "`json:\"metadata,omitempty\"`" + `
}
`,
			wantMethods: []string{"Foo.MarshalJSON"},
		},
		"ListAndNested": {
			reason: "lists (ListMeta) and structs that aren't root objects get no MarshalJSON",
			input: `package v1alpha1

import metav1 "dev.crossplane.io/models/io/k8s/meta/v1"

type FooList struct {
	APIVersion *string           ` + "`json:\"apiVersion,omitempty\"`" + `
	Kind       *string           ` + "`json:\"kind,omitempty\"`" + `
	Metadata   *metav1.ListMeta  ` + "`json:\"metadata,omitempty\"`" + `
}

type FooRef struct {
	APIVersion *string ` + "`json:\"apiVersion,omitempty\"`" + `
	Kind       *string ` + "`json:\"kind,omitempty\"`" + `
	Name       *string ` + "`json:\"name,omitempty\"`" + `
}
`,
			notMethods: []string{"FooList.MarshalJSON", "FooRef.MarshalJSON"},
		},
		"ExistingMarshalJSON": {
			reason: "a type that already has a MarshalJSON is left alone, rather than given a duplicate",
			input: `package v1alpha1

import (
	"encoding/json"

	metav1 "dev.crossplane.io/models/io/k8s/meta/v1"
)

type Foo struct {
	APIVersion *string             ` + "`json:\"apiVersion,omitempty\"`" + `
	Kind       *string             ` + "`json:\"kind,omitempty\"`" + `
	Metadata   *metav1.ObjectMeta  ` + "`json:\"metadata,omitempty\"`" + `
}

func (a Foo) MarshalJSON() ([]byte, error) { return json.Marshal(struct{}{}) }
`,
			wantMethods: []string{"Foo.MarshalJSON"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := addManagedFieldsMarshalers(tc.input)
			if err != nil {
				t.Fatalf("\n%s\naddManagedFieldsMarshalers(...): unexpected error: %v", tc.reason, err)
			}
			methods := roMethods(t, got)
			for _, m := range tc.wantMethods {
				if !methods[m] {
					t.Errorf("\n%s\nmissing method %s in:\n%s", tc.reason, m, got)
				}
			}
			for _, m := range tc.notMethods {
				if methods[m] {
					t.Errorf("\n%s\nunexpected method %s in:\n%s", tc.reason, m, got)
				}
			}
			if n := strings.Count(got, ") MarshalJSON()"); n > 1 {
				t.Errorf("\n%s\nwant at most one MarshalJSON, got %d in:\n%s", tc.reason, n, got)
			}
			if !strings.Contains(got, `"encoding/json"`) && len(tc.wantMethods) > 0 {
				t.Errorf("\n%s\nencoding/json not imported in:\n%s", tc.reason, got)
			}
		})
	}
}

// TestGeneratedRootsOmitManagedFields asserts that generated root objects get
// the managedFields-stripping MarshalJSON whether or not runtime.Object
// generation is on. The two modes use different ObjectMeta types: the generated
// mirror by default, the real apimachinery type with runtime objects on.
func TestGeneratedRootsOmitManagedFields(t *testing.T) {
	for name, g := range map[string]goGenerator{
		"Default":        {},
		"RuntimeObjects": {runtimeObjects: true},
		"Accessors":      {accessors: true, runtimeObjects: true},
	} {
		t.Run(name, func(t *testing.T) {
			inputFS := afero.NewBasePathFs(afero.FromIOFS{FS: testdataFS}, "testdata")
			schemaFS, err := g.GenerateFromCRD(t.Context(), inputFS, nil)
			if err != nil {
				t.Fatal(err)
			}
			src, err := afero.ReadFile(schemaFS, "models/co/acme/platform/v1alpha1/xaccountscaffold.go")
			if err != nil {
				t.Fatal(err)
			}
			methods := roMethods(t, string(src))
			if !methods["XAccountScaffold.MarshalJSON"] {
				t.Error("root type XAccountScaffold has no MarshalJSON")
			}
			if methods["XAccountScaffoldSpec.MarshalJSON"] {
				t.Error("nested type XAccountScaffoldSpec should not get a MarshalJSON")
			}
		})
	}
}
