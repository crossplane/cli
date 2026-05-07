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

package dependency

import (
	"context"
	"encoding/json"
	"io"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/afero"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	runtimexpkg "github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xpkg/parser"

	"github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/internal/schemas/generator"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"
)

const testPackageYAML = `apiVersion: meta.pkg.crossplane.io/v1
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
`

// parsedTestPackage parses testPackageYAML once into a *parser.Package
// that the fake client can hand back from Get.
func parsedTestPackage(t *testing.T) *parser.Package {
	t.Helper()
	metaScheme, err := runtimexpkg.BuildMetaScheme()
	if err != nil {
		t.Fatalf("build meta scheme: %v", err)
	}
	objScheme, err := runtimexpkg.BuildObjectScheme()
	if err != nil {
		t.Fatalf("build object scheme: %v", err)
	}
	pkg, err := parser.New(metaScheme, objScheme).Parse(context.Background(), io.NopCloser(strings.NewReader(testPackageYAML)))
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	return pkg
}

// fakeClient is a fake xpkg.Client. Get returns a pre-canned Package per ref;
// ListVersions returns a fixed tag list, so a real Resolver can be wired on
// top.
type fakeClient struct {
	packages map[string]*runtimexpkg.Package
	tags     []string
}

func (f *fakeClient) Get(_ context.Context, ref string, _ ...runtimexpkg.GetOption) (*runtimexpkg.Package, error) {
	pkg, ok := f.packages[ref]
	if !ok {
		return nil, errors.New("not found")
	}
	return pkg, nil
}

func (f *fakeClient) ListVersions(_ context.Context, _ string, _ ...runtimexpkg.GetOption) ([]string, error) {
	return f.tags, nil
}

func makePackage(t *testing.T, source, digest, version string) *runtimexpkg.Package {
	t.Helper()
	return &runtimexpkg.Package{
		Package: parsedTestPackage(t),
		Source:  source,
		Digest:  digest,
		Version: version,
	}
}

func newTestManager(t *testing.T, fc *fakeClient) (*Manager, afero.Fs) {
	t.Helper()
	schemaFS := afero.NewMemMapFs()
	m := NewManager(
		&v1alpha1.Project{
			Spec: v1alpha1.ProjectSpec{
				Paths: &v1alpha1.ProjectPaths{Schemas: "schemas"},
			},
		},
		afero.NewMemMapFs(),
		WithSchemaFS(schemaFS),
		WithSchemaGenerators([]generator.Interface{}),
		WithXpkgClient(fc),
		WithResolver(clixpkg.NewResolver(fc)),
	)
	return m, schemaFS
}

func TestManager_AddPackage(t *testing.T) {
	tests := map[string]struct {
		ref     string
		tags    []string
		fetchAt string
		wantKey string
	}{
		"ConstraintCollapsesToResolvedVersion": {
			ref:     "pkg.example/foo:>=v0.0.0",
			tags:    []string{"v0.5.2"},
			fetchAt: "pkg.example/foo:v0.5.2",
			wantKey: "xpkg://pkg.example/foo:v0.5.2",
		},
		"ExactVersionMatchesResolved": {
			ref:     "pkg.example/foo:v0.5.2",
			tags:    []string{"v0.5.2"},
			fetchAt: "pkg.example/foo:v0.5.2",
			wantKey: "xpkg://pkg.example/foo:v0.5.2",
		},
		"DigestRefUsesDigestForm": {
			ref:     "pkg.example/foo@sha256:5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03",
			fetchAt: "pkg.example/foo@sha256:5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03",
			wantKey: "xpkg://pkg.example/foo@sha256:5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fc := &fakeClient{
				packages: map[string]*runtimexpkg.Package{
					tc.fetchAt: makePackage(t, "pkg.example/foo", "sha256:5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03", ""),
				},
				tags: tc.tags,
			}
			m, schemaFS := newTestManager(t, fc)

			if _, err := m.AddPackage(context.Background(), tc.ref, false); err != nil {
				t.Fatalf("AddPackage: %v", err)
			}

			bs, err := afero.ReadFile(schemaFS, ".lock.json")
			if err != nil {
				t.Fatalf("read lock: %v", err)
			}
			var got struct {
				Packages map[string]string `json:"packages"`
			}
			if err := json.Unmarshal(bs, &got); err != nil {
				t.Fatalf("unmarshal lock: %v", err)
			}
			if _, ok := got.Packages[tc.wantKey]; !ok {
				t.Errorf("lock has no entry for %q; keys = %v", tc.wantKey, slices.Collect(maps.Keys(got.Packages)))
			}
			if len(got.Packages) != 1 {
				t.Errorf("lock packages = %d, want 1; got %v", len(got.Packages), got.Packages)
			}
		})
	}
}
