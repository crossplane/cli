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
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	renderv1alpha1 "github.com/crossplane/cli/v2/proto/render/v1alpha1"
)

// envHelperMode is the env var that, when set on a re-exec of the test binary,
// causes TestMain to dispatch to runRenderHelper instead of running tests.
// The value selects which canned response the helper emits.
const envHelperMode = "GO_HELPER_LOCAL_RENDER_MODE"

// TestMain re-uses os.Args[0] as a stand-in for the `crossplane` binary when
// the engine_local tests want to control its stdout/stderr/exit-code. When
// envHelperMode is set we play the helper role and exit; otherwise we run the
// normal test suite.
func TestMain(m *testing.M) {
	if mode := os.Getenv(envHelperMode); mode != "" {
		runRenderHelper(mode)
		// runRenderHelper always exits.
	}
	os.Exit(m.Run())
}

// runRenderHelper emulates `crossplane internal render` for a single canned
// scenario. It reads (and discards) stdin, optionally writes a marshaled
// RenderResponse to stdout, optionally writes a message to stderr, and exits
// with the scenario's exit code.
func runRenderHelper(mode string) {
	_, _ = io.Copy(io.Discard, os.Stdin)

	rsp := &renderv1alpha1.RenderResponse{
		Output: &renderv1alpha1.RenderResponse_Composite{
			Composite: &renderv1alpha1.CompositeOutput{},
		},
	}
	rspBytes, err := proto.Marshal(rsp)
	if err != nil {
		fmt.Fprintf(os.Stderr, "helper: cannot marshal canned response: %v\n", err)
		os.Exit(127)
	}

	switch mode {
	case "success":
		_, _ = os.Stdout.Write(rspBytes)
		os.Exit(0)
	case "fatal-with-partial":
		_, _ = os.Stdout.Write(rspBytes)
		fmt.Fprint(os.Stderr, "boom: pipeline step requested fatal")
		os.Exit(ExitCodePipelineFatal)
	case "fatal-no-partial":
		fmt.Fprint(os.Stderr, "boom: pipeline step requested fatal but produced no output")
		os.Exit(ExitCodePipelineFatal)
	case "hard-fail":
		fmt.Fprint(os.Stderr, "the binary is sad")
		os.Exit(1)
	default:
		fmt.Fprintf(os.Stderr, "helper: unknown mode %q\n", mode)
		os.Exit(127)
	}
}

func TestLocalRenderEngine_Render(t *testing.T) {
	cases := map[string]struct {
		mode      string
		wantRsp   bool
		wantErr   bool
		wantInErr []string
	}{
		"Success": {
			mode:    "success",
			wantRsp: true,
		},
		"FatalWithPartialOutput": {
			mode:    "fatal-with-partial",
			wantRsp: true,
			wantErr: true,
			wantInErr: []string{
				"pipeline returned fatal",
				"boom: pipeline step requested fatal",
			},
		},
		"FatalWithNoPartialOutput": {
			mode:    "fatal-no-partial",
			wantRsp: false,
			wantErr: true,
			wantInErr: []string{
				"cannot run crossplane internal render",
				"pipeline step requested fatal but produced no output",
			},
		},
		"HardFail": {
			mode:    "hard-fail",
			wantRsp: false,
			wantErr: true,
			wantInErr: []string{
				"cannot run crossplane internal render",
				"the binary is sad",
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv(envHelperMode, tc.mode)

			e := &localRenderEngine{BinaryPath: os.Args[0]}

			rsp, err := e.Render(context.Background(), &renderv1alpha1.RenderRequest{})

			switch {
			case tc.wantErr && err == nil:
				t.Fatalf("Render(): want error, got nil")
			case !tc.wantErr && err != nil:
				t.Fatalf("Render(): unexpected error: %v", err)
			}

			for _, want := range tc.wantInErr {
				if err == nil {
					t.Errorf("Render(): error is nil but expected to contain %q", want)
					continue
				}
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Render(): error %q does not contain %q", err.Error(), want)
				}
			}

			switch {
			case tc.wantRsp && rsp == nil:
				t.Errorf("Render(): want non-nil response, got nil")
			case !tc.wantRsp && rsp != nil:
				t.Errorf("Render(): want nil response, got %+v", rsp)
			}
		})
	}
}
