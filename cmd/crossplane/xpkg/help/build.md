The `xpkg build` command builds a package file from a local directory of
files. The CLI combines a directory of YAML files and packages them as an [OCI
container image](https://opencontainers.org/), applying the annotations and
values required by the [Crossplane XPKG
specification](https://github.com/crossplane/crossplane/blob/main/contributing/specifications/xpkg.md).

`crossplane xpkg build` supports building Configuration, Function, and Provider
package types.

The command recursively looks in `--package-root` for files ending in `.yml` or
`.yaml` and attempts to combine them into a package. All YAML files must be
valid Kubernetes manifests with `apiVersion`, `kind`, `metadata`, and `spec`
fields.

## Ignore files

Use `--ignore` to provide a comma-separated list of files and directories
(relative to `--package-root`) to skip. Wildcards are supported. Directories
themselves cannot be excluded.

```shell
crossplane xpkg build --ignore="./test/*,kind-config.yaml"
```

## Set the package name

By default the package is named from a combination of `metadata.name` and a hash
of the package contents, and written to `--package-root`. Override the location
and filename with `--package-file` (`-o`):

```shell
crossplane xpkg build -o /home/crossplane/example.xpkg
```

## Include examples

Include YAML files demonstrating how to use the package with `--examples-root`
(`-e`). Defaults to `./examples`.

## Include a runtime image

Function and Provider packages can embed a controller container image so that
the package can also be used to run the controller. Embedding is supported for
those package kinds only.

> **Note:** Images referenced with `--embed-runtime-image` must be in the local
> Docker cache. Use `docker pull` to download a missing image.

Use `--embed-runtime-image-tarball` to embed a local OCI image tarball instead
of an image from the Docker cache.

## Examples

Build a package from the files in the 'package' directory:

```shell
crossplane xpkg build --package-root=package/
```

Build a Provider package that embeds the controller OCI image so the package can
also run the provider.

```shell
crossplane xpkg build --embed-runtime-image=cc873e13cdc1
```
