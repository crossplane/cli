The `project push` command pushes the xpkgs produced by `crossplane project
build` to an OCI registry. It pushes both the Configuration package and any
embedded function packages built from the project. The `push` command uses
registry credentials from the local `docker` configuration; pushing to a private
registry may require a prior `docker login`.

By default the command pushes to the repository specified in
`crossplane-project.yaml` and uses a tag generated from the package contents.
Override either with `--repository` and `--tag` (`-t`). To push a specific
package file instead of the project's default output, use `--package-file`.

> **Important:** The repository influences the function names used for embedded
> function references in compositions. You must specify the same repository when
> building and pushing a project.

## Examples

Push the project's packages using the repository and a generated tag:

```shell
crossplane project push
```

Push using an explicit tag:

```shell
crossplane project push --tag=v1.2.3
```

Push to a different repository than the one in the project file:

```shell
crossplane project push --repository=xpkg.crossplane.io/my-org/my-project --tag=v1.2.3
```
