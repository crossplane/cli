package render

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
