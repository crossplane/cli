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

Generate runtime.Object methods and per-package AddToScheme helpers on generated
Go models (off by default), so generated types can be registered in a
runtime.Scheme:

```shell
crossplane config set features.generateGoRuntimeObjects true
```
