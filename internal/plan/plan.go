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

package plan

import (
	"context"

	diffv1alpha1 "github.com/crossplane/upjet/v2/proto/diff/v1alpha1"
	"google.golang.org/protobuf/types/known/structpb"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

// Plan asks a provider diff server what applying desired would do. actual is
// the live resource the desired one corresponds to, carrying its
// status.atProvider and external-name annotation; nil means the resource does
// not exist yet and plans as a create. store seeds the server's in-memory
// Kubernetes client with objects (Secrets, referenced resources) it may need
// to resolve the desired state; it is already in proto form so callers can
// convert a shared object once across many Plan calls.
//
// gRPC errors are returned as-is so callers can branch on them; use
// IsNotSupported to detect the "this resource cannot be planned" case that
// should fall back to an approximate client-side diff.
func Plan(ctx context.Context, c diffv1alpha1.PlanServiceClient, desired, actual *unstructured.Unstructured, store []*structpb.Struct) (*ResourcePlan, error) {
	req := &diffv1alpha1.PlanRequest{KubernetesObjectStore: store}

	var err error
	if req.DesiredResource, err = structpb.NewStruct(desired.Object); err != nil {
		return nil, errors.Wrap(err, "cannot convert desired resource")
	}
	if actual != nil {
		if req.ActualResource, err = structpb.NewStruct(actual.Object); err != nil {
			return nil, errors.Wrap(err, "cannot convert actual resource")
		}
	}

	rsp, err := c.Plan(ctx, req)
	if err != nil {
		return nil, err
	}
	return FromResponse(desired, rsp)
}
