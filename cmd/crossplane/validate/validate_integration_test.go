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

package validate

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	pkgvalidate "github.com/crossplane/cli/v2/pkg/validate"
)

// parseCmd parses the given CLI args through Kong and returns the
// populated Cmd, the kong.Context, and any parse error. Failures of
// kong.New itself fatal the test — those indicate a static-struct bug
// rather than a runtime input issue. Use this directly when a test is
// asserting on Kong's parse behaviour (e.g. an invalid flag value).
func parseCmd(t *testing.T, args ...string) (*Cmd, *kong.Context, error) {
	t.Helper()
	var c Cmd
	parser, err := kong.New(&c)
	if err != nil {
		t.Fatalf("kong.New(): %v", err)
	}
	kongCtx, err := parser.Parse(args)
	return &c, kongCtx, err
}

// runCmd parses args, t.Fatals on a parse failure, then invokes
// Cmd.Run and returns whatever was written to stdout plus the error
// returned by Run. Tests asserting on Run's behaviour (the common case)
// use this; tests asserting on parse behaviour use parseCmd directly.
func runCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	c, kongCtx, err := parseCmd(t, args...)
	if err != nil {
		t.Fatalf("kong.Parse(%v): %v", args, err)
	}
	var stdout bytes.Buffer
	kongCtx.Stdout = &stdout
	runErr := c.Run(kongCtx, logging.NewNopLogger())
	return stdout.String(), runErr
}

// commonArgs are the fixture arguments shared by every e2e test:
// pre-populated cache + a stand-in crossplane image whose package.yaml
// lives under testdata/cache. Both keep the test offline.
var commonArgs = []string{
	"--cache-dir=testdata/cache",
	"--crossplane-image=xpkg.crossplane.io/crossplane/crossplane:v0.0.0-test",
}

// TestParseRejectsUnknownOutputFormat asserts that an unknown --output
// value is rejected at parse time by rendererFlag.Decode, before Run is
// ever invoked.
func TestParseRejectsUnknownOutputFormat(t *testing.T) {
	args := append([]string{
		"testdata/cmd/crd.yaml",
		"testdata/cmd/resources_valid.yaml",
		"--output=xml",
	}, commonArgs...)
	_, _, err := parseCmd(t, args...)
	if err == nil {
		t.Errorf("kong.Parse(--output=xml) = nil; want decoder error")
	}
}

// TestRun drives the validate command end-to-end through Kong, against
// real fixture files and a pre-populated cache directory that keeps the
// run offline. Nothing is mocked; the case table covers
// text/json/yaml × valid/invalid/missing × flag interactions.
func TestRun(t *testing.T) {
	cases := map[string]struct {
		reason     string
		extensions string
		resources  string
		extraArgs  []string
		wantErr    bool
		// assertText is invoked when --output is text (the default). It
		// receives the captured stdout.
		assertText func(t *testing.T, stdout string)
		// assertJSON is invoked when --output=json. It is given the
		// already-parsed ValidationResult.
		assertJSON func(t *testing.T, result *pkgvalidate.ValidationResult)
		// assertYAML, same idea but for --output=yaml.
		assertYAML func(t *testing.T, result *pkgvalidate.ValidationResult)
	}{
		"DefaultTextValid": {
			reason:     "Default text mode emits the [✓] success line and the totals summary.",
			extensions: "testdata/cmd/crd.yaml",
			resources:  "testdata/cmd/resources_valid.yaml",
			assertText: func(t *testing.T, out string) {
				t.Helper()
				if !strings.Contains(out, "[✓] cmd.example.org/v1alpha1, Kind=Test, ok-instance") {
					t.Errorf("missing success line in output:\n%s", out)
				}
				if !strings.Contains(out, "Total 1 resources: 0 missing schemas, 1 success cases, 0 failure cases") {
					t.Errorf("missing summary line in output:\n%s", out)
				}
			},
		},
		"TextInvalidExitsNonZero": {
			reason:     "An invalid resource produces an [x] schema-error line and the command exits non-zero.",
			extensions: "testdata/cmd/crd.yaml",
			resources:  "testdata/cmd/resources_invalid.yaml",
			wantErr:    true,
			assertText: func(t *testing.T, out string) {
				t.Helper()
				if !strings.Contains(out, "[x] schema validation error cmd.example.org/v1alpha1, Kind=Test, bad-instance") {
					t.Errorf("missing schema-error line in output:\n%s", out)
				}
			},
		},
		"JSONValid": {
			reason:     "--output=json on a valid resource emits a structured payload with one Valid entry.",
			extensions: "testdata/cmd/crd.yaml",
			resources:  "testdata/cmd/resources_valid.yaml",
			extraArgs:  []string{"--output=json"},
			assertJSON: func(t *testing.T, r *pkgvalidate.ValidationResult) {
				t.Helper()
				if r.Summary.Total != 1 || r.Summary.Valid != 1 {
					t.Errorf("Summary = %+v; want Total=1 Valid=1", r.Summary)
				}
				if len(r.Resources) != 1 || r.Resources[0].Status != pkgvalidate.ValidationStatusValid {
					t.Errorf("Resources = %+v; want one Valid entry", r.Resources)
				}
			},
		},
		"JSONInvalidExitsNonZero": {
			reason:     "--output=json on an invalid resource surfaces a schema-typed error and exits non-zero.",
			extensions: "testdata/cmd/crd.yaml",
			resources:  "testdata/cmd/resources_invalid.yaml",
			extraArgs:  []string{"--output=json"},
			wantErr:    true,
			assertJSON: func(t *testing.T, r *pkgvalidate.ValidationResult) {
				t.Helper()
				if r.Summary.Invalid != 1 {
					t.Errorf("Summary.Invalid = %d; want 1", r.Summary.Invalid)
				}
				if len(r.Resources) != 1 || r.Resources[0].Status != pkgvalidate.ValidationStatusInvalid {
					t.Errorf("Resources = %+v; want one Invalid entry", r.Resources)
				}
				if len(r.Resources[0].Errors) == 0 || r.Resources[0].Errors[0].Type != pkgvalidate.FieldErrorTypeSchema {
					t.Errorf("Resources[0].Errors = %+v; want at least one schema error", r.Resources[0].Errors)
				}
			},
		},
		"YAMLValid": {
			reason:     "--output=yaml round-trips a valid resource through YAML decoding.",
			extensions: "testdata/cmd/crd.yaml",
			resources:  "testdata/cmd/resources_valid.yaml",
			extraArgs:  []string{"--output=yaml"},
			assertYAML: func(t *testing.T, r *pkgvalidate.ValidationResult) {
				t.Helper()
				if r.Summary.Total != 1 || r.Summary.Valid != 1 {
					t.Errorf("Summary = %+v; want Total=1 Valid=1", r.Summary)
				}
			},
		},
		"JSONMissingSchemaNoFlag": {
			reason:     "Without --error-on-missing-schemas, a missing schema is reported but does not fail the run.",
			extensions: "testdata/cmd/crd.yaml",
			resources:  "testdata/cmd/resources_missing.yaml",
			extraArgs:  []string{"--output=json"},
			assertJSON: func(t *testing.T, r *pkgvalidate.ValidationResult) {
				t.Helper()
				if r.Summary.MissingSchemas != 1 || r.Summary.Invalid != 0 {
					t.Errorf("Summary = %+v; want MissingSchemas=1 Invalid=0", r.Summary)
				}
				if len(r.Resources) != 1 || r.Resources[0].Status != pkgvalidate.ValidationStatusMissingSchema {
					t.Errorf("Resources = %+v; want one MissingSchema entry", r.Resources)
				}
			},
		},
		"JSONMissingSchemaWithFlag": {
			reason:     "--error-on-missing-schemas escalates a missing schema to a non-zero exit.",
			extensions: "testdata/cmd/crd.yaml",
			resources:  "testdata/cmd/resources_missing.yaml",
			extraArgs:  []string{"--output=json", "--error-on-missing-schemas"},
			wantErr:    true,
			assertJSON: func(t *testing.T, r *pkgvalidate.ValidationResult) {
				t.Helper()
				if r.Summary.MissingSchemas != 1 {
					t.Errorf("Summary.MissingSchemas = %d; want 1", r.Summary.MissingSchemas)
				}
			},
		},
		"SkipSuccessResultsTextSuppressesCheckmark": {
			reason:     "--skip-success-results suppresses [✓] lines but the summary still reports the success count.",
			extensions: "testdata/cmd/crd.yaml",
			resources:  "testdata/cmd/resources_valid.yaml",
			extraArgs:  []string{"--skip-success-results"},
			assertText: func(t *testing.T, out string) {
				t.Helper()
				if strings.Contains(out, "[✓]") {
					t.Errorf("--skip-success-results should suppress [✓] lines; got:\n%s", out)
				}
				if !strings.Contains(out, "1 success cases") {
					t.Errorf("summary should still report success cases; got:\n%s", out)
				}
			},
		},
		"SkipSuccessResultsJSONStillIncludesValid": {
			reason:     "--skip-success-results is text-only; the JSON payload still includes Valid entries so consumers can filter themselves.",
			extensions: "testdata/cmd/crd.yaml",
			resources:  "testdata/cmd/resources_valid.yaml",
			extraArgs:  []string{"--output=json", "--skip-success-results"},
			assertJSON: func(t *testing.T, r *pkgvalidate.ValidationResult) {
				t.Helper()
				if r.Summary.Valid != 1 {
					t.Errorf("--skip-success-results must not strip valid entries from JSON; got %+v", r)
				}
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			args := append([]string{tc.extensions, tc.resources}, append(commonArgs, tc.extraArgs...)...)
			stdout, err := runCmd(t, args...)
			if (err != nil) != tc.wantErr {
				t.Fatalf("%s\nRun() err = %v, wantErr = %v\n--- stdout ---\n%s", tc.reason, err, tc.wantErr, stdout)
			}
			// Strip any package-fetcher chatter the manager writes before
			// the validation result so downstream parsers see only the
			// payload.
			payload := stripFetcherNoise(stdout)

			if tc.assertText != nil {
				tc.assertText(t, payload)
			}
			if tc.assertJSON != nil {
				var got pkgvalidate.ValidationResult
				if err := json.Unmarshal([]byte(payload), &got); err != nil {
					t.Fatalf("stdout is not valid JSON: %v\n%s", err, payload)
				}
				tc.assertJSON(t, &got)
			}
			if tc.assertYAML != nil {
				var got pkgvalidate.ValidationResult
				if err := yaml.Unmarshal([]byte(payload), &got); err != nil {
					t.Fatalf("stdout is not valid YAML: %v\n%s", err, payload)
				}
				tc.assertYAML(t, &got)
			}
		})
	}
}

// stripFetcherNoise drops any header lines the manager writes to stdout
// (cache notices, "schemas does not exist, downloading: ...") so the
// downstream parsers see only the validation payload. It looks for the
// first line that starts a payload — JSON {, YAML resources:/summary:
// header, or a text marker like [✓]/[x]/[!]/Total — and returns from there.
func stripFetcherNoise(out string) string {
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		t := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(t, "{"):
			return strings.Join(lines[i:], "\n")
		case strings.HasPrefix(t, "resources:") || strings.HasPrefix(t, "summary:"):
			return strings.Join(lines[i:], "\n")
		case strings.HasPrefix(t, "[✓]") || strings.HasPrefix(t, "[x]") || strings.HasPrefix(t, "[!]") || strings.HasPrefix(t, "Total"):
			return strings.Join(lines[i:], "\n")
		}
	}
	return out
}
