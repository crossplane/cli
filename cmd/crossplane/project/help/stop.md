The `project stop` command tears down the local development control plane
previously created by `crossplane project run`. The KIND cluster and the local
OCI registry are both removed.

When run from a project directory the control plane name is derived from the
project name. When run outside a project directory, pass `--control-plane-name`
to identify the control plane to tear down. Pass `--registry-dir` to point at
the local registry directory used by `project run` if it was overridden there.

## Examples

Tear down the development control plane for the project in the current
directory:

```shell
crossplane project stop
```

Tear down a specific local dev control plane by name:

```shell
crossplane project stop --control-plane-name=my-dev-cp
```
