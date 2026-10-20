The `project simulate` command previews how applying a project's Compositions,
or changed Composition or composite resource files, would change the composite
resources in a cluster. It renders each affected composite resource and shows
the difference from what's in the cluster. It doesn't change the cluster.

The `-f` flag picks what to simulate. Repeat it to pass more than one file or
directory, or pass `-` to read from standard input:

- A project file, the default, simulates the project. The command builds the
  project's embedded functions and loads them into the local Docker daemon,
  resolves its function dependencies, and collects the Compositions under its
  APIs path. It renders each composite resource that uses one of those
  Compositions with the project's version of the Composition, using the
  project's functions in place of the cluster's functions of the same name.
- Composition files simulate those Compositions without a project. The command
  renders each composite resource in the cluster that uses one of them, with
  the functions installed in the cluster.
- Composite resource or claim files simulate those resources. The command
  renders each one with the Composition the cluster selects for it and the
  functions installed in the cluster.

A project file must be the only `-f` input, and one run can't mix Compositions
with composite resources. Any other resource, such as package metadata, a
Provider or a ConfigMap, is rejected: each one must be a kind an XRD in the
cluster defines as a composite resource or claim.

The command connects to the cluster in your current Kubernetes context, or the
one `--context` names.

The command needs Docker, to build and run functions, and read access to the
cluster's Crossplane resources. It verifies the rendered resources with
server-side dry-run requests, so it also needs the `create` and `patch` verbs on
them. Use `--as`, `--as-group`, and `--as-uid` to simulate as another user.

By default, `simulate` skips composite resources with a `Manual` composition
update policy, because they don't adopt a new Composition revision on their
own. Pass `--include-pinned` to simulate them too.

Some flags apply only to some inputs. `--namespace`, `--name`,
`--include-pinned`, `--analyze-on`, and `--minimize-composition` choose which
composite resources a Composition change reaches, so composite resource files
don't take them. `--max-concurrency` and `--cache-dir` configure the project's
function builds, so only a project file takes them.

The `--timeout` flag bounds the whole simulation, not each composite resource.
It doesn't include the time spent building functions.

The command accepts the same diff-tuning flags as `crossplane-diff`:

- `--ignore-paths` leaves paths out of the diff.
- `--function-credentials` and `--function-registry-override` configure the
  functions the render runs.
- `--max-nested-depth` limits nested composite resource recursion.
- `--dry-run-on` picks which resources the server-side dry run verifies:
  `existing` or `all`, the default.
- `--analyze-on` sets the smallest Composition change that triggers impact
  analysis: `spec-change`, `any-change`, or `always`.
- `--minimize-composition` collapses each changed Composition to one line in
  the diff output.
- `--eventual-state` shows the state after all reconciliation cycles complete.
- `--max-recv-message-size` raises the function gRPC message limit, in MB.
- `--no-color` and `--compact` change how the diff prints.
- `--output-version` picks the schema version of JSON and YAML output: `v1`,
  the default, or `v2`.

The command exits with one of these statuses, so a CI pipeline can gate on
the result:

- 0: the simulation succeeded and found no changes.
- 1: the simulation failed.
- 2: only schema validation failed.
- 3: the simulation succeeded and found changes.
- 130: the simulation was interrupted.

When more than one applies, the lowest of 1, 2 and 3 wins.

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

Simulate a changed Composition without a project:

```shell
crossplane project simulate -f composition.yaml
```

Simulate changed composite resources:

```shell
crossplane project simulate -f xr.yaml -f claim.yaml
```

Simulate composite resources read from standard input:

```shell
cat xr.yaml | crossplane project simulate -f -
```
