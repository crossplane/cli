The `xr generate` command creates a Composite Resource (XR) from a Claim YAML.

It reads the Claim from a file (or stdin), produces an XR (same spec, derived
kind, optional Claim reference), and writes the result to stdout or to a file.

## Examples

Generate an XR from `claim.yaml` and print it to stdout (kind is `X` + Claim's
kind):

```shell
crossplane xr generate claim.yaml
```

Generate an XR from `claim.yaml` and write it to `xr.yaml`:

```shell
crossplane xr generate claim.yaml -o xr.yaml
```

Generate an XR with an explicit name (overrides the default suffix or Claim
name):

```shell
crossplane xr generate claim.yaml --name my-xr
```

Generate an XR with a specific kind:

```shell
crossplane xr generate claim.yaml --kind MyCompositeResource
```

Generate an XR with the kind resolved from its XRD:

```shell
crossplane xr generate claim.yaml --xrd xrd.yaml
```

Generate a directly linked XR (no Claim reference, no name suffix):

```shell
crossplane xr generate claim.yaml --direct
```

Generate an XR with a fresh random `metadata.uid`:

```shell
crossplane xr generate claim.yaml --gen-uid
```

Use in `crossplane render`:

```shell
crossplane render <(crossplane xr generate claim.yaml) composition.yaml functions.yaml
```

Read the Claim from stdin:

```shell
cat claim.yaml | crossplane xr generate -
```
