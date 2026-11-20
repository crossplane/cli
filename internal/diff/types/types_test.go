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

package types

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	un "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	dt "github.com/crossplane/cli/v2/internal/diff/result"
	"github.com/crossplane/cli/v2/internal/health"
)

func TestRenderedResourcesHealth(t *testing.T) {
	obj := func(apiVersion, kind, name, readyStatus string) *un.Unstructured {
		u := &un.Unstructured{Object: map[string]any{}}
		u.SetAPIVersion(apiVersion)
		u.SetKind(kind)
		u.SetName(name)
		if readyStatus != "" {
			_ = un.SetNestedSlice(u.Object, []any{map[string]any{"type": "Ready", "status": readyStatus}}, "status", "conditions")
		}
		return u
	}
	ref := func(apiVersion, kind, name string) corev1.ObjectReference {
		return corev1.ObjectReference{APIVersion: apiVersion, Kind: kind, Name: name}
	}
	healthy := &health.Health{State: health.Healthy}
	notReady := &health.Health{State: health.NotReady, Ready: &health.Condition{Status: corev1.ConditionFalse}}

	parent := obj("example.org/v1", "XParent", "parent", "")
	child := obj("example.org/v1", "XChild", "child", "False")
	grandchild := obj("v1", "ConfigMap", "grandchild", "")
	leaf := obj("v1", "Secret", "leaf", "")
	childRef := ref("example.org/v1", "XChild", "child")
	grandchildRef := ref("v1", "ConfigMap", "grandchild")
	leafRef := ref("v1", "Secret", "leaf")

	type args struct {
		build func() RenderedResources
	}
	cases := map[string]struct {
		reason string
		args   args
		want   dt.CompositeHealth
	}{
		"Empty": {
			reason: "Nothing recorded means no live composite and nothing composed.",
			args:   args{build: func() RenderedResources { return RenderedResources{} }},
			want:   dt.CompositeHealth{},
		},
		"CompositeAndObserved": {
			reason: "The composite gives XR, and each observed resource an entry with no via, in diff key order.",
			args: args{build: func() RenderedResources {
				var r RenderedResources
				r.SetComposite(parent)
				r.AddObserved(grandchild)
				r.AddObserved(child)
				return r
			}},
			want: dt.CompositeHealth{
				XR: healthy,
				Composed: []dt.ResourceHealth{
					{ObjectReference: childRef, Health: notReady},
					{ObjectReference: grandchildRef, Health: healthy},
				},
			},
		},
		"NoComposite": {
			reason: "A composite the cluster does not have yet records no XR health.",
			args: args{build: func() RenderedResources {
				var r RenderedResources
				r.SetComposite(nil)
				return r
			}},
			want: dt.CompositeHealth{},
		},
		"NestedMergedOnceWithVia": {
			reason: "Merging a nested composite's render lists what it composed with the nested composite as via, and the nested composite itself once, as the parent observed it.",
			args: args{build: func() RenderedResources {
				var r, nested RenderedResources
				r.SetComposite(parent)
				r.AddObserved(child)
				nested.SetComposite(child)
				nested.AddObserved(grandchild)
				r.Merge(nested)
				return r
			}},
			want: dt.CompositeHealth{
				XR: healthy,
				Composed: []dt.ResourceHealth{
					{ObjectReference: childRef, Health: notReady},
					{ObjectReference: grandchildRef, Via: &childRef, Health: healthy},
				},
			},
		},
		"NestedNotObservedByParent": {
			reason: "A nested composite the parent did not observe (found by name instead) is still listed, from its own render.",
			args: args{build: func() RenderedResources {
				var r, nested RenderedResources
				r.SetComposite(parent)
				nested.SetComposite(child)
				r.Merge(nested)
				return r
			}},
			want: dt.CompositeHealth{
				XR:       healthy,
				Composed: []dt.ResourceHealth{{ObjectReference: childRef, Health: notReady}},
			},
		},
		"DeeplyNestedKeepsDirectParent": {
			reason: "Via names the composite that directly composes a resource, however deep, not an intermediate one.",
			args: args{build: func() RenderedResources {
				var r, nested, deeper RenderedResources
				r.SetComposite(parent)
				nested.SetComposite(child)
				deeper.SetComposite(grandchild)
				deeper.AddObserved(leaf)
				nested.Merge(deeper)
				r.Merge(nested)
				return r
			}},
			want: dt.CompositeHealth{
				XR: healthy,
				Composed: []dt.ResourceHealth{
					{ObjectReference: childRef, Health: notReady},
					{ObjectReference: grandchildRef, Via: &childRef, Health: healthy},
					{ObjectReference: leafRef, Via: &grandchildRef, Health: healthy},
				},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := tc.args.build()
			if diff := cmp.Diff(tc.want, r.Health()); diff != "" {
				t.Errorf("\n%s\nHealth(): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}
