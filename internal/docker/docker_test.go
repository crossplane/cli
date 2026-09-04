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
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/moby/moby/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

func TestStartAndAttach(t *testing.T) {
	t.Parallel()

	errStart := errors.New("start failed")
	errAttach := errors.New("attach failed")

	type args struct {
		stdin     bool
		startErr  error
		attachErr error
	}
	type want struct {
		calls   []string
		err     error
		options client.ContainerAttachOptions
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"Success": {
			reason: "A container must be started before attaching, and attach must replay logs while streaming all configured channels.",
			args:   args{stdin: true},
			want: want{
				calls: []string{"start", "attach"},
				options: client.ContainerAttachOptions{
					Stream: true,
					Stdout: true,
					Stderr: true,
					Stdin:  true,
					Logs:   true,
				},
			},
		},
		"StartFailureDoesNotAttach": {
			reason: "Attaching cannot succeed when starting the container fails, so the start error must be preserved and attach must not be attempted.",
			args:   args{startErr: errStart},
			want: want{
				calls: []string{"start"},
				err:   errStart,
			},
		},
		"AttachFailure": {
			reason: "An attach failure after a successful start must be preserved for callers.",
			args:   args{attachErr: errAttach},
			want: want{
				calls: []string{"start", "attach"},
				err:   errAttach,
				options: client.ContainerAttachOptions{
					Stream: true,
					Stdout: true,
					Stderr: true,
					Logs:   true,
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			calls := []string{}
			var gotOptions client.ContainerAttachOptions
			_, err := startAndAttach(t.Context(), "container-id", tc.args.stdin, runContainerCalls{
				start: func(_ context.Context, id string, _ client.ContainerStartOptions) error {
					if diff := cmp.Diff("container-id", id); diff != "" {
						t.Errorf("%s\nstart container ID: -want, +got:\n%s", tc.reason, diff)
					}
					calls = append(calls, "start")
					return tc.args.startErr
				},
				attach: func(_ context.Context, id string, opts client.ContainerAttachOptions) (client.ContainerAttachResult, error) {
					if diff := cmp.Diff("container-id", id); diff != "" {
						t.Errorf("%s\nattach container ID: -want, +got:\n%s", tc.reason, diff)
					}
					calls = append(calls, "attach")
					gotOptions = opts
					return client.ContainerAttachResult{}, tc.args.attachErr
				},
			})

			if diff := cmp.Diff(tc.want.calls, calls); diff != "" {
				t.Errorf("%s\nstartAndAttach(...) calls: -want, +got:\n%s", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.options, gotOptions); diff != "" {
				t.Errorf("%s\nstartAndAttach(...) attach options: -want, +got:\n%s", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("%s\nstartAndAttach(...): -want error, +got error:\n%s", tc.reason, diff)
			}
		})
	}
}
