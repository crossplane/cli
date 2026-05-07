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

package generator

import (
	"embed"
	"encoding/json"
	"testing"

	"github.com/invopop/jsonschema"
	"github.com/spf13/afero"
)

//go:embed testdata/*.json
var testdataJSONFS embed.FS

func TestGenerateFromCRD(t *testing.T) {
	inputFS := afero.NewBasePathFs(afero.FromIOFS{FS: testdataFS}, "testdata")
	schemaFS, err := jsonGenerator{}.GenerateFromCRD(t.Context(), inputFS, nil)
	if err != nil {
		t.Fatal(err)
	}

	expectedFiles := []string{
		"models/io-k8s-apimachinery-pkg-apis-meta-v1-DeleteOptions.schema.json",
		"models/io-k8s-apimachinery-pkg-apis-meta-v1-FieldsV1.schema.json",
		"models/io-k8s-apimachinery-pkg-apis-meta-v1-ListMeta.schema.json",
		"models/io-k8s-apimachinery-pkg-apis-meta-v1-ManagedFieldsEntry.schema.json",
		"models/io-k8s-apimachinery-pkg-apis-meta-v1-ObjectMeta.schema.json",
		"models/io-k8s-apimachinery-pkg-apis-meta-v1-OwnerReference.schema.json",
		"models/io-k8s-apimachinery-pkg-apis-meta-v1-Patch.schema.json",
		"models/io-k8s-apimachinery-pkg-apis-meta-v1-Preconditions.schema.json",
		"models/io-k8s-apimachinery-pkg-apis-meta-v1-StatusCause.schema.json",
		"models/io-k8s-apimachinery-pkg-apis-meta-v1-StatusDetails.schema.json",
		"models/io-k8s-apimachinery-pkg-apis-meta-v1-Status.schema.json",
		"models/io-k8s-apimachinery-pkg-apis-meta-v1-Time.schema.json",
		"models/co-acme-platform-v1alpha1-AccountScaffold.schema.json",
		"models/co-acme-platform-v1alpha1-AccountScaffoldList.schema.json",
		"models/co-acme-platform-v1alpha1-XAccountScaffold.schema.json",
		"models/co-acme-platform-v1alpha1-XAccountScaffoldList.schema.json",
	}

	for _, path := range expectedFiles {
		exists, err := afero.Exists(schemaFS, path)
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("expected model file %s does not exist", path)
		}

		contents, err := afero.ReadFile(schemaFS, path)
		if err != nil {
			t.Fatal(err)
		}

		var schema jsonschema.Schema
		if err := json.Unmarshal(contents, &schema); err != nil {
			t.Fatalf("failed to unmarshal %s: %v", path, err)
		}
	}
}

func TestGenerateFromOpenAPI(t *testing.T) {
	inputFS := afero.NewBasePathFs(afero.FromIOFS{FS: testdataJSONFS}, "testdata")
	schemaFS, err := jsonGenerator{}.GenerateFromOpenAPI(t.Context(), inputFS, nil)
	if err != nil {
		t.Fatal(err)
	}

	expectedFiles := []string{
		"models/io-k8s-api-authentication-v1-BoundObjectReference.schema.json",
		"models/io-k8s-api-authentication-v1-TokenRequest.schema.json",
		"models/io-k8s-api-authentication-v1-TokenRequestSpec.schema.json",
		"models/io-k8s-api-authentication-v1-TokenRequestStatus.schema.json",
		"models/io-k8s-api-autoscaling-v1-Scale.schema.json",
		"models/io-k8s-api-autoscaling-v1-ScaleSpec.schema.json",
		"models/io-k8s-api-autoscaling-v1-ScaleStatus.schema.json",
		"models/io-k8s-api-core-v1-ConfigMap.schema.json",
		"models/io-k8s-api-core-v1-Pod.schema.json",
		"models/io-k8s-api-core-v1-Service.schema.json",
		"models/io-k8s-api-policy-v1-Eviction.schema.json",
		"models/io-k8s-apimachinery-pkg-api-resource-Quantity.schema.json",
		"models/io-k8s-apimachinery-pkg-apis-meta-v1-Condition.schema.json",
		"models/io-k8s-apimachinery-pkg-apis-meta-v1-ObjectMeta.schema.json",
		"models/io-k8s-apimachinery-pkg-apis-meta-v1-Status.schema.json",
		"models/io-k8s-apimachinery-pkg-runtime-RawExtension.schema.json",
		"models/io-k8s-apimachinery-pkg-util-intstr-IntOrString.schema.json",
	}

	for _, path := range expectedFiles {
		exists, err := afero.Exists(schemaFS, path)
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("expected model file %s does not exist", path)
		}

		contents, err := afero.ReadFile(schemaFS, path)
		if err != nil {
			t.Fatal(err)
		}

		var schema jsonschema.Schema
		if err := json.Unmarshal(contents, &schema); err != nil {
			t.Fatalf("failed to unmarshal %s: %v", path, err)
		}
	}
}
