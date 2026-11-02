The `dependency update-cache` command updates the local dependency cache for the
current project. It re-resolves semantic version constraints to specific
versions (fetching newer versions if available), caches all dependencies, and
re-generates language bindings (schemas) for them if needed.

## Project and package metadata files

The command reads dependencies from a project file (`crossplane-project.yaml`)
or from Configuration, Provider, or Function package metadata (`crossplane.yaml`).
When `-f` isn't set, it uses `crossplane-project.yaml` in the current directory if present,
otherwise `crossplane.yaml`.

For package metadata, the xpkg dependencies listed in `spec.dependsOn` are cached.
Schemas are written to the directory given by `--schemas-dir`, relative
to the file (default `schemas`).
For a project file, `--schemas-dir` overrides `paths.schemas`.

Schemas are generated for all supported languages unless `--schema-languages`
lists a subset (`go`, `json`, `kcl`, `python`, `rust`).
For a project file, the flag overrides `spec.schemas.languages`.

Package metadata can't declare Kubernetes core API dependencies.
Use `crossplane schema generate` to generate schemas for them.

## Examples

Update the cache and generate schemas for the dependencies of a Configuration
package, writing schemas to `gen/schemas`:

```shell
crossplane dependency update-cache -f crossplane.yaml --schemas-dir gen/schemas
```

Generate only Python schemas:

```shell
crossplane dependency update-cache -f crossplane.yaml --schema-languages python
```
