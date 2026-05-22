The `cluster top` command returns current resource usage (CPU and memory) by
Crossplane pods. Like `kubectl top pods`, it requires the
[Metrics Server](https://kubernetes-sigs.github.io/metrics-server/).

## Examples

Show resource usage for all Crossplane pods in the `crossplane-system`
namespace:

```shell
crossplane cluster top
```

Show resource usage for all Crossplane pods in the `default` namespace:

```shell
crossplane cluster top -n default
```

Add a summary of resource usage for all Crossplane pods on top of the results:

```shell
crossplane cluster top -s
```
