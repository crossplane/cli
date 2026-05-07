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

package crd

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/afero"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"

	_ "embed"
)

//go:embed testdata/claimable-xrd.yaml
var claimableXRDBytes []byte

//go:embed testdata/unclaimable-xrd.yaml
var unclaimableXRDBytes []byte

func TestProcessXRD(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		xrdBytes []byte

		expectedXRKind     string
		expectedXRListKind string

		expectedClaimKind     string
		expectedClaimListKind string
	}{
		"ClaimableXRD": {
			xrdBytes:              claimableXRDBytes,
			expectedXRKind:        "XStorageBucket",
			expectedXRListKind:    "XStorageBucketList",
			expectedClaimKind:     "StorageBucket",
			expectedClaimListKind: "StorageBucketList",
		},
		"UnclaimableXRD": {
			xrdBytes:           unclaimableXRDBytes,
			expectedXRKind:     "XInternalBucket",
			expectedXRListKind: "XInternalBucketList",
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			outFS := afero.NewMemMapFs()
			xrPath, claimPath, err := ProcessXRD(outFS, tc.xrdBytes, "output", "/")
			if err != nil {
				t.Fatal(err)
			}

			if xrPath != "" {
				if tc.expectedXRKind == "" {
					t.Fatalf("unexpected XR CRD generated at %q", xrPath)
				}
				xrBytes, err := afero.ReadFile(outFS, xrPath)
				if err != nil {
					t.Fatal(err)
				}

				var xrCRD extv1.CustomResourceDefinition
				if err := yaml.Unmarshal(xrBytes, &xrCRD); err != nil {
					t.Fatal(err)
				}

				if diff := cmp.Diff(tc.expectedXRKind, xrCRD.Spec.Names.Kind); diff != "" {
					t.Errorf("XR Kind (-want +got):\n%s", diff)
				}
				if diff := cmp.Diff(tc.expectedXRListKind, xrCRD.Spec.Names.ListKind); diff != "" {
					t.Errorf("XR ListKind (-want +got):\n%s", diff)
				}
			}

			if claimPath != "" {
				if tc.expectedClaimKind == "" {
					t.Fatalf("unexpected claim CRD generated at %q", claimPath)
				}
				claimBytes, err := afero.ReadFile(outFS, claimPath)
				if err != nil {
					t.Fatal(err)
				}

				var claimCRD extv1.CustomResourceDefinition
				if err := yaml.Unmarshal(claimBytes, &claimCRD); err != nil {
					t.Fatal(err)
				}

				if diff := cmp.Diff(tc.expectedClaimKind, claimCRD.Spec.Names.Kind); diff != "" {
					t.Errorf("Claim Kind (-want +got):\n%s", diff)
				}
				if diff := cmp.Diff(tc.expectedClaimListKind, claimCRD.Spec.Names.ListKind); diff != "" {
					t.Errorf("Claim ListKind (-want +got):\n%s", diff)
				}
			}
		})
	}
}
