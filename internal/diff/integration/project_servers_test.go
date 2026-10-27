//go:build integration

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

package integration

import (
	"context"
	"strings"
	"sync"

	diffv1alpha1 "github.com/crossplane/upjet/v2/proto/diff/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/crossplane/cli/v2/internal/diffserver"
	"github.com/crossplane/cli/v2/internal/simulate"
)

// recordingServers is a simulate.Servers that starts nothing. It records the
// inputs it is asked to ensure and routes XDownstreamResource to plans only
// once an input whose image contains wantImage has been ensured.
type recordingServers struct {
	wantImage string
	plans     diffv1alpha1.PlanServiceClient

	mu      sync.Mutex
	ensured []diffserver.Input
}

var _ simulate.Servers = (*recordingServers)(nil)

func (r *recordingServers) Ensure(_ context.Context, inputs []diffserver.Input) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensured = append(r.ensured, inputs...)
	return nil
}

func (r *recordingServers) Route(gk schema.GroupKind) (diffv1alpha1.PlanServiceClient, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if gk.Kind != "XDownstreamResource" {
		return nil, false
	}
	for _, in := range r.ensured {
		if strings.Contains(in.Image, r.wantImage) {
			return r.plans, true
		}
	}
	return nil, false
}

func (r *recordingServers) Stop(context.Context) {}
