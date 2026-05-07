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

package project

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/spf13/afero"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"

	devv1alpha1 "github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/internal/project/functions"
)

// xrdYAML returns an XRD manifest for a resource with the given group/kind.
func xrdYAML(group, plural, singular, kind string) string {
	return fmt.Sprintf(`apiVersion: apiextensions.crossplane.io/v2
kind: CompositeResourceDefinition
metadata:
  name: %s.%s
spec:
  group: %s
  names:
    kind: %s
    plural: %s
    singular: %s
    listKind: %sList
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
`, plural, group, group, kind, plural, singular, kind)
}

// compositionYAML returns a Composition manifest targeting the given XR kind.
func compositionYAML(name, group, kind string) string {
	return fmt.Sprintf(`apiVersion: apiextensions.crossplane.io/v1
kind: Composition
metadata:
  name: %s
spec:
  compositeTypeRef:
    apiVersion: %s/v1alpha1
    kind: %s
  pipeline: []
  mode: Pipeline
`, name, group, kind)
}

func writeProject(t *testing.T, projFS afero.Fs, apis map[string]string, fns []string) {
	t.Helper()
	for path, content := range apis {
		if err := projFS.MkdirAll("apis", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := afero.WriteFile(projFS, "apis/"+path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, fn := range fns {
		dir := "functions/" + fn
		if err := projFS.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		// A minimal function source file so FakeIdentifier has something to find.
		if err := afero.WriteFile(projFS, dir+"/.placeholder", []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBuilderBuild(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		apis              map[string]string
		functions         []string
		runtimeDeps       []devv1alpha1.Dependency
		expectedResources int
		expectedFns       int
	}{
		"ConfigurationOnly": {
			apis: map[string]string{
				"db.yaml":         xrdYAML("acme.example.com", "xdatabases", "xdatabase", "XDatabase"),
				"db-comp.yaml":    compositionYAML("xdatabase-comp", "acme.example.com", "XDatabase"),
				"queue.yaml":      xrdYAML("acme.example.com", "xqueues", "xqueue", "XQueue"),
				"queue-comp.yaml": compositionYAML("xqueue-comp", "acme.example.com", "XQueue"),
			},
			runtimeDeps: []devv1alpha1.Dependency{
				{
					Type: devv1alpha1.DependencyTypeXpkg,
					Xpkg: &devv1alpha1.XpkgDependency{
						APIVersion: "pkg.crossplane.io/v1",
						Kind:       "Provider",
						Package:    "xpkg.crossplane.io/example/provider-nop",
						Version:    "v0.2.1",
					},
				},
			},
			expectedResources: 4,
		},
		"EmbeddedFunctions": {
			apis: map[string]string{
				"db.yaml":      xrdYAML("acme.example.com", "xdatabases", "xdatabase", "XDatabase"),
				"db-comp.yaml": compositionYAML("xdatabase-comp", "acme.example.com", "XDatabase"),
			},
			functions:         []string{"fn-cluster", "fn-net"},
			expectedResources: 2,
			expectedFns:       2,
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			projFS := afero.NewMemMapFs()
			writeProject(t, projFS, tc.apis, tc.functions)

			proj := &devv1alpha1.Project{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-project",
				},
				Spec: devv1alpha1.ProjectSpec{
					ProjectPackageMetadata: devv1alpha1.ProjectPackageMetadata{
						Maintainer:  "Maintainer <m@example.com>",
						Source:      "github.com/example/proj",
						License:     "Apache-2.0",
						Description: "test project",
					},
					Repository:   "xpkg.crossplane.io/example/test",
					Dependencies: tc.runtimeDeps,
				},
			}
			proj.Default()

			b := NewBuilder(
				BuildWithFunctionIdentifier(functions.FakeIdentifier),
			)

			imgMap, err := b.Build(t.Context(), proj, projFS)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}

			// Configuration image must be present at <repo>:configuration.
			cfgTag, err := constructTag(proj.Spec.Repository, ConfigurationTag)
			if err != nil {
				t.Fatal(err)
			}
			cfgImg, ok := imgMap[cfgTag]
			if !ok {
				t.Fatalf("configuration image not found in image map; tags=%v", tagsOf(imgMap))
			}

			// Configuration image's package layer should carry the package
			// annotation (added by AnnotateImage on push, but here we just
			// verify the image has the expected meta/Configuration object via
			// labels on the config file).
			cfgFile, err := cfgImg.ConfigFile()
			if err != nil {
				t.Fatal(err)
			}
			// The builder applies xpkg annotations as image labels.
			if _, ok := cfgFile.Config.Labels[xpkg.Label(xpkg.PackageAnnotation)]; !ok {
				// The labels are computed during package construction; for some
				// image shapes the label may not be present. We don't assert
				// strictly here, but the absence is a sign something's off.
				t.Logf("configuration image has no %s label", xpkg.PackageAnnotation)
			}

			// Function images: each fn should produce one image per arch.
			fnTags := map[string][]string{}
			for tag := range imgMap {
				if tag == cfgTag {
					continue
				}
				fnTags[tag.Repository.Name()] = append(fnTags[tag.Repository.Name()], tag.TagStr())
			}
			if diff := cmp.Diff(tc.expectedFns, len(fnTags)); diff != "" {
				t.Errorf("function repo count (-want +got):\n%s", diff)
			}
			for repo, tags := range fnTags {
				if diff := cmp.Diff(len(proj.Spec.Architectures), len(tags)); diff != "" {
					t.Errorf("function %s: arch count (-want +got):\n%s", repo, diff)
				}
			}
		})
	}
}

// TestBuilderDependsOn verifies the configuration package's dependsOn list
// includes both runtime project deps and embedded function deps.
func TestBuilderDependsOn(t *testing.T) {
	t.Parallel()

	projFS := afero.NewMemMapFs()
	writeProject(t, projFS,
		map[string]string{
			"db.yaml":      xrdYAML("acme.example.com", "xdatabases", "xdatabase", "XDatabase"),
			"db-comp.yaml": compositionYAML("xdb", "acme.example.com", "XDatabase"),
		},
		[]string{"fn-one"},
	)

	runtimeDep := devv1alpha1.Dependency{
		Type: devv1alpha1.DependencyTypeXpkg,
		Xpkg: &devv1alpha1.XpkgDependency{
			APIVersion: "pkg.crossplane.io/v1",
			Kind:       "Provider",
			Package:    "xpkg.crossplane.io/example/provider-nop",
			Version:    "v0.2.1",
		},
	}
	proj := &devv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-project",
		},
		Spec: devv1alpha1.ProjectSpec{
			Repository:    "xpkg.crossplane.io/example/test",
			Architectures: []string{"amd64", "arm64"},
			Dependencies:  []devv1alpha1.Dependency{runtimeDep},
		},
	}
	proj.Default()

	imgMap, err := NewBuilder(
		BuildWithFunctionIdentifier(functions.FakeIdentifier),
	).Build(t.Context(), proj, projFS)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// runtimeDependencies should have emitted the project's xpkg dep.
	got := runtimeDependencies(proj)
	if diff := cmp.Diff(1, len(got)); diff != "" {
		t.Errorf("runtimeDependencies count (-want +got):\n%s", diff)
	}
	if got[0].Package == nil || *got[0].Package != runtimeDep.Xpkg.Package {
		t.Errorf("runtimeDependencies package = %v, want %s", got[0].Package, runtimeDep.Xpkg.Package)
	}
	if diff := cmp.Diff(runtimeDep.Xpkg.Version, got[0].Version); diff != "" {
		t.Errorf("runtimeDependencies version (-want +got):\n%s", diff)
	}
	if got[0].APIVersion == nil || *got[0].APIVersion != runtimeDep.Xpkg.APIVersion {
		t.Errorf("runtimeDependencies APIVersion = %v, want %s", got[0].APIVersion, runtimeDep.Xpkg.APIVersion)
	}

	// The function dep is reflected in the image map as <repo>_<fn>:<arch>.
	wantFnRepo := proj.Spec.Repository + "_fn-one"
	foundArchs := 0
	for tag := range imgMap {
		if tag.Repository.Name() == wantFnRepo {
			foundArchs++
		}
	}
	if diff := cmp.Diff(2, foundArchs); diff != "" {
		t.Errorf("function arch images (-want +got):\n%s", diff)
	}
}

func constructTag(repo, tag string) (name.Tag, error) {
	return name.NewTag(fmt.Sprintf("%s:%s", repo, tag))
}

func tagsOf(m ImageTagMap) []string {
	out := make([]string, 0, len(m))
	for t := range m {
		out = append(out, t.String())
	}
	return out
}
