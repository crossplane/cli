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

If your environment requires private CA trust for image pulls, you can mount a
host directory that contains containerd registry certs using
`--containerd-certs-dir`. The directory is mounted at `/etc/containerd/certs.d`
in the control-plane node.

The certificates must already be present on your host before running this
command, and `--containerd-certs-dir` must point to a directory tree that
follows the containerd `certs.d` layout expected by the Kind node.

`hosts.toml` is optional. Use it when you need custom registry host behavior
(for example mirrors or explicit endpoint/capability settings). For CA-only
trust, `ca.crt` is often sufficient.

See containerd registry host configuration documentation:
https://github.com/containerd/containerd/blob/main/docs/hosts.md

Example host directory structure:

```text
/certs/containerd-certs/
  _default/
    hosts.toml (optional)
    ca.crt
  registry-1.docker.io/
    hosts.toml (optional)
    ca.crt
  ghcr.io/
    hosts.toml (optional)
    ca.crt
```

Use `_default` for fallback trust/rules that should apply when a registry-
specific directory is not present.

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

Run with private CA trust material mounted into the control-plane node:

```shell
crossplane project run --containerd-certs-dir=/certs/containerd-certs
```
