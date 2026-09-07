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
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/crossplane/crossplane-runtime/v2/pkg/resource/unstructured/composed"
	ucomposite "github.com/crossplane/crossplane-runtime/v2/pkg/resource/unstructured/composite"
)

func TestFilterObservedXR(t *testing.T) {
	type args struct {
		xr       *ucomposite.Unstructured
		observed []composed.Unstructured
	}

	// composite builds a composite resource (XR) with the given identity.
	composite := func(apiVersion, kind, namespace, name string) *ucomposite.Unstructured {
		xr := ucomposite.New()
		xr.SetAPIVersion(apiVersion)
		xr.SetKind(kind)
		xr.SetNamespace(namespace)
		xr.SetName(name)
		return xr
	}

	// observed builds an observed composed resource with the given identity.
	observed := func(apiVersion, kind, namespace, name string) composed.Unstructured {
		o := composed.New()
		o.SetAPIVersion(apiVersion)
		o.SetKind(kind)
		o.SetNamespace(namespace)
		o.SetName(name)
		return *o
	}

	// keys projects resources to comparable "apiVersion/kind/namespace/name"
	// strings so we compare identity rather than the underlying maps.
	keys := func(rs []composed.Unstructured) []string {
		out := make([]string, len(rs))
		for i := range rs {
			out[i] = fmt.Sprintf("%s/%s/%s/%s", rs[i].GetAPIVersion(), rs[i].GetKind(), rs[i].GetNamespace(), rs[i].GetName())
		}
		return out
	}

	cases := map[string]struct {
		reason string
		args   args
		want   []string
	}{
		"DropsObservedCopyOfXR": {
			// The observed file contains a copy of the XR (as `crossplane
			// render` emits it) alongside a real composed resource. Only the
			// composed resource should survive.
			reason: "An observed resource matching the XR's GVK, namespace and name should be dropped.",
			args: args{
				xr: composite("example.org/v1", "XR", "default", "my-xr"),
				observed: []composed.Unstructured{
					observed("example.org/v1", "XR", "default", "my-xr"),
					observed("example.org/v1", "Composed", "default", "cd-a"),
				},
			},
			want: []string{"example.org/v1/Composed/default/cd-a"},
		},
		"KeepsAllWhenNoXRCopy": {
			reason: "Observed resources should be returned untouched when none of them is the XR.",
			args: args{
				xr: composite("example.org/v1", "XR", "default", "my-xr"),
				observed: []composed.Unstructured{
					observed("example.org/v1", "Composed", "default", "cd-a"),
					observed("example.org/v1", "Composed", "default", "cd-b"),
				},
			},
			want: []string{
				"example.org/v1/Composed/default/cd-a",
				"example.org/v1/Composed/default/cd-b",
			},
		},
		"DropsClusterScopedXRCopy": {
			// Cluster-scoped XR: namespace is empty on both the XR and its copy.
			reason: "An observed copy of a cluster scoped XR should be dropped, matching on an empty namespace.",
			args: args{
				xr: composite("example.org/v1", "XR", "", "my-xr"),
				observed: []composed.Unstructured{
					observed("example.org/v1", "XR", "", "my-xr"),
					observed("example.org/v1", "Composed", "", "cd-a"),
				},
			},
			want: []string{"example.org/v1/Composed//cd-a"},
		},
		"KeepsSameNameDifferentKind": {
			// A resource sharing the XR's name and namespace but a different
			// kind is not the XR and must be kept.
			reason: "An observed resource sharing the XR's namespace and name but not its kind should be kept.",
			args: args{
				xr: composite("example.org/v1", "XR", "default", "my-xr"),
				observed: []composed.Unstructured{
					observed("example.org/v1", "Other", "default", "my-xr"),
				},
			},
			want: []string{"example.org/v1/Other/default/my-xr"},
		},
		"EmptyObserved": {
			reason: "No observed resources should produce no observed resources.",
			args: args{
				xr:       composite("example.org/v1", "XR", "default", "my-xr"),
				observed: nil,
			},
			want: []string{},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := filterObservedXR(tc.args.observed, tc.args.xr)
			if diff := cmp.Diff(tc.want, keys(got)); diff != "" {
				t.Errorf("\n%s\nfilterObservedXR(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}
