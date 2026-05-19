The `xpkg install` command installs a package in a Crossplane control plane. It
uses `~/.kube/config` to connect to the control plane; override the path with
the `KUBECONFIG` environment variable.

Specify the package kind, fully-qualified package OCI reference, and optionally
a name for the package inside Crossplane:

```shell
crossplane xpkg install <package-kind> <oci-ref> [<optional-name>]
```

The `<package-kind>` is one of `configuration`, `function`, or `provider`.

> **Important:** The package reference must be fully qualified, including the
> registry, repository, and tag (e.g., registry.example.com/package:v1.0.0).

## Wait for package install

By default the command returns as soon as Crossplane accepts the package. It
does not wait for the download or install to complete. To inspect download or
installation problems, run `kubectl describe <kind>`.

Use `--wait` (`-w`) to make the command wait for the package to become `HEALTHY`
before returning. The command returns an error if the wait time expires before
the package is healthy.

## Require manual package activation

Pass `-m` (`--manual-activation`) to set the package's
`revisionActivationPolicy` to `Manual`, which prevents automatic upgrades of the
package.

## Authenticate to a private registry

To authenticate to a private package registry use `--package-pull-secrets` with
a comma-separated list of Kubernetes Secret names.

> **Important:** The secrets must be in the same namespace as the Crossplane
> pod.

## Customize the number of stored package revisions

By default Crossplane keeps only the active revision and one inactive revision
in the local package cache. Increase the number of stored revisions with `-r`
(`--revision-history-limit`).

## Examples

Wait 1 minute for the package to finish installing before returning:

```shell
crossplane xpkg install provider xpkg.crossplane.io/crossplane-contrib/provider-aws-eks:v0.41.0 --wait=1m
```

Install a Function named function-eg using a custom runtime config:

```shell
crossplane xpkg install function xpkg.crossplane.io/crossplane/function-example:v0.1.4 function-eg \
  --runtime-config=customconfig
```
