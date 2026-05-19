The `project build` command builds a Crossplane Project into a set of xpkgs.  It
builds each embedded function in the project and a Configuration package that
ties everything together. A special `.xpkg` file containing all the built
packages is written to the project's output directory (`_output/` by
default). This file can be consumed by the `project push` command to push the
packages to an OCI registry.

The repository for the built Configuration is taken from `spec.repository` in
`crossplane-project.yaml`. Override it for a single build with `--repository`.

> **Important:** The repository is used to construct the function names used for
> embedded function references in compositions. The same repository must be
> specified when building and pushing a project.

The build reuses the dependency cache populated by `crossplane dependency add`
and `crossplane dependency update-cache`. Override the cache location with
`--cache-dir` or the `CROSSPLANE_XPKG_CACHE` environment variable.

## Examples

Build the project in the current directory:

```shell
crossplane project build
```

Build the project, overriding the repository:

```shell
crossplane project build --repository=xpkg.crossplane.io/my-org/my-project
```

Build the project into a custom output directory:

```shell
crossplane project build -o ./packages
```
