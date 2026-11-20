The `resource resume` command resumes the reconciliation of a paused Crossplane resource (Claim,
Composite, or Managed Resource) by removing the `crossplane.io/paused` annotation.
It also sets the `crossplane.io/reconcile-requested-at` annotation to the
current time, so the resource is reconciled right away instead of waiting for
the next poll interval.

The command requires a resource type. The resource name is optional; when it
is omitted, the command applies to all resources of that type:

```shell
crossplane resource resume <resource kind> [<resource name>]
```

Kubernetes-style `<kind>/<name>` input works too: for example, `crossplane
resource resume example.crossplane.io/my-xr`.

You can further specify the kind as `TYPE[.VERSION][.GROUP]` if needed; for
example, `mykind.example.org` or `mykind.v1alpha1.example.org`.

By default, `crossplane resource resume` uses the Kubernetes configuration at
`~/.kube/config`. Override with the `KUBECONFIG` environment variable.

## Cascade

By default, `resume` only applies to the requested resource. Use `--cascade`
to resume the whole resource tree recursively. Combine it with `--watch` to
follow the status afterwards:

```shell
crossplane resource resume <resource kind> <resource name> --cascade --watch
```

## Output options

After applying the change, `resume` prints the affected resources to the
terminal as a tree, the same way `crossplane resource trace` does. Without
`--cascade` only the requested resources are printed.

Change the format with `-o` (`--output`): `wide`, `json`, `yaml`, or `dot` (for
a [Graphviz](https://graphviz.org/docs/layouts/dot/) graph).

## Examples

Resume a `MyKind` resource named `my-res` in the namespace `my-ns`:

```shell
crossplane resource resume mykind my-res -n my-ns
```

Resume all `MyKind` resources in the namespace `my-ns`:

```shell
crossplane resource resume mykind -n my-ns
```

Resume a resource and all of its descendants:

```shell
crossplane resource resume mykind my-res -n my-ns --cascade
```

Resume a resource and watch it continuously until its deletion:

```shell
crossplane resource resume mykind my-res -n my-ns --watch
```
