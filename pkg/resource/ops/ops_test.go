/*
Copyright 2025 The Crossplane Authors.

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

package ops

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	opsv1alpha1 "github.com/crossplane/crossplane/apis/v2/ops/v1alpha1"

	"github.com/crossplane/cli/v2/pkg/resource"
)

func buildResource(apiVersion, kind, namespace, name string, labels map[string]string) *resource.Resource {
	u := unstructured.Unstructured{}
	u.SetAPIVersion(apiVersion)
	u.SetKind(kind)
	u.SetNamespace(namespace)
	u.SetName(name)
	u.SetLabels(labels)

	return &resource.Resource{Unstructured: u}
}

func buildWatchOp(name string, watch opsv1alpha1.WatchSpec) *opsv1alpha1.WatchOperation {
	return &opsv1alpha1.WatchOperation{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       opsv1alpha1.WatchOperationSpec{Watch: watch},
	}
}

type operationOpt func(*opsv1alpha1.Operation)

func triggeredByResource(apiVersion, kind, namespace, name string) operationOpt {
	return func(op *opsv1alpha1.Operation) {
		op.SetAnnotations(map[string]string{
			opsv1alpha1.AnnotationWatchedResourceAPIVersion: apiVersion,
			opsv1alpha1.AnnotationWatchedResourceKind:       kind,
			opsv1alpha1.AnnotationWatchedResourceNamespace:  namespace,
			opsv1alpha1.AnnotationWatchedResourceName:       name,
		})
	}
}

func ownedBy(kind, name string) operationOpt {
	return func(op *opsv1alpha1.Operation) {
		op.SetOwnerReferences([]metav1.OwnerReference{{
			APIVersion: opsv1alpha1.SchemeGroupVersion.String(),
			Kind:       kind,
			Name:       name,
			Controller: new(true),
		}})
		if kind == opsv1alpha1.WatchOperationKind {
			op.SetLabels(map[string]string{opsv1alpha1.LabelWatchOperationName: name})
		}
	}
}

func appliedResource(apiVersion, kind, namespace, name string) operationOpt {
	return func(op *opsv1alpha1.Operation) {
		ref := opsv1alpha1.AppliedResourceRef{APIVersion: apiVersion, Kind: kind, Name: name}
		if namespace != "" {
			ref.Namespace = new(namespace)
		}

		op.Status.AppliedResourceRefs = append(op.Status.AppliedResourceRefs, ref)
	}
}

func buildOperation(name string, opts ...operationOpt) *opsv1alpha1.Operation {
	op := &opsv1alpha1.Operation{ObjectMeta: metav1.ObjectMeta{Name: name}}
	for _, o := range opts {
		o(op)
	}

	return op
}

// kindNames flattens a tree of Resource into "Kind/name relation=<relation>"
// strings, children indented by depth, to easily assert on the tree shape and
// the Relation field set on the nodes attached by the Enricher.
func kindNames(r *resource.Resource, depth int) []string {
	prefix := ""
	for range depth {
		prefix += "  "
	}

	line := prefix + r.Unstructured.GetKind() + "/" + r.Unstructured.GetName()
	if r.Relation != "" {
		line += " relation=" + r.Relation
	}

	out := make([]string, 0, len(r.Children)+1)
	out = append(out, line)

	for _, c := range r.Children {
		out = append(out, kindNames(c, depth+1)...)
	}

	return out
}

func TestEnrich(t *testing.T) {
	s := runtime.NewScheme()
	if err := opsv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("cannot build scheme: %v", err)
	}

	type args struct {
		mode    Mode
		objects []client.Object
		root    *resource.Resource
	}

	cases := map[string]struct {
		reason string
		args   args
		want   []string
	}{
		"ModeNoneAttachesNothing": {
			reason: "Should not attach anything when mode is none.",
			args: args{
				mode: ModeNone,
				objects: []client.Object{
					buildWatchOp("watch-all", opsv1alpha1.WatchSpec{APIVersion: "example.org/v1", Kind: "Thing"}),
				},
				root: buildResource("example.org/v1", "Thing", "default", "my-thing", nil),
			},
			want: []string{
				"Thing/my-thing",
			},
		},
		"TriggeredAttachesWatchOperationsAndTheirOperations": {
			reason: "Should attach WatchOperations watching the resource, with the Operations they created for it as children, excluding Operations triggered by other resources.",
			args: args{
				mode: ModeTriggered,
				objects: []client.Object{
					buildWatchOp("watch-things", opsv1alpha1.WatchSpec{APIVersion: "example.org/v1", Kind: "Thing"}),
					buildWatchOp("watch-others", opsv1alpha1.WatchSpec{APIVersion: "example.org/v1", Kind: "Other"}),
					buildOperation("watch-things-123",
						ownedBy(opsv1alpha1.WatchOperationKind, "watch-things"),
						triggeredByResource("example.org/v1", "Thing", "default", "my-thing")),
					buildOperation("watch-things-456",
						ownedBy(opsv1alpha1.WatchOperationKind, "watch-things"),
						triggeredByResource("example.org/v1", "Thing", "default", "another-thing")),
				},
				root: buildResource("example.org/v1", "Thing", "default", "my-thing", nil),
			},
			want: []string{
				"Thing/my-thing",
				"  WatchOperation/watch-things relation=watching",
				"    Operation/watch-things-123 relation=triggered",
			},
		},
		"TriggeredRespectsNamespaceAndLabels": {
			reason: "Should not attach WatchOperations whose watch namespace or labels don't match the resource.",
			args: args{
				mode: ModeTriggered,
				objects: []client.Object{
					buildWatchOp("wrong-namespace", opsv1alpha1.WatchSpec{APIVersion: "example.org/v1", Kind: "Thing", Namespace: "other"}),
					buildWatchOp("wrong-labels", opsv1alpha1.WatchSpec{APIVersion: "example.org/v1", Kind: "Thing", MatchLabels: map[string]string{"type": "wall"}}),
					buildWatchOp("matching-labels", opsv1alpha1.WatchSpec{APIVersion: "example.org/v1", Kind: "Thing", Namespace: "default", MatchLabels: map[string]string{"type": "radiator"}}),
				},
				root: buildResource("example.org/v1", "Thing", "default", "my-thing", map[string]string{"type": "radiator"}),
			},
			want: []string{
				"Thing/my-thing",
				"  WatchOperation/matching-labels relation=watching",
			},
		},
		"AllGroupsAppliedOperationsUnderTheirCreator": {
			reason: "Should attach Operations that applied the resource under the CronOperation that created them, and standalone ones directly.",
			args: args{
				mode: ModeAll,
				objects: []client.Object{
					&opsv1alpha1.CronOperation{ObjectMeta: metav1.ObjectMeta{Name: "nightly"}},
					buildOperation("nightly-123",
						ownedBy(opsv1alpha1.CronOperationKind, "nightly"),
						appliedResource("example.org/v1", "Thing", "default", "my-thing")),
					buildOperation("standalone",
						appliedResource("example.org/v1", "Thing", "default", "my-thing")),
					buildOperation("other-resource",
						appliedResource("example.org/v1", "Thing", "default", "another-thing")),
				},
				root: buildResource("example.org/v1", "Thing", "default", "my-thing", nil),
			},
			want: []string{
				"Thing/my-thing",
				"  CronOperation/nightly relation=applied",
				"    Operation/nightly-123",
				"  Operation/standalone relation=applied",
			},
		},
		"AllDoesNotDuplicateTriggeredOperations": {
			reason: "Should attach an Operation both triggered by and applying the resource only once, under its WatchOperation.",
			args: args{
				mode: ModeAll,
				objects: []client.Object{
					buildWatchOp("watch-things", opsv1alpha1.WatchSpec{APIVersion: "example.org/v1", Kind: "Thing"}),
					buildOperation("watch-things-123",
						ownedBy(opsv1alpha1.WatchOperationKind, "watch-things"),
						triggeredByResource("example.org/v1", "Thing", "default", "my-thing"),
						appliedResource("example.org/v1", "Thing", "default", "my-thing")),
				},
				root: buildResource("example.org/v1", "Thing", "default", "my-thing", nil),
			},
			want: []string{
				"Thing/my-thing",
				"  WatchOperation/watch-things relation=watching",
				"    Operation/watch-things-123 relation=triggered",
			},
		},
		"EnrichesChildrenToo": {
			reason: "Should attach operations to the resources deeper in the tree, e.g. an Operation triggered by a watched resource applying a sibling.",
			args: args{
				mode: ModeAll,
				objects: []client.Object{
					buildWatchOp("watch-walls", opsv1alpha1.WatchSpec{APIVersion: "example.org/v1", Kind: "Thing", MatchLabels: map[string]string{"type": "wall"}}),
					buildOperation("watch-walls-123",
						ownedBy(opsv1alpha1.WatchOperationKind, "watch-walls"),
						triggeredByResource("example.org/v1", "Thing", "default", "wall"),
						appliedResource("example.org/v1", "Thing", "default", "radiator")),
				},
				root: func() *resource.Resource {
					root := buildResource("example.org/v1", "XThings", "default", "my-xr", nil)
					root.Children = []*resource.Resource{
						buildResource("example.org/v1", "Thing", "default", "wall", map[string]string{"type": "wall"}),
						buildResource("example.org/v1", "Thing", "default", "radiator", map[string]string{"type": "radiator"}),
					}

					return root
				}(),
			},
			want: []string{
				"XThings/my-xr",
				"  Thing/wall",
				"    WatchOperation/watch-walls relation=watching",
				"      Operation/watch-walls-123 relation=triggered",
				"  Thing/radiator",
				"    WatchOperation/watch-walls relation=applied",
				"      Operation/watch-walls-123",
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(s).WithObjects(tc.args.objects...).Build()

			e := NewEnricher(c, tc.args.mode)
			if err := e.Enrich(context.Background(), tc.args.root); err != nil {
				t.Fatalf("\n%s\nEnrich(...): unexpected error: %v", tc.reason, err)
			}

			if diff := cmp.Diff(tc.want, kindNames(tc.args.root, 0)); diff != "" {
				t.Errorf("\n%s\nEnrich(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestEnrichNoOpsAPI(t *testing.T) {
	// A scheme without the ops types registered, as a stand-in for a cluster
	// that doesn't support operations.
	c := fake.NewClientBuilder().WithScheme(runtime.NewScheme()).Build()

	root := buildResource("example.org/v1", "Thing", "default", "my-thing", nil)

	e := NewEnricher(c, ModeAll)
	if err := e.Enrich(context.Background(), root); err != nil {
		t.Errorf("Enrich(...): expected no error when the ops API is not available, got: %v", err)
	}

	if diff := cmp.Diff([]string{"Thing/my-thing"}, kindNames(root, 0)); diff != "" {
		t.Errorf("Enrich(...): -want, +got:\n%s", diff)
	}
}
