The `xpkg push` command pushes a Crossplane package file to any OCI registry. A
package's OCI tag must be a semantic version. Credentials for the registry are
retrieved from `docker` configuration; pushing to a private registry may require
a prior `docker login`.

By default the command looks in the current directory for a single `.xpkg` file
to push. To push multiple files (e.g. a multi-platform package) or a specific
`.xpkg` file, use `-f` (`--package-files`).

> **Important:** The destination must be fully qualified, including the
> registry, repository, and tag (e.g., registry.example.com/package:v1.0.0).

## Examples

Push a multi-platform package:

```shell
crossplane xpkg push -f function-amd64.xpkg,function-arm64.xpkg \
  xpkg.crossplane.io/crossplane/function-example:v1.0.0
```

Push the single xpkg file in the current directory:

```shell
crossplane xpkg push xpkg.crossplane.io/crossplane/function-example:v1.0.0
```

Push to Docker Hub:

```shell
crossplane xpkg push docker.io/crossplane/function-example:v1.0.0
```
