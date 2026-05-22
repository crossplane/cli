The `project run` command builds a Crossplane Project and runs it on a local
development control plane for testing.

This command:

- Builds all embedded functions defined in the project.
- Creates (or reuses) a local development control plane running in a KIND
  cluster, with a local OCI registry for packages.
- Loads the project's packages into the local OCI registry.
- Installs the project's Configuration on the control plane.
- Updates kubeconfig so `kubectl` points at the development control plane.

By default, `run` names the control plane after the project. Use
`--control-plane-name` to choose a different name, which is useful when running
multiple projects side-by-side.

You can use a Crossplane version other than the latest stable version by
specifying the `--crossplane-version` flag.

You can provide resources to apply around the project install:

- `--init-resources` applies one or more files *before* installing the
  Configuration (useful for things like `ImageConfig`).
- `--extra-resources` applies one or more files *after* installing the
  Configuration and its dependencies (useful for things like `ProviderConfig`).

## Examples

Build and run the project on the default local development control plane:

```shell
crossplane project run
```

Run on a control plane with a specific name (created if it doesn't exist):

```shell
crossplane project run --control-plane-name=my-dev-ctp
```

Pin the Crossplane version installed in the dev control plane:

```shell
crossplane project run --crossplane-version=v2.2.1
```

Apply `imageconfig.yaml` before installing the Configuration, and
`providerconfig.yaml` after:

```shell
crossplane project run --init-resources=imageconfig.yaml --extra-resources=providerconfig.yaml
```
