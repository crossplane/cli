The `dependency update-cache` command updates the local dependency cache for the
current project. It caches all dependencies listed in the
`crossplane-project.yaml` file and re-generates language bindings (schemas) for
them if needed. Any dependency whose version is expressed as a semantic version
constraint will have the constraint re-resolved to a specific version (i.e.,
schemas will be updated if a newer version is available).
