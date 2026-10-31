package diffprocessor

import (
	"testing"

	gcmp "github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	dt "github.com/crossplane/cli/v2/internal/diff/result"
	tu "github.com/crossplane/cli/v2/internal/diff/testutils"
)

// errMessage is err's message, or "" for none, so verdicts compare as plain values.
func errMessage(err error) string {
	if err == nil {
		return ""
	}

	return err.Error()
}

func TestRenderOverlapErrors(t *testing.T) {
	const key = "example.org/v1/Bucket/default/shared"

	// bucket is the shared resource as one render produces it: diffed as diffType, with spec.value set
	// to value, controlled by the XR named controller ("" for none) under uid. Clean mirrors
	// cleanupForDiff, which strips ownerReferences before anything is compared.
	bucket := func(diffType dt.DiffType, controller, uid, value string) *dt.ResourceDiff {
		b := tu.NewResource("example.org/v1", "Bucket", "shared").InNamespace("default").WithSpecField("value", value)
		if controller != "" {
			b = b.WithControllerReference("XR", controller, "example.org/v1", uid)
		}

		raw := b.Build()
		clean := raw.DeepCopy()
		clean.SetOwnerReferences(nil)

		return &dt.ResourceDiff{
			Gvk:          schema.GroupVersionKind{Group: "example.org", Version: "v1", Kind: "Bucket"},
			Namespace:    "default",
			ResourceName: "shared",
			DiffType:     diffType,
			Desired:      dt.ResourceViews{Raw: raw, Clean: clean},
		}
	}

	// atV1beta1 re-expresses a bucket diff at example.org/v1beta1, the same object at another served version.
	const betaKey = "example.org/v1beta1/Bucket/default/shared"

	atV1beta1 := func(d *dt.ResourceDiff) *dt.ResourceDiff {
		d.Gvk.Version = "v1beta1"
		d.Desired.Raw.SetAPIVersion("example.org/v1beta1")
		d.Desired.Clean.SetAPIVersion("example.org/v1beta1")

		return d
	}

	group := func(name string, diffs map[string]*dt.ResourceDiff) dt.XRDiffGroup {
		return dt.XRDiffGroup{XR: corev1.ObjectReference{APIVersion: "example.org/v1", Kind: "XR", Name: name}, Diffs: diffs}
	}

	sharing := func(names ...string) func(func(i int) *dt.ResourceDiff) []dt.XRDiffGroup {
		return func(diff func(i int) *dt.ResourceDiff) []dt.XRDiffGroup {
			groups := make([]dt.XRDiffGroup, len(names))
			for i, n := range names {
				groups[i] = group(n, map[string]*dt.ResourceDiff{key: diff(i)})
			}

			return groups
		}
	}

	generated := func(groups []dt.XRDiffGroup) []dt.XRDiffGroup {
		for i := range groups {
			groups[i].NameGenerated = true
		}

		return groups
	}

	two := sharing("a", "b")

	const (
		contention = `cannot combine diffs: resource "example.org/v1/Bucket/default/shared" would be controlled by more ` +
			`than one XR (XR/a, XR/b); Crossplane gives it to whichever of them creates it first, and the other fails to ` +
			`reconcile it, so no diff predicts applying these inputs together — diff them separately`
		disagreement = `cannot combine diffs: inputs XR/a, XR/b both produce resource "example.org/v1/Bucket/default/shared", ` +
			`but differently, so no single diff is correct for both — diff them separately`
		placeholder = `cannot combine diffs: inputs XR/g-(generated), XR/g-(generated) both produce resource ` +
			`"example.org/v1/Bucket/default/shared" only because simulate gives XRs that share a generateName the ` +
			`same placeholder name; the API server would name them differently, so they cannot be told apart here — diff ` +
			`them separately`
	)

	tests := map[string]struct {
		reason string
		groups []dt.XRDiffGroup
		want   []string
	}{
		"KeysProducedOnceAreFine": {
			reason: "Only a key produced by more than one render can overlap.",
			groups: []dt.XRDiffGroup{
				group("a", map[string]*dt.ResourceDiff{key: bucket(dt.DiffTypeModified, "a", "1", "v")}),
				group("b", map[string]*dt.ResourceDiff{"example.org/v1/Bucket/default/other": bucket(dt.DiffTypeModified, "b", "2", "v")}),
			},
		},
		"DifferentControllersContendEvenWhenIdentical": {
			reason: "Crossplane gives the object to whichever XR creates it first; the renderings cannot change that.",
			groups: two(func(i int) *dt.ResourceDiff { return bucket(dt.DiffTypeModified, []string{"a", "b"}[i], "u", "v") }),
			want:   []string{contention},
		},
		"AllEqualIsTolerated": {
			reason: "Equal diffs appear in no rendered view, so merging them loses nothing.",
			groups: two(func(i int) *dt.ResourceDiff { return bucket(dt.DiffTypeEqual, []string{"a", "b"}[i], "u", "v") }),
		},
		"KeptEqualBesideModifiedDisagrees": {
			reason: "A kept-equal rendering of one object beside a modified one is a disagreement: applying both " +
				"inputs either changes the object or does not, and no single diff says which.",
			groups: two(func(i int) *dt.ResourceDiff {
				return bucket([]dt.DiffType{dt.DiffTypeEqual, dt.DiffTypeModified}[i], "parent", "u", []string{"1", "2"}[i])
			}),
			want: []string{disagreement},
		},
		"SameControllerIdenticalIsMerged": {
			reason: "One change reached twice through one controller; the controller is compared by name, not UID.",
			groups: two(func(i int) *dt.ResourceDiff { return bucket(dt.DiffTypeModified, "parent", []string{"1", "2"}[i], "v") }),
		},
		"SameControllerDifferentRenderingsDisagree": {
			reason: "No single diff is right for both.",
			groups: two(func(i int) *dt.ResourceDiff { return bucket(dt.DiffTypeModified, "parent", "u", []string{"1", "2"}[i]) }),
			want:   []string{disagreement},
		},
		"UncontrolledDifferentRenderingsDisagree": {
			reason: "The same holds for an object no XR controls.",
			groups: two(func(i int) *dt.ResourceDiff { return bucket(dt.DiffTypeModified, "", "", []string{"1", "2"}[i]) }),
			want:   []string{disagreement},
		},
		"RemovalIsJudgedByItsCurrentController": {
			reason: "A removal has no rendered form, so its controller is read from the current object.",
			groups: two(func(i int) *dt.ResourceDiff {
				d := bucket(dt.DiffTypeRemoved, []string{"a", "b"}[i], "u", "v")
				d.Current, d.Desired = d.Desired, dt.ResourceViews{}

				return d
			}),
			want: []string{contention},
		},
		"SharedGenerateNameCannotBeToldApart": {
			reason: "Both render under one placeholder, so even identical renderings must not merge.",
			groups: generated(sharing("g-(generated)", "g-(generated)")(func(int) *dt.ResourceDiff {
				return bucket(dt.DiffTypeModified, "parent", "u", "v")
			})),
			want: []string{placeholder},
		},
		"DifferentGenerateNamesAreJudgedOnTheirMerits": {
			reason: "Different generateNames get different placeholders, so the overlap is an ordinary one.",
			groups: generated(sharing("g-(generated)", "h-(generated)")(func(int) *dt.ResourceDiff {
				return bucket(dt.DiffTypeModified, "parent", "u", "v")
			})),
		},
		"OneObjectAtTwoVersionsContends": {
			reason: "Served versions are views of one stored object, so different diff keys do not make different objects.",
			groups: []dt.XRDiffGroup{
				group("a", map[string]*dt.ResourceDiff{key: bucket(dt.DiffTypeModified, "a", "u", "v")}),
				group("b", map[string]*dt.ResourceDiff{betaKey: atV1beta1(bucket(dt.DiffTypeModified, "b", "u", "v"))}),
			},
			want: []string{`cannot combine diffs: resource "example.org/v1/Bucket/default/shared" (also rendered as ` +
				`"example.org/v1beta1/Bucket/default/shared") would be controlled by more than one XR (XR/a, XR/b); ` +
				`Crossplane gives it to whichever of them creates it first, and the other fails to reconcile it, so no ` +
				`diff predicts applying these inputs together — diff them separately`},
		},
		"OneObjectAtTwoVersionsDisagrees": {
			reason: "Renderings at different versions differ in shape, so even one controller cannot merge them.",
			groups: []dt.XRDiffGroup{
				group("a", map[string]*dt.ResourceDiff{key: bucket(dt.DiffTypeModified, "parent", "u", "v")}),
				group("b", map[string]*dt.ResourceDiff{betaKey: atV1beta1(bucket(dt.DiffTypeModified, "parent", "u", "v"))}),
			},
			want: []string{`cannot combine diffs: inputs XR/a, XR/b both produce resource ` +
				`"example.org/v1/Bucket/default/shared" (also rendered as "example.org/v1beta1/Bucket/default/shared"), ` +
				`but differently, so no single diff is correct for both — diff them separately`},
		},
		"OverlapsAreReportedInKeyOrder": {
			reason: "Map iteration is unordered; the reported errors must not be.",
			groups: []dt.XRDiffGroup{
				group("a", map[string]*dt.ResourceDiff{
					"example.org/v1/Bucket/default/z": bucket(dt.DiffTypeModified, "", "", "1"),
					key:                               bucket(dt.DiffTypeModified, "", "", "1"),
				}),
				group("b", map[string]*dt.ResourceDiff{
					"example.org/v1/Bucket/default/z": bucket(dt.DiffTypeModified, "", "", "2"),
					key:                               bucket(dt.DiffTypeModified, "", "", "2"),
				}),
			},
			want: []string{
				disagreement,
				`cannot combine diffs: inputs XR/a, XR/b both produce resource "example.org/v1/Bucket/default/z", ` +
					`but differently, so no single diff is correct for both — diff them separately`,
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			errs := renderOverlapErrors(tt.groups)

			got := make([]string, 0, len(errs))
			for _, err := range errs {
				got = append(got, errMessage(err))
			}

			if diff := gcmp.Diff(tt.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("%s\nrenderOverlapErrors(...): -want, +got:\n%s", tt.reason, diff)
			}
		})
	}
}

func TestDiffKeyIdentity(t *testing.T) {
	tests := map[string]struct {
		key  string
		want string
	}{
		"GroupedAPIVersion":           {key: "example.org/v1/Bucket/default/b", want: "example.org/Bucket/default/b"},
		"CoreAPIVersion":              {key: "v1/ConfigMap/default/cm", want: "/ConfigMap/default/cm"},
		"ClusterScoped":               {key: "example.org/v1/Bucket//b", want: "example.org/Bucket//b"},
		"VersionDoesNotAffectIt":      {key: "example.org/v2/Bucket/default/b", want: "example.org/Bucket/default/b"},
		"TooFewSegmentsHasNoIdentity": {key: "Bucket/b", want: ""},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := diffKeyIdentity(tt.key); got != tt.want {
				t.Errorf("diffKeyIdentity(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}
