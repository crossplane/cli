The `resource trace` command traces a Crossplane resource (Claim, Composite, or
Managed Resource) to give a detailed view of its relationships and help
troubleshoot compositions.

The command requires a resource type and a resource name:

```shell
crossplane resource trace <resource kind> <resource name>
```

Kubernetes-style `<kind>/<name>` input works too: for example, `crossplane
resource trace example.crossplane.io/my-xr`.

You can further specify the kind as `TYPE[.VERSION][.GROUP]` if needed; for
example, `mykind.example.org` or `mykind.v1alpha1.example.org`.

By default, `crossplane resource trace` uses the Kubernetes configuration at
`~/.kube/config`. Override with the `KUBECONFIG` environment variable.

## Output options

By default, `trace` prints to the terminal as a tree, truncating the `Ready` and
`Status` messages to 64 characters.

Change the format with `-o` (`--output`): `wide`, `json`, `yaml`, or `dot` (for
a [Graphviz](https://graphviz.org/docs/layouts/dot/) graph).

### Wide output

Use `--output=wide` to print the full `Ready` and `Status` messages even when
they exceed 64 characters, and other kind-specific printer columns.

### Graphviz dot output

Use `--output=dot` to print a textual
[Graphviz dot](https://graphviz.org/docs/layouts/dot/) graph. Pipe to `dot` to
render an image:

```shell
crossplane resource trace cluster.aws.platformref.upbound.io platform-ref-aws -o dot | dot -Tpng -o graph.png
```

## Print connection secrets

Use `--show-connection-secrets` to include connection-secret names alongside the
other resources. Secret values are never printed. Output includes the secret
name and namespace.

## Print related operations

The `--show-operations` flag controls the display of operations
(`ops.crossplane.io`) related to each traced resource:

- `none` (default): hide operations.
- `triggered`: show the WatchOperations watching each resource, with the
  Operations they created in response to changes to it as children.
- `all`: also show the Operations that applied changes to each resource,
  grouped under the CronOperation or WatchOperation that created them, if
  any.

Operations aren't part of the resource tree, they only relate to it, so the
default output marks them with an arrow (`├◀`) pointing at the resource they
relate to. The structured output formats include the relation (`watching`,
`triggered` or `applied`) as a `relation` field instead.

The flag has no effect when tracing packages, or when the cluster doesn't
support operations.

## Print package dependencies

The `--show-package-dependencies` flag controls how the display of package
dependencies:

- `unique` (default): include each required package only once.
- `all`: show every package that requires the same dependency.
- `none`: hide all package dependencies.

## Print package revisions

The `--show-package-revisions` flag controls the display of package revisions:

- `active` (default): show only the active revisions.
- `all`: show all revisions, including inactive ones.
- `none`: hide all revisions.

## Examples

Trace a `MyKind` resource named `my-res` in the namespace `my-ns`:

```shell
crossplane resource trace mykind my-res -n my-ns
```

Trace all `MyKind` resources in the namespace `my-ns`:

```shell
crossplane resource trace mykind -n my-ns
```

Wide format with full errors, condition messages, and kind-specific columns:

```shell
crossplane resource trace mykind my-res -n my-ns -o wide
```

Show connection secret names alongside the resources:

```shell
crossplane resource trace mykind my-res -n my-ns --show-connection-secrets
```

Show all operations related to the resources, for example the WatchOperations
watching them and the Operations that changed them:

```shell
crossplane resource trace mykind my-res -n my-ns --show-operations=all
```

Output a Graphviz dot graph and pipe to dot to generate a PNG:

```shell
crossplane resource trace mykind my-res -n my-ns -o dot | dot -Tpng -o output.png
```

Output all retrieved resources as JSON and pipe to jq for color:

```shell
crossplane resource trace mykind my-res -n my-ns -o json | jq
```

Output debug logs to stderr while piping a dot graph to dot:

```shell
crossplane resource trace mykind my-res -n my-ns -o dot --verbose | dot -Tpng -o output.png
```

Watch a resource continuously until its deletion:

```shell
crossplane resource trace mykind my-res -n my-ns --watch
```
