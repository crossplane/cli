/*
Copyright 2024 The Crossplane Authors.

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

// Package validate implements offline schema validation of Crossplane resources.
package validate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/spf13/afero"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/crossplane/crossplane-runtime/v2/pkg/version"

	"github.com/crossplane/cli/v2/cmd/crossplane/common/load"
	pkgvalidate "github.com/crossplane/cli/v2/pkg/validate"
	"github.com/crossplane/cli/v2/pkg/validate/output"

	_ "embed"
)

//go:embed help/validate.md
var helpDetail string

// errWriteOutput is the error message wrapped around I/O failures when the
// validate command writes to its output writer.
const errWriteOutput = "cannot write output"

// rendererFlag adapts output.RendererFor to Kong's MapperValue interface
// so the --output flag is decoded straight into a typed output.Renderer
// at parse time. Cmd then carries the resolved renderer as a dependency
// instead of a format identifier.
//
// The wrapper lives in this package (the CLI consumer) rather than in
// render so the render package stays free of any kong dependency and
// can be imported by non-CLI consumers like crossplane-diff.
type rendererFlag struct {
	output.Renderer
}

// Decode implements kong.MapperValue.
func (f *rendererFlag) Decode(ctx *kong.DecodeContext) error {
	var s string
	if err := ctx.Scan.PopValueInto("output", &s); err != nil {
		return err
	}
	r, err := output.RendererFor(output.Format(s))
	if err != nil {
		return err
	}
	f.Renderer = r
	return nil
}

// Cmd arguments and flags for render subcommand.
type Cmd struct {
	// Arguments.
	Extensions string `arg:"" help:"Extension sources as a comma-separated list of files, directories, or '-' for standard input."`
	Resources  string `arg:"" help:"Resource sources as a comma-separated list of files, directories, or '-' for standard input."`

	// Flags. Keep them in alphabetical order.
	CacheDir              string `default:"~/.crossplane/cache"                                        help:"Absolute path to the cache directory for downloaded schemas." predictor:"directory"`
	CleanCache            bool   `help:"Clean the cache directory before downloading package schemas."`
	CrossplaneImage       string `help:"Specify the Crossplane image for validating built-in schemas."`
	ErrorOnMissingSchemas bool   `default:"false"                                                      help:"Return non zero exit code if missing schemas."`
	// rendererFlag.Decode rejects unknown formats, which is what Kong's
	// "enum" tag would normally enforce — but enum doesn't apply to
	// MapperValue-backed fields. The help text is the user-facing list
	// of valid values.
	Output             rendererFlag `default:"text"                        help:"Output format for validation results (text, json, or yaml)."                                                                                                                                                                                                  short:"o"`
	SkipSuccessResults bool         `help:"Skip printing success results."`
	UpdateCache        bool         `default:"false"                       help:"Update cached schemas by downloading the latest version that satisfies a constraint. May be useful if you are using semantic version constraints and want to get the latest version, but this slows down the cache lookup due to the required network calls."`

	fs afero.Fs
}

// Help prints out the help for the validate command.
func (c *Cmd) Help() string {
	return helpDetail
}

// AfterApply implements kong.AfterApply. The renderer is already resolved
// by Kong's MapperValue plumbing on Cmd.Output by the time this runs, so
// AfterApply only sets the filesystem.
func (c *Cmd) AfterApply() error {
	c.fs = afero.NewOsFs()
	return nil
}

// Run validate.
func (c *Cmd) Run(k *kong.Context, _ logging.Logger) error {
	if c.Resources == "-" && c.Extensions == "-" {
		return errors.New("cannot use stdin for both extensions and resources")
	}

	if len(c.CrossplaneImage) < 1 {
		c.CrossplaneImage = fmt.Sprintf("xpkg.crossplane.io/crossplane/crossplane:%s", version.New().GetVersionString())
	}

	// Load all extensions
	extensionLoader, err := load.NewLoader(c.Extensions)
	if err != nil {
		return errors.Wrapf(err, "cannot load extensions from %q", c.Extensions)
	}

	extensions, err := extensionLoader.Load()
	if err != nil {
		return errors.Wrapf(err, "cannot load extensions from %q", c.Extensions)
	}

	// Load all resources
	resourceLoader, err := load.NewLoader(c.Resources)
	if err != nil {
		return errors.Wrapf(err, "cannot load resources from %q", c.Resources)
	}

	resources, err := resourceLoader.Load()
	if err != nil {
		return errors.Wrapf(err, "cannot load resources from %q", c.Resources)
	}

	if strings.HasPrefix(c.CacheDir, "~/") {
		homeDir, _ := os.UserHomeDir()
		c.CacheDir = filepath.Join(homeDir, c.CacheDir[2:])
	}

	m := NewManager(c.CacheDir, c.fs, k.Stdout, WithCrossplaneImage(c.CrossplaneImage), WithUpdateCache(c.UpdateCache))

	// Convert XRDs/CRDs to CRDs and add package dependencies
	if err := m.PrepExtensions(extensions); err != nil {
		return errors.Wrapf(err, "cannot prepare extensions")
	}

	// Download package base layers to cache and load them as CRDs
	if err := m.CacheAndLoad(c.CleanCache); err != nil {
		return errors.Wrapf(err, "cannot download and load cache")
	}

	// Validate resources against schemas, render in the requested format,
	// and return a CLI-shaped error when validation didn't pass.
	result, err := pkgvalidate.SchemaValidate(context.Background(), resources, m.crds)
	if err != nil {
		return errors.Wrapf(err, "cannot validate resources")
	}

	if err := c.Output.Render(result, k.Stdout, output.Options{SkipSuccessResults: c.SkipSuccessResults}); err != nil {
		return errors.Wrap(err, "cannot render validation result")
	}

	return pkgvalidate.ResultError(result, c.ErrorOnMissingSchemas)
}
