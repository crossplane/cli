The `project stop` command tears down the local development control plane
created by `crossplane project run`. It removes both the KIND cluster and the
local OCI registry.

When run from a project directory, the `stop` command tears down the control
plane whose name matches the project name. When run outside a project directory,
pass `--control-plane-name` to identify the control plane to tear down. If you
passed `--registry-dir` to `crossplane project run`, pass it to `crossplane
project stop` as well to clean up the registry data.

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
