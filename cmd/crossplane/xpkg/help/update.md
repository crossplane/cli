The `xpkg update` command updates a package in a Crossplane control plane. It
uses `~/.kube/config` to connect to the control plane; override the path with
the `KUBECONFIG` environment variable.

Specify the package kind, a new fully-qualified package OCI reference, and
optionally the name of the package already installed in Crossplane:

```shell
crossplane xpkg update <package-kind> <oci-ref> [<optional-name>]
```

> **Important:** The package reference must be fully qualified, including the
> registry, repository, and tag (e.g., registry.example.com/package:v1.0.0).

## Examples

Update the Function named function-eg to a new version:

```shell
crossplane xpkg update function xpkg.crossplane.io/crossplane/function-example:v0.1.5 function-eg
```

Update to the latest patch version of a Provider:

```shell
crossplane xpkg update provider xpkg.crossplane.io/crossplane-contrib/provider-aws-s3:v2.0.0
```
