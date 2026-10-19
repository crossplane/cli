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

package diffprocessor

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	gcmp "github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	apiextensionsv1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1"
	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"

	"github.com/crossplane/cli/v2/cmd/crossplane/render"
	xp "github.com/crossplane/cli/v2/internal/diff/client/crossplane"
	tu "github.com/crossplane/cli/v2/internal/diff/testutils"
)

func testFn(name, pkg string) pkgv1.Function {
	return pkgv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       pkgv1.FunctionSpec{PackageSpec: pkgv1.PackageSpec{Package: pkg}},
	}
}

func pipelineComp(name string, fnNames ...string) *apiextensionsv1.Composition {
	comp := &apiextensionsv1.Composition{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       apiextensionsv1.CompositionSpec{Mode: apiextensionsv1.CompositionModePipeline},
	}
	for i, fn := range fnNames {
		comp.Spec.Pipeline = append(comp.Spec.Pipeline, apiextensionsv1.PipelineStep{
			Step:        "step-" + string(rune('a'+i)),
			FunctionRef: apiextensionsv1.FunctionReference{Name: fn},
		})
	}

	return comp
}

// clusterFnClient resolves pipeline steps against installed, as the real
// function client does, and records the steps it was asked for.
func clusterFnClient(installed map[string]pkgv1.Function, asked *[][]string, mu *sync.Mutex) xp.FunctionClient {
	return tu.NewMockFunctionClient().
		WithGetFunctionsFromPipeline(func(comp *apiextensionsv1.Composition) ([]pkgv1.Function, error) {
			if comp.Spec.Mode != apiextensionsv1.CompositionModePipeline {
				return nil, errors.Errorf("unsupported composition Mode '%s'", comp.Spec.Mode)
			}

			names := make([]string, 0, len(comp.Spec.Pipeline))
			out := make([]pkgv1.Function, 0, len(comp.Spec.Pipeline))

			for _, step := range comp.Spec.Pipeline {
				fn, ok := installed[step.FunctionRef.Name]
				if !ok {
					return nil, errors.Errorf("function %q referenced in pipeline step %q not found", step.FunctionRef.Name, step.Step)
				}

				names = append(names, step.FunctionRef.Name)
				out = append(out, *fn.DeepCopy())
			}

			if asked != nil {
				mu.Lock()
				*asked = append(*asked, names)
				mu.Unlock()
			}

			return out, nil
		}).
		Build()
}

func TestProjectFunctionProvider_GetFunctionsForComposition(t *testing.T) {
	installed := map[string]pkgv1.Function{
		"function-a": testFn("function-a", "xpkg.io/cluster/function-a:v1"),
		"function-b": testFn("function-b", "xpkg.io/cluster/function-b:v1"),
	}

	type want struct {
		fns    []pkgv1.Function
		asked  [][]string
		errSub string
	}

	cases := map[string]struct {
		reason  string
		project []pkgv1.Function
		comp    *apiextensionsv1.Composition
		want    want
	}{
		"OverrideInstalled": {
			reason:  "A project function replaces the installed function of the same name; the other steps come from the cluster.",
			project: []pkgv1.Function{testFn("function-a", "xpkg.io/project/function-a:local")},
			comp:    pipelineComp("comp", "function-a", "function-b"),
			want: want{
				fns: []pkgv1.Function{
					testFn("function-a", "xpkg.io/project/function-a:local"),
					testFn("function-b", "xpkg.io/cluster/function-b:v1"),
				},
				asked: [][]string{{"function-b"}},
			},
		},
		"NotInstalled": {
			reason:  "A project function that is not installed resolves without asking the cluster for it.",
			project: []pkgv1.Function{testFn("function-new", "xpkg.io/project/function-new:local")},
			comp:    pipelineComp("comp", "function-a", "function-new"),
			want: want{
				fns: []pkgv1.Function{
					testFn("function-a", "xpkg.io/cluster/function-a:v1"),
					testFn("function-new", "xpkg.io/project/function-new:local"),
				},
				asked: [][]string{{"function-a"}},
			},
		},
		"AllProject": {
			reason:  "When every step is a project function the cluster is not asked.",
			project: []pkgv1.Function{testFn("function-a", "xpkg.io/project/function-a:local")},
			comp:    pipelineComp("comp", "function-a", "function-a"),
			want: want{
				fns: []pkgv1.Function{
					testFn("function-a", "xpkg.io/project/function-a:local"),
					testFn("function-a", "xpkg.io/project/function-a:local"),
				},
			},
		},
		"Fallback": {
			reason:  "With no matching project function the whole pipeline comes from the cluster.",
			project: []pkgv1.Function{testFn("function-other", "xpkg.io/project/function-other:local")},
			comp:    pipelineComp("comp", "function-a", "function-b"),
			want: want{
				fns: []pkgv1.Function{
					testFn("function-a", "xpkg.io/cluster/function-a:v1"),
					testFn("function-b", "xpkg.io/cluster/function-b:v1"),
				},
				asked: [][]string{{"function-a", "function-b"}},
			},
		},
		"ExactNameMatch": {
			reason:  "Matching is by exact function name, not prefix or package.",
			project: []pkgv1.Function{testFn("function", "xpkg.io/project/function-a:local"), testFn("Function-A", "xpkg.io/project/function-a:local")},
			comp:    pipelineComp("comp", "function-a"),
			want: want{
				fns:   []pkgv1.Function{testFn("function-a", "xpkg.io/cluster/function-a:v1")},
				asked: [][]string{{"function-a"}},
			},
		},
		"LaterDuplicateWins": {
			reason:  "When project functions share a name the later one (an embedded function, after dependencies) wins.",
			project: []pkgv1.Function{testFn("function-a", "xpkg.io/dep/function-a:v2"), testFn("function-a", "xpkg.io/project/function-a:local")},
			comp:    pipelineComp("comp", "function-a"),
			want: want{
				fns: []pkgv1.Function{testFn("function-a", "xpkg.io/project/function-a:local")},
			},
		},
		"ClusterMissing": {
			reason:  "A step that is neither a project function nor installed fails as it does without the overlay.",
			project: []pkgv1.Function{testFn("function-a", "xpkg.io/project/function-a:local")},
			comp:    pipelineComp("comp", "function-a", "function-missing"),
			want:    want{errSub: `function "function-missing" referenced in pipeline step "step-b" not found`},
		},
		"NotPipeline": {
			reason:  "A non-pipeline composition is left to the wrapped provider to reject.",
			project: []pkgv1.Function{testFn("function-a", "xpkg.io/project/function-a:local")},
			comp: &apiextensionsv1.Composition{
				ObjectMeta: metav1.ObjectMeta{Name: "comp"},
				Spec:       apiextensionsv1.CompositionSpec{Mode: "Resources"},
			},
			want: want{errSub: "unsupported composition Mode"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var (
				asked [][]string
				mu    sync.Mutex
			)

			p := NewProjectFunctionProviderFactory(tc.project)(clusterFnClient(installed, &asked, &mu), tu.TestLogger(t, false))

			got, err := p.GetFunctionsForComposition(tc.comp)
			if tc.want.errSub != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want.errSub) {
					t.Fatalf("\n%s\nGetFunctionsForComposition(): want error containing %q, got %v", tc.reason, tc.want.errSub, err)
				}

				return
			}

			if err != nil {
				t.Fatalf("\n%s\nGetFunctionsForComposition(): %v", tc.reason, err)
			}

			if diff := gcmp.Diff(tc.want.fns, got); diff != "" {
				t.Errorf("\n%s\nGetFunctionsForComposition(): -want, +got:\n%s", tc.reason, diff)
			}

			if diff := gcmp.Diff(tc.want.asked, asked); diff != "" {
				t.Errorf("\n%s\nsteps asked of the cluster: -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestProjectFunctionProvider_CopyIsolation(t *testing.T) {
	installed := map[string]pkgv1.Function{"function-b": testFn("function-b", "xpkg.io/cluster/function-b:v1")}
	project := []pkgv1.Function{testFn("function-a", "xpkg.io/project/function-a:local")}
	project[0].SetAnnotations(map[string]string{"k": "v"})

	p := NewProjectFunctionProvider(NewCachedFunctionProvider(clusterFnClient(installed, nil, nil), tu.TestLogger(t, false)), project, tu.TestLogger(t, false))
	comp := pipelineComp("comp", "function-a", "function-b", "function-a")

	first, err := p.GetFunctionsForComposition(comp)
	if err != nil {
		t.Fatalf("GetFunctionsForComposition(): %v", err)
	}

	want, err := p.GetFunctionsForComposition(comp)
	if err != nil {
		t.Fatalf("GetFunctionsForComposition(): %v", err)
	}

	// Two steps naming the same function must not share a map either.
	first[0].Annotations["k"] = "mutated"
	if first[2].Annotations["k"] != "v" {
		t.Fatal("steps naming the same function share their annotations")
	}

	// Mutate everything the render engine and the outer wrappers touch.
	for i := range first {
		first[i].Spec.Package = "mutated"
		if first[i].Annotations == nil {
			first[i].Annotations = map[string]string{}
		}
		first[i].Annotations["k"] = "mutated"
		first[i].Annotations[render.AnnotationKeyRuntimeDockerSession] = "mutated"
	}

	// Mutating the input after construction must not reach the overlay.
	project[0].Spec.Package = "mutated-input"
	project[0].Annotations["k"] = "mutated-input"

	got, err := p.GetFunctionsForComposition(comp)
	if err != nil {
		t.Fatalf("GetFunctionsForComposition(): %v", err)
	}

	if diff := gcmp.Diff(want, got); diff != "" {
		t.Errorf("a mutated result or input leaked into a later call: -want, +got:\n%s", diff)
	}
}

func TestProjectFunctionProvider_PullPolicy(t *testing.T) {
	// Daemon-loaded images (project.EmbeddedFunctionsToDaemon) must not be
	// pulled: an overlaid function gets no pull-policy annotation, so the
	// docker runtime uses its default, which pulls only an absent image.
	p := NewProjectFunctionProvider(NewCachedFunctionProvider(clusterFnClient(nil, nil, nil), tu.TestLogger(t, false)),
		[]pkgv1.Function{testFn("function-a", "xpkg.io/project/function-a:local")}, tu.TestLogger(t, false))

	fns, err := p.GetFunctionsForComposition(pipelineComp("comp", "function-a"))
	if err != nil {
		t.Fatalf("GetFunctionsForComposition(): %v", err)
	}

	rt, err := render.GetRuntimeDocker(fns[0], tu.TestLogger(t, false))
	if err != nil {
		t.Fatalf("GetRuntimeDocker(): %v", err)
	}

	if rt.PullPolicy != render.AnnotationValueRuntimeDockerPullPolicyIfNotPresent {
		t.Errorf("PullPolicy: want %q, got %q", render.AnnotationValueRuntimeDockerPullPolicyIfNotPresent, rt.PullPolicy)
	}
}

func TestProjectFunctionProvider_Concurrent(t *testing.T) {
	installed := map[string]pkgv1.Function{"function-b": testFn("function-b", "xpkg.io/cluster/function-b:v1")}

	var fetches atomic.Int32

	inner := tu.NewMockFunctionClient().
		WithGetFunctionsFromPipeline(func(comp *apiextensionsv1.Composition) ([]pkgv1.Function, error) {
			fetches.Add(1)
			return clusterFnClient(installed, nil, nil).GetFunctionsFromPipeline(comp)
		}).
		Build()

	p := NewProjectFunctionProviderFactory([]pkgv1.Function{testFn("function-a", "xpkg.io/project/function-a:local")})(inner, tu.TestLogger(t, false))
	comp := pipelineComp("comp", "function-a", "function-b")
	want := []pkgv1.Function{
		testFn("function-a", "xpkg.io/project/function-a:local"),
		testFn("function-b", "xpkg.io/cluster/function-b:v1"),
	}

	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			got, err := p.GetFunctionsForComposition(comp)
			if err != nil {
				t.Errorf("GetFunctionsForComposition(): %v", err)
				return
			}

			if diff := gcmp.Diff(want, got); diff != "" {
				t.Errorf("GetFunctionsForComposition(): -want, +got:\n%s", diff)
			}

			// Each caller mutates its own copy, as the render engine does.
			for i := range got {
				got[i].SetAnnotations(map[string]string{render.AnnotationKeyRuntimeDockerSession: "run"})
				got[i].Spec.Package = "mutated"
			}
		})
	}

	wg.Wait()

	if n := fetches.Load(); n != 1 {
		t.Errorf("cluster fetches: want 1 (cached), got %d", n)
	}
}
