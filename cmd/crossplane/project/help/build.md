The `project build` command builds a Crossplane Project into a set of xpkgs. It
builds each embedded function in the project and a Configuration package that
ties everything together. The output of the build is a special `.xpkg` file
containing all the built packages, placed in the project's output directory
(`_output/` by default). The `project push` command can consume packages from
the output file and push them to an OCI registry.

The `build` command constructs the repository for the built Configuration from
`spec.repository` in `crossplane-project.yaml`. Override it for a single build
with `--repository`.

> **Important:** The repository influences the function names used for embedded
> function references in compositions. You must specify the same repository when
> building and pushing a project.

The build reuses the dependency cache populated by `crossplane dependency add`
and `crossplane dependency update-cache`. Override the cache location with
`--cache-dir` or the `CROSSPLANE_XPKG_CACHE` environment variable.

Embedded functions are built onto a runtime base image pulled from a registry.
Those base image layers are cached on disk, under `crossplane/base-images` in
your user cache directory, so that repeat builds read them locally instead of
downloading them again. The first build of a project fills the cache and is
correspondingly slower than the ones after it.

Cached layers are addressed by their content digest, so a cached layer is never
stale and the cache never needs invalidating. It does grow, though: nothing
prunes it today, so each new base image version adds to it rather than
replacing what came before. Expect tens of megabytes per base image, per
architecture. Delete the directory to reclaim the space; the next build
refills what it needs.

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
