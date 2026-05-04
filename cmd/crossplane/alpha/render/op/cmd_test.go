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

package op

import (
	"bytes"
	"context"
	"io"
	"testing"
	"testing/fstest"
	"time"

	"github.com/alecthomas/kong"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/spf13/afero"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"
	"github.com/crossplane/crossplane/v2/cmd/crank/render"
	renderv1alpha1 "github.com/crossplane/crossplane/v2/proto/render/v1alpha1"

	_ "embed"
)

//go:embed testdata/cmd/operation.yaml
var operationYAML string

//go:embed testdata/cmd/operation-not-op.yaml
var operationNotOpYAML string

//go:embed testdata/cmd/functions.yaml
var functionsYAML string

//go:embed testdata/cmd/watched-multi.yaml
var watchedMultiYAML string

//go:embed testdata/cmd/output/success.yaml
var successOutput string

//go:embed testdata/cmd/output/include-function-results.yaml
var includeFunctionResultsOutput string

//go:embed testdata/cmd/output/include-full-operation.yaml
var includeFullOperationOutput string

func newEngineFunc(engine render.Engine) func(*render.EngineFlags, logging.Logger) render.Engine {
	return func(*render.EngineFlags, logging.Logger) render.Engine {
		return engine
	}
}

// newTestFS builds an in-memory filesystem seeded with the default happy-path
// fixtures. Entries in extra are overlaid on top; an entry with an empty value
// removes the file from the FS.
func newTestFS(extra map[string]string) afero.Fs {
	files := map[string]*fstest.MapFile{
		"operation.yaml": {Data: []byte(operationYAML)},
		"functions.yaml": {Data: []byte(functionsYAML)},
	}
	for k, v := range extra {
		if v == "" {
			delete(files, k)
			continue
		}
		files[k] = &fstest.MapFile{Data: []byte(v)}
	}
	return afero.FromIOFS{FS: fstest.MapFS(files)}
}

func mustNewStruct(t *testing.T, data map[string]any) *structpb.Struct {
	t.Helper()
	s, err := structpb.NewStruct(data)
	if err != nil {
		t.Fatalf("structpb.NewStruct: %v", err)
	}
	return s
}

func TestCmdRun(t *testing.T) {
	type args struct {
		cmd Cmd
	}
	type want struct {
		err    error
		stdout string
	}
	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"Success": {
			reason: "Happy path: load fixtures, render an Operation, and emit YAML for the operation and one applied resource.",
			args: args{
				cmd: Cmd{
					Operation: "operation.yaml",
					Functions: "functions.yaml",
					Timeout:   time.Minute,
					fs:        newTestFS(nil),
					newEngine: newEngineFunc(&render.MockEngine{
						MockRender: func(_ context.Context, req *renderv1alpha1.RenderRequest) (*renderv1alpha1.RenderResponse, error) {
							return &renderv1alpha1.RenderResponse{
								Output: &renderv1alpha1.RenderResponse_Operation{
									Operation: &renderv1alpha1.OperationOutput{
										Operation: req.GetOperation().GetOperation(),
										AppliedResources: []*structpb.Struct{
											mustNewStruct(t, map[string]any{
												"apiVersion": "example.org/v1alpha1",
												"kind":       "AppliedResource",
												"metadata": map[string]any{
													"name": "applied-foo",
												},
												"spec": map[string]any{"coolField": "applied!"},
											}),
										},
									},
								},
							}, nil
						},
					}),
				},
			},
			want: want{
				stdout: successOutput,
			},
		},
		"LoadOperationError": {
			reason: "Missing operation file should return a wrapped load error.",
			args: args{
				cmd: Cmd{
					Operation: "missing.yaml",
					Functions: "functions.yaml",
					Timeout:   time.Minute,
					fs:        newTestFS(nil),
				},
			},
			want: want{err: cmpopts.AnyError},
		},
		"LoadOperationWrongKind": {
			reason: "An input that is not an Operation/CronOperation/WatchOperation should error.",
			args: args{
				cmd: Cmd{
					Operation: "operation.yaml",
					Functions: "functions.yaml",
					Timeout:   time.Minute,
					fs:        newTestFS(map[string]string{"operation.yaml": operationNotOpYAML}),
				},
			},
			want: want{err: cmpopts.AnyError},
		},
		"LoadRequiredResourcesError": {
			reason: "Missing required resources file should return a wrapped load error.",
			args: args{
				cmd: Cmd{
					Operation:         "operation.yaml",
					Functions:         "functions.yaml",
					RequiredResources: "missing.yaml",
					Timeout:           time.Minute,
					fs:                newTestFS(nil),
				},
			},
			want: want{err: cmpopts.AnyError},
		},
		"LoadRequiredSchemasError": {
			reason: "Missing required schemas directory should return a wrapped load error.",
			args: args{
				cmd: Cmd{
					Operation:       "operation.yaml",
					Functions:       "functions.yaml",
					RequiredSchemas: "missing",
					Timeout:         time.Minute,
					fs:              newTestFS(nil),
				},
			},
			want: want{err: cmpopts.AnyError},
		},
		"LoadWatchedResourceError": {
			reason: "Missing watched resource file should return a wrapped load error.",
			args: args{
				cmd: Cmd{
					Operation:       "operation.yaml",
					Functions:       "functions.yaml",
					WatchedResource: "missing.yaml",
					Timeout:         time.Minute,
					fs:              newTestFS(nil),
				},
			},
			want: want{err: cmpopts.AnyError},
		},
		"WatchedResourceNotExactlyOne": {
			reason: "The watched resource file must contain exactly one resource.",
			args: args{
				cmd: Cmd{
					Operation:       "operation.yaml",
					Functions:       "functions.yaml",
					WatchedResource: "watched.yaml",
					Timeout:         time.Minute,
					fs:              newTestFS(map[string]string{"watched.yaml": watchedMultiYAML}),
				},
			},
			want: want{err: cmpopts.AnyError},
		},
		"LoadFunctionsError": {
			reason: "Missing functions file should return a wrapped load error.",
			args: args{
				cmd: Cmd{
					Operation: "operation.yaml",
					Functions: "missing.yaml",
					Timeout:   time.Minute,
					fs:        newTestFS(nil),
				},
			},
			want: want{err: cmpopts.AnyError},
		},
		"InvalidAnnotationOverride": {
			reason: "Function annotation overrides must be in key=value form.",
			args: args{
				cmd: Cmd{
					Operation:           "operation.yaml",
					Functions:           "functions.yaml",
					FunctionAnnotations: []string{"not-a-key-value"},
					Timeout:             time.Minute,
					fs:                  newTestFS(nil),
				},
			},
			want: want{err: cmpopts.AnyError},
		},
		"LoadFunctionCredentialsError": {
			reason: "Missing function credentials file should return a wrapped load error.",
			args: args{
				cmd: Cmd{
					Operation:           "operation.yaml",
					Functions:           "functions.yaml",
					FunctionCredentials: "missing.yaml",
					Timeout:             time.Minute,
					fs:                  newTestFS(nil),
				},
			},
			want: want{err: cmpopts.AnyError},
		},
		"EngineSetupError": {
			reason: "Engine.Setup failures should propagate.",
			args: args{
				cmd: Cmd{
					Operation: "operation.yaml",
					Functions: "functions.yaml",
					Timeout:   time.Minute,
					fs:        newTestFS(nil),
					newEngine: newEngineFunc(&render.MockEngine{
						MockSetup: func(_ context.Context, _ []pkgv1.Function) (func(), error) {
							return func() {}, errors.New("setup blew up")
						},
					}),
				},
			},
			want: want{err: cmpopts.AnyError},
		},
		"EngineRenderError": {
			reason: "Engine.Render failures should be wrapped.",
			args: args{
				cmd: Cmd{
					Operation: "operation.yaml",
					Functions: "functions.yaml",
					Timeout:   time.Minute,
					fs:        newTestFS(nil),
					newEngine: newEngineFunc(&render.MockEngine{
						MockRender: func(_ context.Context, _ *renderv1alpha1.RenderRequest) (*renderv1alpha1.RenderResponse, error) {
							return nil, errors.New("render blew up")
						},
					}),
				},
			},
			want: want{err: cmpopts.AnyError},
		},
		"RenderResponseMissingOperation": {
			reason: "A RenderResponse without an operation output should error.",
			args: args{
				cmd: Cmd{
					Operation: "operation.yaml",
					Functions: "functions.yaml",
					Timeout:   time.Minute,
					fs:        newTestFS(nil),
					newEngine: newEngineFunc(&render.MockEngine{
						MockRender: func(_ context.Context, _ *renderv1alpha1.RenderRequest) (*renderv1alpha1.RenderResponse, error) {
							return &renderv1alpha1.RenderResponse{}, nil
						},
					}),
				},
			},
			want: want{err: cmpopts.AnyError},
		},
		"IncludeFunctionResults": {
			reason: "When --include-function-results is set, Result documents should appear in stdout.",
			args: args{
				cmd: Cmd{
					Operation:              "operation.yaml",
					Functions:              "functions.yaml",
					IncludeFunctionResults: true,
					Timeout:                time.Minute,
					fs:                     newTestFS(nil),
					newEngine: newEngineFunc(&render.MockEngine{
						MockRender: func(_ context.Context, req *renderv1alpha1.RenderRequest) (*renderv1alpha1.RenderResponse, error) {
							return &renderv1alpha1.RenderResponse{
								Output: &renderv1alpha1.RenderResponse_Operation{
									Operation: &renderv1alpha1.OperationOutput{
										Operation: req.GetOperation().GetOperation(),
										Events: []*renderv1alpha1.Event{{
											Type:    "Normal",
											Reason:  "Hello",
											Message: "function says hi",
										}},
									},
								},
							}, nil
						},
					}),
				},
			},
			want: want{
				stdout: includeFunctionResultsOutput,
			},
		},
		"IncludeFullOperation": {
			reason: "With --include-full-operation, the rendered Operation includes the original spec.",
			args: args{
				cmd: Cmd{
					Operation:            "operation.yaml",
					Functions:            "functions.yaml",
					IncludeFullOperation: true,
					Timeout:              time.Minute,
					fs:                   newTestFS(nil),
					newEngine: newEngineFunc(&render.MockEngine{
						MockRender: func(_ context.Context, req *renderv1alpha1.RenderRequest) (*renderv1alpha1.RenderResponse, error) {
							return &renderv1alpha1.RenderResponse{
								Output: &renderv1alpha1.RenderResponse_Operation{
									Operation: &renderv1alpha1.OperationOutput{
										Operation: req.GetOperation().GetOperation(),
									},
								},
							}, nil
						},
					}),
				},
			},
			want: want{
				stdout: includeFullOperationOutput,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			buf := &bytes.Buffer{}
			kctx := &kong.Context{Kong: &kong.Kong{Stdout: buf, Stderr: io.Discard}}

			err := tc.args.cmd.Run(kctx, logging.NewNopLogger())
			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nRun(...): -want error, +got error:\n%s", tc.reason, diff)
			}

			if diff := cmp.Diff(tc.want.stdout, buf.String()); diff != "" {
				t.Errorf("\n%s\nRun(...): -want stdout +got stdout:\n%s", tc.reason, diff)
			}
		})
	}
}
