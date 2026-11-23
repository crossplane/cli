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

// Package images is the single source of truth for the container images and
// packages the CLI uses by default: function base images, the images schema
// generation and the dev control plane run, and default package dependencies.
//
// The defaults live in the embedded images.yaml. A host CLI embedding the
// crossplane commands as a library can replace the whole catalog with
// Override, which must be called at startup, before any image is read.
package images

import (
	"fmt"
	"sync"
	"sync/atomic"

	"sigs.k8s.io/yaml"

	_ "embed"
)

//go:embed images.yaml
var embeddedYAML []byte

// Language is a supported project language.
type Language string

// Supported languages.
const (
	Go           Language = "go"
	GoTemplating Language = "go-templating"
	KCL          Language = "kcl"
	Python       Language = "python"
	Rust         Language = "rust"
)

// ImageType is the role an image plays for a language.
type ImageType string

// Supported image types.
const (
	// ImageBase is the base image an embedded function's code is layered
	// onto.
	ImageBase ImageType = "baseImage"
	// ImageBuild is the image a function is compiled in.
	ImageBuild ImageType = "buildImage"
	// ImageRuntime is the base image a compiled function runs on.
	ImageRuntime ImageType = "runtimeImage"
	// ImageSchema is the image that generates the language's schema models.
	ImageSchema ImageType = "schemaImage"
)

// Tool identifies an image or package the CLI uses outside of any one
// language.
type Tool string

// Supported tools.
const (
	// ToolFunctionAutoReady is the function package composition generate
	// adds as the default auto-ready pipeline step.
	ToolFunctionAutoReady Tool = "functionAutoReady"
	// ToolDevRegistry is the local registry image the dev control plane
	// sideloads packages through.
	ToolDevRegistry Tool = "devRegistry"
	// ToolDevRegistryConfigurator is the utility image that configures the
	// dev control plane's containerd to trust the local registry.
	ToolDevRegistryConfigurator Tool = "devRegistryConfigurator"
)

type catalog struct {
	Languages map[Language]map[ImageType]string `json:"languages"`
	Tools     map[Tool]string                   `json:"tools"`
}

// The catalog yaml is read exactly once, on first use; Override replaces it
// before that. Catalog reads after the parse are lock-free.
//
//nolint:gochecknoglobals // The catalog is process-wide by design, like the defaults it replaces.
var (
	catalogYAML atomic.Pointer[[]byte]
	loadOnce    sync.Once
	loaded      *catalog
)

// Override replaces the entire catalog with the given yaml, in the same
// schema as this package's images.yaml. For host CLIs embedding the
// crossplane commands as a library; it must be called at startup, before any
// image is read, and panics when called later — by then defaults may already
// have been handed out, so a late override is a bug at the call site.
func Override(imagesYAML []byte) {
	if loaded != nil {
		panic("images.Override must be called before any image is read from the catalog")
	}
	y := imagesYAML
	catalogYAML.Store(&y)
}

func load() *catalog {
	loadOnce.Do(func() {
		y := embeddedYAML
		if o := catalogYAML.Load(); o != nil {
			y = *o
		}

		c := &catalog{}
		if err := yaml.Unmarshal(y, c); err != nil {
			// The embedded yaml parsing is covered by tests, and a host CLI
			// supplying broken yaml has no caller that could handle it.
			panic(fmt.Sprintf("images catalog yaml is invalid: %v", err))
		}
		loaded = c
	})

	return loaded
}

// Image returns the image used for the given language and role. It panics if
// the pair isn't in the catalog: the set of keys is fixed at compile time,
// so a miss is a programming error, mirroring the constants this replaces.
func Image(l Language, t ImageType) string {
	img, ok := load().Languages[l][t]
	if !ok {
		panic(fmt.Sprintf("no %s image for language %s in the image catalog", t, l))
	}

	return img
}

// ToolImage returns the image or package used for the given tool. It panics
// if the tool isn't in the catalog, mirroring the constants this replaces.
func ToolImage(t Tool) string {
	img, ok := load().Tools[t]
	if !ok {
		panic(fmt.Sprintf("no image for tool %s in the image catalog", t))
	}

	return img
}

// All returns every image in the catalog, keyed "<language>.<type>" and
// "tools.<tool>".
func All() map[string]string {
	c := load()
	out := make(map[string]string, len(c.Tools)+len(c.Languages)*2)
	for l, types := range c.Languages {
		for t, img := range types {
			out[fmt.Sprintf("%s.%s", l, t)] = img
		}
	}
	for t, img := range c.Tools {
		out["tools."+string(t)] = img
	}

	return out
}
