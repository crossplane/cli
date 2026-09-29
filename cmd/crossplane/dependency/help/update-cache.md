The `dependency update-cache` command updates the local dependency cache for the
current project. It re-resolves semantic version constraints to specific
versions (fetching newer versions if available), caches all dependencies, and
re-generates language bindings (schemas) for them if needed.

## Project and package metadata files

The command reads dependencies from a project file (`crossplane-project.yaml`)
or from Configuration package metadata (`crossplane.yaml`).
When `-f` is not set, it uses `crossplane-project.yaml` in the current directory if present,
otherwise `crossplane.yaml`.

For package metadata, the xpkg dependencies listed in `spec.dependsOn` are cached.
Schemas are written to the directory given by `--schemas-dir`, relative
to the file (default `schemas`).
For a project file, `--schemas-dir` overrides `paths.schemas`.

## Examples

Update the cache and generate schemas for the dependencies of a Configuration
package, writing schemas to `gen/schemas`:

```shell
crossplane dependency update-cache -f crossplane.yaml --schemas-dir gen/schemas
```
