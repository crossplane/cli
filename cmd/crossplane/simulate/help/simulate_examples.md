## Examples

Preview what applying resources.yaml would do:

```shell
crossplane resource simulate -f resources.yaml
```

Preview against specific provider versions instead of the installed ones,
from a checked-in file of Provider manifests:

```shell
crossplane resource simulate -f resources.yaml --providers providers.yaml
```

Keep diff servers running so later runs start faster, via a provider manifest
annotated with `simulate.crossplane.io/runtime-docker-cleanup: Orphan`:

```shell
crossplane resource simulate -f resources.yaml --providers providers.yaml
```

Iterate on a provider's diff server from source, with no package to build or
push: run `go run ./cmd/provider internal diff-server` in the provider
repository, then simulate with a Provider manifest annotated with
`simulate.crossplane.io/runtime: Development` and
`simulate.crossplane.io/runtime-development-api-groups: ec2.aws.upbound.io,ec2.aws.m.upbound.io`:

```shell
crossplane resource simulate -f resources.yaml --providers providers.yaml
```

Emit machine-readable results for CI gating. Each plan carries the live
object as `before` (absent for a create) and the desired manifest as `after`,
both without status, server-managed metadata and ignored paths, and with
Secret values redacted:

```shell
crossplane resource simulate -f resources.yaml -o json
```
