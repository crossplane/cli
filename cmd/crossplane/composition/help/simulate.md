The `composition simulate` command previews how applying changed Compositions
would change the composite resources in a cluster. It renders each composite
resource that uses one of the Compositions with the changed version, and shows
the difference from what's in the cluster. It doesn't change the cluster.

`-f` (`--input`) names a file, or a directory of YAML files, of Compositions to
simulate, and is required. Repeat it for more; `-f -` reads standard input.
Every resource in them must be a Composition; to simulate a project, use
`crossplane project simulate`.
Functions come from the cluster. `--cache-dir` sets where provider package
contents are cached.

The command connects to the cluster in your current Kubernetes context, or the
one `--context` names.

The command needs Docker, to run functions, and read access to the cluster's
Crossplane resources. It verifies the rendered resources with server-side
dry-run requests, so it also needs the `create` and `patch` verbs on them. Use
`--as`, `--as-group`, and `--as-uid` to simulate as another user.

