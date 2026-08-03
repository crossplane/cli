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

func TestGenerateFromCRDRuntimeObjectsArtifacts(t *testing.T) {
	inputFS := afero.NewBasePathFs(afero.FromIOFS{FS: testdataFS}, "testdata")
	schemaFS, err := goGenerator{runtimeObjects: true}.GenerateFromCRD(t.Context(), inputFS, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Root types implement runtime.Object + schema.ObjectKind.
	crd, err := afero.ReadFile(schemaFS, "models/co/acme/platform/v1alpha1/xaccountscaffold.go")
	if err != nil {
		t.Fatal(err)
	}
	methods := roMethods(t, string(crd))
	for _, m := range []string{
		"XAccountScaffold.DeepCopyInto", "XAccountScaffold.DeepCopy", "XAccountScaffold.DeepCopyObject",
		"XAccountScaffold.GetObjectKind", "XAccountScaffold.SetGroupVersionKind",
		"XAccountScaffoldSpec.DeepCopyInto", "XAccountScaffoldSpec.DeepCopy",
	} {
		if !methods[m] {
			t.Errorf("expected %s in generated CRD model", m)
		}
	}
	// Nested non-root struct must not be a runtime.Object.
	if methods["XAccountScaffoldSpec.DeepCopyObject"] {
		t.Error("nested struct should not implement runtime.Object")
	}

	// A groupversion_info.go is generated for each package, carrying the real API
	// group: the CRD's own group, and the core (empty) group for the built-in
	// metav1 package the CRD path emits alongside it.
	gvis := map[string]string{
		"models/co/acme/platform/v1alpha1/groupversion_info.go": `GroupVersion = schema.GroupVersion{Group: "platform.acme.co", Version: "v1alpha1"}`,
		"models/io/k8s/meta/v1/groupversion_info.go":            `GroupVersion = schema.GroupVersion{Group: "", Version: "v1"}`,
	}
	for path, want := range gvis {
		gvi, err := afero.ReadFile(schemaFS, path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(gvi), want) {
			t.Errorf("expected %q in %s, got:\n%s", want, path, gvi)
		}
	}
}

func TestGenerateFromCRDNoRuntimeObjectsByDefault(t *testing.T) {
	inputFS := afero.NewBasePathFs(afero.FromIOFS{FS: testdataFS}, "testdata")
	schemaFS, err := goGenerator{}.GenerateFromCRD(t.Context(), inputFS, nil)
	if err != nil {
		t.Fatal(err)
	}

	crd, err := afero.ReadFile(schemaFS, "models/co/acme/platform/v1alpha1/xaccountscaffold.go")
	if err != nil {
		t.Fatal(err)
	}
	if roMethods(t, string(crd))["XAccountScaffold.DeepCopyObject"] {
		t.Error("runtime.Object methods must not be generated when feature is disabled")
	}
	if exists, _ := afero.Exists(schemaFS, "models/co/acme/platform/v1alpha1/groupversion_info.go"); exists {
		t.Error("groupversion_info.go must not exist when feature is disabled")
	}

	// The module files don't depend on the feature flag: we write one go.mod and
	// go.sum either way, so a generated function resolves the same dependency
	// graph whether or not runtime.Object generation is on.
	mod, err := afero.ReadFile(schemaFS, "models/go.mod")
	if err != nil {
		t.Fatal(err)
	}
	if string(mod) != goModContents {
		t.Errorf("generated go.mod differs from goModContents:\n%s", mod)
	}
	sum, err := afero.ReadFile(schemaFS, "models/go.sum")
	if err != nil {
		t.Fatal(err)
	}
	if string(sum) != goSumContents {
		t.Errorf("generated go.sum differs from goSumContents")
	}
}

// TestGenerateFromOpenAPIBuiltInGroupVersions checks the API groups we register
// built-in Kubernetes types under. The group labels the generator uses for these
// packages are synthetic — they only drive the directory layout — so registering
// a type under the label would produce a GVK that disagrees with the type's own
// apiVersion. See goPackage.
func TestGenerateFromOpenAPIBuiltInGroupVersions(t *testing.T) {
	inputFS := afero.NewBasePathFs(afero.FromIOFS{FS: testdataJSONFS}, "testdata")
	schemaFS, err := goGenerator{runtimeObjects: true}.GenerateFromOpenAPI(t.Context(), inputFS, nil)
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string]struct {
		path string
		want string
	}{
		"CoreV1": {
			path: "models/io/k8s/core/v1/groupversion_info.go",
			want: `GroupVersion = schema.GroupVersion{Group: "", Version: "v1"}`,
		},
		"MetaV1": {
			path: "models/io/k8s/core/meta/v1/groupversion_info.go",
			want: `GroupVersion = schema.GroupVersion{Group: "", Version: "v1"}`,
		},
		"RealGroupIsUnchanged": {
			path: "models/io/k8s/authentication/v1/groupversion_info.go",
			want: `GroupVersion = schema.GroupVersion{Group: "authentication.k8s.io", Version: "v1"}`,
		},
		"UnsuffixedRealGroupIsUnchanged": {
			path: "models/io/k8s/autoscaling/v1/groupversion_info.go",
			want: `GroupVersion = schema.GroupVersion{Group: "autoscaling", Version: "v1"}`,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			bs, err := afero.ReadFile(schemaFS, tc.path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(bs), tc.want) {
				t.Errorf("expected %q in %s, got:\n%s", tc.want, tc.path, bs)
			}
		})
	}
}
