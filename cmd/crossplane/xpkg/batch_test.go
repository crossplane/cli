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

package xpkg

import (
	"context"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/afero"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xpkg/parser"

	_ "embed"
)

var (
	//go:embed testdata/batch-parser/crd.yaml
	batchTestCRD string
	//go:embed testdata/batch-parser/crd-leading-separator.yaml
	batchTestCRDWithSeparator string
	//go:embed testdata/batch-parser/provider-meta.yaml
	batchTestMetadataTemplate string
)

// batchTestMetadata fills the fixture's PADDING placeholder with annotation.
func batchTestMetadata(annotation string) string {
	return strings.Replace(batchTestMetadataTemplate, "PADDING", annotation, 1)
}

func TestBatchParserBackend(t *testing.T) {
	type args struct {
		metadata string
		crd      string
	}
	type want struct {
		annotations map[string]string
		objects     []string
	}

	long := strings.Repeat("x", 8192)

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"LargeMetadata": {
			reason: "A metadata template larger than a single read must be streamed intact.",
			args:   args{metadata: batchTestMetadata(long), crd: batchTestCRD},
			want: want{
				annotations: map[string]string{"padding": long},
				objects:     []string{"buckets.s3.aws.upbound.io"},
			},
		},
		"CRDWithoutLeadingSeparator": {
			reason: "A CRD file that does not start with --- must be parsed as its own document.",
			args:   args{metadata: batchTestMetadata("p"), crd: batchTestCRD},
			want: want{
				annotations: map[string]string{"padding": "p"},
				objects:     []string{"buckets.s3.aws.upbound.io"},
			},
		},
		"CRDWithLeadingSeparator": {
			reason: "A CRD file that starts with --- must not produce an empty document.",
			args:   args{metadata: batchTestMetadata("p"), crd: batchTestCRDWithSeparator},
			want: want{
				annotations: map[string]string{"padding": "p"},
				objects:     []string{"buckets.s3.aws.upbound.io"},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			if err := afero.WriteFile(fs, "/crds/s3_buckets.yaml", []byte(tc.args.crd), 0o644); err != nil {
				t.Fatal(err)
			}
			b := &batchParserBackend{
				packageMetadata: tc.args.metadata,
				fs:              fs,
				options:         []parser.BackendOption{parser.FsDir("/crds"), parser.FsFilters(buildFilters("/crds", nil)...)},
			}

			metaScheme, err := xpkg.BuildMetaScheme()
			if err != nil {
				t.Fatal(err)
			}
			objScheme, err := xpkg.BuildObjectScheme()
			if err != nil {
				t.Fatal(err)
			}

			rc, err := b.Init(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			pkg, err := parser.New(metaScheme, objScheme).Parse(context.Background(), rc)
			if err != nil {
				t.Fatalf("\n%s\nParse(...): unexpected error: %v", tc.reason, err)
			}

			var annotations map[string]string
			if meta := pkg.GetMeta(); len(meta) == 1 {
				annotations = meta[0].(client.Object).GetAnnotations()
			}
			if diff := cmp.Diff(tc.want.annotations, annotations); diff != "" {
				t.Errorf("\n%s\nParse(...): -want meta annotations, +got:\n%s", tc.reason, diff)
			}

			objects := make([]string, 0, len(pkg.GetObjects()))
			for _, o := range pkg.GetObjects() {
				objects = append(objects, o.(client.Object).GetName())
			}
			if diff := cmp.Diff(tc.want.objects, objects); diff != "" {
				t.Errorf("\n%s\nParse(...): -want object names, +got:\n%s", tc.reason, diff)
			}
		})
	}
}
