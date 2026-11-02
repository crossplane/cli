//go:build compilegate

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

// The tests in this file are the real correctness gate for the generated Rust
// models: they materialize the crate on disk and shell out to cargo to compile
// it, which catches emitter bugs that no amount of string assertions in Go
// will. They also compile consumer crates that depend on the models by path,
// the way a generated function does, and round-trip resources through serde.
//
// They need a Rust toolchain and network access to fetch serde, which the Nix
// sandbox our unit tests run in (see nix/checks.nix) has neither of. Rather
// than skip at runtime, which reports as a test that ran, they are behind a
// build tag, so it's explicit that a plain `go test ./...` does not cover them:
//
//	go test -tags compilegate -run Rust ./internal/schemas/generator/...
//
// golangci-lint builds this file (see build-tags in .golangci.yml) so it can't
// rot silently.

package generator

import (
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/afero"
)

// rustCargo runs a cargo command in dir, failing the test with its output.
// Warnings are denied: generated code that compiles with warnings is a bug in
// the emitter, not something for a user to live with.
func rustCargo(t *testing.T, dir string, args ...string) {
	t.Helper()

	if _, err := exec.LookPath("cargo"); err != nil {
		t.Skip("cargo is not on PATH, skipping the Rust compile gate")
	}

	cmd := exec.CommandContext(t.Context(), "cargo", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "RUSTFLAGS=-D warnings", "CARGO_TERM_COLOR=never")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cargo %v failed: %v\n%s", args, err, out)
	}
}

// rustMaterialize writes a generator's output to a temporary directory and
// compiles the crate in it, every feature on. It returns the directory, which
// holds the crate under models.
func rustMaterialize(t *testing.T, schemaFS afero.Fs) string {
	t.Helper()

	dir := t.TempDir()
	roMaterialize(t, schemaFS, dir)
	rustCargo(t, filepath.Join(dir, rustModelsDir), "check", "--all-targets", "--all-features")
	return dir
}

// rustWriteConsumer writes a crate next to the generated models that depends on
// them by path, which is how a generated function consumes them, and gives it
// the given integration test. With no features listed the crate's default is
// on, every model; otherwise only the listed features are, which is what a
// function that compiles a few API groups out of many does.
func rustWriteConsumer(t *testing.T, dir, test string, features ...string) string {
	t.Helper()

	dependency := `crossplane-models = { path = "../models" }`
	if len(features) > 0 {
		quoted := make([]string, len(features))
		for i, f := range features {
			quoted[i] = strconv.Quote(f)
		}
		dependency = `crossplane-models = { path = "../models", default-features = false, features = [` + strings.Join(quoted, ", ") + `] }`
	}

	consumer := filepath.Join(dir, "consumer")
	for p, contents := range map[string]string{
		"Cargo.toml": `[package]
name = "consumer"
version = "0.0.1"
edition = "2024"
publish = false

[dependencies]
` + dependency + `
serde_json = "1"
`,
		"src/lib.rs":         "",
		"tests/roundtrip.rs": test,
	} {
		full := filepath.Join(consumer, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	return consumer
}

// TestGeneratedRustModelsFromCRDCompile compiles the models generated for the
// testdata XRDs and CRDs, then round-trips an XR through them from a consumer
// crate that enables only the features it needs. The round trip is the
// contract a composition function depends on: an observed resource
// deserializes even with fields the model doesn't know, and a resource built
// from Default serializes to nothing but its apiVersion and kind, so a
// function's desired state only claims the fields it set.
func TestGeneratedRustModelsFromCRDCompile(t *testing.T) {
	inputFS := afero.NewBasePathFs(afero.FromIOFS{FS: testdataFS}, "testdata")
	schemaFS, err := rustGenerator{}.GenerateFromCRD(t.Context(), inputFS, nil)
	if err != nil {
		t.Fatal(err)
	}

	dir := rustMaterialize(t, schemaFS)

	consumer := rustWriteConsumer(t, dir, `use crossplane_models::co::acme::platform::v1alpha1::XAccountScaffold;
use crossplane_models::io::upbound::azure::web::v1beta2::LinuxFunctionApp;

// An observed composite resource, as a function receives it: fields the model
// was not generated from, and integers where the schema says integer.
#[test]
fn deserializes_observed_state() {
    let observed = serde_json::json!({
        "apiVersion": "platform.acme.co/v1alpha1",
        "kind": "XAccountScaffold",
        "metadata": {
            "name": "test-account",
            "annotations": {"crossplane.io/composition-resource-name": "x"},
            "generation": 3
        },
        "spec": {
            "parameters": {"name": "acme"},
            "somethingTheModelHasNeverSeen": {"nested": true}
        },
        "status": {
            "conditions": [{"type": "Ready", "status": "True", "reason": "Available"}]
        }
    });

    let xr: XAccountScaffold = serde_json::from_value(observed).unwrap();
    assert_eq!(xr.api_version.as_deref(), Some("platform.acme.co/v1alpha1"));
    assert_eq!(
        xr.spec.unwrap().parameters.unwrap().name.as_deref(),
        Some("acme")
    );

    let conditions = xr.status.unwrap().conditions.unwrap();
    assert_eq!(conditions[0].r#type.as_deref(), Some("Ready"));
}

// A resource a function composes carries its own group, version and kind, and
// serializing it claims nothing else.
#[test]
fn default_carries_only_the_group_version_kind() {
    assert_eq!(XAccountScaffold::API_VERSION, "platform.acme.co/v1alpha1");
    assert_eq!(XAccountScaffold::KIND, "XAccountScaffold");

    let got = serde_json::to_value(XAccountScaffold::default()).unwrap();
    assert_eq!(
        got,
        serde_json::json!({
            "apiVersion": "platform.acme.co/v1alpha1",
            "kind": "XAccountScaffold"
        })
    );

    let got = serde_json::to_value(LinuxFunctionApp::default()).unwrap();
    assert_eq!(
        got,
        serde_json::json!({
            "apiVersion": "web.azure.upbound.io/v1beta2",
            "kind": "LinuxFunctionApp"
        })
    );
}

// Struct update syntax on a generated model is how a function sets the fields
// it owns, and the fields it leaves alone stay out of the serialized output.
#[test]
fn composes_a_resource_field_by_field() {
    use crossplane_models::io::upbound::azure::web::v1beta2::{
        LinuxFunctionAppSpec, LinuxFunctionAppSpecForProvider,
    };

    let app = LinuxFunctionApp {
        spec: Some(LinuxFunctionAppSpec {
            for_provider: Some(LinuxFunctionAppSpecForProvider {
                location: Some("westeurope".to_string()),
                ..Default::default()
            }),
            ..Default::default()
        }),
        ..Default::default()
    };

    let got = serde_json::to_value(&app).unwrap();
    assert_eq!(
        got,
        serde_json::json!({
            "apiVersion": "web.azure.upbound.io/v1beta2",
            "kind": "LinuxFunctionApp",
            "spec": {"forProvider": {"location": "westeurope"}}
        })
    );
}
`, "co-acme-platform-v1alpha1", "io-upbound-azure-web-v1beta2")

	rustCargo(t, consumer, "test")
}

// TestGeneratedRustModelsFromOpenAPICompile compiles the models generated from
// the Kubernetes OpenAPI documents. It is the wider net: core/v1 alone brings a
// few hundred types, every shared apimachinery package, the scalar unions
// (Quantity, IntOrString) and the deep nesting of PodSpec.
func TestGeneratedRustModelsFromOpenAPICompile(t *testing.T) {
	inputFS := afero.NewBasePathFs(afero.FromIOFS{FS: testdataJSONFS}, "testdata")
	schemaFS, err := rustGenerator{}.GenerateFromOpenAPI(t.Context(), inputFS, nil)
	if err != nil {
		t.Fatal(err)
	}

	dir := rustMaterialize(t, schemaFS)

	consumer := rustWriteConsumer(t, dir, `use crossplane_models::io::k8s::api::core::v1::Pod;
use crossplane_models::io::k8s::apimachinery::pkg::api::resource::Quantity;
use crossplane_models::io::k8s::apimachinery::pkg::util::intstr::IntOrString;

#[test]
fn deserializes_a_pod() {
    let observed = serde_json::json!({
        "apiVersion": "v1",
        "kind": "Pod",
        "metadata": {"name": "test"},
        "spec": {
            "containers": [{
                "name": "app",
                "image": "example.com/app:v1",
                "ports": [{"containerPort": 8080}],
                "resources": {"limits": {"memory": "128Mi", "cpu": 1}}
            }]
        }
    });

    let pod: Pod = serde_json::from_value(observed).unwrap();
    let containers = pod.spec.unwrap().containers.unwrap();
    assert_eq!(containers[0].name.as_deref(), Some("app"));
    assert_eq!(containers[0].ports.as_ref().unwrap()[0].container_port, Some(8080));

    // Quantity holds either of its scalar forms, whichever the server sent.
    let limits = containers[0].resources.as_ref().unwrap().limits.as_ref().unwrap();
    assert_eq!(limits["memory"], Quantity::String("128Mi".to_string()));
    assert_eq!(limits["cpu"], Quantity::Number(1.0));
}

#[test]
fn int_or_string_holds_both_forms() {
    let port: IntOrString = serde_json::from_value(serde_json::json!(8080)).unwrap();
    assert_eq!(port, IntOrString::Int(8080));

    let named: IntOrString = serde_json::from_value(serde_json::json!("http")).unwrap();
    assert_eq!(named, IntOrString::String("http".to_string()));
}
`)

	rustCargo(t, consumer, "test")
}

// TestGeneratedRustModelsFromBothFlowsCompile compiles the crate the schema
// manager ends up with when a project has both CRD and Kubernetes sources:
// each flow's output copied over the other, in either order, and the module
// tree rebuilt. The shared Kubernetes types are written by both, so this is
// where a type declared twice would surface as a compile error.
func TestGeneratedRustModelsFromBothFlowsCompile(t *testing.T) {
	gen := rustGenerator{}
	fromCRD, err := gen.GenerateFromCRD(t.Context(), afero.NewBasePathFs(afero.FromIOFS{FS: testdataFS}, "testdata"), nil)
	if err != nil {
		t.Fatal(err)
	}
	fromOpenAPI, err := gen.GenerateFromOpenAPI(t.Context(), afero.NewBasePathFs(afero.FromIOFS{FS: testdataJSONFS}, "testdata"), nil)
	if err != nil {
		t.Fatal(err)
	}
	crd := rustReadTree(t, fromCRD)
	openAPI := rustReadTree(t, fromOpenAPI)

	cases := map[string]struct {
		reason string
		args   struct {
			sources []map[string]string
		}
	}{
		"CRDThenOpenAPI": {
			reason: "A Kubernetes dependency added to a project that already has CRD models leaves a crate that compiles.",
			args:   struct{ sources []map[string]string }{sources: []map[string]string{crd, openAPI}},
		},
		"OpenAPIThenCRD": {
			reason: "CRD models generated into a project that already has Kubernetes models leave a crate that compiles.",
			args:   struct{ sources []map[string]string }{sources: []map[string]string{openAPI, crd}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			schemaFS := afero.NewMemMapFs()
			for _, source := range tc.args.sources {
				for p, contents := range source {
					if err := afero.WriteFile(schemaFS, p, []byte(contents), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				if err := BuildRustModuleTree(afero.NewBasePathFs(schemaFS, path.Join("/", rustModelsDir))); err != nil {
					t.Fatalf("\n%s\nBuildRustModuleTree(...): %v", tc.reason, err)
				}
			}

			rustMaterialize(t, schemaFS)
		})
	}
}

// TestGeneratedRustModelShapesCompile compiles the CRD shapes the testdata does
// not have, and exercises them from a consumer: a field that is an integer or
// a string, and a kind named after the Default trait every resource implements.
func TestGeneratedRustModelShapesCompile(t *testing.T) {
	inputFS := afero.NewMemMapFs()
	for p, crd := range map[string]string{
		"gateway.yaml": rustTestCRD("Gateway", "gateways", `              port:
                description: The port, by number or name.
                x-kubernetes-int-or-string: true
`),
		"default.yaml": rustTestCRD("Default", "defaults", `              value:
                type: string
`),
	} {
		if err := afero.WriteFile(inputFS, p, []byte(crd), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	schemaFS, err := rustGenerator{}.GenerateFromCRD(t.Context(), inputFS, nil)
	if err != nil {
		t.Fatal(err)
	}

	dir := rustMaterialize(t, schemaFS)

	consumer := rustWriteConsumer(t, dir, `use crossplane_models::com::example::v1::{Default_, Gateway, GatewaySpecPort};

#[test]
fn int_or_string_field_holds_both_forms() {
    let by_number: Gateway = serde_json::from_value(serde_json::json!({"spec": {"port": 80}})).unwrap();
    assert_eq!(by_number.spec.unwrap().port, Some(GatewaySpecPort::Int(80)));

    let by_name: Gateway = serde_json::from_value(serde_json::json!({"spec": {"port": "http"}})).unwrap();
    assert_eq!(by_name.spec.unwrap().port, Some(GatewaySpecPort::String("http".to_string())));
}

#[test]
fn a_kind_named_default_still_implements_default() {
    let got = serde_json::to_value(Default_::default()).unwrap();
    assert_eq!(got, serde_json::json!({"apiVersion": "example.com/v1", "kind": "Default"}));
}
`)

	rustCargo(t, consumer, "test")
}
