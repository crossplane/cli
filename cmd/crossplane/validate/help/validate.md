The `resource validate` command validates the provided Crossplane resources
against the schemas of the provided extensions (XRDs, CRDs, Providers,
Functions, and Configurations). It uses the Kubernetes API server's validation
library plus additional checks (such as unknown-field detection, a common source
of difficult-to-debug Crossplane issues).

If Providers or Configurations are provided as extensions, they are downloaded
and loaded as CRDs before validation. If `--cache-dir` is not set, it defaults
to `~/.crossplane/cache`. Clean the cache before downloading schemas with
`--clean-cache`.

All validation is performed offline using the Kubernetes API server's validation
library — no Crossplane instance or control plane is required.

`crossplane resource validate` supports validating:

- A managed or composite resource against a Provider or XRD schema.
- The output of `crossplane composition render`.
- An XRD's [Common Expression
  Language](https://kubernetes.io/docs/reference/using-api/cel/) (CEL) rules.
- Resources against a directory of schemas.

## Validate resources against a schema

When validating against a Provider, the command downloads the Provider package
to `--cache-dir`. Access to a Kubernetes cluster or Crossplane pod is not
required — only the ability to download the Provider package.

Create a Provider manifest:

```yaml
apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: crossplane-contrib-provider-aws-iam
spec:
  package: xpkg.crossplane.io/crossplane-contrib/provider-aws-iam:v2.0.0
```

Provide a managed resource to validate:

```yaml
apiVersion: iam.aws.m.upbound.io/v1beta1
kind: AccessKey
metadata:
  namespace: default
  name: sample-access-key-0
spec:
  forProvider:
    userSelector:
      matchLabels:
        example-name: test-user-0
```

Run validate with both files:

```shell
crossplane resource validate provider.yaml managedResource.yaml
```

## Validate render output

Pipe the output of `crossplane composition render` to `validate` to check
complete Crossplane resource pipelines, including XRs, Compositions, and
Functions. Use `--include-full-xr` on `render`, and `-` (read stdin) on
`validate`:

```shell
crossplane composition render xr.yaml composition.yaml func.yaml --include-full-xr | \
    crossplane resource validate schemas.yaml -
```

## Validate CEL rules

XRDs can define [validation
rules](https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definitions/#validation-rules)
in CEL via `x-kubernetes-validations`. `validate` evaluates them:

```yaml
apiVersion: apiextensions.crossplane.io/v1
kind: CompositeResourceDefinition
metadata:
  name: myxrs.example.crossplane.io
spec:
  # ... versions[].schema.openAPIV3Schema:
  #   spec:
  #     x-kubernetes-validations:
  #       - rule: "self.minReplicas <= self.replicas && self.replicas <= self.maxReplicas"
  #         message: "replicas should be in between minReplicas and maxReplicas."
```

## Validate against a directory of schemas

`validate` can also take a directory of schema YAML files. Only `.yaml` and
`.yml` files are processed; other files are ignored.

```text
schemas/
├── platform-ref-aws.yaml
├── providers/
│   └── provider-aws-iam.yaml
└── xrds/
    └── xrd.yaml
```

```shell
crossplane resource validate schemas/ resources.yaml
```

## Examples

Validate resources against extensions in extensions.yaml:

```shell
crossplane resource validate extensions.yaml resources.yaml
```

Validate resources in a directory against extensions in another directory:

```shell
crossplane resource validate crossplane.yaml,extensionsDir/ resourceDir/
```

Pin the Crossplane image version used during validation:

```shell
crossplane resource validate extensions.yaml resources.yaml \
  --crossplane-image=xpkg.crossplane.io/crossplane/crossplane:v1.20.0
```

Skip success log lines (only print problems):

```shell
crossplane resource validate extensionsDir/ resourceDir/ --skip-success-results
```

Validate the output of render against extensions in a directory:

```shell
crossplane composition render xr.yaml composition.yaml func.yaml --include-full-xr | \
    crossplane resource validate extensionsDir/ -
```

Use a custom cache directory and clean it before downloading schemas:

```shell
crossplane resource validate extensionsDir/ resourceDir/ --cache-dir .cache --clean-cache
```
