package render

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"
)

func TestSetDefaultCrossplaneDockerNetwork(t *testing.T) {
	type args struct {
		flags     EngineFlags
		functions []pkgv1.Function
	}
	type want struct {
		flags EngineFlags
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"ExplicitNetworkIsPreserved": {
			reason: "An explicit --crossplane-docker-network value should not be overwritten by function annotations.",
			args: args{
				flags: EngineFlags{CrossplaneDockerNetwork: "explicit-network"},
				functions: []pkgv1.Function{
					functionWithAnnotations(map[string]string{AnnotationKeyRuntimeDockerNetwork: "function-network"}),
				},
			},
			want: want{
				flags: EngineFlags{CrossplaneDockerNetwork: "explicit-network"},
			},
		},
		"FirstFunctionAnnotationIsUsed": {
			reason: "When no explicit network is set, the render engine should join the first function runtime Docker network.",
			args: args{
				functions: []pkgv1.Function{
					functionWithAnnotations(map[string]string{"example.org/other": "ignored"}),
					functionWithAnnotations(map[string]string{AnnotationKeyRuntimeDockerNetwork: "first-network"}),
					functionWithAnnotations(map[string]string{AnnotationKeyRuntimeDockerNetwork: "second-network"}),
				},
			},
			want: want{
				flags: EngineFlags{CrossplaneDockerNetwork: "first-network"},
			},
		},
		"NoNetwork": {
			reason: "The flags should remain unchanged when no function has a runtime Docker network annotation.",
			args: args{
				functions: []pkgv1.Function{
					functionWithAnnotations(nil),
					functionWithAnnotations(map[string]string{"example.org/other": "ignored"}),
				},
			},
			want: want{},
		},
		"NoFunctionsPreservesDefaultBehavior": {
			reason: "No functions should leave CrossplaneDockerNetwork unset so engine setup can use its default temporary network behavior.",
			want:   want{},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Log(tc.reason)

			tc.args.flags.SetDefaultCrossplaneDockerNetwork(tc.args.functions)
			if diff := cmp.Diff(tc.want.flags, tc.args.flags); diff != "" {
				t.Errorf("SetDefaultCrossplaneDockerNetwork(...), -want, +got:\n%s", diff)
			}
		})
	}
}
