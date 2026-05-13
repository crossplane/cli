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

package project

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	xpkgv1beta1 "github.com/crossplane/crossplane/apis/v2/pkg/v1beta1"
)

func newFakeClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		Build()
}

func TestApplyResources(t *testing.T) {
	t.Parallel()

	configMapJSON := func(name string) []byte {
		cm := &corev1.ConfigMap{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "v1",
				Kind:       "ConfigMap",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "default",
			},
			Data: map[string]string{"key": "value"},
		}
		bs, err := json.Marshal(cm)
		if err != nil {
			t.Fatal(err)
		}
		return bs
	}

	t.Run("AppliesConfigMaps", func(t *testing.T) {
		t.Parallel()

		cl := newFakeClient(t)
		resources := []runtime.RawExtension{
			{Raw: configMapJSON("one")},
			{Raw: configMapJSON("two")},
		}

		if err := ApplyResources(t.Context(), cl, resources); err != nil {
			t.Fatalf("ApplyResources: %v", err)
		}

		for _, name := range []string{"one", "two"} {
			var got corev1.ConfigMap
			if err := cl.Get(t.Context(), types.NamespacedName{Name: name, Namespace: "default"}, &got); err != nil {
				t.Errorf("configmap %q not found: %v", name, err)
			}
		}
	})

	t.Run("IdempotentOnExisting", func(t *testing.T) {
		t.Parallel()

		cl := newFakeClient(t)
		resources := []runtime.RawExtension{{Raw: configMapJSON("repeated")}}

		if err := ApplyResources(t.Context(), cl, resources); err != nil {
			t.Fatalf("first ApplyResources: %v", err)
		}
		if err := ApplyResources(t.Context(), cl, resources); err != nil {
			t.Fatalf("second ApplyResources: %v", err)
		}
	})

	t.Run("EmptyRawErrors", func(t *testing.T) {
		t.Parallel()

		cl := newFakeClient(t)
		err := ApplyResources(t.Context(), cl, []runtime.RawExtension{{}})
		if err == nil {
			t.Fatal("expected error for empty raw, got nil")
		}
	})
}

func TestLookupLockPackage(t *testing.T) {
	t.Parallel()

	pkgs := []xpkgv1beta1.LockPackage{
		{
			Name:    "function-auto-ready",
			Source:  "xpkg.crossplane.io/crossplane-contrib/function-auto-ready",
			Version: "v0.3.0",
		},
		{
			Name:    "provider-nop",
			Source:  "xpkg.crossplane.io/crossplane-contrib/provider-nop",
			Version: "v0.2.1",
		},
	}

	tcs := map[string]struct {
		source     string
		constraint string
		wantName   string
		wantFound  bool
	}{
		"ExactMatch": {
			source:     "xpkg.crossplane.io/crossplane-contrib/function-auto-ready",
			constraint: "v0.3.0",
			wantName:   "function-auto-ready",
			wantFound:  true,
		},
		"SemverMatch": {
			source:     "xpkg.crossplane.io/crossplane-contrib/provider-nop",
			constraint: ">=v0.2.0",
			wantName:   "provider-nop",
			wantFound:  true,
		},
		"SemverMiss": {
			source:     "xpkg.crossplane.io/crossplane-contrib/provider-nop",
			constraint: ">=v1.0.0",
			wantFound:  false,
		},
		"SourceMiss": {
			source:     "xpkg.crossplane.io/other/thing",
			constraint: ">=v0.0.0",
			wantFound:  false,
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, found := lookupLockPackage(pkgs, tc.source, tc.constraint)
			if diff := cmp.Diff(tc.wantFound, found); diff != "" {
				t.Errorf("found (-want +got):\n%s", diff)
			}
			if !tc.wantFound {
				return
			}
			if diff := cmp.Diff(tc.wantName, got.Name); diff != "" {
				t.Errorf("name (-want +got):\n%s", diff)
			}
		})
	}
}

func TestSourcesEqual(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		a, b string
		want bool
	}{
		"Equal":         {"xpkg.crossplane.io/org/pkg", "xpkg.crossplane.io/org/pkg", true},
		"WithRegistry":  {"index.docker.io/library/nginx", "index.docker.io/library/nginx", true},
		"DifferentPath": {"xpkg.crossplane.io/org/one", "xpkg.crossplane.io/org/two", false},
		"InvalidA":      {"not a valid ref", "xpkg.crossplane.io/org/pkg", false},
		"InvalidB":      {"xpkg.crossplane.io/org/pkg", "not a valid ref", false},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := sourcesEqual(tc.a, tc.b)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		})
	}
}

func TestIsPermanentError(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		err  error
		want bool
	}{
		"Nil":          {nil, false},
		"BadRequest":   {apierrors.NewBadRequest("bad"), true},
		"Unauthorized": {apierrors.NewUnauthorized("nope"), true},
		"Forbidden":    {apierrors.NewForbidden(schemaGR(), "x", nil), true},
		"Timeout":      {apierrors.NewTimeoutError("slow", 1), false},
		"NotFound":     {apierrors.NewNotFound(schemaGR(), "x"), false},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := isPermanentError(tc.err)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		})
	}
}

func TestIsRetryableServerError(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		err  error
		want bool
	}{
		"Nil":           {nil, false},
		"Timeout":       {apierrors.NewTimeoutError("slow", 1), true},
		"ServerTimeout": {apierrors.NewServerTimeout(schemaGR(), "x", 1), true},
		"InternalError": {apierrors.NewInternalError(errExample()), true},
		"BadRequest":    {apierrors.NewBadRequest("bad"), false},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := isRetryableServerError(tc.err)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		})
	}
}

// Helpers.

func schemaGR() schema.GroupResource {
	return schema.GroupResource{Group: "", Resource: "configmaps"}
}

func errExample() error {
	return context.DeadlineExceeded
}
