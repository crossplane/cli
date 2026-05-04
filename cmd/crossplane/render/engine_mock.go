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

package render

import (
	"context"

	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"
	renderv1alpha1 "github.com/crossplane/crossplane/v2/proto/render/v1alpha1"
)

// MockEngine is a function-field mock of the Engine interface.
type MockEngine struct {
	MockCheckContextSupport func() error
	MockSetup               func(ctx context.Context, fns []pkgv1.Function) (func(), error)
	MockRender              func(ctx context.Context, req *renderv1alpha1.RenderRequest) (*renderv1alpha1.RenderResponse, error)
}

// CheckContextSupport calls MockCheckContextSupport. If unset, it returns nil.
func (m *MockEngine) CheckContextSupport() error {
	if m.MockCheckContextSupport == nil {
		return nil
	}
	return m.MockCheckContextSupport()
}

// Setup calls MockSetup. If unset, it returns a no-op cleanup and no error.
func (m *MockEngine) Setup(ctx context.Context, fns []pkgv1.Function) (func(), error) {
	if m.MockSetup == nil {
		return func() {}, nil
	}
	return m.MockSetup(ctx, fns)
}

// Render calls MockRender. If unset, it returns a RenderResponse echoing the
// request's composite resource as the output.
func (m *MockEngine) Render(ctx context.Context, req *renderv1alpha1.RenderRequest) (*renderv1alpha1.RenderResponse, error) {
	if m.MockRender == nil {
		return &renderv1alpha1.RenderResponse{
			Output: &renderv1alpha1.RenderResponse_Composite{
				Composite: &renderv1alpha1.CompositeOutput{
					CompositeResource: req.GetComposite().GetCompositeResource(),
				},
			},
		}, nil
	}
	return m.MockRender(ctx, req)
}
