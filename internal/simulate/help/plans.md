`simulate` first computes the cluster diff: it renders each composite resource
and compares the result with the cluster through a server-side dry run, like
`kubectl diff`.

By default `simulate` also runs provider plans: it asks each provider's diff
server to plan the managed resources, so the plan shows changes the cluster
can't see. The diff servers come from `--providers` when set, otherwise from
the project's provider dependencies when simulating a project, otherwise from
the providers installed on the cluster that own the rendered resources, started
on demand. A resource no diff server serves keeps its cluster diff. In `json`
and `yaml` output, `approximate: true` on a plan means it was computed
client-side, without a provider diff, because the provider couldn't diff that
resource. The result's `notes` say when provider plans were skipped or no diff
server served the resources; `-o diff` prints the no-diff-server note to
stderr.

Provider plans are controlled by these flags:

- `--skip-plan` doesn't run provider diff servers and shows only the
  cluster diff. `--providers` and `--session-id` do nothing with it, so
  they print a warning and are ignored.
- `--providers` takes a YAML file or directory of Provider manifests to run as
  diff servers, instead of discovering providers from the cluster. The manifests
  can carry per-provider runtime annotations.
- `--session-id` sets the `simulate.crossplane.io/session` label on diff server
  containers. It defaults to a random per-process ID.
