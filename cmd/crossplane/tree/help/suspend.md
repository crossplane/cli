The `resource suspend` command pauses the reconciliation of a Crossplane resource (Claim, Composite, or
Managed Resource) by setting the `crossplane.io/paused` annotation.

The command requires a resource type. The resource name is optional; when it
is omitted, the command applies to all resources of that type:

```shell
crossplane resource suspend <resource kind> [<resource name>]
```

Kubernetes-style `<kind>/<name>` input works too: for example, `crossplane
resource suspend example.crossplane.io/my-xr`.

You can further specify the kind as `TYPE[.VERSION][.GROUP]` if needed; for
example, `mykind.example.org` or `mykind.v1alpha1.example.org`.

By default, `crossplane resource suspend` uses the Kubernetes configuration at
`~/.kube/config`. Override with the `KUBECONFIG` environment variable.

## Cascade

By default, `suspend` only applies to the requested resource. Use `--cascade`
to suspend the whole resource tree recursively. Combine it with `--watch` to
follow the status afterwards:

```shell
crossplane resource suspend <resource kind> <resource name> --cascade --watch
```

## Output options

After applying the change, `suspend` prints the affected resources to the
terminal as a tree, the same way `crossplane resource trace` does. Without
`--cascade` only the requested resources are printed.

Change the format with `-o` (`--output`): `wide`, `json`, `yaml`, or `dot` (for
a [Graphviz](https://graphviz.org/docs/layouts/dot/) graph).

## Examples

Suspend a `MyKind` resource named `my-res` in the namespace `my-ns`:

```shell
crossplane resource suspend mykind my-res -n my-ns
```

Suspend all `MyKind` resources in the namespace `my-ns`:

```shell
crossplane resource suspend mykind -n my-ns
```

Suspend a resource and all of its descendants:

```shell
crossplane resource suspend mykind my-res -n my-ns --cascade
```

Suspend a resource and watch it continuously until its deletion:

```shell
crossplane resource suspend mykind my-res -n my-ns --watch
```
