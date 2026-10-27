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
own. A pinned composite whose revision calls a function the project changes is
still simulated, rendered against the revision it's pinned to. Pass
`--include-pinned` to render every pinned composite against the new Composition
instead.

Some flags apply only to some inputs. `--namespace`, `--name`,
`--include-pinned`, `--analyze-on`, and `--minimize-composition` choose which
composite resources a Composition change reaches, so composite resource files
don't take them. `--max-concurrency` configures the project's function builds,
so only a project file takes it. `--cache-dir` caches function builds and
provider packages, so Composition and composite resource files take it unless
`--skip-plan` is set.

The `-o` flag picks the output format:

- `plan`, the default, prints each change as a field-level plan, grouped under
  the composite resource it belongs to. A changed Composition prints first,
  with whether applying it creates a new Composition revision.
- `json` and `yaml` print the same plan as a structured result, for CI and
  other tools. Each plan carries the objects it compares, as `before` (the
  live object, absent for a create) and `after` (the rendered object, absent
  for a delete), with ignored paths, status and server-managed metadata
  removed and Secret values redacted. A provider-planned resource's `after`
  is the manifest sent to the provider, not the provider's view.
- `diff` prints the line diff `crossplane-diff` prints.

For example, a changed Composition run with `--skip-plan` prints a plan like this:

```
[~] Composition.apiextensions.crossplane.io xnetworks.example.org (update)
    Applying this creates a new CompositionRevision, which 1 composite would adopt.
    spec:
      pipeline[0]:
        input:
          ~ region: "us-west-1" -> "us-east-1"

XR: XNetwork.example.org team-a/prod-network
  [~] VPC.ec2.aws.upbound.io prod-network (update)
      spec:
        forProvider:
          ~ region: "us-west-1" -> "us-east-1"

Plan: 0 to add, 2 to change, 0 to destroy.

Note: Cloud-layer plan skipped (--skip-plan).
```

The `--timeout` flag, 2m by default, bounds the time to simulate one composite
resource: its render and its resources' provider plans. Function and provider
image pulls don't count, but the render image pull counts against the first
composite resource's budget.

By default `simulate` also runs provider plans: it asks each provider's diff
server to plan the managed resources, so the plan shows changes the cluster
can't see. The diff servers come from `--providers` when set, otherwise from
the project's provider dependencies in project mode, otherwise from the
providers installed on the cluster that own the rendered resources, started on
demand. A resource no diff server serves keeps its cluster-layer diff. In `json`
and `yaml` output, `approximate: true` on a plan means it was computed
client-side, without a provider diff, because the provider couldn't diff that
resource. The result's `notes` say when provider plans were skipped or no diff
server served the resources.

Provider plans are controlled by these flags:

- `--skip-plan` doesn't run provider diff servers and shows only the
  cluster-layer diff. It can't be combined with `--providers` or
  `--session-id`.
- `--providers` takes a YAML file or directory of Provider manifests to run as
  diff servers, instead of discovering providers from the cluster.
- `--image-configs` takes a YAML file of ImageConfig manifests that rewrite
  package paths for pulls: the images of the functions the render runs, except
  the project's embedded functions, provider images, and in project mode the
  project's function and provider dependencies. It replaces the project's imageConfigs
  for those pulls rather than adding to them; schema generation images still
  use the project's imageConfigs. Without it, the project's imageConfigs apply when
  `-f` names a project file; in Composition and XR modes `simulate` falls back,
  best effort, to the project file in the current directory.
- `--session-id` sets the `simulate.crossplane.io/session` label on diff server
  containers. It defaults to a random per-process ID.

The render takes the same render engine flags as `crossplane render`:
`--crossplane-version` and `--crossplane-image`, v2.3.4 or later, or
`--crossplane-binary`, pick what renders, and `--crossplane-docker-network`
names the Docker network the render and function containers join. Without
`--crossplane-docker-network`, the `CROSSPLANE_DIFF_DOCKER_NETWORK` environment
variable names it.

The command accepts the same diff-tuning flags as `crossplane-diff`:

- `--ignore-paths` leaves paths out of the diff.
- `--function-credentials` passes credentials to the functions the render runs.
- `--max-nested-depth` limits nested composite resource recursion.
- `--dry-run-on` picks which resources the server-side dry run verifies:
  `existing` or `all`, the default.
- `--analyze-on` sets the smallest Composition change that triggers impact
  analysis: `spec-change`, `any-change`, or `always`.
- `--minimize-composition` collapses each changed Composition to one line in
  `-o diff` output.
- `--eventual-state` shows the state after all reconciliation cycles complete.
- `--max-recv-message-size` raises the function gRPC message limit, in MB.
- `--no-color` and `--compact` change how `-o diff` prints.

The command exits with one of these statuses, so a CI pipeline can gate on
the result:

- 0: the simulation succeeded and found no changes.
- 1: the simulation failed, including when the -f inputs can't be read or
  can't be simulated together. If a provider plan fails, so does its whole
  composite resource, which then reports an error entry and none of its
  resources' plans.
- 2: only schema validation failed.
- 3: the simulation succeeded and found changes, including drift a provider
  plan found that the cluster diff didn't.
- 80: the flags were invalid, so nothing was simulated.
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

`--name` takes `[namespace/]name` and is repeatable or comma-separated. Simulate
two composite resources in different namespaces:

```shell
crossplane project simulate --name=team-a/my-database --name=team-b/my-database
```

Print the plan as JSON:

```shell
crossplane project simulate -o json
```

Print the line diff `crossplane-diff` prints:

```shell
crossplane project simulate -o diff
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
