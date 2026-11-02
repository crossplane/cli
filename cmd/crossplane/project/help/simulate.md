The `project simulate` command previews how applying a project's Compositions
would change the composite resources in a cluster. It renders each affected
composite resource and shows the difference from what's in the cluster. It
doesn't change the cluster. To simulate Composition files against a cluster
without a project, use `crossplane composition simulate`.

The `-f` flag names the project file, `crossplane-project.yaml` by default. The
command builds the project's embedded functions and loads them into the local
Docker daemon, resolves its function dependencies, and collects the
Compositions under its APIs path. It renders each composite resource that uses
one of those Compositions with the project's version of the Composition, using
the project's functions in place of the cluster's functions of the same name.
`--max-concurrency` caps the concurrent function builds, and `--cache-dir` sets
where package contents are cached.

The command connects to the cluster in your current Kubernetes context, or the
one `--context` names.

The command needs Docker, to build and run functions, and read access to the
cluster's Crossplane resources. It verifies the rendered resources with
server-side dry-run requests, so it also needs the `create` and `patch` verbs on
them. Use `--as`, `--as-group`, and `--as-uid` to simulate as another user.

For example, a run with `--skip-plan` whose project changes a Composition
prints a plan like this:

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

Note: Provider plan skipped (--skip-plan).
```

