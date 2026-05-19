The `resource trace` command traces a Crossplane resource (Claim, Composite, or
Managed Resource) to give a detailed view of its relationships — helpful for
troubleshooting.

The command requires a resource type and a resource name:

```shell
crossplane resource trace <resource kind> <resource name>
```

Kubernetes-style `<kind>/<name>` input works too — for example, `crossplane
resource trace example.crossplane.io/my-xr`.

If needed the resource kind can be specified further as
`TYPE[.VERSION][.GROUP]`, for example `mykind.example.org` or
`mykind.v1alpha1.example.org`.

By default, `crossplane resource trace` uses the Kubernetes configuration at
`~/.kube/config`. Override with the `KUBECONFIG` environment variable.

## Output options

By default, `trace` prints to the terminal as a tree, truncating the `Ready` and
`Status` messages to 64 characters.

Change the format with `-o` (`--output`): `wide`, `json`, `yaml`, or `dot` (for
a [Graphviz](https://graphviz.org/docs/layouts/dot/) graph).

### Wide output

Use `--output=wide` to print the full `Ready` and `Status` messages even when
they exceed 64 characters, and additional per-kind columns (for example,
composed resource names for composite resources, or image used for packages).

### Graphviz dot output

Use `--output=dot` to print a textual [Graphviz
dot](https://graphviz.org/docs/layouts/dot/) graph. Pipe to `dot` to render an
image:

```shell
crossplane resource trace cluster.aws.platformref.upbound.io platform-ref-aws -o dot | dot -Tpng -o graph.png
```

## Print connection secrets

Use `--show-connection-secrets` to include connection-secret names alongside the
other resources. Secret values are never printed. Output includes the secret
name and namespace.

## Print package dependencies

The `--show-package-dependencies` flag controls how package dependencies are
displayed:

- `unique` (default) — include each required package only once.
- `all` — show every package that requires the same dependency.
- `none` — hide all package dependencies.

## Print package revisions

The `--show-package-revisions` flag controls which package revisions are shown:

- `active` (default) — show only the active revisions.
- `all` — show all revisions, including inactive ones.
- `none` — hide all revisions.

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

Watch a resource continuously until it is deleted:

```shell
crossplane resource trace mykind my-res -n my-ns --watch
```
