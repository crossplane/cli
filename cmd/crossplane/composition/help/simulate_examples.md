
## Examples

Simulate a changed Composition against the current context:

```shell
crossplane composition simulate -f composition.yaml
```

Simulate two Compositions, only for composite resources in one namespace:

```shell
crossplane composition simulate -f network.yaml -f database.yaml -n team-a
```

Simulate a Composition read from standard input, for a single composite
resource:

```shell
cat composition.yaml | crossplane composition simulate -f - --name=team-a/my-database
```

Print the plan as JSON, without provider plans:

```shell
crossplane composition simulate -f composition.yaml -o json --skip-plan
```
