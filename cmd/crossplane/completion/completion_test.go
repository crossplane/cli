package completion

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/posener/complete"

	"github.com/crossplane/cli/v2/internal/kube"
)

func TestParseConfigFlags(t *testing.T) {
	cases := map[string]struct {
		reason string
		args   []string
		want   kube.ConfigFlags
	}{
		"Equals": {
			reason: "The --flag=value form is parsed.",
			args:   []string{"trace", "x", "--kubeconfig=/tmp/kubeconfig"},
			want:   kube.ConfigFlags{Kubeconfig: "/tmp/kubeconfig"},
		},
		"Space": {
			reason: "The --flag value form is parsed.",
			args:   []string{"--kubeconfig", "/tmp/kubeconfig"},
			want:   kube.ConfigFlags{Kubeconfig: "/tmp/kubeconfig"},
		},
		"None": {
			reason: "No --kubeconfig flag yields the zero value.",
			args:   []string{"trace", "x"},
			want:   kube.ConfigFlags{},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := parseConfigFlags(complete.Args{All: tc.args})
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("%s\nparseConfigFlags(): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestParseImpersonation(t *testing.T) {
	cases := map[string]struct {
		reason string
		args   []string
		want   kube.ImpersonationFlags
	}{
		"Equals": {
			reason: "The --flag=value form is parsed.",
			args:   []string{"trace", "x", "--as=jane", "--as-uid=42", "--as-group=team-a"},
			want:   kube.ImpersonationFlags{As: "jane", AsUID: "42", AsGroup: []string{"team-a"}},
		},
		"Space": {
			reason: "The --flag value form is parsed.",
			args:   []string{"--as", "jane", "--as-uid", "42", "--as-group", "team-a"},
			want:   kube.ImpersonationFlags{As: "jane", AsUID: "42", AsGroup: []string{"team-a"}},
		},
		"RepeatableGroups": {
			reason: "--as-group can be repeated.",
			args:   []string{"--as-group=team-a", "--as-group", "team-b"},
			want:   kube.ImpersonationFlags{AsGroup: []string{"team-a", "team-b"}},
		},
		"None": {
			reason: "No impersonation flags yields the zero value.",
			args:   []string{"trace", "x"},
			want:   kube.ImpersonationFlags{},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := parseImpersonation(complete.Args{All: tc.args})
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("%s\nparseImpersonation(): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}
