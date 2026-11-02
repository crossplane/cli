The `composition simulate` command previews how applying changed Compositions
would change the composite resources in a cluster. It renders each composite
resource that uses one of the Compositions with the changed version, and shows
the difference from what's in the cluster. It doesn't change the cluster.

> **Important:** Although it doesn't change the cluster, this command needs
> `create` and `patch` permission on the resources it renders. It checks each
> rendered resource with a server-side dry-run request, and Kubernetes
> authorizes a dry-run request like a real one. It also needs read access to
> the cluster's Crossplane resources, and Docker to run functions. Use `--as`,
> `--as-group`, and `--as-uid` to simulate as another user.

The command connects to the cluster in your current Kubernetes context, or the
one `--context` names.

`-f` (`--input`) names a file, or a directory of YAML files, of Compositions to
simulate, and is required. Give a comma-separated list, or repeat it, for more; `-f -` reads standard input.
Every resource in them must be a Composition; to simulate a project, use
`crossplane project simulate`, and to simulate composite resources or claims,
use `crossplane xr simulate`.
Functions come from the cluster. `--cache-dir` sets where provider package
contents are cached.

