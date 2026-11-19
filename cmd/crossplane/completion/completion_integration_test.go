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

package completion

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/posener/complete"
	authenticationv1 "k8s.io/api/authentication/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/crossplane/cli/v2/internal/testutils/integration"
)

// The contexts writeKubeconfig puts in the kubeconfig the predictors load.
const (
	// ctxAdmin is the envtest cluster admin's, with no namespace.
	ctxAdmin = "admin"
	// ctxAdminTeamA is ctxAdmin with namespace team-a.
	ctxAdminTeamA = "admin-team-a"
	// ctxUnreachable points at a port nothing listens on.
	ctxUnreachable = "unreachable"
)

func TestMain(m *testing.M) {
	// Kill envtest servers that an earlier run left behind when it died abnormally.
	integration.ReapOrphanedEnvtest(func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) })
	os.Exit(m.Run())
}

// TestKubernetesResourcePredictorIntegration predicts resource types from a real API server's discovery.
func TestKubernetesResourcePredictorIntegration(t *testing.T) {
	cluster := startCluster(t, nil)

	type args struct {
		currentContext string
		line           string
	}

	cases := map[string]struct {
		reason string
		args   args
		want   []string
	}{
		"CustomResource": {
			reason: "A served CRD is predicted as <plural>.<version>.<group>.",
			args:   args{currentContext: ctxAdmin, line: "wid"},
			want:   []string{"widgets.v1alpha1.completion.example.org"},
		},
		"CoreResource": {
			// The predictor would write <plural>.<version>, but discovery leaves a core resource's version unset.
			reason: "A core resource is predicted by its plural alone.",
			args:   args{currentContext: ctxAdmin, line: "namespaces"},
			want:   []string{"namespaces"},
		},
		"NoMatch": {
			reason: "Nothing is predicted when no resource has the typed prefix.",
			args:   args{currentContext: ctxAdmin, line: "nosuchresource"},
		},
		"ContextOverride": {
			reason: "--context selects the cluster to discover, over the kubeconfig's current context.",
			args:   args{currentContext: ctxUnreachable, line: "--context admin wid"},
			want:   []string{"widgets.v1alpha1.completion.example.org"},
		},
		"ShortContextOverride": {
			reason: "-c is --context's short form.",
			args:   args{currentContext: ctxUnreachable, line: "-c admin wid"},
			want:   []string{"widgets.v1alpha1.completion.example.org"},
		},
		"ContextOverrideUnreachable": {
			reason: "Nothing is predicted when the --context cluster can't be reached.",
			args:   args{currentContext: ctxAdmin, line: "--context unreachable wid"},
		},
		"Impersonation": {
			reason: "Every authenticated user, so an impersonated one, may discover resources.",
			args:   args{currentContext: ctxAdmin, line: "--as someone-else wid"},
			want:   []string{"widgets.v1alpha1.completion.example.org"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			writeKubeconfig(t, cluster, tc.args.currentContext)

			got := kubernetesResourcePredictor().Predict(completeArgs(tc.args.line))
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("%s\nPredict(%q): -want, +got:\n%s", tc.reason, tc.args.line, diff)
			}
		})
	}
}

// TestKubernetesResourceNamePredictorIntegration predicts the names of a resource type's objects from a real API
// server.
func TestKubernetesResourceNamePredictorIntegration(t *testing.T) {
	cluster := startCluster(t, []rbacv1.PolicyRule{{
		APIGroups: []string{"completion.example.org"},
		Resources: []string{"widgets"},
		Verbs:     []string{"list"},
	}})
	user := userName(t, cluster)

	type args struct {
		currentContext string
		line           string
	}

	cases := map[string]struct {
		reason string
		args   args
		want   []string
	}{
		"DefaultNamespace": {
			reason: "Without a namespace from the flags or the context, names come from the default namespace.",
			args:   args{currentContext: ctxAdmin, line: "widgets "},
			want:   []string{"widget-alpha", "widget-beta"},
		},
		"Prefix": {
			reason: "Only names with the typed prefix are predicted.",
			args:   args{currentContext: ctxAdmin, line: "widgets widget-a"},
			want:   []string{"widget-alpha"},
		},
		"ShortName": {
			reason: "A resource type typed by its short name is resolved.",
			args:   args{currentContext: ctxAdmin, line: "wdg "},
			want:   []string{"widget-alpha", "widget-beta"},
		},
		"FullyQualified": {
			reason: "A resource type typed as <plural>.<version>.<group> is resolved.",
			args:   args{currentContext: ctxAdmin, line: "widgets.v1alpha1.completion.example.org "},
			want:   []string{"widget-alpha", "widget-beta"},
		},
		"UnknownResource": {
			reason: "Nothing is predicted for a resource type the server doesn't serve.",
			args:   args{currentContext: ctxAdmin, line: "nosuchresources "},
		},
		"NamespaceOverride": {
			reason: "--namespace selects the namespace to list.",
			args:   args{currentContext: ctxAdmin, line: "--namespace team-a widgets "},
			want:   []string{"widget-gamma"},
		},
		"ShortNamespaceOverride": {
			reason: "-n is --namespace's short form.",
			args:   args{currentContext: ctxAdmin, line: "-n team-a widgets "},
			want:   []string{"widget-gamma"},
		},
		"ContextNamespace": {
			reason: "Without a namespace flag, names come from the current context's namespace.",
			args:   args{currentContext: ctxAdminTeamA, line: "widgets "},
			want:   []string{"widget-gamma"},
		},
		"NamespaceOverrideBeatsContextNamespace": {
			reason: "--namespace wins over the context's namespace.",
			args:   args{currentContext: ctxAdminTeamA, line: "-n default widgets "},
			want:   []string{"widget-alpha", "widget-beta"},
		},
		"ContextOverride": {
			reason: "--context selects the cluster to list, over the kubeconfig's current context.",
			args:   args{currentContext: ctxUnreachable, line: "--context admin widgets "},
			want:   []string{"widget-alpha", "widget-beta"},
		},
		"ContextOverrideNamespace": {
			reason: "Without a namespace flag, names come from the --context context's namespace.",
			args:   args{currentContext: ctxAdmin, line: "--context admin-team-a widgets "},
			want:   []string{"widget-gamma"},
		},
		"ContextOverrideUnreachable": {
			reason: "Nothing is predicted when the --context cluster can't be reached.",
			args:   args{currentContext: ctxAdmin, line: "--context unreachable widgets "},
		},
		"ImpersonateAllowed": {
			reason: "An impersonated user allowed to list the resource type gets its names.",
			args:   args{currentContext: ctxAdmin, line: "--as " + user + " widgets "},
			want:   []string{"widget-alpha", "widget-beta"},
		},
		"ImpersonateEqualsForm": {
			reason: "--as=<user> impersonates like --as <user>.",
			args:   args{currentContext: ctxAdmin, line: "--as=" + user + " widgets "},
			want:   []string{"widget-alpha", "widget-beta"},
		},
		"ImpersonateForbiddenResource": {
			reason: "Nothing is predicted for a resource type the impersonated user may not list.",
			args:   args{currentContext: ctxAdmin, line: "--as " + user + " configmaps "},
		},
		"NotImpersonatingResource": {
			reason: "The admin lists the resource type the impersonated user may not, so the user's RBAC denied it.",
			args:   args{currentContext: ctxAdmin, line: "configmaps "},
			want:   []string{"settings"},
		},
		"ImpersonateUnboundUser": {
			reason: "Nothing is predicted for an impersonated user bound to no role.",
			args:   args{currentContext: ctxAdmin, line: "--as someone-else widgets "},
		},
		"ImpersonateGroup": {
			reason: "--as-group impersonates the user's groups, here one granting everything.",
			args:   args{currentContext: ctxAdmin, line: "--as someone-else --as-group system:masters widgets "},
			want:   []string{"widget-alpha", "widget-beta"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			writeKubeconfig(t, cluster, tc.args.currentContext)

			got := kubernetesResourceNamePredictor().Predict(completeArgs(tc.args.line))
			if diff := cmp.Diff(tc.want, got, cmpopts.SortSlices(func(a, b string) bool { return a < b })); diff != "" {
				t.Errorf("%s\nPredict(%q): -want, +got:\n%s", tc.reason, tc.args.line, diff)
			}
		})
	}
}

// TestContextPredictorIntegration predicts the kubeconfig's contexts.
func TestContextPredictorIntegration(t *testing.T) {
	cluster := startCluster(t, nil)

	type args struct {
		currentContext string
		line           string
	}

	cases := map[string]struct {
		reason string
		args   args
		want   []string
	}{
		"All": {
			reason: "Every context is predicted when nothing is typed.",
			args:   args{currentContext: ctxAdmin, line: "--context "},
			want:   []string{ctxAdmin, ctxAdminTeamA, ctxUnreachable},
		},
		"Prefix": {
			reason: "Only contexts with the typed prefix are predicted.",
			args:   args{currentContext: ctxAdmin, line: "--context adm"},
			want:   []string{ctxAdmin, ctxAdminTeamA},
		},
		"NoMatch": {
			reason: "Nothing is predicted when no context has the typed prefix.",
			args:   args{currentContext: ctxAdmin, line: "--context nosuchcontext"},
		},
		"UnreachableCurrentContext": {
			reason: "Contexts come from the kubeconfig alone, so an unreachable current context doesn't matter.",
			args:   args{currentContext: ctxUnreachable, line: "--context unr"},
			want:   []string{ctxUnreachable},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			writeKubeconfig(t, cluster, tc.args.currentContext)

			got := contextPredictor().Predict(completeArgs(tc.args.line))
			if diff := cmp.Diff(tc.want, got, cmpopts.SortSlices(func(a, b string) bool { return a < b })); diff != "" {
				t.Errorf("%s\nPredict(%q): -want, +got:\n%s", tc.reason, tc.args.line, diff)
			}
		})
	}
}

// TestNamespacePredictorIntegration predicts namespaces from a real API server.
func TestNamespacePredictorIntegration(t *testing.T) {
	cluster := startCluster(t, []rbacv1.PolicyRule{{
		APIGroups: []string{"completion.example.org"},
		Resources: []string{"widgets"},
		Verbs:     []string{"list"},
	}})
	user := userName(t, cluster)

	type args struct {
		currentContext string
		line           string
	}

	cases := map[string]struct {
		reason string
		args   args
		want   []string
	}{
		"Prefix": {
			reason: "Namespaces with the typed prefix are predicted.",
			args:   args{currentContext: ctxAdmin, line: "--namespace team"},
			want:   []string{"team-a", "team-b"},
		},
		"Exact": {
			reason: "A fully typed namespace is predicted.",
			args:   args{currentContext: ctxAdmin, line: "--namespace default"},
			want:   []string{"default"},
		},
		"NoMatch": {
			reason: "Nothing is predicted when no namespace has the typed prefix.",
			args:   args{currentContext: ctxAdmin, line: "--namespace nosuchnamespace"},
		},
		"ContextNamespaceIgnored": {
			reason: "Namespaces are cluster-scoped, so the current context's namespace doesn't filter them.",
			args:   args{currentContext: ctxAdminTeamA, line: "--namespace team"},
			want:   []string{"team-a", "team-b"},
		},
		"UnreachableCurrentContext": {
			reason: "Nothing is predicted when the current context's cluster can't be reached.",
			args:   args{currentContext: ctxUnreachable, line: "--namespace team"},
		},
		"ContextOverride": {
			reason: "--context selects the cluster to list, over the kubeconfig's current context.",
			args:   args{currentContext: ctxUnreachable, line: "--context admin --namespace team"},
			want:   []string{"team-a", "team-b"},
		},
		"ShortContextOverride": {
			reason: "-c is --context's short form.",
			args:   args{currentContext: ctxUnreachable, line: "-c admin --namespace team"},
			want:   []string{"team-a", "team-b"},
		},
		"AttachedContextOverride": {
			reason: "--context=value selects the cluster to list, like --context value.",
			args:   args{currentContext: ctxUnreachable, line: "--context=admin --namespace team"},
			want:   []string{"team-a", "team-b"},
		},
		"AttachedShortContextOverride": {
			reason: "-cvalue selects the cluster to list, like -c value.",
			args:   args{currentContext: ctxUnreachable, line: "-cadmin --namespace team"},
			want:   []string{"team-a", "team-b"},
		},
		"ContextOverrideUnreachable": {
			reason: "Nothing is predicted when the --context cluster can't be reached.",
			args:   args{currentContext: ctxAdmin, line: "--context unreachable --namespace team"},
		},
		"ImpersonateForbidden": {
			reason: "Nothing is predicted for an impersonated user who may not list namespaces.",
			args:   args{currentContext: ctxAdmin, line: "--as " + user + " --namespace team"},
		},
		"ImpersonateGroup": {
			reason: "--as-group impersonates the user's groups, here one granting everything.",
			args:   args{currentContext: ctxAdmin, line: "--as=someone-else --as-group=system:masters --namespace team"},
			want:   []string{"team-a", "team-b"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			writeKubeconfig(t, cluster, tc.args.currentContext)

			got := namespacePredictor().Predict(completeArgs(tc.args.line))
			if diff := cmp.Diff(tc.want, got, cmpopts.SortSlices(func(a, b string) bool { return a < b })); diff != "" {
				t.Errorf("%s\nPredict(%q): -want, +got:\n%s", tc.reason, tc.args.line, diff)
			}
		})
	}
}

// startCluster starts a cluster serving widgets in the default and team-a namespaces, and namespaces team-a and
// team-b. Non-nil rules also provision a user granted only them (see integration.Setup.RBACRules).
func startCluster(t *testing.T, rules []rbacv1.PolicyRule) *integration.Cluster {
	t.Helper()

	return integration.Start(t, integration.Setup{
		CRDDirs:    []string{"testdata/integration/crds"},
		SetupFiles: []string{"testdata/integration/setup.yaml"},
		RBACRules:  rules,
	})
}

// userName returns the name of the user the cluster's RBACRules were granted to, for --as.
func userName(t *testing.T, cluster *integration.Cluster) string {
	t.Helper()

	kube, err := kubernetes.NewForConfig(cluster.UserConfig)
	if err != nil {
		t.Fatalf("cannot create the RBAC-limited user's client: %v", err)
	}

	review, err := kube.AuthenticationV1().SelfSubjectReviews().Create(cluster.Context, &authenticationv1.SelfSubjectReview{}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("cannot review the RBAC-limited user: %v", err)
	}

	return review.Status.UserInfo.Username
}

// writeKubeconfig points KUBECONFIG, which the predictors load, at a kubeconfig of the ctxAdmin, ctxAdminTeamA and
// ctxUnreachable contexts, with currentContext current.
func writeKubeconfig(t *testing.T, cluster *integration.Cluster, currentContext string) {
	t.Helper()

	cfg, err := clientcmd.Load(cluster.Kubeconfig)
	if err != nil {
		t.Fatalf("cannot load the cluster's kubeconfig: %v", err)
	}

	admin := cfg.Contexts[cfg.CurrentContext]
	cfg.Clusters[ctxUnreachable] = &clientcmdapi.Cluster{Server: "https://127.0.0.1:1"}
	cfg.Contexts = map[string]*clientcmdapi.Context{
		ctxAdmin:       {Cluster: admin.Cluster, AuthInfo: admin.AuthInfo},
		ctxAdminTeamA:  {Cluster: admin.Cluster, AuthInfo: admin.AuthInfo, Namespace: "team-a"},
		ctxUnreachable: {Cluster: ctxUnreachable, AuthInfo: admin.AuthInfo},
	}
	cfg.CurrentContext = currentContext

	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := clientcmd.WriteToFile(*cfg, path); err != nil {
		t.Fatalf("cannot write kubeconfig: %v", err)
	}

	t.Setenv("KUBECONFIG", path)
}

// completeArgs returns the Args the shell completes for the words after a command: a trailing space means the last
// word is complete and an empty one is being typed.
func completeArgs(line string) complete.Args {
	all := strings.Fields(line)
	if strings.HasSuffix(line, " ") {
		all = append(all, "")
	}

	a := complete.Args{All: all, Completed: all[:len(all)-1], Last: all[len(all)-1]}
	if n := len(a.Completed); n > 0 {
		a.LastCompleted = a.Completed[n-1]
	}

	return a
}
