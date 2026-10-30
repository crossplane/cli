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

package common

import "errors"

// ExitCodeChanges is the status a simulate command exits with when the
// simulation succeeded and found changes.
const ExitCodeChanges = 3

// ExitError is an error that sets the command's exit status; it implements
// kong.ExitCoder. With a nil Err it reports a result that is not an error,
// such as changes found: main exits with Code without printing anything.
// Return it by value and unwrapped: main exits silently only when the
// returned error carries no message besides a silent ExitError, so one
// wrapped with a message or joined with another error, or a pointer, is
// printed as an error.
type ExitError struct {
	// Err is the error to print, or nil to exit silently.
	Err error

	// Code is the status to exit with.
	Code int
}

// Error returns Err's message, or an empty string when Err is nil.
func (e ExitError) Error() string {
	if e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

// ExitCode implements kong.ExitCoder.
func (e ExitError) ExitCode() int {
	return e.Code
}

// Unwrap returns Err.
func (e ExitError) Unwrap() error {
	return e.Err
}

// Silent reports whether e exits without printing anything.
func (e ExitError) Silent() bool {
	return e.Err == nil
}

// SilentExit returns the code of a silent ExitError value in err's chain
// when err has no message of its own, and false otherwise. kong's
// Context.Run joins the command's error with its AfterRun hooks' errors, so
// the check can't require err to be the ExitError itself; requiring an empty
// message still keeps a wrapping or joined error from being swallowed.
func SilentExit(err error) (int, bool) {
	var e ExitError
	if err == nil || err.Error() != "" || !errors.As(err, &e) || !e.Silent() {
		return 0, false
	}
	return e.Code, true
}
