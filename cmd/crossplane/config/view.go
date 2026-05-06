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
	"fmt"

	"github.com/alecthomas/kong"
	"github.com/spf13/afero"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	cfgpkg "github.com/crossplane/cli/v2/internal/config"
)

type viewCmd struct {
	fs afero.Fs
}

func (c *viewCmd) AfterApply() error {
	c.fs = afero.NewOsFs()
	return nil
}

// Run prints the current effective config as YAML.
func (c *viewCmd) Run(k *kong.Context, path ConfigPath) error {
	cfg, err := cfgpkg.Load(c.fs, string(path))
	if err != nil {
		return errors.Wrap(err, "cannot load config")
	}
	out, err := yaml.Marshal(cfg)
	if err != nil {
		return errors.Wrap(err, "cannot marshal config")
	}
	if _, err := fmt.Fprint(k.Stdout, string(out)); err != nil {
		return errors.Wrap(err, "cannot write config")
	}
	return nil
}
