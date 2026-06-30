# Testing TypeScript Support in Crossplane CLI

This guide walks through testing the TypeScript support added in PR #170. We'll create a complete Crossplane configuration project with a TypeScript composition function.

An example project is located at <https://github.com/stevendborrelli/configuration-aws-network-ts-xp-cli>.

## Prerequisites

- Go 1.25+
- Docker
- Node.js 24+ (for local development)
- A Kubernetes cluster with Crossplane installed
- Access to push packages to a registry (e.g., `xpkg.upbound.io`)

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

## Step 2: Create a New Project

```bash
# Initialize the project (this creates the directory)
crossplane project init configuration-aws-network-ts \
  --registry xpkg.upbound.io/your-org

cd configuration-aws-network-ts
```

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
        package: xpkg.upbound.io/upbound/provider-aws-ec2
        version: ">=v2.6.0"
    - type: xpkg
      xpkg:
        package: xpkg.crossplane.io/crossplane-contrib/function-auto-ready
        version: ">=v0.7.0"
```

## Step 4: Add Dependencies

When a dependency is added to a Crossplane project:

- The dependency is deployed to the cluster
- The CLI generates Schemas from any CRDS

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
kind: XNetwork
metadata:
  name: example-network
spec:
  region: us-west-2
  cidrBlock: "10.0.0.0/16"
EOF
```

Then generate the XRD from the example:

```bash
# Generate an XRD from the example XR
crossplane xrd generate examples/network/example.yaml
```

Edit `apis/xnetwork/definition.yaml` to add spec fields:

```yaml
apiVersion: apiextensions.crossplane.io/v1
kind: CompositeResourceDefinition
metadata:
  name: xnetworks.aws.platform.upbound.io
spec:
  group: aws.platform.upbound.io
  names:
    kind: XNetwork
    plural: xnetworks
  claimNames:
    kind: Network
    plural: networks
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

## Step 6: Create a TypeScript Function

```bash
# Generate a TypeScript function scaffold
crossplane function generate network --language typescript
```

**Note**: You can also generate a function and add it to a composition pipeline in one step:

```bash
crossplane function generate network apis/xnetwork/composition.yaml --language typescript
```

This creates `functions/network/` with:
- `package.json` - Dependencies including `@crossplane-org/function-sdk-typescript`
- `tsconfig.json` - TypeScript configuration
- `src/main.ts` - Entry point
- `src/function.ts` - Function implementation template

## Step 7: Implement the Function

The generated `functions/network/src/function.ts` contains a template implementation. A full example is available at [function.ts](https://github.com/stevendborrelli/configuration-aws-network-ts-xp-cli/blob/main/functions/network/src/function.ts).

Edit the `function.ts` to create a VPC:

```typescript
import {
  type RunFunctionRequest,
  type RunFunctionResponse,
  type FunctionHandler,
  type Logger,
  to,
  normal,
  fatal,
  getObservedCompositeResource,
  getDesiredComposedResources,
  setDesiredComposedResources,
} from '@crossplane-org/function-sdk-typescript';

// Import the generated types from crossplane-models
import { VPC } from 'crossplane-models/ec2.aws.upbound.io/v1beta1';

/**
 * Function is a Crossplane composition function that creates a VPC.
 */
export class Function implements FunctionHandler {
  async RunFunction(req: RunFunctionRequest, logger?: Logger): Promise<RunFunctionResponse> {
    let rsp = to(req);

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

    // Get the desired composed resources from previous functions in the pipeline.
    const desiredComposed = getDesiredComposedResources(req);

    // Create a VPC using the generated TypeScript class
    const vpc = new VPC({
      metadata: {
        name: `${xrName}-vpc`,
        annotations: {
          'crossplane.io/external-name': `${xrName}-vpc`,
        },
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

    // Add the VPC to desired resources
    desiredComposed['vpc'] = { resource: vpc };

    // Update the response with the desired composed resources.
    rsp = setDesiredComposedResources(rsp, desiredComposed);

    normal(rsp, 'Successfully composed VPC resource');
    return rsp;
  }
}
```

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
    "build": "tsgo",
    "local": "node dist/main.js --insecure --debug"
  },
  "dependencies": {
    "@crossplane-org/function-sdk-typescript": "^0.5.0",
    "@types/node": "^26.0.0",
    "commander": "^15.0.0",
    "crossplane-models": "file:../../schemas/typescript",
    "kubernetes-models": "^4.5.1",
    "pino": "^10.3.0"
  },
  "devDependencies": {
    "@typescript/native-preview": "^7.0.0-dev.20260627.1",
    "typescript": "^6.0.0"
  }
}
```

## Step 8: Generate Schemas

Before building, generate the TypeScript schemas from the dependencies:

```bash
# This happens automatically during build, but you can trigger it manually
crossplane project build
```

After this, `schemas/typescript/` will contain generated TypeScript models including:

- `ec2.aws.upbound.io/v1beta1/VPC.ts` - VPC class with full type definitions
- `aws.platform.upbound.io/v1alpha1/XNetwork.ts` - Your XRD's types

## Step 9: Local Development (Optional)

For local development and IDE support:

```bash
cd functions/network

# Install dependencies (including the local schemas package)
npm install

# Build locally to check for TypeScript errors
npm run build
```

## Step 10: Create a Composition

```bash
# Generate a composition from the XRD
crossplane composition generate apis/xnetwork/definition.yaml
```

This generates a basic composition with `function-auto-ready`. You need to add your embedded function to the pipeline.

The function name in the composition follows the pattern: `<org>-<project>-<function-name>` derived from the project repository. For example, if your repository is `xpkg.upbound.io/your-org/configuration-aws-network-ts` and your function is named `network`, the functionRef name will be `your-org-configuration-aws-network-ts-network`.

Edit `apis/xnetwork/composition.yaml` to add your function before `function-auto-ready`. The `name` of the function is of the format:
`<registry org>-<project name><function name>`.

```yaml
apiVersion: apiextensions.crossplane.io/v1
kind: Composition
metadata:
  name: xnetworks.aws.platform.upbound.io
spec:
  compositeTypeRef:
    apiVersion: aws.platform.upbound.io/v1alpha1
    kind: XNetwork
  mode: Pipeline
  pipeline:
    - step: network
      functionRef:
        name: your-org-configuration-aws-network-tsnetwork
    - step: crossplane-contrib-function-auto-ready
      functionRef:
        name: crossplane-contrib-function-auto-ready
```

**Tip**: You can generate the function and add it to the composition pipeline automatically by running:

```bash
crossplane function generate network apis/xnetwork/composition.yaml --language typescript
```

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

## Step 12: Test Locally with a Dev Cluster

For quick local testing, use `crossplane project run` to spin up a local Kubernetes cluster with Crossplane and your configuration automatically deployed:

```bash
# Start a local dev cluster and deploy the project
crossplane project run
```

This will:

1. Create a local Kind cluster
2. Install Crossplane
3. Build and deploy your configuration package
4. Install all provider dependencies

Once the cluster is running, configure AWS credentials for the provider:

```bash
# Create AWS credentials secret (creds.conf should contain your AWS credentials)
# Format: [default]
#         aws_access_key_id = YOUR_ACCESS_KEY
#         aws_secret_access_key = YOUR_SECRET_KEY
kubectl create secret generic aws-creds -n default --from-file=creds=creds.conf

# Create a ProviderConfig to use the credentials
kubectl apply -f - <<EOF
apiVersion: aws.upbound.io/v1beta1
kind: ProviderConfig
metadata:
  name: default
spec:
  credentials:
    source: Secret
    secretRef:
      name: aws-creds
      namespace: default
      key: creds
EOF
```

Now you can test your configuration:

```bash
# Create a claim to test your function
kubectl apply -f - <<EOF
apiVersion: aws.platform.upbound.io/v1alpha1
kind: Network
metadata:
  name: my-network
  namespace: default
spec:
  region: us-west-2
  cidrBlock: "10.0.0.0/16"
EOF

# Watch the resources being created
kubectl get managed -w

# Check the XR status
kubectl get xnetwork my-network -o yaml
```

When you're done testing, tear down the local cluster:

```bash
# Stop and remove the local dev cluster
crossplane project stop
```

## Step 13: Push and Install (Production)

For deploying to a production cluster, push the package to a registry:

```bash
# Push to a registry (from the project directory with the .xpkg in _output/)
crossplane xpkg push \
  -f _output/configuration-aws-network-ts.xpkg \
  xpkg.upbound.io/your-org/configuration-aws-network-ts:v0.1.0

# Install on a cluster
kubectl apply -f - <<EOF
apiVersion: pkg.crossplane.io/v1
kind: Configuration
metadata:
  name: configuration-aws-network-ts
spec:
  package: xpkg.upbound.io/your-org/configuration-aws-network-ts:v0.1.0
EOF

# Create a claim to test
kubectl apply -f - <<EOF
apiVersion: aws.platform.upbound.io/v1alpha1
kind: Network
metadata:
  name: my-network
  namespace: default
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

### Missing crossplane-models

If imports from `crossplane-models` fail:

1. Ensure `schemas.languages` includes `typescript` in `crossplane-project.yaml`
2. Run `crossplane project build` to generate schemas
3. Verify `schemas/typescript/` exists and contains the expected types

### Runtime module not found

If the function fails at runtime with "Cannot find package 'crossplane-models'":

1. Ensure the `file:` dependency path in `package.json` is correct
2. The CLI automatically dereferences symlinks during build - check that the function image includes the actual files

## Reference Project

For a complete working example, see:
https://github.com/stevendborrelli/configuration-aws-network-ts-xp-cli

This repository demonstrates all the patterns described in this guide.
