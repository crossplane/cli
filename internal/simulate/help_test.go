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

package simulate

import (
	"strings"
	"testing"
)

func TestHelp(t *testing.T) {
	cases := map[string]struct {
		selection bool
		want      bool
	}{
		"WithSelection":    {selection: true, want: true},
		"WithoutSelection": {selection: false, want: false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := Help(tc.selection)
			if got := strings.Contains(h, "`--include-pinned`"); got != tc.want {
				t.Errorf("Help(%t) mentions --include-pinned: got %t, want %t", tc.selection, got, tc.want)
			}
			if !strings.HasSuffix(h, helpExit) {
				t.Errorf("Help(%t) doesn't end with the exit codes", tc.selection)
			}
		})
	}
}
