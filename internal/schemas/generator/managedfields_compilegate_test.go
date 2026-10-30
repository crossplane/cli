//go:build compilegate

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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
)

// managedFieldsConsumer reproduces the round trip a composition function
// makes: decode the observed composite resource, as the API server returned
// it, into a generated model, then encode the model back into a desired
// resource. managedFields must be readable after the decode, and absent after
// the encode, because the API server rejects a server-side apply request that
// sets metadata.managedFields. The %s verbs are filled with mode-specific
// code, since the two modes use different ObjectMeta types.
const managedFieldsConsumer = `package consumer

import (
	"encoding/json"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"

	v1alpha1 "dev.crossplane.io/models/co/acme/platform/v1alpha1"
)

func managedFieldsCount(xr *v1alpha1.XAccountScaffold) int {
	%s
}

func observed() map[string]any {
	return map[string]any{
		"apiVersion": "platform.acme.co/v1alpha1",
		"kind":       "XAccountScaffold",
		"metadata": map[string]any{
			"name": "example",
			"managedFields": []any{
				map[string]any{
					"manager":    "crossplane",
					"operation":  "Apply",
					"apiVersion": "platform.acme.co/v1alpha1",
					"fieldsType": "FieldsV1",
				},
			},
		},
	}
}

func assertNoManagedFields(t *testing.T, how string, u map[string]any) {
	t.Helper()
	meta, ok := u["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("%%s: metadata missing: %%v", how, u)
	}
	if meta["name"] != "example" {
		t.Errorf("%%s: metadata.name lost: %%v", how, meta)
	}
	if _, ok := meta["managedFields"]; ok {
		t.Errorf("%%s: metadata.managedFields survived the round trip: %%v", how, meta)
	}
}

func TestUnstructuredRoundTrip(t *testing.T) {
	xr := &v1alpha1.XAccountScaffold{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(observed(), xr); err != nil {
		t.Fatal(err)
	}
	if got := managedFieldsCount(xr); got != 1 {
		t.Fatalf("decoded managedFields: want 1 entry, got %%d", got)
	}
	u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(xr)
	if err != nil {
		t.Fatal(err)
	}
	assertNoManagedFields(t, "unstructured", u)
	if got := managedFieldsCount(xr); got != 1 {
		t.Errorf("encoding modified the object: want 1 managedFields entry, got %%d", got)
	}
}

func TestJSONRoundTrip(t *testing.T) {
	in, err := json.Marshal(observed())
	if err != nil {
		t.Fatal(err)
	}
	xr := &v1alpha1.XAccountScaffold{}
	if err := json.Unmarshal(in, xr); err != nil {
		t.Fatal(err)
	}
	if got := managedFieldsCount(xr); got != 1 {
		t.Fatalf("decoded managedFields: want 1 entry, got %%d", got)
	}
	for how, v := range map[string]any{"pointer": xr, "value": *xr} {
		out, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		u := map[string]any{}
		if err := json.Unmarshal(out, &u); err != nil {
			t.Fatal(err)
		}
		assertNoManagedFields(t, "json "+how, u)
	}
}
%s`

// managedFieldsListTest covers the runtime-object mode's List type: each item
// is encoded through the root type's MarshalJSON too.
const managedFieldsListTest = `
func TestListOmitsManagedFields(t *testing.T) {
	xr := v1alpha1.XAccountScaffold{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(observed(), &xr); err != nil {
		t.Fatal(err)
	}
	list := &v1alpha1.XAccountScaffoldList{Items: []v1alpha1.XAccountScaffold{xr}}
	out, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	u := map[string]any{}
	if err := json.Unmarshal(out, &u); err != nil {
		t.Fatal(err)
	}
	items, ok := u["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("unexpected items: %v", u["items"])
	}
	assertNoManagedFields(t, "list item", items[0].(map[string]any))
}
`

// TestGeneratedModelsOmitManagedFields runs the round trip against the
// generated module in both generation modes.
func TestGeneratedModelsOmitManagedFields(t *testing.T) {
	cases := map[string]struct {
		g     goGenerator
		count string
		extra string
	}{
		"Default": {
			g: goGenerator{},
			count: `if xr.Metadata == nil || xr.Metadata.ManagedFields == nil {
		return 0
	}
	return len(*xr.Metadata.ManagedFields)`,
		},
		"RuntimeObjects": {
			g:     goGenerator{runtimeObjects: true},
			count: `return len(xr.GetManagedFields())`,
			extra: managedFieldsListTest,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			inputFS := afero.NewBasePathFs(afero.FromIOFS{FS: testdataFS}, "testdata")
			schemaFS, err := tc.g.GenerateFromCRD(t.Context(), inputFS, nil)
			if err != nil {
				t.Fatal(err)
			}

			dir := t.TempDir()
			roMaterialize(t, schemaFS, dir)

			consumer := fmt.Sprintf(managedFieldsConsumer, tc.count, tc.extra)
			consumerDir := filepath.Join(dir, "models", "consumer")
			if err := os.MkdirAll(consumerDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(consumerDir, "consumer_test.go"), []byte(consumer), 0o644); err != nil {
				t.Fatal(err)
			}

			modelsDir := filepath.Join(dir, "models")
			resolveGeneratedModuleDeps(t, modelsDir)

			cmd := exec.CommandContext(t.Context(), "go", "test", "./...")
			cmd.Dir = modelsDir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("generated models leaked managedFields: %v\n%s", err, out)
			}
		})
	}
}
