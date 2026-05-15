The `composition convert composition-environment` command converts a Crossplane
Composition to use `function-environment-configs` in place of native Composition
Environments, which were removed in Crossplane v1.18.

It adds a function pipeline step using
`crossplane-contrib/function-environment-configs` if needed. By default the
function is referenced as `function-environment-configs`, but this can be
overridden with `--function-environment-configs-ref`.

## Examples

Convert an existing pipeline mode Composition using native Composition
Environment to `function-environment-configs`:

```shell
crossplane composition convert composition-environment composition.yaml \
  -o composition-environment.yaml
```

Use a different functionRef and output to stdout:

```shell
crossplane composition convert composition-environment composition.yaml \
  --function-environment-configs-ref=local-function-environment-configs
```

Read a composition from stdin and output the updated composition on stdout:

```shell
cat composition.yaml | crossplane composition convert composition-environment
```
