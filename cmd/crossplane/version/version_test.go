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

package version

import (
	"testing"

	"github.com/alecthomas/kong"
)

func TestImpersonationFlagsParse(t *testing.T) {
	var c Cmd

	p, err := kong.New(&c)
	if err != nil {
		t.Fatalf("kong.New(): unexpected error: %v", err)
	}

	if _, err := p.Parse([]string{"--as=jane", "--as-uid=42"}); err != nil {
		t.Fatalf("Parse(): unexpected error: %v", err)
	}

	if c.Impersonation.As != "jane" {
		t.Errorf("As: want %q, got %q", "jane", c.Impersonation.As)
	}
	if c.Impersonation.AsUID != "42" {
		t.Errorf("AsUID: want %q, got %q", "42", c.Impersonation.AsUID)
	}
}
