The `xrd generate` command creates a CompositeResourceDefinition (XRD) from
either an example Composite Resource (XR) or a SimpleSchema document, and writes
it into the project's APIs directory.

By default the input is treated as an XR; pass `--from simpleschema` to generate
an XRD from a SimpleSchema definition instead.

## Examples

Generate an XRD from an example Composite Resource (XR) and save it under the
project's APIs directory:

```shell
crossplane xrd generate examples/cluster/example.yaml
```

Generate an XRD with a specific plural form, useful when automatic pluralization
is wrong (e.g., "postgres"):

```shell
crossplane xrd generate examples/postgres/example.yaml --plural postgreses
```

Generate an XRD and save it to a custom path within the project's APIs
directory:

```shell
crossplane xrd generate examples/postgres/example.yaml --path database/definition.yaml
```

Generate an XRD from a SimpleSchema document:

```shell
crossplane xrd generate apis/network/schema.yaml --from simpleschema
```
