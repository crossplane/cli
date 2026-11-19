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

package diffprocessor

import "sync"

// Progress receives coarse processing progress from the diff processors:
// which composite is being worked on, and how many of how many are done.
// Rendering and planning a composite can take a while, and without this the
// CLI looks hung between its last setup step and the plan output.
//
// The reporter is settable after processor construction, so a caller can
// install it when its output machinery (e.g. a spinner session) is running
// and remove it afterwards. Safe for concurrent use; a nil *Progress or an
// unset reporter drops reports.
type Progress struct {
	mu sync.Mutex
	fn func(done, total int, detail string)
}

// NewProgress returns a Progress with no reporter installed.
func NewProgress() *Progress {
	return &Progress{}
}

// SetReporter installs fn as the reporter, replacing any previous one. A nil
// fn removes the reporter.
func (p *Progress) SetReporter(fn func(done, total int, detail string)) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fn = fn
}

// Report reports that done of total composites are processed and detail is
// being worked on.
func (p *Progress) Report(done, total int, detail string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	fn := p.fn
	p.mu.Unlock()
	if fn != nil {
		fn(done, total, detail)
	}
}
