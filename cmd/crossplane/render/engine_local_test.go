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

	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/structpb"

	renderv1alpha1 "github.com/crossplane/cli/v2/proto/render/v1alpha1"
)

// envHelperMode drives the "helper process" pattern used by these tests.
// When set in a process's environment, TestMain below dispatches to
// runRenderHelper (which exits) instead of running the test suite.
//
// Why this pattern.  localRenderEngine.Render exec's a binary at a path the
// caller supplies, then reads its stdout, captures its stderr, and
// inspects its exit code. To exercise the four behavioural branches the
// engine cares about (success / pipeline-fatal with partial stdout /
// pipeline-fatal with empty stdout / hard-fail with stderr) we need a
// "binary" that can do each on demand. The options are:
//
//  1. Ship a shell script per case — cross-platform pain, plus shell
//     script bytes are awkward when the binary needs to write a real
//     marshaled RenderResponse to stdout.
//  2. Build a side helper binary in testdata/ — extra build orchestration.
//  3. Re-exec the test binary itself, switching its behaviour on an env
//     var. This is the standard Go stdlib idiom (used in os/exec's own
//     tests) and is what we do here.
//
// How it works.  Tests do `t.Setenv(envHelperMode, "<case>")` and pass
// os.Args[0] as the binary path. The engine exec's that binary; the child
// process inherits the env var, TestMain sees it set, and dispatches to
// runRenderHelper which writes the canned response and exits with the
// chosen code — never reaching m.Run(). The parent reads the result as if
// it had just exec'd a real `crossplane internal render`. When the env
// var is unset (the normal `go test` invocation) TestMain falls through
// to m.Run() and the tests behave like any other Go tests.
//
// The cost is a few extra lines of TestMain plumbing; the benefit is a
// fully self-contained, deterministic, cross-platform fake binary with
// access to the same proto types the tests assert on.
const envHelperMode = "GO_HELPER_LOCAL_RENDER_MODE"

// TestMain re-uses os.Args[0] as a stand-in for the `crossplane` binary when
// the engine_local tests want to control its stdout/stderr/exit-code. When
// envHelperMode is set we play the helper role and exit; otherwise we run the
// normal test suite. See the envHelperMode doc above for why.
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

	rsp := helperResponse()
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

// helperResponse returns the canned RenderResponse the helper writes to
// stdout. Shared with the test so we can assert exact-content round-trip.
func helperResponse() *renderv1alpha1.RenderResponse {
	xr, _ := structpb.NewStruct(map[string]any{
		"apiVersion": "example.org/v1",
		"kind":       "XR",
		"metadata":   map[string]any{"name": "test-xr"},
	})
	return &renderv1alpha1.RenderResponse{
		Output: &renderv1alpha1.RenderResponse_Composite{
			Composite: &renderv1alpha1.CompositeOutput{
				CompositeResource: xr,
			},
		},
	}
}

func TestLocalRenderEngineRender(t *testing.T) {
	type args struct {
		mode string
	}

	type want struct {
		rsp                  *renderv1alpha1.RenderResponse
		wantErr              bool
		wantInErr            []string
		wantSingleOccurrence []string
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"Success": {
			reason: "Render returns the unmarshaled response and no error on a clean exit.",
			args:   args{mode: "success"},
			want:   want{rsp: helperResponse()},
		},
		"FatalWithPartialOutput": {
			reason: "On exit-3 with non-empty stdout, Render parses the partial response and returns it alongside a stderr-bearing error.",
			args:   args{mode: "fatal-with-partial"},
			want: want{
				rsp:     helperResponse(),
				wantErr: true,
				wantInErr: []string{
					"pipeline returned fatal",
					"boom: pipeline step requested fatal",
				},
				wantSingleOccurrence: []string{"boom: pipeline step requested fatal"},
			},
		},
		"FatalWithNoPartialOutput": {
			reason: "On exit-3 with empty stdout, Render falls back to the hard-fail path with stderr in the error.",
			args:   args{mode: "fatal-no-partial"},
			want: want{
				wantErr: true,
				wantInErr: []string{
					"crossplane internal render returned error with output",
					"pipeline step requested fatal but produced no output",
					"exit status 3",
				},
				wantSingleOccurrence: []string{"pipeline step requested fatal but produced no output"},
			},
		},
		"HardFail": {
			reason: "Non-fatal exit codes surface stderr in the returned error.",
			args:   args{mode: "hard-fail"},
			want: want{
				wantErr: true,
				wantInErr: []string{
					"crossplane internal render returned error with output",
					"the binary is sad",
					"exit status 1",
				},
				wantSingleOccurrence: []string{"the binary is sad"},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv(envHelperMode, tc.args.mode)

			e := &localRenderEngine{BinaryPath: os.Args[0]}

			rsp, err := e.Render(context.Background(), &renderv1alpha1.RenderRequest{})

			switch {
			case tc.want.wantErr && err == nil:
				t.Fatalf("\n%s\nRender(...): want error, got nil", tc.reason)
			case !tc.want.wantErr && err != nil:
				t.Fatalf("\n%s\nRender(...): unexpected error: %v", tc.reason, err)
			}

			for _, s := range tc.want.wantInErr {
				if err == nil {
					t.Errorf("\n%s\nRender(...): error is nil but expected to contain %q", tc.reason, s)
					continue
				}
				if !strings.Contains(err.Error(), s) {
					t.Errorf("\n%s\nRender(...): error %q does not contain %q", tc.reason, err.Error(), s)
				}
			}

			for _, s := range tc.want.wantSingleOccurrence {
				if err == nil {
					t.Errorf("\n%s\nRender(...): error is nil but expected exactly one occurrence of %q", tc.reason, s)
					continue
				}
				if got := strings.Count(err.Error(), s); got != 1 {
					t.Errorf("\n%s\nRender(...): error %q contains %q %d times, want exactly 1 (double-formatting bug?)", tc.reason, err.Error(), s, got)
				}
			}

			if diff := cmp.Diff(tc.want.rsp, rsp, protocmp.Transform()); diff != "" {
				t.Errorf("\n%s\nRender(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}
