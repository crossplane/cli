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

package docker

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/moby/moby/client"
)

func TestStartAndAttach(t *testing.T) {
	errStart := errors.New("start failed")
	errAttach := errors.New("attach failed")

	cases := map[string]struct {
		stdin       bool
		startErr    error
		attachErr   error
		wantCalls   []string
		wantErr     string
		wantOptions client.ContainerAttachOptions
	}{
		"Success": {
			stdin:     true,
			wantCalls: []string{"start", "attach"},
			wantOptions: client.ContainerAttachOptions{
				Stream: true,
				Stdout: true,
				Stderr: true,
				Stdin:  true,
				Logs:   true,
			},
		},
		"StartFailureDoesNotAttach": {
			startErr:  errStart,
			wantCalls: []string{"start"},
			wantErr:   "failed to start container: start failed",
		},
		"AttachFailure": {
			attachErr: errAttach,
			wantCalls: []string{"start", "attach"},
			wantErr:   "failed to attach to container: attach failed",
			wantOptions: client.ContainerAttachOptions{
				Stream: true,
				Stdout: true,
				Stderr: true,
				Logs:   true,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			calls := []string{}
			var gotOptions client.ContainerAttachOptions
			_, err := startAndAttach(context.Background(), "container-id", tc.stdin, runContainerCalls{
				start: func(_ context.Context, id string, _ client.ContainerStartOptions) error {
					if id != "container-id" {
						t.Errorf("start id = %q, want container-id", id)
					}
					calls = append(calls, "start")
					return tc.startErr
				},
				attach: func(_ context.Context, id string, opts client.ContainerAttachOptions) (client.ContainerAttachResult, error) {
					if id != "container-id" {
						t.Errorf("attach id = %q, want container-id", id)
					}
					calls = append(calls, "attach")
					gotOptions = opts
					return client.ContainerAttachResult{}, tc.attachErr
				},
			})

			if strings.Join(calls, ",") != strings.Join(tc.wantCalls, ",") {
				t.Errorf("calls = %v, want %v", calls, tc.wantCalls)
			}
			if gotOptions != tc.wantOptions {
				t.Errorf("attach options = %+v, want %+v", gotOptions, tc.wantOptions)
			}
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && (err == nil || err.Error() != tc.wantErr):
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}
