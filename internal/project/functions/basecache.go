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

package functions

import (
	"os"
	"path/filepath"
)

// DefaultBaseImageCacheDir returns the default per-user cache directory for
// function runtime base image layers. It sits beside the xpkg cache rather
// than inside it, since the two hold different kinds of artifact and are
// pruned on different terms.
func DefaultBaseImageCacheDir() string {
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "crossplane", "base-images")
}
