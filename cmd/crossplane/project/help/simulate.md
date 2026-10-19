The `project simulate` command previews how applying a project's Compositions
would change the composite resources in a cluster. It renders each composite
resource that uses one of the project's Compositions with the project's
version of that Composition, and shows the difference from what's in the
cluster. It doesn't change the cluster.

This command:

- Builds the project's embedded functions and loads them into the local
  Docker daemon, and resolves the project's function dependencies.
- Collects the Compositions under the project's APIs path.
- Connects to the cluster in your current Kubernetes context, or the one
  `--context` names.
- Renders each affected composite resource with the project's functions in
  place of the cluster's functions of the same name, and the cluster's
  functions for every other pipeline step.
- Prints the difference.

The command needs Docker, to build and run functions, and read access to the
cluster's Crossplane resources. It verifies the rendered resources with
server-side dry-run requests, so it also needs the `create` and `patch` verbs on
them. Use `--as`, `--as-group`, and `--as-uid` to simulate as another user.

By default, `simulate` skips composite resources with a `Manual` composition
update policy, because they don't adopt a new Composition revision on their
own. Pass `--include-pinned` to simulate them too.

The `--timeout` flag bounds the whole simulation, not each composite resource.
It doesn't include the time spent building functions.

## Examples

Simulate the project in the current directory against the current context:

```shell
crossplane project simulate
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

Print the result as JSON:

```shell
crossplane project simulate -o json
```
