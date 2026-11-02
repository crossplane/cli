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

package simulate

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

func TestSilentExit(t *testing.T) {
	type want struct {
		code   int
		silent bool
	}
	type args struct {
		err error
	}
	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"Nil":      {reason: "No error isn't a silent exit.", args: args{err: nil}},
		"Silent":   {reason: "A silent ExitError value exits silently with its code.", args: args{err: ExitError{Code: ExitCodeChanges}}, want: want{code: ExitCodeChanges, silent: true}},
		"WithErr":  {reason: "An ExitError with an error is printed.", args: args{err: ExitError{Code: 2, Err: errors.New("boom")}}},
		"Wrapped":  {reason: "A wrapped silent ExitError is printed, so the wrapper's message isn't lost.", args: args{err: errors.Wrap(ExitError{Code: ExitCodeChanges}, "boom")}},
		"KongJoin": {reason: "kong's Run joins a silent ExitError with nil hook errors, which still exits silently.", args: args{err: errors.Join(ExitError{Code: ExitCodeChanges}, nil)}, want: want{code: ExitCodeChanges, silent: true}},
		"Joined":   {reason: "A silent ExitError joined with a real error is printed.", args: args{err: errors.Join(ExitError{Code: ExitCodeChanges}, errors.New("boom"))}},
		"Pointer":  {reason: "A pointer isn't matched; ExitError must be returned by value.", args: args{err: &ExitError{Code: ExitCodeChanges}}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			code, silent := SilentExit(tc.args.err)
			if diff := cmp.Diff(tc.want, want{code: code, silent: silent}, cmp.AllowUnexported(want{})); diff != "" {
				t.Errorf("\n%s\nSilentExit(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}
