
## Examples

Simulate a changed composite resource against the current context:

```shell
crossplane xr simulate -f xr.yaml
```

Simulate a composite resource and a claim:

```shell
crossplane xr simulate -f network.yaml -f database-claim.yaml
```

Simulate a composite resource read from standard input:

```shell
cat xr.yaml | crossplane xr simulate -f -
```

Print the plan as JSON, without provider plans:

```shell
crossplane xr simulate -f xr.yaml -o json --skip-plan
```
