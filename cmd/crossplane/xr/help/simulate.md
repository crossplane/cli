The `xr simulate` command previews how applying changed composite resources or
claims would change a cluster. It renders each one with the Composition the
cluster selects for it and the functions installed in the cluster, and shows
the difference from what's in the cluster. It doesn't change the cluster.

`-f` (`--input`) names a file, or a directory of YAML files, of composite
resources or claims to simulate, and is required. Repeat it for more; `-f -`
reads standard input. Every resource in them must be a kind an XRD in the cluster defines as a
composite resource or claim; to simulate a Composition, use
`crossplane composition simulate`, and to simulate a project, use
`crossplane project simulate`. A claim is simulated as its composite resource,
and its result is listed under the claim's name.
Functions come from the cluster. `--cache-dir` sets where provider package
contents are cached.

The command connects to the cluster in your current Kubernetes context, or the
one `--context` names.

The command needs Docker, to run functions, and read access to the cluster's
Crossplane resources. It verifies the rendered resources with server-side
dry-run requests, so it also needs the `create` and `patch` verbs on them. Use
`--as`, `--as-group`, and `--as-uid` to simulate as another user.

