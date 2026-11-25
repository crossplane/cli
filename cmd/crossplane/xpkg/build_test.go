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
	"archive/tar"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"

	"github.com/crossplane/cli/v2/internal/terminal"
	"github.com/crossplane/cli/v2/internal/testutils/xpkgtest"
)

// packageContents returns the file names in the image's flattened filesystem
// and the content of its package.yaml.
func packageContents(t *testing.T, path string) ([]string, string) {
	t.Helper()
	img, err := tarball.ImageFromPath(path, nil)
	if err != nil {
		t.Fatalf("failed to read package %s: %v", path, err)
	}
	rc := mutate.Extract(img)
	defer rc.Close()
	var names []string
	var pkg string
	tr := tar.NewReader(rc)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
		if h.Name == xpkg.StreamFile {
			b, err := io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}
			pkg = string(b)
		}
	}
	slices.Sort(names)
	return names, pkg
}

func TestBuildCmdRun(t *testing.T) {
	type args struct {
		// fixture is the testdata tree copied into the package root.
		fixture string
		// remove lists paths deleted from the copied fixture.
		remove      []string
		packageFile string
		ignore      []string
	}
	type want struct {
		// output is the package file name; a generated name is matched by prefix.
		output string
		files  []string
		hasXRD bool
		err    error
		// errPrefix is our own wrap of a cause we don't control.
		errPrefix string
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"DefaultOutputName": {
			reason: "A package should be written to <root>/<name>-<digest>.xpkg with its resources and examples.",
			args:   args{fixture: "testdata/package/configuration"},
			want: want{
				output: "test-config-",
				files:  []string{xpkg.XpkgExamplesFile, xpkg.StreamFile},
				hasXRD: true,
			},
		},
		"ExplicitOutputAndIgnore": {
			reason: "--package-file should set the output path, and --ignore should drop matching files from the package.",
			args: args{
				fixture:     "testdata/package/configuration",
				remove:      []string{"examples"},
				packageFile: "out.xpkg",
				ignore:      []string{"apis/xrd.yaml"},
			},
			want: want{
				output: "out.xpkg",
				files:  []string{xpkg.StreamFile},
			},
		},
		"MissingMetadata": {
			reason: "A package root without crossplane.yaml should fail to build.",
			args:   args{fixture: "testdata/package/configuration", remove: []string{"crossplane.yaml"}},
			want:   want{err: errors.Wrap(errors.New("not exactly one package meta type"), errBuildPackage)},
		},
		"InvalidResource": {
			reason: "A configuration containing a CRD should fail the configuration linter.",
			args:   args{fixture: "testdata/package/invalid-configuration"},
			// The cause lists crossplane-runtime's linter messages.
			want: want{errPrefix: errBuildPackage + ": failed to lint package: "},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.CopyFS(root, os.DirFS(tc.args.fixture)); err != nil {
				t.Fatal(err)
			}
			// Some cases build from part of the fixture.
			for _, p := range tc.args.remove {
				if err := os.RemoveAll(filepath.Join(root, p)); err != nil {
					t.Fatal(err)
				}
			}

			c := &buildCmd{
				PackageRoot:  root,
				ExamplesRoot: filepath.Join(root, "examples"),
				Ignore:       tc.args.ignore,
			}
			if tc.args.packageFile != "" {
				c.PackageFile = filepath.Join(root, tc.args.packageFile)
			}
			if err := c.AfterApply(); err != nil {
				t.Fatal(err)
			}

			err := c.Run(logging.NewNopLogger(), terminal.NewNopSpinnerPrinter())
			if tc.want.errPrefix != "" {
				// Only our wrap prefix is ours; see the case for the cause.
				if err == nil || !strings.HasPrefix(err.Error(), tc.want.errPrefix) {
					t.Fatalf("\n%s\nRun(...): want error starting %q, got %v", tc.reason, tc.want.errPrefix, err)
				}
				return
			}
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Fatalf("\n%s\nRun(...): -want error, +got error:\n%s", tc.reason, diff)
			}
			if tc.want.err != nil {
				return
			}

			matches, err := filepath.Glob(filepath.Join(root, "*.xpkg"))
			if err != nil {
				t.Fatal(err)
			}
			if len(matches) != 1 || !strings.HasPrefix(filepath.Base(matches[0]), tc.want.output) {
				t.Fatalf("\n%s\nRun(...): want one package named %s*, got %v", tc.reason, tc.want.output, matches)
			}

			files, pkg := packageContents(t, matches[0])
			if diff := cmp.Diff(tc.want.files, files); diff != "" {
				t.Errorf("\n%s\nRun(...): package files -want, +got:\n%s", tc.reason, diff)
			}
			if got := strings.Contains(pkg, "CompositeResourceDefinition"); got != tc.want.hasXRD {
				t.Errorf("\n%s\nRun(...): package.yaml contains XRD = %t, want %t", tc.reason, got, tc.want.hasXRD)
			}
		})
	}
}

func TestBuildCmdRuntimeImageTarball(t *testing.T) {
	root := t.TempDir()
	// Only the metadata: examples would add a layer.
	meta, err := os.ReadFile("testdata/package/configuration/crossplane.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "crossplane.yaml"), meta, 0o600); err != nil {
		t.Fatal(err)
	}

	base := xpkgtest.Image(t, xpkgtest.WithPlatform("linux", "arm64"))
	baseLayers, err := base.Layers()
	if err != nil {
		t.Fatal(err)
	}
	tb := filepath.Join(t.TempDir(), "runtime.tar")
	if err := tarball.WriteToFile(tb, nil, base); err != nil {
		t.Fatal(err)
	}

	missing := filepath.Join(root, "missing.tar")

	type args struct {
		tarball string
	}
	type want struct {
		err error
	}
	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"EmbedsBase": {
			reason: "The runtime tarball's layers and platform should be the base of the package.",
			args:   args{tarball: tb},
		},
		"MissingTarball": {
			reason: "An unreadable runtime tarball should fail before building.",
			args:   args{tarball: missing},
			want: want{err: errors.Wrap(
				errors.Wrap(openErr(missing), errLoadRuntimeTarball),
				errGetRuntimeBaseImageOpts)},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "pkg.xpkg")
			c := &buildCmd{PackageRoot: root, ExamplesRoot: filepath.Join(root, "examples"), PackageFile: out, EmbedRuntimeImageTarball: tc.args.tarball}
			if err := c.AfterApply(); err != nil {
				t.Fatal(err)
			}
			err := c.Run(logging.NewNopLogger(), terminal.NewNopSpinnerPrinter())
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Fatalf("\n%s\nRun(...): -want error, +got error:\n%s", tc.reason, diff)
			}
			if tc.want.err != nil {
				return
			}
			img, err := tarball.ImageFromPath(out, nil)
			if err != nil {
				t.Fatal(err)
			}
			layers, err := img.Layers()
			if err != nil {
				t.Fatal(err)
			}
			if len(layers) != len(baseLayers)+1 {
				t.Errorf("\n%s\nRun(...): want %d layers (base + package), got %d", tc.reason, len(baseLayers)+1, len(layers))
			}
			cfg, err := img.ConfigFile()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Architecture != "arm64" {
				t.Errorf("\n%s\nRun(...): want base architecture arm64, got %q", tc.reason, cfg.Architecture)
			}
		})
	}
}
