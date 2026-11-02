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

package composition

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/kong"
	"github.com/google/go-cmp/cmp"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	"github.com/crossplane/cli/v2/internal/simulate"
)

// parseSimulate parses args as `simulate` flags, running kong's validation.
func parseSimulate(t *testing.T, args ...string) (*simulateCmd, error) {
	t.Helper()

	var cli struct {
		Simulate simulateCmd `cmd:""`
	}
	parser, err := kong.New(&cli, kong.Exit(func(int) {}))
	if err != nil {
		t.Fatalf("kong.New(...): %v", err)
	}
	_, err = parser.Parse(append([]string{"simulate"}, args...))
	return &cli.Simulate, err
}

func TestSimulateFlags(t *testing.T) {
	cases := map[string]struct {
		reason string
		args   []string
		input  []string
		err    string
	}{
		"Defaults": {
			reason: "With only -f the shared defaults apply.",
			args:   []string{"-f", "comp.yaml"},
			input:  []string{"comp.yaml"},
		},
		"RepeatedInput": {
			reason: "-f repeats, splits on commas, and takes - for stdin.",
			args:   []string{"-f", "a,b.yaml", "--input", "-"},
			input:  []string{"a", "b.yaml", "-"},
		},
		"InputRequired": {
			reason: "Without -f the command fails rather than waiting on stdin.",
			err:    "--input",
		},
		"BadName": {
			reason: "A malformed --name fails CompositionFlags' Validate.",
			args:   []string{"-f", "comp.yaml", "--name=a/b/c"},
			err:    "--name",
		},
		"NoMaxConcurrency": {
			reason: "--max-concurrency is project-only.",
			args:   []string{"-f", "comp.yaml", "--max-concurrency=2"},
			err:    "max-concurrency",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c, err := parseSimulate(t, tc.args...)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("\n%s\nParse(%v): want error containing %q, got %v", tc.reason, tc.args, tc.err, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("\n%s\nParse(%v): %v", tc.reason, tc.args, err)
			}
			if diff := cmp.Diff(tc.input, c.Input); diff != "" {
				t.Errorf("\n%s\nInput: -want, +got:\n%s", tc.reason, diff)
			}
			if c.Output != "plan" || c.Timeout != 2*time.Minute {
				t.Errorf("\n%s\nOutput, Timeout: want plan, 2m, got %q, %v", tc.reason, c.Output, c.Timeout)
			}
		})
	}
}

func TestSimulateHelp(t *testing.T) {
	h := (&simulateCmd{}).Help()
	for _, want := range []string{"composition simulate", simulate.Help(true), "## Examples"} {
		if !strings.Contains(h, want) {
			t.Errorf("Help(): want it to contain %q", want)
		}
	}
}

func TestSimulateRejectsNonComposition(t *testing.T) {
	path := filepath.Join(t.TempDir(), "xr.yaml")
	if err := os.WriteFile(path, []byte("apiVersion: example.org/v1\nkind: XNetwork\nmetadata:\n  name: net\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Run loads -f before it uses k or Docker.
	err := (&simulateCmd{Input: []string{path}}).Run(context.Background(), nil, logging.NewNopLogger(), nil)
	want := `-f inputs include XNetwork "net" (example.org/v1); composition simulate takes Composition files; xr simulate takes composite resource or claim files`
	if err == nil || err.Error() != want {
		t.Errorf("Run(...): want error %q, got %v", want, err)
	}
}
