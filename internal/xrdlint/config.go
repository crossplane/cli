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
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"
	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	utiljson "k8s.io/apimachinery/pkg/util/json"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

// Config file identity.
const (
	// ConfigFileName is the file the CLI searches for, upward from the working
	// directory.
	ConfigFileName = ".crossplane-lint.yaml"

	// ConfigAPIVersion is the apiVersion of a lint config.
	ConfigAPIVersion = "lint.crossplane.io/v1alpha1"

	// ConfigKind is the kind of a lint config.
	ConfigKind = "LintConfig"
)

// Config is a parsed lint config file. It holds only overrides of the
// built-in defaults.
type Config struct {
	APIVersion string                `yaml:"apiVersion"`
	Kind       string                `yaml:"kind"`
	Rules      map[string]RuleConfig `yaml:"rules"`

	// CustomRules is parsed only so that a config using it gets a clear
	// error. Custom CEL rules are not implemented yet.
	CustomRules yaml.Node `yaml:"customRules"`
}

// RuleConfig is one entry under rules. It is always an object with a
// required severity and, for a rule that takes them, options.
type RuleConfig struct {
	Severity Severity       `yaml:"severity"`
	Options  map[string]any `yaml:"options"`
}

// ParseConfig parses a lint config strictly: unknown keys, duplicate keys and
// malformed rule values are errors. An empty file is a valid config with no
// overrides.
func ParseConfig(data []byte) (*Config, error) {
	cfg := &Config{}
	if len(bytes.TrimSpace(data)) == 0 {
		return cfg, nil
	}

	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		return nil, errors.Wrap(err, "cannot parse lint config")
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("a lint config must be exactly one YAML document")
	}
	if err := checkRuleValues(&doc); err != nil {
		return nil, errors.Wrap(err, "cannot parse lint config")
	}

	// Every rule value is an object, so it decodes into a plain struct and
	// KnownFields covers its keys as well as the top-level ones.
	strict := yaml.NewDecoder(bytes.NewReader(data))
	strict.KnownFields(true)
	if err := strict.Decode(cfg); err != nil {
		return nil, errors.Wrap(err, "cannot parse lint config")
	}

	if cfg.APIVersion != ConfigAPIVersion {
		return nil, errors.Errorf("lint config apiVersion must be %q, got %q", ConfigAPIVersion, cfg.APIVersion)
	}
	if cfg.Kind != ConfigKind {
		return nil, errors.Errorf("lint config kind must be %q, got %q", ConfigKind, cfg.Kind)
	}
	if !cfg.CustomRules.IsZero() {
		return nil, errors.Errorf("line %d: customRules are not supported yet", cfg.CustomRules.Line)
	}

	ids := make([]string, 0, len(cfg.Rules))
	for id := range cfg.Rules {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		sev := cfg.Rules[id].Severity
		if sev == "" {
			return nil, errors.Errorf("rule %q: severity is required", id)
		}
		if _, err := ParseSeverity(string(sev)); err != nil {
			return nil, errors.Wrapf(err, "rule %q", id)
		}
	}
	return cfg, nil
}

// checkRuleValues reports a rule value that is not an object, with a message
// that says what to write instead. yaml.v3's own type errors do not.
func checkRuleValues(doc *yaml.Node) error {
	if len(doc.Content) == 0 {
		return nil
	}
	_, rules := mapEntry(doc.Content[0], "rules")
	if rules == nil || rules.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(rules.Content); i += 2 {
		id, v := rules.Content[i].Value, rules.Content[i+1]
		switch {
		case v.Kind == yaml.ScalarNode && v.Tag == "!!null":
			return errors.Errorf("line %d: rule %q has no value; give it a severity key set to off, warn or error", v.Line, id)
		case v.Kind == yaml.ScalarNode:
			return errors.Errorf("line %d: rule %q: a rule value is an object, not a bare severity; move %q under a severity key", v.Line, id, v.Value)
		case v.Kind == yaml.SequenceNode:
			return errors.Errorf("line %d: rule %q: the list form is not supported; a rule value is an object with a severity key and, optionally, an options key", v.Line, id)
		case v.Kind != yaml.MappingNode:
			return errors.Errorf("line %d: rule %q: a rule value is an object with severity and options", v.Line, id)
		}
		if _, opts := mapEntry(v, "options"); opts != nil && opts.Kind != yaml.MappingNode {
			return errors.Errorf("line %d: rule %q: options must be an object", opts.Line, id)
		}
	}
	return nil
}

// LoadConfig reads and parses the config file at path.
func LoadConfig(fs afero.Fs, path string) (*Config, error) {
	data, err := afero.ReadFile(fs, path)
	if err != nil {
		return nil, errors.Wrapf(err, "cannot read lint config %q", path)
	}
	cfg, err := ParseConfig(data)
	return cfg, errors.Wrapf(err, "invalid lint config %q", path)
}

// FindConfig searches dir and its ancestors for ConfigFileName and returns the
// nearest one, or "" if there is none.
func FindConfig(fs afero.Fs, dir string) (string, error) {
	for {
		p := filepath.Join(dir, ConfigFileName)
		_, err := fs.Stat(p)
		if err == nil {
			return p, nil
		}
		if !os.IsNotExist(err) {
			return "", errors.Wrapf(err, "cannot check for lint config %q", p)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}

// Setting is a rule's effective configuration.
type Setting struct {
	Severity Severity
	// Options is the rule's options as JSON, or nil if none are configured.
	Options []byte
}

// Resolve combines the config with the rules' defaults and returns the
// effective setting of every rule in the registry. A nil config applies the
// defaults. Entries naming no known rule, and options that fail the rule's
// schema, are errors.
func (c *Config) Resolve(reg *Registry) (map[string]Setting, error) {
	out := map[string]Setting{}
	for _, rule := range reg.All() {
		m := rule.Meta()
		sev := m.DefaultSeverity
		if sev == "" {
			sev = SeverityOff
		}
		out[m.ID] = Setting{Severity: sev}
	}
	if c == nil {
		return out, nil
	}

	ids := make([]string, 0, len(c.Rules))
	for id := range c.Rules {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		rc := c.Rules[id]
		rule, ok := reg.Get(id)
		if !ok {
			return nil, errors.Errorf("unknown rule %q", id)
		}
		s := Setting{Severity: rc.Severity}
		if rc.Options != nil {
			opts, err := validateOptions(rule.Meta(), rc.Options)
			if err != nil {
				return nil, err
			}
			s.Options = opts
		}
		out[id] = s
	}
	return out, nil
}

// validateOptions checks options against the rule's declared schema and
// returns them as JSON.
func validateOptions(m Meta, opts map[string]any) ([]byte, error) {
	if m.Options == nil {
		return nil, errors.Errorf("rule %q takes no options", m.ID)
	}

	raw, err := json.Marshal(opts)
	if err != nil {
		return nil, errors.Wrapf(err, "cannot encode options for rule %q", m.ID)
	}
	// Decode with whole numbers as int64, as the API server does, so that
	// integer fields validate.
	var obj map[string]any
	if err := utiljson.Unmarshal(raw, &obj); err != nil {
		return nil, errors.Wrapf(err, "cannot decode options for rule %q", m.ID)
	}

	for k := range obj {
		if _, ok := m.Options.Properties[k]; !ok && m.Options.AdditionalProperties == nil {
			return nil, errors.Errorf("invalid options for rule %q: unknown option %q", m.ID, k)
		}
	}

	internal := &apiextensions.JSONSchemaProps{}
	if err := extv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(m.Options, internal, nil); err != nil {
		return nil, errors.Wrapf(err, "cannot read the options schema of rule %q", m.ID)
	}
	v, _, err := validation.NewSchemaValidator(internal)
	if err != nil {
		return nil, errors.Wrapf(err, "cannot build the options schema of rule %q", m.ID)
	}
	if errs := validation.ValidateCustomResource(nil, obj, v); len(errs) > 0 {
		return nil, errors.Wrapf(errs.ToAggregate(), "invalid options for rule %q", m.ID)
	}
	return raw, nil
}
