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

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gcmp "github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	ucomposite "github.com/crossplane/crossplane-runtime/v2/pkg/resource/unstructured/composite"

	apiextensionsv1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1"
	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"

	"github.com/crossplane/cli/v2/pkg/render"
	renderv1alpha1 "github.com/crossplane/cli/v2/proto/render/v1alpha1"
)

// newTestRenderFn builds an EngineRenderFn whose Session renders on the supplied mock engine and
// leases in a temporary directory. Its functions run as development runtimes (see
// minimalRenderInputs), so nothing needs a Docker daemon.
func newTestRenderFn(t *testing.T, mock *render.MockEngine) *EngineRenderFn {
	t.Helper()

	return &EngineRenderFn{
		session: render.NewSession(render.EngineFlags{}, logging.NewNopLogger(),
			render.WithLeaseDir(t.TempDir()),
			render.WithEngineFactory(func(*render.EngineFlags, logging.Logger) render.Engine { return mock }),
			render.WithWarn(func(err error) { t.Errorf("unexpected warning: %v", err) }),
		),
	}
}

// The three tests below (HappyPath, CleanupIdempotent, ConcurrentRenders) each
// exercise a distinct lifecycle property of EngineRenderFn — they're not
// data-variant cases of a single operation. HappyPath asserts renders succeed
// and share one engine setup. CleanupIdempotent asserts the engine's setup
// cleanup runs once however many times Cleanup is called. ConcurrentRenders spawns concurrent
// goroutines and asserts their renders are inside the engine at the same time
// while the function they share starts once. Forcing these into a table would require per-row
// setup hooks, per-row assertion sets, and per-row concurrency primitives —
// the rows would share almost nothing. Procedural tests read more clearly
// here.

// minimalRenderInputs returns RenderInputs with just enough populated that
// BuildCompositeRequest will not error during marshaling. Includes a single
// development-runtime function, which the Session "starts" without a container.
func minimalRenderInputs() RenderInputs {
	xr := ucomposite.New()
	xr.SetAPIVersion("example.org/v1")
	xr.SetKind("XExample")
	xr.SetName("test-xr")

	return RenderInputs{
		CompositeResource: xr,
		Composition: &apiextensionsv1.Composition{
			Spec: apiextensionsv1.CompositionSpec{
				Mode: apiextensionsv1.CompositionModePipeline,
			},
		},
		Functions: []pkgv1.Function{
			{ObjectMeta: metav1.ObjectMeta{
				Name: "fn-default",
				Annotations: map[string]string{
					render.AnnotationKeyRuntime:                  string(render.AnnotationValueRuntimeDevelopment),
					render.AnnotationKeyRuntimeDevelopmentTarget: "fn-default:9443",
				},
			}},
		},
	}
}

func TestEngineRenderFn_HappyPath(t *testing.T) {
	ctx := t.Context()

	var renderCalls atomic.Int32

	mock := &render.MockEngine{
		MockRender: func(_ context.Context, req *renderv1alpha1.RenderRequest) (*renderv1alpha1.RenderResponse, error) {
			renderCalls.Add(1)
			// Echo the composite resource back so ParseCompositeResponse succeeds.
			if c := req.GetComposite(); c == nil {
				t.Fatalf("expected composite input on request")
			}

			return &renderv1alpha1.RenderResponse{
				Output: &renderv1alpha1.RenderResponse_Composite{
					Composite: &renderv1alpha1.CompositeOutput{
						CompositeResource: req.GetComposite().GetCompositeResource(),
					},
				},
			}, nil
		},
	}

	e := newTestRenderFn(t, mock)

	// First render: Setup + StartFunctionRuntimes should each run once.
	out, err := e.Render(ctx, logging.NewNopLogger(), minimalRenderInputs())
	if err != nil {
		t.Fatalf("first Render: unexpected error: %v", err)
	}

	if out.CompositeResource == nil {
		t.Fatalf("first Render: expected CompositeResource in output")
	}

	if got := renderCalls.Load(); got != 1 {
		t.Fatalf("first Render: engine.Render calls = %d, want 1", got)
	}

	// Second render: runtimes should be reused, no new start call.
	if _, err := e.Render(ctx, logging.NewNopLogger(), minimalRenderInputs()); err != nil {
		t.Fatalf("second Render: unexpected error: %v", err)
	}

	if got := renderCalls.Load(); got != 2 {
		t.Fatalf("second Render: engine.Render calls = %d, want 2", got)
	}
}

func TestEngineRenderFn_CleanupIdempotent(t *testing.T) {
	ctx := t.Context()

	var setupCalls, setupCleanupCalls int32

	mock := &render.MockEngine{
		MockSetup: func(_ context.Context, _ []pkgv1.Function) (func(), error) {
			atomic.AddInt32(&setupCalls, 1)
			return func() { atomic.AddInt32(&setupCleanupCalls, 1) }, nil
		},
	}

	e := newTestRenderFn(t, mock)

	// Render once to establish state.
	if _, err := e.Render(ctx, logging.NewNopLogger(), minimalRenderInputs()); err != nil {
		t.Fatalf("Render: unexpected error: %v", err)
	}

	if got := atomic.LoadInt32(&setupCalls); got != 1 {
		t.Fatalf("Render: MockSetup = %d, want 1", got)
	}

	// First real cleanup: runs the setup cleanup exactly once.
	if err := e.Cleanup(ctx); err != nil {
		t.Fatalf("first Cleanup: unexpected error: %v", err)
	}

	if got := atomic.LoadInt32(&setupCleanupCalls); got != 1 {
		t.Fatalf("first Cleanup: setupCleanup = %d, want 1", got)
	}

	// Second cleanup: idempotent (no extra setup cleanup).
	if err := e.Cleanup(ctx); err != nil {
		t.Fatalf("second Cleanup: unexpected error: %v", err)
	}

	if got := atomic.LoadInt32(&setupCleanupCalls); got != 1 {
		t.Fatalf("second Cleanup: setupCleanup = %d, want still 1", got)
	}
}

func TestEngineRenderFn_ConcurrentRenders(t *testing.T) {
	ctx := t.Context()

	const renders = 4

	// Each render waits inside the engine until every render is inside it, so the test only passes
	// if no lock is held across engine.Render. A serialized implementation times out instead.
	var inside sync.WaitGroup
	inside.Add(renders)

	allInside := make(chan struct{})
	go func() {
		inside.Wait()
		close(allInside)
	}()

	var setupCalls atomic.Int32

	mock := &render.MockEngine{
		MockSetup: func(_ context.Context, _ []pkgv1.Function) (func(), error) {
			setupCalls.Add(1)
			return func() {}, nil
		},
		MockRender: func(_ context.Context, req *renderv1alpha1.RenderRequest) (*renderv1alpha1.RenderResponse, error) {
			inside.Done()

			select {
			case <-allInside:
			case <-time.After(10 * time.Second):
				return nil, errors.New("renders did not overlap: another render never entered the engine")
			}

			return &renderv1alpha1.RenderResponse{
				Output: &renderv1alpha1.RenderResponse_Composite{
					Composite: &renderv1alpha1.CompositeOutput{
						CompositeResource: req.GetComposite().GetCompositeResource(),
					},
				},
			}, nil
		},
	}

	e := newTestRenderFn(t, mock)

	var wg sync.WaitGroup
	for range renders {
		wg.Go(func() {
			if _, err := e.Render(ctx, logging.NewNopLogger(), minimalRenderInputs()); err != nil {
				t.Errorf("Render: %v", err)
			}
		})
	}

	wg.Wait()

	// Every render needs fn-default; it must be set up and started once, not once per render.
	if got := setupCalls.Load(); got != 1 {
		t.Errorf("engine.Setup calls = %d, want 1", got)
	}
}

// TestNewEngineRenderFn_ResolvedRenderImage asserts that the render-backend
// selector arguments reach the engine, and reach it in a form that is actually
// pullable. The engine resolves it with render.CrossplaneImageFromFlags and
// keeps it in the image field; without that field a caller that dropped the
// pin entirely would look identical to one that honoured it (crossplane-diff#488).
func TestNewEngineRenderFn_ResolvedRenderImage(t *testing.T) {
	stable := render.DefaultCrossplaneImage + ":stable"

	tests := map[string]struct {
		binaryPath string
		version    string
		image      string
		want       string
	}{
		"NoSelectorResolvesStable": {
			want: stable,
		},
		"VersionPinResolvesVersionedTag": {
			version: "v2.4.0",
			want:    render.DefaultCrossplaneImage + ":v2.4.0",
		},
		// Upstream formats the version into the tag verbatim and publishes only
		// v-prefixed tags, so the bare form ValidateMinRenderVersion accepts has
		// to be normalized before it gets here or it becomes an unpullable
		// reference.
		"BareVersionPinIsNormalized": {
			version: "2.3.4",
			want:    render.DefaultCrossplaneImage + ":v2.3.4",
		},
		"NonSemverVersionPinPassesThrough": {
			version: "stable",
			want:    stable,
		},
		"ImageOverrideResolvesVerbatim": {
			image: "example.com/mirror/crossplane:v2.4.0",
			want:  "example.com/mirror/crossplane:v2.4.0",
		},
		// The local engine pulls nothing.
		"BinaryPathResolvesNoImage": {
			binaryPath: "/usr/local/bin/crossplane",
			want:       "",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			e := NewEngineRenderFn(logging.NewNopLogger(), NewAdvisoryCollector(nil), tt.binaryPath, tt.version, tt.image, "")

			if diff := gcmp.Diff(tt.want, e.image); diff != "" {
				t.Errorf("image mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestNewEngineRenderFn_UncomparableImageWarns asserts that an image reference
// whose version cannot be compared against MinCrossplaneRenderVersion draws an
// advisory rather than silence. Refusing such a reference would break the
// private-mirror and digest-pinning cases --crossplane-image exists to serve, so
// a warning is the only thing left that keeps the floor visible.
func TestNewEngineRenderFn_UncomparableImageWarns(t *testing.T) {
	tests := map[string]struct {
		version     string
		image       string
		wantWarning bool
	}{
		"UncomparableImageWarns": {
			image:       "internal-mirror/crossplane",
			wantWarning: true,
		},
		"ComparableImageStaysQuiet": {
			image:       "internal-mirror/crossplane:v2.4.1",
			wantWarning: false,
		},
		// The engine's own :stable default is not the user handing us an
		// uncomparable reference, and warning on it would fire on every default
		// invocation.
		"NoSelectorStaysQuiet": {
			wantWarning: false,
		},
		"VersionPinStaysQuiet": {
			version:     "v2.4.0",
			wantWarning: false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			advisories := NewAdvisoryCollector(nil)

			NewEngineRenderFn(logging.NewNopLogger(), advisories, "", tt.version, tt.image, "")

			got := advisories.Advisories()

			if !tt.wantWarning {
				if len(got) != 0 {
					t.Fatalf("warnings = %v, want none", got)
				}

				return
			}

			if len(got) != 1 {
				t.Fatalf("warnings = %v, want exactly 1", got)
			}

			if !strings.Contains(got[0].Message, tt.image) {
				t.Errorf("warning = %q, want it to name the image %q", got[0].Message, tt.image)
			}
		})
	}
}

// TestEngineRenderFn_StaleRenderImageHint asserts that the one render failure
// shape that is purely an environment problem — a render backend so old it has
// no `crossplane internal render` subcommand, which is what a never-refreshed
// local `:stable` image degrades into — carries remediation instead of leaving
// the user with a bare kong usage error. The hint must augment the original
// error, never replace or soften it.
func TestEngineRenderFn_StaleRenderImageHint(t *testing.T) {
	// The verbatim shape upstream surfaces: the container's stderr, wrapped and
	// marked as render.ErrStaleRenderBackend.
	staleErr := errors.Errorf("crossplane internal render in Docker returned error with output: crossplane: error: unexpected argument internal: %w", render.ErrStaleRenderBackend)

	tests := map[string]struct {
		image string
		// renderErr is what the engine returns alongside a nil response.
		renderErr error
		// wantHintFor, when non-empty, must appear in the final error, as must
		// MinCrossplaneRenderVersion.
		wantHintFor string
	}{
		"StaleDockerImageGetsHint": {
			image:       "xpkg.crossplane.io/crossplane/crossplane:stable",
			renderErr:   staleErr,
			wantHintFor: "xpkg.crossplane.io/crossplane/crossplane:stable",
		},
		"StaleLocalBinaryGetsHint": {
			image:       "",
			renderErr:   errors.Errorf("crossplane: error: unexpected argument internal: %w", render.ErrStaleRenderBackend),
			wantHintFor: MinCrossplaneRenderVersion,
		},
		// Narrowness matters more than coverage here: misattributing an ordinary
		// pipeline or connectivity failure to a stale image would send users to
		// fix the wrong thing.
		"UnrelatedRenderFailureGetsNoHint": {
			image:     "xpkg.crossplane.io/crossplane/crossplane:stable",
			renderErr: errors.New("cannot connect to the Docker daemon"),
		},
		"UnrelatedUnexpectedArgumentGetsNoHint": {
			image:     "xpkg.crossplane.io/crossplane/crossplane:stable",
			renderErr: errors.New("crossplane: error: unexpected argument render"),
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()

			mock := &render.MockEngine{
				MockRender: func(_ context.Context, _ *renderv1alpha1.RenderRequest) (*renderv1alpha1.RenderResponse, error) {
					return nil, tt.renderErr
				},
			}

			e := newTestRenderFn(t, mock)
			e.image = tt.image

			_, err := e.Render(ctx, logging.NewNopLogger(), minimalRenderInputs())
			if err == nil {
				t.Fatalf("Render() = nil error, want the render failure surfaced")
			}

			// Whatever else happens, the original failure must survive intact.
			if !errors.Is(err, tt.renderErr) {
				t.Errorf("Render() error = %v, want it to wrap the engine error", err)
			}

			if !strings.Contains(err.Error(), tt.renderErr.Error()) {
				t.Errorf("Render() error = %q, want it to contain the engine message %q", err.Error(), tt.renderErr.Error())
			}

			if tt.wantHintFor == "" {
				if strings.Contains(err.Error(), MinCrossplaneRenderVersion) {
					t.Errorf("Render() error = %q, want no stale-image hint", err.Error())
				}

				return
			}

			for _, want := range []string{tt.wantHintFor, MinCrossplaneRenderVersion} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Render() error = %q, want it to contain %q", err.Error(), want)
				}
			}
		})
	}
}

func TestEngineRenderFn_StartFunctionsOutsideXRBudget(t *testing.T) {
	mock := &render.MockEngine{
		// Starting the functions outlasts the XR's budget and fails if it runs under it.
		MockSetup: func(ctx context.Context, _ []pkgv1.Function) (func(), error) {
			time.Sleep(150 * time.Millisecond)
			return func() {}, ctx.Err()
		},
		MockRender: func(_ context.Context, req *renderv1alpha1.RenderRequest) (*renderv1alpha1.RenderResponse, error) {
			return &renderv1alpha1.RenderResponse{
				Output: &renderv1alpha1.RenderResponse_Composite{
					Composite: &renderv1alpha1.CompositeOutput{CompositeResource: req.GetComposite().GetCompositeResource()},
				},
			}, nil
		},
	}

	e := newTestRenderFn(t, mock)

	xrCtx, cancel := NewXRContext(t.Context(), 50*time.Millisecond)
	defer cancel()

	if _, err := e.Render(xrCtx, logging.NewNopLogger(), minimalRenderInputs()); err != nil {
		t.Errorf("Render(...): function start counted against the XR budget: %v", err)
	}
}
