The `xpkg get-crds` command downloads CRDs from Crossplane package
dependencies (providers, functions, configurations) and writes them as YAML
files to the specified output directory. With `--json-schema`, it extracts the
OpenAPI v3 schemas from CRDs and writes them as JSON Schema files suitable for
use with YAML language servers.

By default, the command organizes files by API group and version (for example,
`<group>/<version>/<kind>.{yaml|json}`). Use `--flat` to write all files
directly to the output directory without subfolders.

It accepts the same extension sources as the `validate` command:
`crossplane.yaml` files, directories containing package manifests, or
Provider/Function/Configuration resources.

## Examples

- Download CRDs organized by group:

    ```shell
    crossplane xpkg get-crds crossplane.yaml --output-dir ./crds
    ```

- Download CRDs as flat files:

    ```shell
    crossplane xpkg get-crds crossplane.yaml --output-dir ./crds --flat
    ```

- Download JSON Schemas for YAML language server:

```shell
crossplane xpkg get-crds crossplane.yaml --output-dir ./schemas --json-schema
```

- Download CRDs from multiple sources:

    ```shell
    crossplane xpkg get-crds crossplane.yaml,providers/ --output-dir ./crds
    ```

- Force re-download of cached schemas:

    ```shell
    crossplane xpkg get-crds crossplane.yaml --output-dir ./crds --clean-cache
    ```
