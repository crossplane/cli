The `dependency add` command adds a dependency to a Crossplane Project metadata
file. It checks that the dependency can be fetched, and caches packages. It
doesn't generate language bindings (schemas); run `crossplane schema update`
afterwards to generate them for the dependency's CRDs.

## Dependency types

Projects support three kinds of dependencies:

- Crossplane packages from an OCI registry (xpkgs).
- Arbitrary CRDs fetched from either an HTTP(S) URL or a Git repository.
- Kubernetes core APIs.

An xpkg dependency may be either a runtime dependency (the default) or a
build-time dependency. Runtime dependencies become dependencies of the
Configuration produced by `crossplane project build` or `crossplane project run`
and are thus installed into a cluster with the Configuration. Build-time
dependencies have schemas generated but don't become Configuration
dependencies. Use the `--api-only` flag to add a build-time xpkg dependency.

Non-xpkg dependencies are always build-time dependencies.

Git repositories are accessed over HTTPS. Use `--git-token` (or the
`CROSSPLANE_GIT_TOKEN` environment variable) to authenticate, and
`--git-username` (or `CROSSPLANE_GIT_USERNAME`, default `x-access-token`) to set
the username.

## Examples

Retrieve the latest available semantic version of `provider-aws-eks` and add it
to the project as a runtime dependency:

```shell
crossplane dependency add xpkg.crossplane.io/crossplane-contrib/provider-aws-eks
```

Retrieve the latest available version greater than `v1.1.0` of
`provider-gcp-storage` and add it to the project as a build-time only
dependency:

```shell
crossplane dependency add --api-only 'xpkg.crossplane.io/crossplane-contrib/provider-gcp-storage:>v1.1.0'
```

Add the core resources from Kubernetes v1.33.0 to the project as a build-time
dependency:

```shell
crossplane dependency add k8s:v1.33.0
```

Add a specific CRD from an HTTP URL to the project as a build-time dependency:

```shell
crossplane dependency add https://raw.githubusercontent.com/cert-manager/cert-manager/refs/heads/master/deploy/crds/cert-manager.io_certificaterequests.yaml
```

Add CRDs from a specific subdirectory of a git repository to the project as a
build-time dependency:

```shell
crossplane dependency add https://github.com/kubernetes-sigs/cluster-api \
    --git-ref=release-1.11 --git-path=config/crd/bases
```

Add CRDs from a private git repository, authenticating with a token from the
environment:

```shell
export CROSSPLANE_GIT_TOKEN=<token>
crossplane dependency add https://<git-server>/<org>/<repo>.git \
    --git-ref=main --git-path=config/crd/bases
```
