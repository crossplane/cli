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

package config

import (
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/afero"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	"github.com/crossplane/cli/v2/internal/config"
)

type setCmd struct {
	Key   string `arg:"" help:"Config key to set (e.g. features.enableAlpha)."`
	Value string `arg:"" help:"Value to assign."`

	fs afero.Fs
}

type boolSetter func(c *config.Config, v bool)

// boolKeys maps supported dotted config keys to setter functions. Adding a new
// boolean key is a single entry here.
//
//nolint:gochecknoglobals // This is a constant.
var boolKeys = map[string]boolSetter{
	"features.enableAlpha": func(c *config.Config, v bool) { c.Features.EnableAlpha = v },
	"features.enableBeta":  func(c *config.Config, v bool) { c.Features.EnableBeta = v },
}

func (c *setCmd) AfterApply() error {
	c.fs = afero.NewOsFs()
	return nil
}

// Run sets a config value and writes the file.
func (c *setCmd) Run(path ConfigPath) error {
	p := string(path)
	if p == "" {
		return errors.New("cannot determine config file path; pass --config or set CROSSPLANE_CONFIG")
	}

	setter, ok := boolKeys[c.Key]
	if !ok {
		return errors.Errorf("unknown config key %q (supported: %s)", c.Key, knownKeysList())
	}
	v, err := strconv.ParseBool(c.Value)
	if err != nil {
		return errors.Wrapf(err, "invalid bool value %q for key %s", c.Value, c.Key)
	}

	cfg, err := config.Load(c.fs, p)
	if err != nil {
		return errors.Wrap(err, "cannot load config")
	}

	setter(cfg, v)

	return config.Save(c.fs, p, cfg)
}

func knownKeysList() string {
	keys := make([]string, 0, len(boolKeys))
	for k := range boolKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}
