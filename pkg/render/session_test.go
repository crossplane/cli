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
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"
)

func TestManagedLabelsSession(t *testing.T) {
	cases := map[string]struct {
		reason  string
		session string
		want    string
	}{
		"Supplied": {
			reason:  "A caller-supplied session ID should be used verbatim.",
			session: "my-session",
			want:    "my-session",
		},
		"Default": {
			reason: "An empty session should fall back to the per-process session ID.",
			want:   sessionID(),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := managedLabels(tc.session)[LabelKeySession]
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("\n%s\nmanagedLabels(...): -want session, +got session:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestSessionIDDefaultIsStable(t *testing.T) {
	first, second := sessionID(), sessionID()
	if first == "" || first != second {
		t.Errorf("sessionID(): want a non-empty, stable per-process ID, got %q then %q", first, second)
	}
}

func TestInjectSessionAnnotation(t *testing.T) {
	fn := func(a map[string]string) pkgv1.Function {
		f := pkgv1.Function{}
		f.SetAnnotations(a)
		return f
	}

	cases := map[string]struct {
		reason  string
		session string
		fns     []pkgv1.Function
		want    []pkgv1.Function
	}{
		"Injects": {
			reason:  "Functions without the annotation should receive the session.",
			session: "my-session",
			fns:     []pkgv1.Function{fn(nil), fn(map[string]string{"other": "x"})},
			want: []pkgv1.Function{
				fn(map[string]string{AnnotationKeyRuntimeDockerSession: "my-session"}),
				fn(map[string]string{"other": "x", AnnotationKeyRuntimeDockerSession: "my-session"}),
			},
		},
		"DoesNotOverwrite": {
			reason:  "A session annotation already on the Function must be preserved.",
			session: "my-session",
			fns:     []pkgv1.Function{fn(map[string]string{AnnotationKeyRuntimeDockerSession: "pinned"})},
			want:    []pkgv1.Function{fn(map[string]string{AnnotationKeyRuntimeDockerSession: "pinned"})},
		},
		"EmptySessionIsNoop": {
			reason: "An empty session must leave Functions untouched so they use the per-process default.",
			fns:    []pkgv1.Function{fn(nil)},
			want:   []pkgv1.Function{fn(nil)},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			injectSessionAnnotation(tc.fns, tc.session)
			if diff := cmp.Diff(tc.want, tc.fns); diff != "" {
				t.Errorf("\n%s\ninjectSessionAnnotation(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestEngineSetupInjectsSession(t *testing.T) {
	cases := map[string]struct {
		reason string
		engine Engine
	}{
		"Docker": {
			reason: "The Docker engine should annotate Functions with its session even when the network is preset.",
			engine: &dockerRenderEngine{network: "preset", session: "my-session", log: logging.NewNopLogger()},
		},
		"Local": {
			reason: "The local engine should annotate Functions with its session, since their containers are still Docker.",
			engine: &localRenderEngine{session: "my-session"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fns := []pkgv1.Function{{}}
			if _, err := tc.engine.Setup(t.Context(), fns); err != nil {
				t.Fatalf("\n%s\nSetup(...): unexpected error: %v", tc.reason, err)
			}
			if got := fns[0].GetAnnotations()[AnnotationKeyRuntimeDockerSession]; got != "my-session" {
				t.Errorf("\n%s\nSetup(...): session annotation: want %q, got %q", tc.reason, "my-session", got)
			}
		})
	}
}

func TestGetRuntimeDockerSession(t *testing.T) {
	fn := pkgv1.Function{}
	fn.SetAnnotations(map[string]string{AnnotationKeyRuntimeDockerSession: "my-session"})

	rd, err := GetRuntimeDocker(fn, logging.NewNopLogger())
	if err != nil {
		t.Fatalf("GetRuntimeDocker(...): unexpected error: %v", err)
	}
	if rd.Session != "my-session" {
		t.Errorf("GetRuntimeDocker(...): Session: want %q, got %q", "my-session", rd.Session)
	}
}
