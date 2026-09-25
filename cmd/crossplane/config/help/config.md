The `config` command manages the configuration file for the `crossplane`
CLI. The configuration file location is, in priority order:

1. The `--config` flag.
2. The `CROSSPLANE_CONFIG` environment variable.
3. `$XDG_CONFIG_HOME/crossplane/config.yaml` (or `~/.config/crossplane/config.yaml`).

## Examples

Show the current effective configuration:

```shell
crossplane config view
```

Enable alpha commands:

```shell
crossplane config set features.enableAlpha true
```

Generate GetX/SetX accessor methods on generated Go models (off by default), so
you can reach generated resources through interfaces and generics:

```shell
crossplane config set features.generateGoModelAccessors true
```

Generate `runtime.Object`, `metav1.Object`, and `metav1.ListInterface`
methods on generated Go models, plus per-package `AddToScheme` helpers (off
by default). These methods let you register generated types with a
`runtime.Scheme` and use them with `sigs.k8s.io/controller-runtime` as
`client.Object` and `client.ObjectList` values, for example with
`client.Client.Get`, `List`, and `Create`:

```shell
crossplane config set features.generateGoRuntimeObjects true
```

**Breaking change:** This flag changes two things in the generated models.
The `Metadata` field's Go type changes from a mirror struct this tool
generates to the real `k8s.io/apimachinery/pkg/apis/meta/v1.ObjectMeta` or
`ListMeta` type. A `<Kind>List` type's `Items` field changes from `*[]Kind`
to `[]Kind`. Both changes let the generated types implement `client.Object`
and `client.ObjectList`.

Turning on this flag also stops the CLI from generating the local
`io/k8s/meta/v1` (or `io/k8s/core/meta/v1`) mirror package. Every reference
to a `meta/v1` type, including `Time`, `OwnerReference`, `LabelSelector`,
`Condition`, `DeleteOptions`, and `MicroTime`, now points to the real
`k8s.io/apimachinery` package instead. Code that imports the mirror package
directly won't build after you turn this flag on.

This flag was off by default and wasn't usable as a `client.Object` before
this change, so we don't provide a migration path. Regenerate your models
after upgrading. The schema manager skips regeneration when your
dependency's version hasn't changed, so run
`crossplane dependency clean-cache` first if regenerating doesn't pick up
the new output.
