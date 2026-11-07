The `xr simulate` command previews how applying changed composite resources or
claims would change a cluster. It renders each one with the Composition the
cluster selects for it and the functions installed in the cluster, and shows
the difference from what's in the cluster. It doesn't change the cluster.

> **Important:** Although it doesn't change the cluster, this command needs
> `create` and `patch` permission on the resources it renders. It checks each
> rendered resource with a server-side dry-run request, and Kubernetes
> authorizes a dry-run request like a real one. It also needs read access to
> the cluster's Crossplane resources, and Docker to run functions. Use `--as`,
> `--as-group`, and `--as-uid` to simulate as another user.

The command connects to the cluster in your current Kubernetes context, or the
one `--context` names.

`-f` (`--input`) names a file, or a directory of YAML files, of composite
resources or claims to simulate, and is required. Give a comma-separated list,
or repeat it, for more; `-f -` reads standard input. Every resource in them
must be a kind an XRD in the cluster defines as a composite resource or claim;
to simulate a Composition, use `crossplane composition simulate`, to simulate a
project, use `crossplane project simulate`, and to simulate managed resources
directly, use `crossplane resource simulate`. A claim is simulated as its
composite resource, and its result is listed under the claim's name.
Functions come from the cluster. `--cache-dir` sets where provider package
contents are cached.

