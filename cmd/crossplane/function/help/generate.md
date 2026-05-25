The `function generate` command creates an embedded function in the specified
language under the project's `functions/` directory. It optionally idempotently
adds the new function to the end of a Composition's pipeline when given a
Composition path.

## Supported languages

The following are valid arguments to the `--language` / `-l` flag:

- `go-templating` (default)
- `go`
- `kcl`
- `python`

## Examples

Create a function with the default language (`go-templating`) in
`functions/fn1`:

```shell
crossplane function generate fn1
```

Create a Python function in `functions/fn2`:

```shell
crossplane function generate fn2 --language python
```

Create a Go function in `functions/compose-cluster` and add it as a pipeline
step in the given Composition:

```shell
crossplane function generate compose-cluster apis/cluster/composition.yaml --language go
```
