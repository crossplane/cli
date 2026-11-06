The `resource reconcile` command triggers an immediate reconciliation of a Crossplane resource (Claim,
Composite, or Managed Resource) by setting the
`crossplane.io/reconcile-requested-at` annotation.

Paused resources (with the `crossplane.io/paused: "true"` annotation) are not
reconciled. If any affected resource is paused, including descendants when
using `--cascade`, the command fails without changing anything. Use
`crossplane resource resume` to resume and reconcile paused resources.

The command requires a resource type. The resource name is optional; when it
is omitted, the command applies to all resources of that type:

```shell
crossplane resource reconcile <resource kind> [<resource name>]
```

Kubernetes-style `<kind>/<name>` input works too: for example, `crossplane
resource reconcile example.crossplane.io/my-xr`.

You can further specify the kind as `TYPE[.VERSION][.GROUP]` if needed; for
example, `mykind.example.org` or `mykind.v1alpha1.example.org`.

By default, `crossplane resource reconcile` uses the Kubernetes configuration at
`~/.kube/config`. Override with the `KUBECONFIG` environment variable.

## Cascade

By default, `reconcile` only applies to the requested resource. Use `--cascade`
to reconcile the whole resource tree recursively. Combine it with `--watch` to
follow the status afterwards:

```shell
crossplane resource reconcile <resource kind> <resource name> --cascade --watch
```

## Output options

After applying the change, `reconcile` prints the affected resources to the
terminal as a tree, the same way `crossplane resource trace` does. Without
`--cascade` only the requested resources are printed.

Change the format with `-o` (`--output`): `wide`, `json`, `yaml`, or `dot` (for
a [Graphviz](https://graphviz.org/docs/layouts/dot/) graph).

## Examples

Reconcile a `MyKind` resource named `my-res` in the namespace `my-ns`:

```shell
crossplane resource reconcile mykind my-res -n my-ns
```

Reconcile all `MyKind` resources in the namespace `my-ns`:

```shell
crossplane resource reconcile mykind -n my-ns
```

Reconcile a resource and all of its descendants:

```shell
crossplane resource reconcile mykind my-res -n my-ns --cascade
```

Reconcile a resource and watch it continuously until its deletion:

```shell
crossplane resource reconcile mykind my-res -n my-ns --watch
```
