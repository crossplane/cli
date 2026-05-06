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

// Package config implements loading the crossplane CLI config file.
package config

import (
	iofs "io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/afero"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

const version1 = 1

// Config is the on-disk configuration for the crossplane CLI.
type Config struct {
	// Version is the version of the config file.
	Version int `json:"version"`

	Features Features `json:"features,omitzero"`
}

// Features configures feature visibility.
type Features struct {
	EnableAlpha bool `json:"enableAlpha,omitempty"`
	EnableBeta  bool `json:"enableBeta,omitempty"`
}

// Load reads a Config from path. A missing file is not an error; the zero
// Config is returned. Unknown fields in the file are an error so typos in
// flag names surface immediately.
func Load(fs afero.Fs, path string) (*Config, error) {
	if path == "" {
		return &Config{Version: version1}, nil
	}
	data, err := afero.ReadFile(fs, path)
	if err != nil {
		if errors.Is(err, iofs.ErrNotExist) {
			return &Config{Version: version1}, nil
		}
		return nil, errors.Wrapf(err, "cannot read config file %s", path)
	}

	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, errors.Wrapf(err, "cannot parse config file %s", path)
	}

	if cfg.Version != version1 {
		return nil, errors.Errorf("unsupported config version %d", cfg.Version)
	}

	return cfg, nil
}

// ResolvePath returns the path to the config file, in priority order:
//  1. flag - the value of the --config flag, if any.
//  2. The CROSSPLANE_CONFIG environment variable.
//  3. DefaultPath() (XDG/HOME-derived).
//
// An empty string is returned only if no source produces one.
func ResolvePath(flag string) string {
	if flag != "" {
		return flag
	}
	if v := os.Getenv("CROSSPLANE_CONFIG"); v != "" {
		return v
	}
	return defaultPath()
}

// defaultPath returns the default location of the config file:
// $XDG_CONFIG_HOME/crossplane/config.yaml, falling back to
// ~/.config/crossplane/config.yaml when XDG_CONFIG_HOME is unset.
// Returns "" if a home directory cannot be determined.
func defaultPath() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "crossplane", "config.yaml")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "crossplane", "config.yaml")
}
