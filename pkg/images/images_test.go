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

package images

import (
	"strings"
	"testing"
)

// TestCatalogComplete pins the set of images the CLI expects the catalog to
// hold: every one of these is read by a call site that used to be a
// constant, so a missing entry would panic at runtime.
func TestCatalogComplete(t *testing.T) {
	type key struct {
		l Language
		t ImageType
	}
	keys := []key{
		{Go, ImageBase},
		{GoTemplating, ImageBase},
		{KCL, ImageBase},
		{KCL, ImageSchema},
		{Python, ImageBuild},
		{Python, ImageRuntime},
		{Python, ImageSchema},
		{Rust, ImageBuild},
		{Rust, ImageRuntime},
	}
	for _, k := range keys {
		if img := Image(k.l, k.t); img == "" {
			t.Errorf("Image(%s, %s) is empty", k.l, k.t)
		}
	}

	for _, tool := range []Tool{ToolFunctionAutoReady, ToolDevRegistry, ToolDevRegistryConfigurator} {
		if img := ToolImage(tool); img == "" {
			t.Errorf("ToolImage(%s) is empty", tool)
		}
	}

	all := All()
	if want, got := len(keys)+3, len(all); got != want {
		t.Errorf("All() returned %d entries, want %d: %v", got, want, all)
	}
	for k, img := range all {
		if img == "" {
			t.Errorf("All()[%s] is empty", k)
		}
	}
}

// TestOverride verifies a host CLI can replace defaults wholesale - another
// registry, repository and tag - which image config prefix rewriting can't
// express.
func TestOverride(t *testing.T) {
	orig := Image(KCL, ImageBase)
	t.Cleanup(func() { Override(KCL, ImageBase, orig) })

	Override(KCL, ImageBase, "xpkg.example.org/mirror/function-kcl-base:v9.9.9-custom")
	if got, want := Image(KCL, ImageBase), "xpkg.example.org/mirror/function-kcl-base:v9.9.9-custom"; got != want {
		t.Errorf("Image() after Override() = %q, want %q", got, want)
	}

	origTool := ToolImage(ToolFunctionAutoReady)
	t.Cleanup(func() { OverrideTool(ToolFunctionAutoReady, origTool) })

	OverrideTool(ToolFunctionAutoReady, "xpkg.example.org/mirror/function-auto-ready")
	if got, want := ToolImage(ToolFunctionAutoReady), "xpkg.example.org/mirror/function-auto-ready"; got != want {
		t.Errorf("ToolImage() after OverrideTool() = %q, want %q", got, want)
	}
	if !strings.HasPrefix(origTool, "xpkg.crossplane.io/") {
		t.Errorf("default function-auto-ready package %q does not live on xpkg.crossplane.io", origTool)
	}
}
