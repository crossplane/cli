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

package runner

import (
	"context"
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/afero"

	"github.com/crossplane/crossplane-runtime/v2/pkg/test"

	"github.com/crossplane/cli/v2/internal/filesystem"

	_ "embed"
)

//go:embed testdata/template.fn.crossplane.io_kclinputs.yaml
var crd []byte

func TestRunContainerWithKCL(t *testing.T) {
	type withFsFn func() afero.Fs

	type args struct {
		baseFolder string
		fs         withFsFn
		runner     SchemaRunner
	}

	type want struct {
		err error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"SuccessWithAccountScaffoldDefinition": {
			reason: "Should successfully generate with crd using MockSchemaRunner.",
			args: args{
				baseFolder: "data/input",
				fs: func() afero.Fs {
					fs := afero.NewMemMapFs()
					_ = fs.Mkdir("data/input", os.ModePerm)
					_ = afero.WriteFile(fs, "data/input/template.fn.crossplane.io_kclinputs.yaml", crd, os.ModePerm)
					return fs
				},
				runner: &MockSchemaRunner{},
			},
			want: want{
				err: nil,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fs := tc.args.fs()

			ctx := context.Background()
			err := tc.args.runner.Generate(ctx, fs, tc.args.baseFolder, "", "mockImage", []string{})

			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nRunContainer(...): -want err, +got err:\n%s", tc.reason, diff)
			}

			outputExists, _ := afero.Exists(fs, "models/k8s/apimachinery/pkg/apis/meta/v1/managed_fields_entry.k")
			if !outputExists {
				t.Errorf("\n%s\nExpected output file not found in in-memory fs", tc.reason)
			}
		})
	}
}

func TestCreateTarFromFs(t *testing.T) {
	type withFsFn func() afero.Fs
	type args struct {
		baseFolder string
		fs         withFsFn
	}
	type want struct {
		tarFileExists bool
		err           error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"SuccessWithValidTar": {
			reason: "Should successfully create tar from valid file system.",
			args: args{
				baseFolder: ".",
				fs: func() afero.Fs {
					fs := afero.NewMemMapFs()
					_ = afero.WriteFile(fs, "file.txt", []byte("hello world"), 0o644)
					return fs
				},
			},
			want: want{
				tarFileExists: true,
				err:           nil,
			},
		},
		"FailWithInvalidPath": {
			reason: "Will not fail to create tar due to invalid file path.",
			args: args{
				baseFolder: "/invalid",
				fs:         afero.NewMemMapFs,
			},
			want: want{
				tarFileExists: true,
				err:           nil,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fs := tc.args.fs()

			tarBuffer, err := filesystem.FSToTar(fs, tc.args.baseFolder)

			tarFileExists := len(tarBuffer) > 0
			if diff := cmp.Diff(tc.want.tarFileExists, tarFileExists); diff != "" {
				t.Errorf("\n%s\ncreateTarFromFs(...): -want tar file, +got no tar file:\n%s", tc.reason, diff)
			}

			if tc.want.err != nil {
				if err == nil {
					t.Errorf("Expected error but got none for test case: %s", name)
				} else if diff := cmp.Diff(tc.want.err.Error(), err.Error()); diff != "" {
					t.Errorf("\n%s\ncreateTarFromFs(...): -want err, +got err:\n%s", tc.reason, diff)
				}
			} else if err != nil {
				t.Errorf("Unexpected error for test case %s: %v", name, err)
			}
		})
	}
}

// MockSchemaRunner simulates a successful container run by generating output in-memory.
type MockSchemaRunner struct{}

// Generate runs MockSchemaRunner generate for Schema.
func (m *MockSchemaRunner) Generate(_ context.Context, fs afero.Fs, _, _ string, _ string, _ []string, _ ...Option) error {
	outputPath := "models/k8s/apimachinery/pkg/apis/meta/v1/managed_fields_entry.k"
	_ = fs.MkdirAll("models/k8s/apimachinery/pkg/apis/meta/v1", os.ModePerm)
	return afero.WriteFile(fs, outputPath, []byte("mock content"), os.ModePerm)
}
