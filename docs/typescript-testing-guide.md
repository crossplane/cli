# Testing TypeScript Support in Crossplane CLI

This guide walks through testing the TypeScript support added in PR #170. We'll create a complete Crossplane configuration project with a TypeScript composition function.

An example Crossplane project is located at <https://github.com/upbound/configuration-aws-network-ts>.

## Prerequisites

- Go 1.25+
- The Github CLI <https://cli.github.com>
- Docker, or an engine that supports `DOCKER_HOST`
- Node.js 24+ (for local development)
- A Kubernetes cluster with Crossplane installed
- Access to push packages to a registry (e.g., `xpkg.upbound.io`)
- (optional) AWS Credentials

## Step 1: Build the CLI from this PR

```bash
# Clone the CLI repository
git clone https://github.com/crossplane/cli.git
cd cli

# Checkout PR #170
gh pr checkout 170

# Build the CLI
go build -o crossplane ./cmd/crossplane

# Verify the build
./crossplane version
```

All subsequent commands should use this locally-compiled version of crossplane.

## Step 2: Create a New Project

```bash
# Initialize the project (this creates the directory)
crossplane project init configuration-aws-network-ts \
  --registry xpkg.upbound.io/your-org

cd configuration-aws-network-ts
```

**Porting an existing repository?** `project init` refuses to write into a directory that
isn't empty, including with `-d .`. Scaffold into a scratch directory and copy
`crossplane-project.yaml` plus the `apis/`, `functions/`, `examples/`, `tests/`, and
`operations/` directories over, or just write `crossplane-project.yaml` by hand.

## Step 3: Configure the Project

Edit `crossplane-project.yaml` to enable TypeScript schema generation and add dependencies:

```yaml
apiVersion: dev.crossplane.io/v1alpha1
kind: Project
metadata:
  name: configuration-aws-network-ts
spec:
  maintainer: Your Name <your.email@example.com>
  repository: xpkg.upbound.io/your-org/configuration-aws-network-ts
  # Enable TypeScript schema generation (opt-in)
  schemas:
    languages:
      - typescript
  dependencies:
    - type: xpkg
      xpkg:
        apiVersion: pkg.crossplane.io/v1
        kind: Provider
        package: xpkg.upbound.io/upbound/provider-aws-ec2
        version: ">=v2.6.0"
    - type: xpkg
      xpkg:
        apiVersion: pkg.crossplane.io/v1
        kind: Function
        package: xpkg.crossplane.io/crossplane-contrib/function-auto-ready
        version: ">=v0.7.0"
```

`apiVersion` and `kind` are required on each `xpkg` dependency. Leaving them out fails
validation on every subsequent command:

```text
crossplane: error: invalid project file: [dependency 0: xpkg: [apiVersion must not be
empty, kind must not be empty]]
```

If you would rather not write them by hand, skip this block and let `crossplane dependency add`
in Step 4 fill the whole section in for you.

## Step 4: Add Dependencies

When a dependency is added to a Crossplane project:

- The package is resolved and cached locally, under `--cache-dir`
  (`$CROSSPLANE_XPKG_CACHE`, defaulting to a per-user directory)
- The CLI generates schemas from any CRDs the package contains
- The dependency is recorded in `crossplane-project.yaml`

No cluster is involved. `dependency add` works before any control plane exists; the dependency is
installed on a control plane later, by `project run` or by applying the built Configuration.

You can add dependencies using `crossplane dependency add` or by
modifying `crossplane-project.yaml`.

```bash
# Add the AWS EC2 provider dependency
crossplane dependency add xpkg.upbound.io/upbound/provider-aws-ec2:v2.6.0

# Add the auto-ready function
crossplane dependency add xpkg.crossplane.io/crossplane-contrib/function-auto-ready:v0.7.0
```

## Step 5: Create an Example Manifest and the API

First, create an example XR file that defines your custom resource:

```bash
mkdir -p examples/network
cat > examples/network/example.yaml << 'EOF'
apiVersion: aws.platform.upbound.io/v1alpha1
kind: Network
metadata:
  name: example-network
  namespace: network-team
spec:
  region: us-west-2
  cidrBlock: "10.0.0.0/16"
EOF
```

Then generate the XRD from the example. This will be our Platform API:

```bash
# Generate an XRD from the example XR
crossplane xrd generate examples/network/example.yaml
```

This writes `apis/networks/definition.yaml` — note the plural directory name. The CLI emits an
`apiextensions.crossplane.io/v2` XRD with no `claimNames`; claims are a v1 concept, and in v2 you
use the XR directly.

**The scope is inferred from the example.** Because the XR above carries
`metadata.namespace`, the generated XRD gets `scope: Namespaced`. Drop the namespace from the
example and you get `scope: Cluster` instead. This walkthrough is namespaced throughout, which is
the usual choice for a platform API a team consumes inside its own namespace, and it is what
[configuration-aws-network-ts](https://github.com/upbound/configuration-aws-network-ts) does.

Edit `apis/networks/definition.yaml` to add descriptions, defaults and status fields:

```yaml
apiVersion: apiextensions.crossplane.io/v2
kind: CompositeResourceDefinition
metadata:
  name: networks.aws.platform.upbound.io
spec:
  group: aws.platform.upbound.io
  names:
    categories:
      - crossplane
    kind: Network
    plural: networks
  scope: Namespaced
  versions:
    - name: v1alpha1
      served: true
      referenceable: true
      schema:
        openAPIV3Schema:
          type: object
          properties:
            spec:
              type: object
              properties:
                region:
                  type: string
                  description: AWS region for the network
                  default: us-west-2
                cidrBlock:
                  type: string
                  description: CIDR block for the VPC
                  default: "10.0.0.0/16"
              required:
                - region
            status:
              type: object
              properties:
                vpcId:
                  type: string
                  description: The ID of the created VPC
```

### Scope determines which types you import

The XRD's scope decides which generated types your function must use, and getting it wrong fails
only at apply time.

- `scope: Namespaced` — what this guide uses — composes **namespaced** managed resources. Import
  from the mirrored `.m.` group: `crossplane-models/ec2.aws.m.upbound.io/v1beta1`.
- `scope: Cluster` composes **cluster-scoped** managed resources. Import from
  `crossplane-models/ec2.aws.upbound.io/v1beta1`.

Mixing them gets you `cannot apply cluster scoped composed resource for a namespaced composite
resource` on the cluster. Note that `crossplane composition render` renders the mismatched
combination without complaint, so this does not surface until you deploy.

A namespaced XR also means the composed resources belong in the XR's namespace. Crossplane does
not infer that for you — the function has to set it, which Step 7 does.

## Step 6: Create a TypeScript Function

```bash
# Generate a TypeScript function scaffold
crossplane function generate network --language typescript
```

**Note**: You can also generate a function and add it to a composition pipeline in one step:

```bash
crossplane function generate network apis/networks/composition.yaml --language typescript
```

This creates `functions/network/` with:

- `package.json` - Dependencies including `@crossplane-org/function-sdk-typescript`
- `tsconfig.json` - TypeScript configuration
- `src/main.ts` - Entry point
- `src/function.ts` - Function implementation template
- `src/function.test.ts` - Starter Vitest test
- `.npmrc` - Sets `install-links=true` (see Step 9)
- `eslint.config.js` and `tsconfig.eslint.json` - Type-aware linting (see Troubleshooting)

## Step 7: Implement the Function

The generated `functions/network/src/function.ts` contains a template implementation. A full example is available at [function.ts](https://github.com/stevendborrelli/configuration-aws-network-ts-xp-cli/blob/main/functions/network/src/function.ts).

Edit the `function.ts` to create a VPC:

```typescript
import {
  type ComposeFunction,
  fatal,
  fromModel,
  getObservedCompositeResource,
  normal,
} from '@crossplane-org/function-sdk-typescript';

// Import the generated types from crossplane-models. This is the mirrored `.m.`
// group, matching the `scope: Namespaced` XRD from Step 5. For a cluster-scoped
// XRD, import from 'crossplane-models/ec2.aws.upbound.io/v1beta1' instead.
import { VPC } from 'crossplane-models/ec2.aws.m.upbound.io/v1beta1';

/**
 * compose is a Crossplane composition function that creates a VPC.
 *
 * serve() hands us a response already built from the request, so there is no
 * to(req) here, and rsp.desired is guaranteed to be present.
 */
export const compose: ComposeFunction = async (req, rsp, logger) => {
  // Get the observed composite resource (XR).
  const observedComposite = getObservedCompositeResource(req);
  if (!observedComposite) {
    fatal(rsp, 'No composite resource found');
    return rsp;
  }
  logger?.debug({ observedComposite }, 'Observed composite resource');

  // Extract spec values from the XR
  const spec = observedComposite.resource?.spec as { region?: string; cidrBlock?: string };
  const region = spec?.region || 'us-west-2';
  const cidrBlock = spec?.cidrBlock || '10.0.0.0/16';
  const xrName = observedComposite.resource?.metadata?.name || 'unknown';

  // A namespaced XR composes namespaced managed resources, and Crossplane does
  // not place them for you — carry the XR's namespace onto everything composed.
  const namespace = observedComposite.resource?.metadata?.namespace;

  // Create a VPC using the generated TypeScript class.
  //
  // Do NOT set crossplane.io/external-name here. For a VPC the external name
  // is the AWS-assigned ID, which the provider writes back after creation.
  // Setting it yourself makes the provider look for a VPC by that name
  // forever, so the resource stays Ready=False/Creating even though the VPC
  // exists in AWS. Only set it when you genuinely control the external
  // identifier, and use tags for human-readable names.
  const vpc = new VPC({
    metadata: {
      name: `${xrName}-vpc`,
      ...(namespace && { namespace: namespace }),
    },
    spec: {
      forProvider: {
        region: region,
        cidrBlock: cidrBlock,
        enableDnsHostnames: true,
        enableDnsSupport: true,
        tags: {
          Name: `${xrName}-vpc`,
          'managed-by': 'crossplane',
        },
      },
    },
  });

  // Validate the model against the CRD schema before composing it.
  vpc.validate();

  // Write the VPC straight onto the response. ComposeResponse narrows desired
  // to non-optional, so there is no need for rsp.desired!. The map holds
  // protobuf Resource values rather than kubernetes-models objects, so convert
  // with fromModel — assigning the model directly fails to compile with TS2739.
  rsp.desired.resources['vpc'] = fromModel(vpc);

  normal(rsp, 'Successfully composed VPC resource');
  return rsp;
};
```

The generated `src/main.ts` hands this to the SDK and needs no edits:

```typescript
serve(compose, { name: 'network' });
```

`serve` parses the standard function flags, builds a logger from `--debug`, starts the gRPC
server, and shuts down cleanly on `SIGINT` and `SIGTERM`.

**On older SDK versions**: `fromModel` accepts the generated models from
`@crossplane-org/function-sdk-typescript@0.6.0` onward. Earlier versions required
`toJSON(): Record<string, unknown>` while `@kubernetes-models/base` declares `toJSON(): unknown`
from v6 onward, so the call failed with TS2345
([function-sdk-typescript#26](https://github.com/crossplane/function-sdk-typescript/pull/26)).
On an SDK older than 0.6.0 the equivalent is
`Resource.fromJSON({ resource: vpc.toJSON() })`, and the function is written as a class
implementing `FunctionHandler` rather than a `ComposeFunction`. The template generates 0.7.0,
so this only matters when working in an existing project pinned to an older SDK.

The generated `package.json` already includes the `crossplane-models` dependency when TypeScript schemas are enabled. It will look like:

```json
{
  "name": "function",
  "version": "0.1.0",
  "description": "A Crossplane composition function.",
  "license": "Apache-2.0",
  "type": "module",
  "main": "dist/main.js",
  "scripts": {
    "build": "tsc",
    "typecheck:legacy": "tsc6 --noEmit",
    "lint": "eslint .",
    "lint:fix": "eslint . --fix",
    "test": "vitest run",
    "test:watch": "vitest",
    "local": "node dist/main.js --insecure --debug"
  },
  "dependencies": {
    "@crossplane-org/function-sdk-typescript": "^0.7.0",
    "@types/node": "^26.0.0",
    "crossplane-models": "file:../../schemas/typescript",
    "kubernetes-models": "^5.0.0"
  },
  "devDependencies": {
    "@eslint/js": "^10.0.1",
    "@typescript/native": "npm:typescript@^7.0.0",
    "eslint": "^10.9.1",
    "typescript": "npm:@typescript/typescript6@^6.0.2",
    "typescript-eslint": "^8.68.0",
    "vitest": "^4.1.11"
  }
}
```

`kubernetes-models` is pinned to `^5.0.0` deliberately: v5 brings `@kubernetes-models/base` v6,
which is the version the generated `crossplane-models` package depends on. Staying on v4 installs
a second, older copy of `base` alongside it.

## Step 8: Generate Schemas

Before building, generate the TypeScript schemas from the dependencies:

```bash
# This happens automatically during build, but you can trigger it manually
crossplane project build
```

The models are generated as TypeScript, then compiled — so `schemas/typescript/` holds JavaScript
plus declarations, not `.ts` sources:

- `ec2.aws.m.upbound.io/v1beta1/VPC.js` and `VPC.d.ts` - namespaced VPC class with full type
  definitions. The cluster-scoped `ec2.aws.upbound.io/` tree is generated alongside it; a
  namespaced XRD uses the `.m.` one.
- `aws.platform.upbound.io/v1alpha1/Network.js` and `Network.d.ts` - Your XRD's types

Schema generation runs once per dependency and is not cheap: adding a function package that
contributes no CRDs at all still costs a few minutes.

## Step 9: Local Development (Optional)

For local development and IDE support:

```bash
cd functions/network

# Install dependencies (including the local schemas package)
npm install

# Build locally to check for TypeScript errors
npm run build

# Run the unit tests
npm test
```

This relies on the `.npmrc` in the generated function directory, which sets:

```ini
install-links=true
```

Without it, npm symlinks `crossplane-models` to `../../schemas/typescript`. Node resolves the
symlink to its real path, which sits outside the function's `node_modules`, so the schemas
package cannot reach its own dependencies and any import of a generated model fails at runtime:

```text
Error [ERR_MODULE_NOT_FOUND]: Cannot find package '@kubernetes-models/base'
imported from .../schemas/typescript/ec2.aws.m.upbound.io/v1beta1/VPC.js
```

`install-links=true` copies the package into `node_modules` instead, which also matches the
layout inside the built function image. If you are working in a project scaffolded before this
setting existed, add the `.npmrc` yourself or run `npm install --install-links`.

## Step 10: Create a Composition

```bash
# Generate a composition from the XRD
crossplane composition generate apis/networks/definition.yaml
```

This generates a basic composition with `function-auto-ready`. You need to add your embedded function to the pipeline.

The functionRef name is derived from the project repository and the function name. The CLI builds the embedded function's image repository as `<repository>_<function-name>`, then converts it to a DNS label — which drops the underscore rather than replacing it. So for repository `xpkg.upbound.io/your-org/configuration-aws-network-ts` and function `network`, the functionRef name is `your-org-configuration-aws-network-tsnetwork`, with no separator before `network`.

Edit `apis/networks/composition.yaml` to add your function before `function-auto-ready`:

```yaml
apiVersion: apiextensions.crossplane.io/v1
kind: Composition
metadata:
  name: networks.aws.platform.upbound.io
spec:
  compositeTypeRef:
    apiVersion: aws.platform.upbound.io/v1alpha1
    kind: Network
  mode: Pipeline
  pipeline:
    - step: network
      functionRef:
        name: your-org-configuration-aws-network-tsnetwork
    - step: crossplane-contrib-function-auto-ready
      functionRef:
        name: crossplane-contrib-function-auto-ready
```

**Tip**: `function generate` can create the function *and* wire it into the pipeline in one go,
which saves the hand-edit above:

```bash
crossplane function generate network apis/networks/composition.yaml --language typescript
```

Use this **instead of Step 6**, not after it. `function generate` refuses to write into a function
directory that already exists, so having followed Step 6 this fails with `function directory
"network" already exists and is not empty`, and the hand-edit above is the way in.

The step is inserted at the **front** of the pipeline, so your function runs before
`function-auto-ready` sees the resources it composes. If the Composition already has a step with that name pointing at a different function — which happens when porting an existing configuration — the command fails rather than creating two steps with the same name, and you edit the pipeline by hand.

### Activating managed resources (Crossplane 2)

Crossplane 2 activates managed resource CRDs through a `ManagedResourceActivationPolicy`, and the
Crossplane Helm chart installs a **wildcard** policy by default — its `provider.defaultActivations`
value is `["*"]`. So on a dev control plane created by `crossplane project run`, every managed
resource is active and composed resources reconcile without you adding anything.

That default is convenient and unlike production, where a control plane usually manages activation
explicitly. Two situations need a policy of your own:

- You pass `crossplane project run --no-default-mrap`, which suppresses the wildcard policy so the
  control plane matches production.
- You deploy to a control plane that does not have the wildcard policy.

In either case, add a `ManagedResourceActivationPolicy` alongside the XRD, listing every managed
resource kind the function creates:

```yaml
apiVersion: apiextensions.crossplane.io/v1alpha1
kind: ManagedResourceActivationPolicy
metadata:
  name: network
spec:
  activate:
    - vpcs.ec2.aws.m.upbound.io
    - subnets.ec2.aws.m.upbound.io
```

Without it the composed resources are created but never reconciled — or their CRDs are absent
altogether, so they are never created at all. `crossplane composition render` does not need the
policy, so this only shows up once you deploy to a cluster.

Testing with `--no-default-mrap` is worth doing before you ship: it is the cheapest way to find
out that an activation policy is incomplete, and the failure is far easier to read locally than in
a production control plane.

## Step 11: Build the Project

```bash
# Build the complete project (configuration + embedded functions)
crossplane project build
```

This will:

1. Generate TypeScript schemas from all dependencies (provider-aws-ec2, your XRD)
2. Build the TypeScript function in a Node.js container
3. Package everything into a Crossplane configuration package

The output will be in `_output/configuration-aws-network-ts.xpkg`.

## Step 12: Test with Composition Render

Before deploying to a cluster, you can test your composition function locally using `crossplane composition render`. This renders the composition pipeline and shows you what resources would be created without needing a Kubernetes cluster.

```bash
# Render the composition with a 5 minute timeout (recommended for TypeScript builds)
crossplane composition render \
  examples/network/example.yaml \
  apis/networks/composition.yaml \
  --timeout=5m
```

The first run may take several minutes as it:

1. Pulls the Node.js build image
2. Runs `npm install` to fetch dependencies
3. Compiles the TypeScript function
4. Executes the function pipeline

Docker and npm caching help on subsequent runs, but not dramatically — the function is rebuilt
in a container every time, so expect a warm render to still take minutes rather than seconds.
Keep `--timeout` generous even once things are cached.

The output shows the rendered XR and all composed resources as YAML:

```bash
# Include function results (informational messages)
crossplane composition render \
  examples/network/example.yaml \
  apis/networks/composition.yaml \
  --timeout=5m \
  --include-function-results

# Include the full XR with spec and metadata
crossplane composition render \
  examples/network/example.yaml \
  apis/networks/composition.yaml \
  --timeout=5m \
  --include-full-xr
```

This is useful for:

- Validating your function logic before deployment
- Debugging composition issues
- Testing changes quickly without a cluster

## Step 13: Test with a Local Dev Cluster

For quick local testing, use `crossplane project run` to spin up a local Kubernetes cluster with Crossplane and your configuration automatically deployed:

```bash
# Start a local dev cluster and deploy the project
crossplane project run
```

Add `--no-default-mrap` to suppress the wildcard activation policy the Crossplane chart installs,
so the control plane behaves like production and the `ManagedResourceActivationPolicy` from Step 10
is what activates your CRDs:

```bash
crossplane project run --no-default-mrap
```

The flag only takes effect when the control plane is **created**. If one already exists it keeps
whatever it was built with, so run `crossplane project stop` first. Verify it applied:

```bash
kubectl -n crossplane-system get deploy crossplane \
  -o jsonpath='{.spec.template.spec.containers[0].args}'
```

With the flag, that shows no `--activation` argument. Without it you get `--activation "*"`.

This will:

1. Create a local Kind cluster
2. Install Crossplane
3. Build and deploy your configuration package
4. Install all provider dependencies

Once the cluster is running, configure AWS credentials for the provider:

The XRD from Step 5 is namespaced, so the ProviderConfig belongs in the XR's namespace and comes
from the mirrored `.m.` group. The secret it points at can live elsewhere:

```bash
kubectl create ns network-team

# Create AWS credentials secret (creds.conf should contain your AWS credentials)
# Format: [default]
#         aws_access_key_id = YOUR_ACCESS_KEY
#         aws_secret_access_key = YOUR_SECRET_KEY
kubectl create secret generic aws-creds -n crossplane-system --from-file=creds=creds.conf

# Create a ProviderConfig to use the credentials
kubectl apply -f - <<EOF
apiVersion: aws.m.upbound.io/v1beta1
kind: ProviderConfig
metadata:
  name: default
  namespace: network-team
spec:
  credentials:
    source: Secret
    secretRef:
      name: aws-creds
      namespace: crossplane-system
      key: creds
EOF
```

`project run` can apply both for you instead, which is how
[configuration-aws-network-ts](https://github.com/upbound/configuration-aws-network-ts) drives its
own testing — `--init-resources` is applied before the Configuration is installed and
`--extra-resources` after, so the namespace exists by the time the ProviderConfig inside it lands:

```bash
crossplane project run \
  --init-resources=examples/network/namespace-network-team.yaml \
  --extra-resources=examples/network/providerconfig.yaml \
  --no-default-mrap
```

Now you can test your configuration:

```bash
# Create an XR to test your function. The XRD from Step 5 is namespaced and has
# no claim, so apply the Network XR directly — v2 drops the X prefix.
kubectl apply -f - <<EOF
apiVersion: aws.platform.upbound.io/v1alpha1
kind: Network
metadata:
  name: my-network
  namespace: network-team
spec:
  region: us-west-2
  cidrBlock: "10.0.0.0/16"
EOF

# Watch the resources being created
kubectl get managed -w

# Check the XR status
kubectl get network.aws.platform.upbound.io my-network -o yaml
```

When you're done testing, tear down the local cluster:

```bash
# Stop and remove the local dev cluster
crossplane project stop
```

## Step 14: Push and Install (Production)

For deploying to a production cluster, push the package to a registry:

```bash
# Push from the project directory, with the .xpkg in _output/
crossplane project push --tag v0.2.0
```

Use `crossplane project push`, not `crossplane xpkg push`. A project produces more than one
package: the configuration, plus one package per embedded function. `project push` uploads all
of them, whereas `xpkg push` would upload only the configuration and leave its function
dependency unresolvable on the cluster.

The embedded function goes to a repository named `<repository>_<function-name>` — so a project
at `xpkg.upbound.io/your-org/configuration-aws-network-ts` with a function named `network`
pushes to `xpkg.upbound.io/your-org/configuration-aws-network-ts_network`. Functions are pushed
first, so if that repository is missing the configuration never gets uploaded at all.

```bash
# Install on a cluster
kubectl apply -f - <<EOF
apiVersion: pkg.crossplane.io/v1
kind: Configuration
metadata:
  name: configuration-aws-network-ts
spec:
  package: xpkg.upbound.io/your-org/configuration-aws-network-ts:v0.2.0
EOF

# Create an XR to test
kubectl apply -f - <<EOF
apiVersion: aws.platform.upbound.io/v1alpha1
kind: Network
metadata:
  name: my-network
  namespace: network-team
spec:
  region: us-west-2
  cidrBlock: "10.0.0.0/16"
EOF

# Watch the resources being created
kubectl get managed -w
```

## Troubleshooting

### TypeScript compilation errors

If you see TypeScript errors during build:

```bash
# Check the function builds locally first
cd functions/network
npm install
npm run build
```

### Lint and test tooling on TypeScript 7

TypeScript 7's native compiler exposes only `typescript/unstable/*`; the JavaScript compiler
API that powers type-aware linting is gone. `typescript-eslint` is built on that API and caps
its `typescript` peer below 7, and there is no release or canary yet that does otherwise.

The scaffold works around this by installing both compilers under aliases:

```json
"devDependencies": {
  "@typescript/native": "npm:typescript@^7.0.0",
  "typescript": "npm:@typescript/typescript6@^6.0.2"
}
```

TypeScript 7 supplies the `tsc` binary that `npm run build` uses. TypeScript 6 keeps the
`typescript` package *name* — which is the specifier `typescript-eslint` imports — and ships
its binary as `tsc6`, so the two never collide. `npm run lint` gets working type-aware rules
and `npm run typecheck:legacy` runs the TypeScript 6 check, which is worth doing once when
upgrading since TypeScript 7 drops deprecated compiler options.

Drop the alias for a plain `typescript` devDependency once TypeScript 7.1 ships its
programmatic API and `typescript-eslint` adopts it.

Tests use [Vitest](https://vitest.dev), which has no `typescript` peer dependency at all.
`ts-jest` also caps its peer below 7; it will resolve against the aliased TypeScript 6 if you
prefer Jest, but Vitest avoids the question.

Whatever you add, incompatible `devDependencies` will not break `crossplane project build`.
The build container installs the compile-time tree with `--legacy-peer-deps`, and strips
`devDependencies` entirely before installing the tree that ships — so neither compiler, nor
your linter, ends up in the function image.

### Missing crossplane-models

If imports from `crossplane-models` fail:

1. Ensure `schemas.languages` includes `typescript` in `crossplane-project.yaml`
2. Run `crossplane project build` to generate schemas
3. Verify `schemas/typescript/` exists and contains the expected types

### Runtime module not found

If the function fails at runtime with "Cannot find package 'crossplane-models'":

1. Ensure the `file:` dependency path in `package.json` is correct
2. The CLI automatically dereferences symlinks during build - check that the function image includes the actual files

### 404 from the registry when pushing

```text
crossplane: error: failed to push function
"xpkg.upbound.io/your-org/configuration-aws-network-ts_network":
GET https://xpkg.upbound.io/service/token?scope=repository%3A...%3Apush%2Cpull
&service=xpkg.upbound.io: unexpected status code 404 Not Found
```

The function package's repository does not exist. Some registries create repositories on first
push; `xpkg.upbound.io` does not, so `<repository>_<function-name>` has to be created before the
first release of a project with an embedded function. This bites when converting an existing
configuration in particular, because the function's repository name changes: a function that
used to ship as `configuration-aws-network-ts-function` becomes
`configuration-aws-network-ts_network`, which has never existed.

Since functions are pushed before the configuration, this fails the whole push and leaves
nothing published — the tag exists with no artifact behind it. Create the repository and re-run
`crossplane project push`; there is no need to re-tag.

### Build timeout during render

If you see an error like:

```text
crossplane: error: cannot build embedded functions: failed to build function "network": failed to build runtime images: typescript build container failed: container unknown failure: context deadline exceeded
```

This means the TypeScript build (including `npm install` and `npm run build`) exceeded the default 1 minute timeout. This commonly happens on the first build when Docker images and npm packages need to be downloaded.

Increase the timeout using the `--timeout` flag:

```bash
# Use a 5 minute timeout
crossplane composition render examples/network/example.yaml apis/networks/composition.yaml --timeout=5m

# Or for larger projects with many dependencies
crossplane composition render examples/network/example.yaml apis/networks/composition.yaml --timeout=10m
```

Subsequent builds are faster as Docker images and npm packages are cached, but the function is
still recompiled in a container on every render, so they are not instant.

## Reference Projects

For a complete working example built from scratch, see:
https://github.com/stevendborrelli/configuration-aws-network-ts-xp-cli

For an example of porting an existing configuration — one that previously built its
function image with a hand-written Dockerfile and packaged it with `crossplane xpkg build`
— see:
https://github.com/upbound/configuration-aws-network-ts

That one is released, so you can see what a project built this way produces without building
anything yourself:

```bash
crossplane xpkg install configuration \
  xpkg.upbound.io/upbound/configuration-aws-network-ts:v0.2.0
```

v0.2.0 is the first release built with `crossplane project build`. The configuration package is
a single manifest; the embedded function at
`xpkg.upbound.io/upbound/configuration-aws-network-ts_network:v0.2.0` is a multi-architecture
index covering `linux/amd64` and `linux/arm64`.

Note that its CI builds the CLI from this PR's branch rather than installing a release, since
the feature has not shipped yet — so v0.2.0 was itself built from an unreleased CLI. That is
marked temporary in `.github/actions/crossplane-cli` and comes out once a release includes the
feature.

Both repositories demonstrate the patterns described in this guide.
