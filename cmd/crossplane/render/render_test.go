package render

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"
)

func TestOverrideFunctionAnnotations(t *testing.T) {
	type args struct {
		functions   []pkgv1.Function
		annotations []string
	}
	type want struct {
		functions []pkgv1.Function
		err       error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"AnnotationsAreAppliedToAllFunctions": {
			reason: "Function annotation flags are global overrides applied to every function before rendering.",
			args: args{
				functions: []pkgv1.Function{
					functionWithAnnotations(map[string]string{"example.org/existing": "value"}),
					functionWithAnnotations(nil),
				},
				annotations: []string{"example.org/override=override-value"},
			},
			want: want{
				functions: []pkgv1.Function{
					functionWithAnnotations(map[string]string{"example.org/existing": "value", "example.org/override": "override-value"}),
					functionWithAnnotations(map[string]string{"example.org/override": "override-value"}),
				},
			},
		},
		"ExistingAnnotationIsOverridden": {
			reason: "A function annotation flag should replace an existing annotation with the same key.",
			args: args{
				functions: []pkgv1.Function{
					functionWithAnnotations(map[string]string{AnnotationKeyRuntimeDockerNetwork: "function-network"}),
				},
				annotations: []string{AnnotationKeyRuntimeDockerNetwork + "=override-network"},
			},
			want: want{
				functions: []pkgv1.Function{
					functionWithAnnotations(map[string]string{AnnotationKeyRuntimeDockerNetwork: "override-network"}),
				},
			},
		},
		"InvalidAnnotationReturnsError": {
			reason: "Invalid function annotation flags should fail instead of being silently ignored.",
			args: args{
				functions: []pkgv1.Function{
					functionWithAnnotations(map[string]string{AnnotationKeyRuntimeDockerNetwork: "function-network"}),
				},
				annotations: []string{"malformed"},
			},
			want: want{
				functions: []pkgv1.Function{
					functionWithAnnotations(map[string]string{AnnotationKeyRuntimeDockerNetwork: "function-network"}),
				},
				err: cmpopts.AnyError,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Log(tc.reason)

			err := OverrideFunctionAnnotations(tc.args.functions, tc.args.annotations)
			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("OverrideFunctionAnnotations(...), -want, +got:\n%s", diff)
			}

			if diff := cmp.Diff(tc.want.functions, tc.args.functions); diff != "" {
				t.Errorf("OverrideFunctionAnnotations(...), -want, +got:\n%s", diff)
			}
		})
	}
}

func functionWithAnnotations(annotations map[string]string) pkgv1.Function {
	return pkgv1.Function{ObjectMeta: metav1.ObjectMeta{Annotations: annotations}}
}

func TestFunctionAddressesStop(t *testing.T) {
	errA := errors.New("boom a")
	errB := errors.New("boom b")

	stopReturns := func(err error) RuntimeContext {
		return RuntimeContext{Target: "fn:9443", Stop: func(context.Context) error { return err }}
	}

	type args struct {
		contexts map[string]RuntimeContext
	}
	type want struct {
		// errs are the errors the returned error must wrap. When empty, Stop
		// must return nil.
		errs []error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"AllSucceed": {
			reason: "Stop should return nil when every runtime stops.",
			args: args{
				contexts: map[string]RuntimeContext{
					"ok-a": stopReturns(nil),
					"ok-b": stopReturns(nil),
				},
			},
		},
		"SomeFail": {
			reason: "Stop should stop every runtime even if some fail, and return every failure.",
			args: args{
				contexts: map[string]RuntimeContext{
					"ok-a":   stopReturns(nil),
					"fail-a": stopReturns(errA),
					"ok-b":   stopReturns(nil),
					"fail-b": stopReturns(errB),
				},
			},
			want: want{
				errs: []error{errA, errB},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// Repeat so Go's randomized map iteration can't hide a regression
			// to returning on the first error.
			for range 20 {
				fa := &FunctionAddresses{contexts: tc.args.contexts}

				err := fa.Stop(t.Context())

				wantErrs := tc.want.errs
				if len(wantErrs) == 0 {
					wantErrs = []error{nil}
				}
				for _, want := range wantErrs {
					if diff := cmp.Diff(want, err, cmpopts.EquateErrors()); diff != "" {
						t.Fatalf("\n%s\nStop(...): -want error, +got error:\n%s", tc.reason, diff)
					}
				}
			}
		})
	}
}

func TestStopFunctionRuntimes(t *testing.T) {
	errBoom := errors.New("boom")

	type args struct {
		// stopErr is what the runtime's Stop returns.
		stopErr error
		// cancelParent cancels the context passed to StopFunctionRuntimes
		// before calling it.
		cancelParent bool
		// noRuntimes passes nil FunctionAddresses.
		noRuntimes bool
	}
	type want struct {
		// ctxErr and hasDeadline describe the context the runtime's Stop
		// was called with.
		ctxErr      error
		hasDeadline bool
		err         error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"ParentContextLive": {
			reason: "StopFunctionRuntimes should stop each runtime with a context bounded by its own timeout.",
			want:   want{hasDeadline: true},
		},
		"ParentContextCancelled": {
			reason: "StopFunctionRuntimes should still stop each runtime, with a live but bounded context, when the parent context is already cancelled.",
			args:   args{cancelParent: true},
			want:   want{hasDeadline: true},
		},
		"StopError": {
			reason: "StopFunctionRuntimes should return the error a runtime fails to stop with.",
			args:   args{stopErr: errBoom},
			want:   want{hasDeadline: true, err: errBoom},
		},
		"NilFunctionAddresses": {
			reason: "StopFunctionRuntimes should do nothing when there are no runtimes.",
			args:   args{noRuntimes: true},
			want:   want{},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// The context the runtime is stopped with is the property under
			// test, so record it.
			var got want
			var fa *FunctionAddresses
			if !tc.args.noRuntimes {
				fa = &FunctionAddresses{contexts: map[string]RuntimeContext{
					"fn": {Target: "fn:9443", Stop: func(ctx context.Context) error {
						got.ctxErr = ctx.Err()
						_, got.hasDeadline = ctx.Deadline()
						return tc.args.stopErr
					}},
				}}
			}

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.args.cancelParent {
				cancel()
			}

			got.err = StopFunctionRuntimes(ctx, fa)

			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(want{}), cmpopts.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nStopFunctionRuntimes(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}
