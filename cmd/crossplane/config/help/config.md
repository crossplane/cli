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

Generate `runtime.Object` methods and per-package `AddToScheme` helpers on
generated Go models (off by default), so you can register generated types with a
`runtime.Scheme`:

```shell
crossplane config set features.generateGoRuntimeObjects true
```
