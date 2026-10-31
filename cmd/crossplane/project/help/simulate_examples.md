
## Examples

Simulate the project in the current directory against the current context:

```shell
crossplane project simulate
```

Simulate the project in another directory:

```shell
crossplane project simulate -f path/to/crossplane-project.yaml
```

Simulate against another context, only for composite resources in one
namespace:

```shell
crossplane project simulate --context=staging -n team-a
```

Simulate a single namespaced composite resource:

```shell
crossplane project simulate -n team-a --name=my-database
```

Simulate two composite resources in different namespaces:

```shell
crossplane project simulate --name=team-a/my-database --name=team-b/my-database
```

Print the plan as JSON:

```shell
crossplane project simulate -o json
```

Print a line diff of each changed resource:

```shell
crossplane project simulate -o diff
```
