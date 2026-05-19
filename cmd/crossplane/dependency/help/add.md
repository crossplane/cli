The `dependency add` command adds a dependency to a Crossplane Project. The
dependency is added to the project's metadata file and language bindings
(schemas) are generated its CRDs when applicable.

## Dependency types

Projects support three kinds of dependencies:

- Crossplane packages from an OCI registry (xpkgs).
- Arbitrary CRDs fetched from either an HTTP(S) URL or a Git repository.
- Kubernetes core APIs.

An xpkg dependency may be either a runtime dependency (the default) or a
build-time dependency. Runtime dependencies become dependencies of the
Configuration produced by `crossplane project build` or `crossplane project run`
and thus get installed into a cluster when the Configuration is
installed. Build-time dependencies, on the other hand, are used only for schema
generation and do not become Configuration dependencies. Use the `--api-only`
flag to indicate that an xpkg dependency should be build-time only.

Non-xpkg dependencies are always build-time dependencies.

## Examples

Retrieve the latest available semantic version of `provider-aws-eks`, generate
schemas for its CRDs, and add it to the project as a runtime dependency:

```shell
crossplane dependency add xpkg.crossplane.io/crossplane-contrib/provider-aws-eks
```

Retrieve the latest available version greater than `v1.1.0` of
`provider-gcp-storage`, generate schemas for its CRDs, and add it to the project
as a build-time only dependency:

```shell
crossplane dependency add --api-only 'xpkg.crossplane.io/crossplane-contrib/provider-gcp-storage:>v1.1.0'
```

Generate schemas for the core resources from Kubernetes v1.33.0 and add it to
the project as a build-time dependency:

```shell
crossplane dependency add k8s:v1.33.0
```

Generate schemas for a specific CRD from an HTTP URL and add it to the project
as a build-time dependency:

```shell
crossplane dependency add https://raw.githubusercontent.com/cert-manager/cert-manager/refs/heads/master/deploy/crds/cert-manager.io_certificaterequests.yaml
```

Generate schemas for CRDs from a specific subdirectory of a git repository and
add it to the project as a build-time dependency:

```shell
crossplane dependency add https://github.com/kubernetes-sigs/cluster-api \
    --git-ref=release-1.11 --git-path=config/crd/bases
```
