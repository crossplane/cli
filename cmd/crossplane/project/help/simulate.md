The `project simulate` command previews how applying a project's Compositions
would change the composite resources in a cluster. It renders each affected
composite resource and shows the difference from what's in the cluster. It
doesn't change the cluster. To simulate Composition files against a cluster
without a project, use `crossplane composition simulate`; to simulate composite
resource or claim files, use `crossplane xr simulate`; to simulate managed
resources directly, use `crossplane resource simulate`.

> **Important:** Although it doesn't change the cluster, this command needs
> `create` and `patch` permission on the resources it renders. It checks each
> rendered resource with a server-side dry-run request, and Kubernetes
> authorizes a dry-run request like a real one. It also needs read access to
> the cluster's Crossplane resources, and Docker to build and run functions.
> Use `--as`, `--as-group`, and `--as-uid` to simulate as another user.

The command connects to the cluster in your current Kubernetes context, or the
one `--context` names.

The `-f` flag names the project file, `crossplane-project.yaml` by default. The
command builds the project's embedded functions and loads them into the local
Docker daemon, resolves its function dependencies, and collects the
Compositions under its APIs path. It renders each composite resource that uses
one of those Compositions with the project's version of the Composition, using
the project's functions in place of the cluster's functions of the same name.
`--max-concurrency` caps the concurrent function builds, and `--cache-dir` sets
where package contents are cached.

`--resource-file` adds composite resource or claim files to render against
the project's Compositions, for example to preview what a new Composition
would create. It takes a file or directory, a comma-separated list of paths,
and can be repeated; `-` reads standard input. Each input is matched to a
project Composition the way Crossplane selects one: its XRD's enforced
Composition, then its `compositionRef`, then its `compositionSelector`, then
its XRD's default Composition, then the only Composition for its type. Its
XRD must be installed in the cluster. `--namespace` sets the namespace of an
input that has none but doesn't filter the inputs; without it, such an input
gets the kubeconfig context's namespace; a cluster-scoped input has none. No
two inputs may share an API group, kind, namespace and name. An input may set
`metadata.generateName` instead of `metadata.name`; it renders as a new
composite resource. The inputs render alongside the composite resources the
run discovers or `--name` selects; a named input with the same API group,
kind, namespace and name as a composite resource in the cluster diffs against
it.

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

