The `clean-cache` command removes all cached package images from the local cache
directory and removes generated schemas. This can help free up disk space, force
re-generation of schemas, or resolve issues with corrupted cache entries.

Like `update-cache`, the command accepts a project file or a `Configuration`
package metadata file (`crossplane.yaml`).
Use `--schemas-dir` to select the schemas directory to remove when it differs from the default.
