This command previews what applying one or more managed resources would do,
without applying anything: which resources it would create, which fields it
would change, and, most importantly, which changes the provider could only
apply by destroying and recreating the external resource.

For each input resource the CLI fetches the live managed resource from the
cluster, if one exists, and asks the provider itself for the verdict: it runs
the provider's package image locally as a diff server (a stateless,
credential-free gRPC service upjet-based providers serve) and sends it the
desired and live state. The provider computes its own diff, so the preview
includes replacement detection, values known only after apply, and redacted
sensitive values. A resource with no live counterpart plans as a create.

Which providers run as diff servers comes from the current kubeconfig context
by default: the CLI follows each resource's kind to the provider that
installed it on the cluster, so the preview matches the provider versions
actually serving those resources. --providers overrides that discovery with
a file of Provider manifests (like render takes Function manifests), for
example to preview how a provider version that isn't installed yet would
treat existing resources. The manifests also carry per-provider runtime
annotations (`simulate.crossplane.io/runtime-docker-publish-address` and
`simulate.crossplane.io/runtime-docker-target`) for Docker setups where the
published port isn't reachable on the address the daemon reports.

Not every provider serves diffs. A provider package declares the capability
in its metadata, and a resource whose provider serves no diff server (or
whose diff server can't compute a diff for it) still appears in the preview
with an approximate client-side comparison, marked [?], rather than failing
the run or dropping out of the preview.

Nothing simulate does writes to the cluster or the cloud: it reads the
cluster with your kubeconfig and RBAC, and the diff servers run locally with
no credentials and no cluster access.

Diff server startup takes seconds per provider image. In an
edit-simulate-edit loop, annotate a provider manifest with
`simulate.crossplane.io/runtime-docker-cleanup: Orphan` (like render's
per-function cleanup annotation) to leave its container running so the next
run reuses it. The containers have names like `crossplane-diff-server-DIGEST`
and carry `simulate.crossplane.io/managed-by` and session labels; any later
run without the annotation cleans them up, or remove them with docker rm.

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

Emit machine-readable results for CI gating:

```shell
crossplane resource simulate -f resources.yaml -o json
```
