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

package xrdlint

import (
	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

// Severity is how a rule's findings are treated.
type Severity string

// Severities.
const (
	// SeverityOff disables a rule.
	SeverityOff Severity = "off"
	// SeverityWarn reports findings without failing the run, unless the
	// warning budget is exceeded.
	SeverityWarn Severity = "warn"
	// SeverityError reports findings and fails the run.
	SeverityError Severity = "error"
)

// ParseSeverity returns the Severity named by s.
func ParseSeverity(s string) (Severity, error) {
	switch sev := Severity(s); sev {
	case SeverityOff, SeverityWarn, SeverityError:
		return sev, nil
	default:
		return "", errors.Errorf("unknown severity %q: must be one of off, warn or error", s)
	}
}
