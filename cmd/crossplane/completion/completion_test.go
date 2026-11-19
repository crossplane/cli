package completion

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/posener/complete"

	"github.com/crossplane/cli/v2/internal/kube"
)

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
		"FlagLast": {
			reason: "A flag that is the last word, its value not yet typed, is ignored.",
			args:   []string{"--as"},
			want:   kube.ImpersonationFlags{},
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

func TestParseContext(t *testing.T) {
	cases := map[string]struct {
		reason string
		args   []string
		want   string
	}{
		"Long": {
			reason: "The --context value is parsed.",
			args:   []string{"trace", "--context", "other", "x"},
			want:   "other",
		},
		"Short": {
			reason: "The -c value is parsed.",
			args:   []string{"-c", "other", "x"},
			want:   "other",
		},
		"LongAttached": {
			reason: "The --context=value form is parsed.",
			args:   []string{"trace", "--context=other", "x"},
			want:   "other",
		},
		"ShortAttached": {
			reason: "The -cvalue form is parsed.",
			args:   []string{"-cother", "x"},
			want:   "other",
		},
		"FlagLast": {
			reason: "A --context that is the last word, its value not yet typed, yields no context.",
			args:   []string{"trace", "--context"},
		},
		"ShortFlagLast": {
			reason: "A -c that is the last word, its value not yet typed, yields no context.",
			args:   []string{"trace", "-c"},
		},
		"None": {
			reason: "No context flag yields no context.",
			args:   []string{"trace", "x"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := parseContext(complete.Args{All: tc.args})
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("%s\nparseContext(): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestParseNamespaceOverride(t *testing.T) {
	cases := map[string]struct {
		reason string
		args   []string
		want   string
	}{
		"Long": {
			reason: "The --namespace value is parsed.",
			args:   []string{"trace", "--namespace", "team-a", "x"},
			want:   "team-a",
		},
		"Short": {
			reason: "The -n value is parsed.",
			args:   []string{"-n", "team-a", "x"},
			want:   "team-a",
		},
		"LongAttached": {
			reason: "The --namespace=value form is parsed.",
			args:   []string{"trace", "--namespace=team-a", "x"},
			want:   "team-a",
		},
		"ShortAttached": {
			reason: "The -nvalue form is parsed.",
			args:   []string{"-nteam-a", "x"},
			want:   "team-a",
		},
		"FlagLast": {
			reason: "A --namespace that is the last word, its value not yet typed, yields no namespace.",
			args:   []string{"trace", "--namespace"},
		},
		"ShortFlagLast": {
			reason: "A -n that is the last word, its value not yet typed, yields no namespace.",
			args:   []string{"trace", "-n"},
		},
		"None": {
			reason: "No namespace flag yields no namespace.",
			args:   []string{"trace", "x"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := parseNamespaceOverride(complete.Args{All: tc.args})
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("%s\nparseNamespaceOverride(): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}
