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

package kube

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
)

// newDiscoveryServer serves plain-JSON discovery for a cluster whose only
// resource is the core v1 namespaced pods resource, with the short name "po".
func newDiscoveryServer(t *testing.T) *httptest.Server {
	t.Helper()

	objs := map[string]any{
		"/api": &metav1.APIVersions{
			TypeMeta: metav1.TypeMeta{Kind: "APIVersions"},
			Versions: []string{"v1"},
		},
		"/api/v1": &metav1.APIResourceList{
			TypeMeta:     metav1.TypeMeta{Kind: "APIResourceList", APIVersion: "v1"},
			GroupVersion: "v1",
			APIResources: []metav1.APIResource{{
				Name:         "pods",
				SingularName: "pod",
				Namespaced:   true,
				Kind:         "Pod",
				Verbs:        metav1.Verbs{"get", "list", "watch"},
				ShortNames:   []string{"po"},
			}},
		},
		"/apis": &metav1.APIGroupList{TypeMeta: metav1.TypeMeta{Kind: "APIGroupList"}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		obj, ok := objs[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(obj)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestNewCachedRESTMapper(t *testing.T) {
	type want struct {
		gvr   schema.GroupVersionResource
		scope meta.RESTScopeName
	}

	cases := map[string]struct {
		reason   string
		resource string
		want     want
	}{
		"ShortName": {
			reason:   "A resource shortcut should expand to the full resource.",
			resource: "po",
			want:     want{gvr: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, scope: meta.RESTScopeNameNamespace},
		},
		"FullName": {
			reason:   "The full resource name should still resolve.",
			resource: "pods",
			want:     want{gvr: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, scope: meta.RESTScopeNameNamespace},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := newDiscoveryServer(t)

			m, err := NewCachedRESTMapper(&rest.Config{Host: srv.URL})
			if err != nil {
				t.Fatalf("NewCachedRESTMapper(...): %v", err)
			}

			gvr, err := m.ResourceFor(schema.GroupVersionResource{Resource: tc.resource})
			if err != nil {
				t.Fatalf("\n%s\nResourceFor(%q): %v", tc.reason, tc.resource, err)
			}
			if diff := cmp.Diff(tc.want.gvr, gvr); diff != "" {
				t.Errorf("\n%s\nResourceFor(%q): -want, +got:\n%s", tc.reason, tc.resource, diff)
			}

			mapping, err := m.RESTMapping(schema.GroupKind{Kind: "Pod"}, "v1")
			if err != nil {
				t.Fatalf("\n%s\nRESTMapping(Pod): %v", tc.reason, err)
			}
			if diff := cmp.Diff(tc.want.scope, mapping.Scope.Name()); diff != "" {
				t.Errorf("\n%s\nRESTMapping(Pod) scope: -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}
