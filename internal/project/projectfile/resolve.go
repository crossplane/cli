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

package projectfile

import (
	"os"
	"path/filepath"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	runtimexpkg "github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"

	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"
)

// Resolve returns the absolute path of the project or package metadata file to
// use. An explicit path is returned as-is (made absolute). Otherwise it probes
// for crossplane-project.yaml and then crossplane.yaml in the working
// directory.
func Resolve(path string) (string, error) {
	if path != "" {
		return filepath.Abs(path)
	}

	for _, name := range []string{clixpkg.ProjectFile, runtimexpkg.MetaFile} {
		abs, err := filepath.Abs(name)
		if err != nil {
			return "", errors.Wrapf(err, "cannot determine path for %q", name)
		}
		if _, err := os.Stat(abs); err == nil {
			return abs, nil
		}
	}

	return "", errors.Errorf("neither %s nor %s found in the current directory", clixpkg.ProjectFile, runtimexpkg.MetaFile)
}
