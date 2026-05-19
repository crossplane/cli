The `cluster top` command returns current resource utilization (CPU and memory)
by Crossplane pods. Similar to `kubectl top pods`, it requires the [Metrics
Server](https://kubernetes-sigs.github.io/metrics-server/) to be correctly
configured and working on the server.

## Examples

Show resource utilization for all Crossplane pods in the `crossplane-system`
namespace:

```shell
crossplane cluster top
```

Show resource utilization for all Crossplane pods in the `default` namespace:

```shell
crossplane cluster top -n default
```

Add a summary of resource utilization for all Crossplane pods on top of the
results:

```shell
crossplane cluster top -s
```
