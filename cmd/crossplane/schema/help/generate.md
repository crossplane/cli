The `schema generate` command generates language bindings (schemas) for a
single source and writes them to a directory. It doesn't read or change any
project or package metadata file.

The source uses the same formats as `crossplane dependency add`:

- A Crossplane package from an OCI registry. Schemas are also generated for
  the package's own dependencies.
- `k8s:<version>` for the Kubernetes core APIs.
- An HTTP(S) URL to CRD files.
- A Git repository URL, with `--git-ref` and optionally `--git-path`.

Use it for schemas a package needs that aren't package dependencies, such as
the Kubernetes core APIs.

## Examples

Generate Python schemas for the Kubernetes v1.33.0 core APIs into `schemas`:

```shell
crossplane schema generate k8s:v1.33.0 --languages python
```

Generate schemas for a provider into `gen/schemas`:

```shell
crossplane schema generate xpkg.crossplane.io/crossplane-contrib/provider-nop:v0.4.0 -o gen/schemas
```
