# Testing Rust Support in Crossplane CLI

This guide walks through testing the Rust support added in PR #374. It builds a complete
Crossplane configuration project whose composition function is written in Rust and composes an S3
bucket with [provider-aws-s3](https://marketplace.upbound.io/providers/upbound/provider-aws-s3).

Everything up to and including Step 12 runs on a laptop with Docker and needs no cluster and no AWS
account. Steps 13 and 14 deploy to a cluster, and create a real bucket if you supply AWS
credentials.

## Prerequisites

- Go 1.26+
- The GitHub CLI <https://cli.github.com>
- Docker, or an engine that supports `DOCKER_HOST`
- Rust 1.85+ with `cargo` (for local development; the project build itself only needs Docker)
- `kubectl`, for Steps 13 and 14
- Access to push packages to a registry (for Step 14)
- (optional) AWS credentials

## Step 1: Build the CLI from this PR

```bash
# Clone the CLI repository
git clone https://github.com/crossplane/cli.git
cd cli

# Check out PR #374
gh pr checkout 374

# Build the CLI
go build -o crossplane ./cmd/crossplane

# Verify the build
./crossplane version
```

All subsequent commands should use this locally compiled version of `crossplane`.

## Step 2: Create a New Project

```bash
# Initialize the project (this creates the directory)
crossplane project init configuration-aws-bucket-rust \
  --registry xpkg.upbound.io/your-org

cd configuration-aws-bucket-rust
```

Choose the registry now rather than later. The CLI names an embedded function after the project's
repository, and `function generate` writes that name into the composition in Step 6. If you change
`spec.repository` afterwards, the composition refers to a function that no longer exists. See
[Unknown function during render](#unknown-function-during-render).

If you don't have a registry account, [ttl.sh](https://ttl.sh) accepts anonymous pushes of
short-lived images. Use `--registry ttl.sh/<some-unique-name>` here and `--tag 2h` in Step 14,
because ttl.sh reads the tag as the image's lifetime.

## Step 3: Configure the Project

Edit `crossplane-project.yaml` to generate only Rust schemas:

```yaml
apiVersion: dev.crossplane.io/v1alpha1
kind: Project
metadata:
  name: configuration-aws-bucket-rust
spec:
  repository: xpkg.upbound.io/your-org/configuration-aws-bucket-rust
  schemas:
    languages:
    - rust
```

This step is optional. A project that doesn't set `spec.schemas.languages` gets schemas for every
language, Rust included. Listing `rust` alone skips the others, which is faster: the Python
generator runs in a container.

## Step 4: Add Dependencies

When a dependency is added to a Crossplane project:

- The package is resolved and cached locally
- The CLI generates schemas from any CRDs the package contains
- The dependency is recorded in `crossplane-project.yaml`

No cluster is involved.

```bash
# Add the AWS S3 provider dependency
crossplane dependency add xpkg.upbound.io/upbound/provider-aws-s3:v2.7.3
```

The Rust models are generated natively by the CLI, without Docker, so this takes a few seconds once
the package is cached.

### Kubernetes built-in types

This guide doesn't need them, but a `k8s` dependency generates Rust models like any other:

```bash
crossplane dependency add k8s:v1.35.0
```

```rust
use crossplane_models::io::k8s::api::apps::v1::Deployment;
use crossplane_models::io::k8s::api::core::v1::Service;
```

The generated crate depends on `serde` and `serde_json` only. It doesn't use `k8s-openapi` or
`kube`.

## Step 5: Create an Example Manifest and the API

First, create an example XR that defines your custom resource:

```bash
mkdir -p examples/storagebucket
cat > examples/storagebucket/example.yaml << 'EOF'
apiVersion: platform.example.com/v1alpha1
kind: StorageBucket
metadata:
  name: example
  namespace: default
spec:
  region: eu-central-1
  versioning: true
EOF
```

Then generate the XRD from the example. This is the platform API:

```bash
# Generate an XRD from the example XR
crossplane xrd generate examples/storagebucket/example.yaml
```

The command writes `apis/storagebuckets/definition.yaml`, a `scope: Namespaced` XRD with a string
`region` and a boolean `versioning`.

### Scope determines which models you import

The XRD's scope decides which generated models your function must use, and getting it wrong fails
only at apply time.

- `scope: Namespaced`, which this guide uses, composes **namespaced** managed resources. Import
  from the mirrored `.m.` group: `crossplane_models::io::upbound::m::aws::s3::v1beta1`.
- `scope: Cluster` composes **cluster-scoped** managed resources. Import from
  `crossplane_models::io::upbound::aws::s3::v1beta2`.

Mixing them gets you `cannot apply cluster scoped composed resource "bucket" (a Bucket named
example) for a namespaced composite resource` on the cluster. `crossplane composition render`
renders the mismatched combination without complaint, so this doesn't surface until you deploy.

A namespaced XR also means the composed resources belong in the XR's namespace. Crossplane doesn't
infer that for you. The function has to set it, which Step 7 does.

## Step 6: Create the Composition and the Rust Function

Generate the composition first, so that `function generate` can add the function to its pipeline:

```bash
# Generate a composition from the XRD
crossplane composition generate apis/storagebuckets/definition.yaml

# Generate a Rust function scaffold and add it to the composition's pipeline
crossplane function generate compose-bucket apis/storagebuckets/composition.yaml --language rust
```

`composition generate` also adds
[function-auto-ready](https://github.com/crossplane-contrib/function-auto-ready) to the project's
dependencies and to the pipeline.

`function generate` creates `functions/compose-bucket/` with:

- `Cargo.toml` - Dependencies including `function-sdk-rust`, and a path dependency on the generated
  `crossplane-models` crate at `../../schemas/rust`
- `rust-toolchain.toml` - The toolchain for working on the function, with `clippy` and `rustfmt`
- `src/main.rs` - Entry point
- `src/function.rs` - Function implementation template with a starter test
- `.gitignore` - Ignores `target/`
- `README.md`

The package is named after the function, and its binary is always called `function`.

The composition now runs your function before `function-auto-ready`:

```yaml
  pipeline:
  - functionRef:
      name: your-org-configuration-aws-bucket-rustcompose-bucket
    step: compose-bucket
  - functionRef:
      name: crossplane-contrib-function-auto-ready
    step: crossplane-contrib-function-auto-ready
```

The `functionRef` name comes from the project repository and the function name. The CLI builds the
embedded function's image repository as `<repository>_<function-name>`, then converts it to a DNS
label, which drops the underscore rather than replacing it and cuts the result at 63 characters.

## Step 7: Implement the Function

Replace the contents of `functions/compose-bucket/src/function.rs`:

```rust
//! Composes an S3 bucket, and optionally its versioning configuration, for a
//! StorageBucket.

use crossplane_models::com::example::platform::v1alpha1::StorageBucket;
use crossplane_models::io::k8s::apimachinery::pkg::apis::meta::v1::ObjectMeta;
// The mirrored `.m.` group holds the namespaced managed resources, which are
// what a namespaced XR composes. A cluster-scoped XRD would import from
// crossplane_models::io::upbound::aws::s3::v1beta2 instead.
use crossplane_models::io::upbound::m::aws::s3::v1beta1::{
    Bucket, BucketSpec, BucketSpecForProvider, BucketVersioning, BucketVersioningSpec,
    BucketVersioningSpecForProvider, BucketVersioningSpecForProviderBucketRef,
    BucketVersioningSpecForProviderVersioningConfiguration,
};
use function_sdk_rust::proto::v1::function_runner_service_server::FunctionRunnerService;
use function_sdk_rust::proto::v1::{RunFunctionRequest, RunFunctionResponse};
use function_sdk_rust::{resource, response};
use tonic::{Request, Response, Status};

/// The composition function.
#[derive(Debug, Default)]
pub struct Function;

#[tonic::async_trait]
impl FunctionRunnerService for Function {
    async fn run_function(
        &self,
        request: Request<RunFunctionRequest>,
    ) -> Result<Response<RunFunctionResponse>, Status> {
        let req = request.into_inner();
        let tag = req.meta.as_ref().map(|m| m.tag.clone()).unwrap_or_default();
        tracing::info!(tag, "running function");

        let mut rsp = response::to(&req, response::DEFAULT_TTL);

        // Read the observed XR into the model generated from the XRD.
        let observed = req.observed.as_ref().and_then(|s| s.composite.as_ref());
        let xr: StorageBucket = match resource::get(observed) {
            Ok(xr) => xr,
            Err(e) => {
                response::fatal(&mut rsp, format!("cannot get xr: {e}"));
                return Ok(Response::new(rsp));
            }
        };

        let metadata = xr.metadata.unwrap_or_default();
        let spec = xr.spec.unwrap_or_default();
        let (Some(name), Some(region)) = (metadata.name, spec.region) else {
            response::fatal(&mut rsp, "xr is missing metadata.name or spec.region");
            return Ok(Response::new(rsp));
        };

        // A namespaced XR composes namespaced resources, and Crossplane doesn't
        // put them in the XR's namespace for you.
        let namespace = metadata.namespace;

        // Every field of a generated model is an Option that is left out when
        // unset, so the desired state holds only the fields set here. Default
        // fills in the apiVersion and kind.
        let bucket = Bucket {
            metadata: Some(ObjectMeta {
                name: Some(name.clone()),
                namespace: namespace.clone(),
                ..Default::default()
            }),
            spec: Some(BucketSpec {
                for_provider: Some(BucketSpecForProvider {
                    region: Some(region.clone()),
                    ..Default::default()
                }),
                ..Default::default()
            }),
            ..Default::default()
        };

        let desired = rsp.desired.get_or_insert_default();
        resource::update(
            desired.resources.entry("bucket".to_string()).or_default(),
            &bucket,
        )
        .map_err(|e| Status::internal(e.to_string()))?;

        if spec.versioning == Some(true) {
            let versioning = BucketVersioning {
                metadata: Some(ObjectMeta {
                    name: Some(format!("{name}-versioning")),
                    namespace,
                    ..Default::default()
                }),
                spec: Some(BucketVersioningSpec {
                    for_provider: Some(BucketVersioningSpecForProvider {
                        region: Some(region),
                        bucket_ref: Some(BucketVersioningSpecForProviderBucketRef {
                            name: Some(name),
                            ..Default::default()
                        }),
                        versioning_configuration: Some(
                            BucketVersioningSpecForProviderVersioningConfiguration {
                                status: Some("Enabled".to_string()),
                                ..Default::default()
                            },
                        ),
                        ..Default::default()
                    }),
                    ..Default::default()
                }),
                ..Default::default()
            };
            resource::update(
                desired
                    .resources
                    .entry("versioning".to_string())
                    .or_default(),
                &versioning,
            )
            .map_err(|e| Status::internal(e.to_string()))?;
        }

        response::normal(&mut rsp, "composed the bucket");

        Ok(Response::new(rsp))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use function_sdk_rust::proto::v1::{Resource, State};

    async fn run(spec: serde_json::Value) -> RunFunctionResponse {
        let mut composite = Resource::default();
        resource::update(
            &mut composite,
            &serde_json::json!({
                "apiVersion": StorageBucket::API_VERSION,
                "kind": StorageBucket::KIND,
                "metadata": {"name": "example", "namespace": "default"},
                "spec": spec,
            }),
        )
        .unwrap();

        let req = RunFunctionRequest {
            observed: Some(State {
                composite: Some(composite),
                ..Default::default()
            }),
            ..Default::default()
        };

        Function
            .run_function(Request::new(req))
            .await
            .unwrap()
            .into_inner()
    }

    #[tokio::test]
    async fn composes_only_the_fields_it_sets() {
        let rsp = run(serde_json::json!({"region": "eu-central-1"})).await;

        let resources = &rsp.desired.as_ref().unwrap().resources;
        assert_eq!(resources.len(), 1);
        assert_eq!(
            resource::struct_to_json(resources["bucket"].resource.as_ref().unwrap()),
            serde_json::json!({
                "apiVersion": "s3.aws.m.upbound.io/v1beta1",
                "kind": "Bucket",
                "metadata": {"name": "example", "namespace": "default"},
                "spec": {"forProvider": {"region": "eu-central-1"}},
            })
        );
    }

    #[tokio::test]
    async fn composes_versioning_when_asked() {
        let rsp = run(serde_json::json!({"region": "eu-central-1", "versioning": true})).await;

        let resources = &rsp.desired.as_ref().unwrap().resources;
        assert!(resources.contains_key("versioning"));
    }
}
```

The function reads the XR through `StorageBucket`, the model generated from the XRD, and writes the
`Bucket` and the `BucketVersioning` through the models generated from provider-aws-s3.

The XR's name becomes the bucket's name. S3 bucket names are global, so `example` is fine for
rendering but is already taken in AWS. Step 13 uses a name of your own.

## Step 8: Look at the Generated Models

The models were generated in Steps 4 to 6 and are regenerated by `crossplane project build`. They
form one Cargo crate, `crossplane-models`, under `schemas/rust/`:

```text
schemas/rust/
├── Cargo.toml
└── src/
    ├── lib.rs
    ├── com/example/platform/v1alpha1/    # your XRD: StorageBucket
    └── io/
        ├── k8s/apimachinery/...          # ObjectMeta and the other shared types
        └── upbound/
            ├── aws/s3/v1beta2/           # cluster-scoped managed resources
            └── m/aws/s3/v1beta1/         # namespaced managed resources
```

- Each API group and version is a module named after the reversed group. Import types from the
  module, whatever file they were generated into: a kind, its list and its `Spec` and `Status`
  helpers share a file (`bucket.rs`), and the module re-exports all of them.
- Every field is an `Option` that is skipped when it serializes, so a function's desired state
  contains only the fields it sets. Unknown fields are ignored when deserializing, so an observed
  resource from a newer provider version still parses.
- A resource type has `API_VERSION` and `KIND` constants, and its `Default` fills both in.
- String enums stay `String`. The allowed values are listed in the field's documentation.
- An object that has named properties and allows others, through `additionalProperties` or
  `x-kubernetes-preserve-unknown-fields`, keeps the others in an `additional_properties` map that
  is flattened into the struct. A resource read into a model and written back loses nothing.
- Two properties that map to one Rust identifier, such as `proxyURL` and `proxyUrl`, become
  `proxy_url` and `proxy_url_2`. Each keeps its own name on the wire.

### Compiling only the models a function uses

A project with several providers generates a lot of models, and a function build compiles all of
them by default. Each module of models is behind a Cargo feature named after its path
(`io::upbound::m::aws::s3::v1beta1` is `io-upbound-m-aws-s3-v1beta1`), all of them enabled by
default. `schemas/rust/Cargo.toml` lists them. To compile only what the function imports, turn the
defaults off in `functions/compose-bucket/Cargo.toml` and name the modules it imports from:

```toml
crossplane-models = { path = "../../schemas/rust", default-features = false, features = [
    "com-example-platform-v1alpha1",
    "io-upbound-m-aws-s3-v1beta1",
] }
```

A feature enables the features of the modules its models refer to, so the shared Kubernetes types
don't need listing: `io::k8s::apimachinery::pkg::apis::meta::v1` comes with either of the two above.
The function in Step 7 imports `ObjectMeta` from it directly, which works for the same reason.

Importing from a module whose feature is off fails to compile, and the compiler names the feature:

```text
error[E0432]: unresolved import `crossplane_models::io::upbound::m::aws::s3::v1beta1`
note: found an item that was configured out
  | #[cfg(feature = "io-upbound-m-aws-s3-v1beta1")]
  |       --------------------------------------- the item is gated behind the `io-upbound-m-aws-s3-v1beta1` feature
```

## Step 9: Local Development (Optional)

The function is an ordinary Cargo package:

```bash
cd functions/compose-bucket

cargo build
cargo test
cargo clippy --all-targets -- -D warnings
cargo fmt --check

cd ../..
```

The scaffold passes all four as generated, and so does the function from Step 7.

For a fast loop, run the function on your machine and point `render` at it, instead of letting
`render` rebuild the function image on every run. Start the function:

```bash
cargo run --manifest-path functions/compose-bucket/Cargo.toml -- --insecure
```

In another terminal, describe the pipeline's functions in a file. The first function's name must be
the `functionRef` name from `apis/storagebuckets/composition.yaml`:

```bash
cat > /tmp/functions.yaml << 'EOF'
apiVersion: pkg.crossplane.io/v1
kind: Function
metadata:
  name: your-org-configuration-aws-bucket-rustcompose-bucket
  annotations:
    render.crossplane.io/runtime: Development
spec:
  package: xpkg.upbound.io/your-org/configuration-aws-bucket-rust_compose-bucket
---
apiVersion: pkg.crossplane.io/v1
kind: Function
metadata:
  name: crossplane-contrib-function-auto-ready
spec:
  package: xpkg.crossplane.io/crossplane-contrib/function-auto-ready:v0.6.1
EOF

crossplane composition render \
  examples/storagebucket/example.yaml \
  apis/storagebuckets/composition.yaml \
  /tmp/functions.yaml
```

With a functions file, `render` doesn't build anything, so this takes about a second.

## Step 10: Activate the Managed Resources

Crossplane v2 supports
[`ManagedResourceActivationPolicy`](https://docs.crossplane.io/latest/managed-resources/managed-resource-activation-policies/),
which limits how many CRDs a provider installs onto a cluster. The Crossplane Helm chart installs a
wildcard policy by default, which activates every CRD a provider ships.

Save this file as `apis/storagebuckets/mrap.yaml`. It's packaged with the project and applied when
the Configuration is installed:

```yaml
apiVersion: apiextensions.crossplane.io/v1alpha1
kind: ManagedResourceActivationPolicy
metadata:
  name: configuration-aws-bucket-rust
spec:
  activate:
  - buckets.s3.aws.m.upbound.io
  - bucketversionings.s3.aws.m.upbound.io
```

`crossplane composition render` doesn't need the policy, so a missing activation only shows up
once you deploy to a cluster without the wildcard policy, as Step 13 does.

## Step 11: Build the Project

```bash
# Build the complete project (configuration + embedded functions)
crossplane project build
```

This will:

1. Generate Rust schemas from all dependencies (provider-aws-s3, your XRD)
2. Compile the Rust function in a `rust:1-bookworm` container, together with the `crossplane-models`
   crate it depends on, once for each architecture in `spec.architectures` (amd64 and arm64 by
   default)
3. Put the binary on `gcr.io/distroless/cc-debian12:nonroot` as `/function`
4. Package everything into a Crossplane configuration package

The output is `_output/configuration-aws-bucket-rust.xpkg`.

Nothing is cached between builds yet: every build downloads and compiles all of the function's
dependencies. Expect a minute or two.

`spec.architectures` may list `amd64` and `arm64`. Anything else fails before the build starts.

## Step 12: Test with Composition Render

Before deploying to a cluster, test the composition with `crossplane composition render`. In a
project directory, `render` builds the embedded functions itself, so you only pass the XR and the
composition:

```bash
crossplane composition render \
  examples/storagebucket/example.yaml \
  apis/storagebuckets/composition.yaml \
  --timeout=5m
```

`--timeout=5m` matters. `render` times out after a minute by default, and it rebuilds the function
from source on every run, which takes longer than that. See
[Build timeout during render](#build-timeout-during-render). Step 9 shows how to render without
rebuilding.

The output shows the XR and the composed resources:

```yaml
---
apiVersion: platform.example.com/v1alpha1
kind: StorageBucket
metadata:
  name: example
  namespace: default
spec:
  crossplane:
    resourceRefs:
    - apiVersion: s3.aws.m.upbound.io/v1beta1
      kind: BucketVersioning
      name: example-versioning
    - apiVersion: s3.aws.m.upbound.io/v1beta1
      kind: Bucket
      name: example
status:
  conditions:
  # ...
---
apiVersion: s3.aws.m.upbound.io/v1beta1
kind: Bucket
metadata:
  annotations:
    crossplane.io/composition-resource-name: bucket
  labels:
    crossplane.io/composite: example
  name: example
  namespace: default
  ownerReferences:
  # ...
spec:
  forProvider:
    region: eu-central-1
---
apiVersion: s3.aws.m.upbound.io/v1beta1
kind: BucketVersioning
metadata:
  annotations:
    crossplane.io/composition-resource-name: versioning
  labels:
    crossplane.io/composite: example
  name: example-versioning
  namespace: default
  ownerReferences:
  # ...
spec:
  forProvider:
    bucketRef:
      name: example
    region: eu-central-1
    versioningConfiguration:
      status: Enabled
```

Note that `spec.forProvider` holds only the fields the function set.

```bash
# Include function results (informational messages)
crossplane composition render \
  examples/storagebucket/example.yaml \
  apis/storagebuckets/composition.yaml \
  --timeout=5m --include-function-results
```

The results include `Pipeline step "compose-bucket": composed the bucket`, the message the function
reports with `response::normal`.

## Step 13: Test with a Local Dev Cluster

`crossplane project run` creates a local Kubernetes cluster with Crossplane, builds the project and
deploys it:

```bash
# Start a local dev cluster and deploy the project
crossplane project run --no-default-mrap
```

`--no-default-mrap` suppresses the wildcard activation policy, so the
`ManagedResourceActivationPolicy` from Step 10 is what activates your CRDs. The flag only takes
effect when the control plane is created. If one already exists, run `crossplane project stop`
first.

Once the cluster is running, check that the function is healthy:

```bash
kubectl get functions.pkg.crossplane.io
kubectl -n crossplane-system logs -l pkg.crossplane.io/function=your-org-configuration-aws-bucket-rustcompose-bucket
```

The function logs `serving FunctionRunnerService` with `"insecure":false`: in a cluster it serves
gRPC over mTLS on port 9443, with the certificates Crossplane mounts for it.

To create a real bucket, give the provider AWS credentials. Namespaced managed resources use the
`ClusterProviderConfig` named `default` unless told otherwise:

```bash
# creds.conf should contain your AWS credentials:
# [default]
# aws_access_key_id = YOUR_ACCESS_KEY
# aws_secret_access_key = YOUR_SECRET_KEY
kubectl create secret generic aws-creds -n crossplane-system --from-file=creds=creds.conf

kubectl apply -f - <<EOF
apiVersion: aws.m.upbound.io/v1beta1
kind: ClusterProviderConfig
metadata:
  name: default
spec:
  credentials:
    source: Secret
    secretRef:
      name: aws-creds
      namespace: crossplane-system
      key: creds
EOF
```

Now create an XR. Its name becomes the bucket's name, so pick one that is unique across all of S3:

```bash
kubectl apply -f - <<EOF
apiVersion: platform.example.com/v1alpha1
kind: StorageBucket
metadata:
  name: your-org-rust-guide-bucket
  namespace: default
spec:
  region: eu-central-1
  versioning: true
EOF

# Watch the resources being created
kubectl get managed -n default -w

# Check the XR
kubectl get -n default storagebucket.platform.example.com your-org-rust-guide-bucket -o yaml
```

Without credentials the XR still reconciles. `kubectl describe` on it shows the event
`Pipeline step "compose-bucket": composed the bucket`, and the `Bucket` and the `BucketVersioning`
exist in the `default` namespace without ever becoming `SYNCED`. That is enough to confirm the
function runs in a cluster.

Delete the XR before tearing the cluster down, so the provider deletes the bucket:

```bash
kubectl delete -n default storagebucket.platform.example.com your-org-rust-guide-bucket

# Stop and remove the local dev cluster
crossplane project stop
```

## Step 14: Push and Install

To deploy to an existing cluster, push the packages to a registry:

```bash
# Push from the project directory, with the .xpkg in _output/
crossplane project push --tag v0.1.0
```

Use `crossplane project push`, not `crossplane xpkg push`. A project produces more than one
package: the configuration, plus one package per embedded function. The function goes to a
repository named `<repository>_<function-name>`, here
`xpkg.upbound.io/your-org/configuration-aws-bucket-rust_compose-bucket`. Functions are pushed first,
so if that repository can't be created the configuration is never uploaded.

```bash
# Install on a cluster
kubectl apply -f - <<EOF
apiVersion: pkg.crossplane.io/v1
kind: Configuration
metadata:
  name: configuration-aws-bucket-rust
spec:
  package: xpkg.upbound.io/your-org/configuration-aws-bucket-rust:v0.1.0
EOF

kubectl get configurations.pkg.crossplane.io,functions.pkg.crossplane.io,providers.pkg.crossplane.io
```

Then configure credentials and create an XR as in Step 13.

## Troubleshooting

### Rust compilation errors

The build container's output is part of the error, so a compile error from `crossplane project
build` includes what `cargo` printed. It's quicker to reproduce locally:

```bash
cd functions/compose-bucket
cargo build
```

### No suitable builder found

```text
crossplane: error: failed to build function "compose-bucket": failed to find a builder: no suitable builder found
```

The CLI you ran doesn't have the Rust builder. Use the binary built in Step 1. The builder
recognizes a function by the `Cargo.toml` at the root of its directory.

### A function must build exactly one binary

The builder runs `cargo build --release --bins` and ships the binary it produces. A package with no
binary target, or with more than one, fails with this message and the names it found. The scaffold
declares one `[[bin]]` named `function`.

### Workspaces and code shared between functions

The build container gets the function's own directory and `schemas/rust`, and nothing else from
the project. So a function can't inherit manifest keys from a Cargo workspace at the project root:

```text
error inheriting `edition` from workspace root manifest's `workspace.package.edition`
Caused by: failed to find a workspace root
```

Nor can it depend on a sibling crate by a path that leaves its directory
(`common = { path = "../common" }`):

```text
error: failed to get `common` as a dependency of package `fn-a v0.1.0`
Caused by: failed to load source for dependency `common`
```

Spell the inherited keys out in the function's own `Cargo.toml`. To share a crate, symlink it into
the function's directory and point the dependency at the symlink (`common = { path = "common" }`):
the build follows symlinks and stages what they point to. The one name that must not be a symlink
is `target`. The build leaves out what is under `target/`, but a symlink called `target` is
followed like any other, and everything behind it is copied into the build container.

### Build timeout during render

```text
crossplane: error: cannot build embedded functions: failed to build function "compose-bucket": failed to build runtime images: rust build container failed: container unknown failure: context deadline exceeded
```

The build didn't finish inside `render`'s timeout, which defaults to 1 minute. Pass `--timeout=5m`,
or render against a function running locally as in Step 9. The build container is stopped when the
timeout hits, so nothing keeps compiling in the background.

### Unknown function during render

```text
cannot run Composition pipeline step "compose-bucket": unknown function "..." - is it listed in the render input?
```

The `functionRef` name in the composition doesn't match the embedded function's name. That happens
when `spec.repository` changed after `function generate` wrote the name, or when the name was
written by hand. The name is the DNS label of `<repository path>_<function-name>`: the registry host
is dropped, `/` becomes `-`, the underscore disappears, and the result is cut at 63 characters. For
`xpkg.upbound.io/your-org/configuration-aws-bucket-rust` and `compose-bucket` that is
`your-org-configuration-aws-bucket-rustcompose-bucket`.

### Cluster scoped composed resource for a namespaced composite resource

The function composes the cluster-scoped managed resources for a namespaced XR. Import the models
from the `.m.` group instead. See
[Scope determines which models you import](#scope-determines-which-models-you-import).

### Missing or stale models

On `main`, the schema manager only ever adds files, and it doesn't record which languages it
generated for. Three things follow, for every language and not only for Rust:

- Adding `rust` to `spec.schemas.languages` of a project that already has dependencies generates
  models for the project's XRDs, but not for the dependencies, because their recorded versions
  haven't changed.
- Renaming or deleting a kind leaves its old model file in `schemas/rust`, still exported by its
  module.
- Removing a language from `spec.schemas.languages` leaves its directory behind.

All three are fixed by clearing the generated schemas and rebuilding:

```bash
crossplane dependency clean-cache --keep-packages
crossplane project build
```

### 404 from the registry when pushing

`project push` pushes the function to `<repository>_<function-name>`. On a registry that doesn't
create repositories on first push, create that repository before pushing.
