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

package render

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
	renderv1alpha1 "github.com/crossplane/crossplane/v2/proto/render/v1alpha1"

	_ "embed"
)

//go:embed testdata/cmd/xr.yaml
var xrYAML string

//go:embed testdata/cmd/xr-extra-spec.yaml
var xrWithExtraSpecYAML string

//go:embed testdata/cmd/xr-wrong-kind.yaml
var xrWrongKindYAML string

//go:embed testdata/cmd/xr-wrong-apiversion.yaml
var xrWrongAPIVersionYAML string

//go:embed testdata/cmd/xr-with-selector.yaml
var xrWithSelectorYAML string

//go:embed testdata/cmd/composition.yaml
var compositionYAML string

//go:embed testdata/cmd/composition-label-mismatch.yaml
var compositionLabelMismatchYAML string

//go:embed testdata/cmd/composition-not-pipeline.yaml
var compositionNotPipelineYAML string

//go:embed testdata/cmd/functions.yaml
var functionsYAML string

//go:embed testdata/cmd/output/success.yaml
var successOutput string

//go:embed testdata/cmd/output/include-function-results.yaml
var includeFunctionResultsOutput string

//go:embed testdata/cmd/output/include-full-xr.yaml
var includeFullXROutput string

func newEngineFunc(engine Engine) func(*EngineFlags, logging.Logger) Engine {
	return func(*EngineFlags, logging.Logger) Engine {
		return engine
	}
}

// newTestFS builds an in-memory filesystem seeded with the default happy-path
// fixtures. Entries in extra are overlaid on top; an entry with an empty value
// removes the file from the FS.
func newTestFS(extra map[string]string) afero.Fs {
	files := map[string]*fstest.MapFile{
		"xr.yaml":          {Data: []byte(xrYAML)},
		"composition.yaml": {Data: []byte(compositionYAML)},
		"functions.yaml":   {Data: []byte(functionsYAML)},
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

func fillResourceRefs(t *testing.T, xr *structpb.Struct) *structpb.Struct {
	t.Helper()

	xr.Fields["spec"] = structpb.NewStructValue(mustNewStruct(t, map[string]any{
		"crossplane": map[string]any{
			"resourceRefs": "these are the resource refs",
		},
	}))

	return xr
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
			reason: "Happy path: load fixtures, render, and emit YAML for the composite and one composed resource.",
			args: args{
				cmd: Cmd{
					CompositeResource: "xr.yaml",
					Composition:       "composition.yaml",
					Functions:         "functions.yaml",
					Timeout:           time.Minute,
					fs:                newTestFS(nil),
					newEngine: newEngineFunc(&MockEngine{
						MockRender: func(_ context.Context, req *renderv1alpha1.RenderRequest) (*renderv1alpha1.RenderResponse, error) {
							return &renderv1alpha1.RenderResponse{
								Output: &renderv1alpha1.RenderResponse_Composite{
									Composite: &renderv1alpha1.CompositeOutput{
										CompositeResource: fillResourceRefs(t, req.GetComposite().GetCompositeResource()),
										ComposedResources: []*structpb.Struct{
											mustNewStruct(t, map[string]any{
												"apiVersion": "example.org/v1alpha1",
												"kind":       "ComposedResource",
												"metadata": map[string]any{
													"name": "composed-foo",
													"annotations": map[string]any{
														"crossplane.io/composition-resource-name": "composed-foo",
													},
												},
												"spec": map[string]any{"coolField": "composed!"},
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
		"LoadCompositeResourceError": {
			reason: "Missing XR file should return a wrapped load error.",
			args: args{
				cmd: Cmd{
					CompositeResource: "missing.yaml",
					Composition:       "composition.yaml",
					Functions:         "functions.yaml",
					Timeout:           time.Minute,
					fs:                newTestFS(nil),
				},
			},
			want: want{
				err: cmpopts.AnyError,
			},
		},
		"LoadCompositionError": {
			reason: "Missing Composition file should return a wrapped load error.",
			args: args{
				cmd: Cmd{
					CompositeResource: "xr.yaml",
					Composition:       "missing.yaml",
					Functions:         "functions.yaml",
					Timeout:           time.Minute,
					fs:                newTestFS(nil),
				},
			},
			want: want{err: cmpopts.AnyError},
		},
		"CompositeTypeRefKindMismatch": {
			reason: "XR kind must match Composition's compositeTypeRef.kind.",
			args: args{
				cmd: Cmd{
					CompositeResource: "xr.yaml",
					Composition:       "composition.yaml",
					Functions:         "functions.yaml",
					Timeout:           time.Minute,
					fs:                newTestFS(map[string]string{"xr.yaml": xrWrongKindYAML}),
				},
			},
			want: want{err: cmpopts.AnyError},
		},
		"CompositeTypeRefAPIVersionMismatch": {
			reason: "XR apiVersion must match Composition's compositeTypeRef.apiVersion.",
			args: args{
				cmd: Cmd{
					CompositeResource: "xr.yaml",
					Composition:       "composition.yaml",
					Functions:         "functions.yaml",
					Timeout:           time.Minute,
					fs:                newTestFS(map[string]string{"xr.yaml": xrWrongAPIVersionYAML}),
				},
			},
			want: want{err: cmpopts.AnyError},
		},
		"MatchLabelMissing": {
			reason: "Composition must carry every label declared in the XR's compositionSelector.matchLabels.",
			args: args{
				cmd: Cmd{
					CompositeResource: "xr.yaml",
					Composition:       "composition.yaml",
					Functions:         "functions.yaml",
					Timeout:           time.Minute,
					fs:                newTestFS(map[string]string{"xr.yaml": xrWithSelectorYAML}),
				},
			},
			want: want{err: cmpopts.AnyError},
		},
		"MatchLabelMismatch": {
			reason: "Composition's label value must equal the XR's matchLabels value.",
			args: args{
				cmd: Cmd{
					CompositeResource: "xr.yaml",
					Composition:       "composition.yaml",
					Functions:         "functions.yaml",
					Timeout:           time.Minute,
					fs: newTestFS(map[string]string{
						"xr.yaml":          xrWithSelectorYAML,
						"composition.yaml": compositionLabelMismatchYAML,
					}),
				},
			},
			want: want{err: cmpopts.AnyError},
		},
		"NotPipelineMode": {
			reason: "render only supports Composition function pipelines.",
			args: args{
				cmd: Cmd{
					CompositeResource: "xr.yaml",
					Composition:       "composition.yaml",
					Functions:         "functions.yaml",
					Timeout:           time.Minute,
					fs:                newTestFS(map[string]string{"composition.yaml": compositionNotPipelineYAML}),
				},
			},
			want: want{err: cmpopts.AnyError},
		},
		"LoadFunctionsError": {
			reason: "Missing functions file should return a wrapped load error.",
			args: args{
				cmd: Cmd{
					CompositeResource: "xr.yaml",
					Composition:       "composition.yaml",
					Functions:         "missing.yaml",
					Timeout:           time.Minute,
					fs:                newTestFS(nil),
				},
			},
			want: want{err: cmpopts.AnyError},
		},
		"InvalidAnnotationOverride": {
			reason: "Function annotation overrides must be in key=value form.",
			args: args{
				cmd: Cmd{
					CompositeResource:   "xr.yaml",
					Composition:         "composition.yaml",
					Functions:           "functions.yaml",
					FunctionAnnotations: []string{"not-a-key-value"},
					Timeout:             time.Minute,
					fs:                  newTestFS(nil),
				},
			},
			want: want{err: cmpopts.AnyError},
		},
		"LoadXRDError": {
			reason: "Missing XRD file should return a wrapped load error.",
			args: args{
				cmd: Cmd{
					CompositeResource: "xr.yaml",
					Composition:       "composition.yaml",
					Functions:         "functions.yaml",
					XRD:               "missing.yaml",
					Timeout:           time.Minute,
					fs:                newTestFS(nil),
				},
			},
			want: want{err: cmpopts.AnyError},
		},
		"LoadFunctionCredentialsError": {
			reason: "Missing function credentials file should return a wrapped load error.",
			args: args{
				cmd: Cmd{
					CompositeResource:   "xr.yaml",
					Composition:         "composition.yaml",
					Functions:           "functions.yaml",
					FunctionCredentials: "missing.yaml",
					Timeout:             time.Minute,
					fs:                  newTestFS(nil),
				},
			},
			want: want{err: cmpopts.AnyError},
		},
		"LoadObservedResourcesError": {
			reason: "Missing observed resources file should return a wrapped load error.",
			args: args{
				cmd: Cmd{
					CompositeResource: "xr.yaml",
					Composition:       "composition.yaml",
					Functions:         "functions.yaml",
					ObservedResources: "missing.yaml",
					Timeout:           time.Minute,
					fs:                newTestFS(nil),
				},
			},
			want: want{err: cmpopts.AnyError},
		},
		"LoadRequiredResourcesError": {
			reason: "Missing required resources file should return a wrapped load error.",
			args: args{
				cmd: Cmd{
					CompositeResource: "xr.yaml",
					Composition:       "composition.yaml",
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
					CompositeResource: "xr.yaml",
					Composition:       "composition.yaml",
					Functions:         "functions.yaml",
					RequiredSchemas:   "missing",
					Timeout:           time.Minute,
					fs:                newTestFS(nil),
				},
			},
			want: want{err: cmpopts.AnyError},
		},
		"EngineSetupError": {
			reason: "Engine.Setup failures should propagate.",
			args: args{
				cmd: Cmd{
					CompositeResource: "xr.yaml",
					Composition:       "composition.yaml",
					Functions:         "functions.yaml",
					Timeout:           time.Minute,
					fs:                newTestFS(nil),
					newEngine: newEngineFunc(&MockEngine{
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
					CompositeResource: "xr.yaml",
					Composition:       "composition.yaml",
					Functions:         "functions.yaml",
					Timeout:           time.Minute,
					fs:                newTestFS(nil),
					newEngine: newEngineFunc(&MockEngine{
						MockRender: func(_ context.Context, _ *renderv1alpha1.RenderRequest) (*renderv1alpha1.RenderResponse, error) {
							return nil, errors.New("render blew up")
						},
					}),
				},
			},
			want: want{err: cmpopts.AnyError},
		},
		"RenderResponseMissingComposite": {
			reason: "A RenderResponse without a composite output should error.",
			args: args{
				cmd: Cmd{
					CompositeResource: "xr.yaml",
					Composition:       "composition.yaml",
					Functions:         "functions.yaml",
					Timeout:           time.Minute,
					fs:                newTestFS(nil),
					newEngine: newEngineFunc(&MockEngine{
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
					CompositeResource:      "xr.yaml",
					Composition:            "composition.yaml",
					Functions:              "functions.yaml",
					IncludeFunctionResults: true,
					Timeout:                time.Minute,
					fs:                     newTestFS(nil),
					newEngine: newEngineFunc(&MockEngine{
						MockRender: func(_ context.Context, req *renderv1alpha1.RenderRequest) (*renderv1alpha1.RenderResponse, error) {
							return &renderv1alpha1.RenderResponse{
								Output: &renderv1alpha1.RenderResponse_Composite{
									Composite: &renderv1alpha1.CompositeOutput{
										CompositeResource: fillResourceRefs(t, req.GetComposite().GetCompositeResource()),
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
		"IncludeFullXR": {
			reason: "With --include-full-xr, the rendered XR is merged into the input XR so the input's spec.fromXR survives alongside any updated fields.",
			args: args{
				cmd: Cmd{
					CompositeResource: "xr.yaml",
					Composition:       "composition.yaml",
					Functions:         "functions.yaml",
					IncludeFullXR:     true,
					Timeout:           time.Minute,
					fs:                newTestFS(map[string]string{"xr.yaml": xrWithExtraSpecYAML}),
					newEngine: newEngineFunc(&MockEngine{
						MockRender: func(_ context.Context, req *renderv1alpha1.RenderRequest) (*renderv1alpha1.RenderResponse, error) {
							return &renderv1alpha1.RenderResponse{
								Output: &renderv1alpha1.RenderResponse_Composite{
									Composite: &renderv1alpha1.CompositeOutput{
										CompositeResource: fillResourceRefs(t, req.GetComposite().GetCompositeResource()),
									},
								},
							}, nil
						},
					}),
				},
			},
			want: want{
				stdout: includeFullXROutput,
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
