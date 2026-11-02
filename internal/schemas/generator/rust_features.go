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

package generator

import (
	"io/fs"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/spf13/afero"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

const (
	// rustDefaultFeature is the feature Cargo enables for a dependent that
	// does not say otherwise.
	rustDefaultFeature = "default"

	// rustAllFeature enables every module of models. It is the default, so a
	// function that does not mention features compiles all of them.
	rustAllFeature = "all"

	// rustFeaturesHeader opens the features table of the generated manifest.
	rustFeaturesHeader = `
# One feature per module of models. A function that imports a few API groups
# from a crate holding many can compile just those: depend on this crate with
# default-features = false and list their features. A feature enables the
# features of the modules its models refer to, so only the modules a function
# imports from need listing.
[features]
`
)

// rustCratePathRE matches a reference to another generated type. The emitter
// writes every such reference as an absolute path, see rustPathForSchemaName.
var rustCratePathRE = regexp.MustCompile(`\bcrate::((?:\w+::)+)\w+`)

// rustFeature is the Cargo feature that gates one module of generated models.
type rustFeature struct {
	name string
	deps []string // the features of the modules this module's models refer to
}

// rustFeatureName returns the name of the feature gating the module in dir:
// its path below src, joined by dashes. Module names hold no dashes, see
// rustModuleSegment, so two modules never share a feature name.
func rustFeatureName(dir string) string {
	return strings.ReplaceAll(strings.TrimPrefix(dir, rustSrcDir+"/"), "/", "-")
}

// rustCollectFeatures returns the feature of every module holding models, keyed
// by the module's directory.
//
// A module holds models if its directory holds a file of types, which makes it
// the module of an API group and version, or of a Kubernetes package. The
// modules above it only declare other modules, cost nothing to compile and are
// not gated. Models directly in src have no module to gate, so they are always
// compiled.
func rustCollectFeatures(crateFS afero.Fs) (map[string]rustFeature, error) {
	files := make(map[string][]string)
	err := afero.Walk(crateFS, rustSrcDir, func(p string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !rustIsTypeFile(info.Name()) {
			return nil
		}
		if dir := path.Dir(p); dir != rustSrcDir {
			files[dir] = append(files[dir], p)
		}
		return nil
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to walk the generated Rust crate")
	}

	features := make(map[string]rustFeature, len(files))
	for dir, paths := range files {
		deps := make(map[string]bool)

		// A module is declared by the module above it, so it is only reachable
		// when every gated module on the way down to it is compiled too.
		for parent := path.Dir(dir); parent != rustSrcDir && parent != "."; parent = path.Dir(parent) {
			if _, ok := files[parent]; ok {
				deps[rustFeatureName(parent)] = true
			}
		}

		for _, p := range paths {
			contents, err := afero.ReadFile(crateFS, p)
			if err != nil {
				return nil, errors.Wrapf(err, "failed to read %q", p)
			}
			for _, target := range rustReferencedModules(string(contents)) {
				if _, ok := files[target]; ok && target != dir {
					deps[rustFeatureName(target)] = true
				}
			}
		}

		features[dir] = rustFeature{name: rustFeatureName(dir), deps: slices.Sorted(maps.Keys(deps))}
	}

	return features, nil
}

// rustReferencedModules returns the directories of the modules a generated
// file's code refers to. Doc comments are schema descriptions, which can hold
// anything, so they are skipped.
func rustReferencedModules(code string) []string {
	var dirs []string
	for line := range strings.SplitSeq(code, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		for _, m := range rustCratePathRE.FindAllStringSubmatch(line, -1) {
			module := strings.Split(strings.TrimSuffix(m[1], "::"), "::")
			dirs = append(dirs, rustModuleDir(module))
		}
	}
	return dirs
}

// rustIsTypeFile reports whether a file in the crate's source tree holds
// generated types, as opposed to declaring modules.
func rustIsTypeFile(name string) bool {
	return strings.HasSuffix(name, ".rs") && name != "mod.rs" && name != "lib.rs"
}

// rustRenderCargoToml renders the crate manifest for the given features.
func rustRenderCargoToml(features map[string]rustFeature) string {
	if len(features) == 0 {
		return rustCargoToml
	}

	sorted := slices.SortedFunc(maps.Values(features), func(a, b rustFeature) int {
		return strings.Compare(a.name, b.name)
	})

	var sb strings.Builder
	sb.WriteString(rustCargoToml)
	sb.WriteString(rustFeaturesHeader)
	sb.WriteString(rustDefaultFeature + " = [\"" + rustAllFeature + "\"]\n")

	sb.WriteString(rustAllFeature + " = [\n")
	for _, f := range sorted {
		sb.WriteString("    \"" + f.name + "\",\n")
	}
	sb.WriteString("]\n")

	for _, f := range sorted {
		quoted := make([]string, len(f.deps))
		for i, dep := range f.deps {
			quoted[i] = "\"" + dep + "\""
		}
		sb.WriteString(f.name + " = [" + strings.Join(quoted, ", ") + "]\n")
	}

	return sb.String()
}
