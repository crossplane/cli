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

package xpkg

import (
	"testing"

	"github.com/alecthomas/kong"
)

func TestInstallImpersonationFlagsParse(t *testing.T) {
	var c installCmd

	p, err := kong.New(&c)
	if err != nil {
		t.Fatalf("kong.New(): unexpected error: %v", err)
	}

	if _, err := p.Parse([]string{"--as-group=team-a-admins", "provider", "example.org/provider-foo:v1.0.0"}); err != nil {
		t.Fatalf("Parse(): unexpected error: %v", err)
	}

	if len(c.Impersonation.AsGroup) != 1 || c.Impersonation.AsGroup[0] != "team-a-admins" {
		t.Errorf("AsGroup: want [team-a-admins], got %v", c.Impersonation.AsGroup)
	}
}

func TestUpdateImpersonationFlagsParse(t *testing.T) {
	var c updateCmd

	p, err := kong.New(&c)
	if err != nil {
		t.Fatalf("kong.New(): unexpected error: %v", err)
	}

	if _, err := p.Parse([]string{"--as=jane", "provider", "example.org/provider-foo:v1.0.1"}); err != nil {
		t.Fatalf("Parse(): unexpected error: %v", err)
	}

	if c.Impersonation.As != "jane" {
		t.Errorf("As: want %q, got %q", "jane", c.Impersonation.As)
	}
}
