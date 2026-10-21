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
and `crossplane schema update`. Override the cache location with `--cache-dir`
or the `CROSSPLANE_XPKG_CACHE` environment variable.

If the project has no schema lock yet, the build runs a schema update first and
writes the lock. Otherwise the build never changes the project's generated
schemas or the schema lock, and never resolves versions again, so a new release
of a dependency can't change or break it. It checks that the schemas match the
lock, and fails if a source changed, the project's dependencies changed, or a
generated file was modified. Files that are missing are recreated from the exact
versions the lock pins, which works offline when the packages are cached. Run
`crossplane schema update` to accept changes and update the lock, or
`crossplane schema generate` to restore modified files.

Creating the lock or recreating missing schema files may clone git
dependencies. The build authenticates with the same `--git-token` and
`--git-username` flags, or the `CROSSPLANE_GIT_TOKEN` and
`CROSSPLANE_GIT_USERNAME` environment variables, as `dependency add`.

The CLI builds embedded functions onto a runtime base image from a registry, and
caches those image layers on disk under `crossplane/base-images` in your user
cache directory. A build that needs a layer already in the cache reads it
locally rather than downloading it again, so a build takes longer the first time
it needs a given base image. Projects share the cache.

Layer filenames are content digests, so a cached layer never goes stale and the
cache never needs invalidating. Nothing prunes it, though, so it grows as base
images change. Delete the directory to reclaim the space; the next build refills
what it needs.

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
