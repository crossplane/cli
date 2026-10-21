The `dependency update-cache` command updates the local dependency cache for the
current project. It re-resolves semantic version constraints to specific
versions (fetching newer versions if available) and fetches all dependencies:
xpkg packages and their transitive dependencies, git repositories, HTTP(S)
sources, and Kubernetes API definitions. xpkg packages are stored in the package
cache, which is shared by all projects.

Git repositories are accessed over HTTPS. Use `--git-token` (or the
`CROSSPLANE_GIT_TOKEN` environment variable) to authenticate, and
`--git-username` (or `CROSSPLANE_GIT_USERNAME`, default `x-access-token`) to set
the username.

The command doesn't generate schemas. Run `crossplane schema update` to generate
schemas for the cached packages.
