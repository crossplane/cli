The `composition generate` command creates a Composition for a
CompositeResourceDefinition (XRD). The generated Composition contains a single
pipeline step that runs `function-auto-ready`, which is automatically added to
the project's dependencies if it isn't already present.

## Examples

Generate a Composition from a CompositeResourceDefinition (XRD) and save it next
to the XRD under the project's APIs directory:

```shell
crossplane composition generate apis/network/definition.yaml
```

Generate a Composition with a custom name prefix:

```shell
crossplane composition generate examples/network/network-aws.yaml --name aws
```

Generate a Composition with a custom plural form, useful when automatic
pluralization is wrong (for example, "postgres"):

```shell
crossplane composition generate examples/database/database.yaml --plural postgreses
```

Write the generated Composition to a specific path:

```shell
crossplane composition generate apis/network/definition.yaml --path apis/network/composition.yaml
```
