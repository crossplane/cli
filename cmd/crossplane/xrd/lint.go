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

package xrd

import (
	"io"
	"os"
	"strconv"

	"github.com/alecthomas/kong"
	"github.com/spf13/afero"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	"github.com/crossplane/cli/v2/internal/xrdlint"
	"github.com/crossplane/cli/v2/internal/xrdlint/rules"

	_ "embed"
)

//go:embed help/lint.md
var lintHelp string

type lintCmd struct {
	// Arguments.
	Paths []string `arg:"" help:"XRD files or directories to lint, or '-' for stdin." predictor:"file"`

	// Flags.
	Config      string `help:"Lint config file. Default: search upward from the working directory for .crossplane-lint.yaml." name:"lint-config"                                                         placeholder:"PATH"    predictor:"file" type:"path"`
	Output      string `default:"text"                                                                                        enum:"text,json,yaml"                                                      help:"Output format." short:"o"`
	MaxWarnings int    `default:"-1"                                                                                          help:"Fail if there are more than this many warnings. -1 means unlimited." placeholder:"N"`

	fs    afero.Fs
	stdin io.Reader
	wd    string
}

func (c *lintCmd) Help() string {
	return lintHelp
}

// AfterApply implements kong.AfterApply.
func (c *lintCmd) AfterApply() error {
	c.fs = afero.NewOsFs()
	c.stdin = os.Stdin
	wd, err := os.Getwd()
	if err != nil {
		return errors.Wrap(err, "cannot get the working directory")
	}
	c.wd = wd
	return nil
}

func (c *lintCmd) Run(k *kong.Context) error {
	r, err := xrdlint.RendererFor(xrdlint.Format(c.Output))
	if err != nil {
		return err
	}

	reg, err := xrdlint.NewRegistry(rules.Builtin()...)
	if err != nil {
		return errors.Wrap(err, "cannot register built-in rules")
	}

	cfg, cfgPath, err := c.loadConfig()
	if err != nil {
		return err
	}
	settings, err := cfg.Resolve(reg)
	if err != nil {
		return errors.Wrapf(err, "invalid lint config %q", cfgPath)
	}

	xrds, err := xrdlint.Load(c.fs, c.stdin, c.Paths)
	if err != nil {
		return err
	}
	if len(xrds) == 0 {
		return errors.New("no XRDs found in the supplied paths")
	}

	res := xrdlint.NewRunner(reg, settings).Run(xrds)
	if err := r.Render(res, k.Stdout); err != nil {
		return err
	}

	switch {
	case res.Summary.Errors > 0:
		return errors.Errorf("lint found %s", count(res.Summary.Errors, "error"))
	case res.Failed(c.MaxWarnings):
		verb := "exceed"
		if res.Summary.Warnings == 1 {
			verb = "exceeds"
		}
		return errors.Errorf("%s %s the maximum of %d", count(res.Summary.Warnings, "warning"), verb, c.MaxWarnings)
	}
	return nil
}

// loadConfig loads the --lint-config file, or the nearest config found upward from
// the working directory, and returns it with its path. It returns a nil
// config if there is none, and the defaults apply.
func (c *lintCmd) loadConfig() (*xrdlint.Config, string, error) {
	path := c.Config
	if path == "" {
		p, err := xrdlint.FindConfig(c.fs, c.wd)
		if err != nil {
			return nil, "", err
		}
		if p == "" {
			return nil, "", nil
		}
		path = p
	}
	cfg, err := xrdlint.LoadConfig(c.fs, path)
	return cfg, path, err
}

func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}
