# Simulating Changes with the Crossplane CLI

* Owner: Christopher Haar (@haarchri)
* Reviewers: Jonathan Ogilvie (@jcogilvie), Theo Chatzimichos
  (@tampakrap), Adam Wolfe-Gordon (@adamwg)
* Status: Draft

## Background

The most requested Crossplane capability that does not exist is `terraform
plan`: show me what a change would do before I apply it. The change might be
an edited managed resource (MR), a new composite resource (XR), an updated
Composition, a changed embedded function, or a provider upgrade. The question
is always the same: *if I apply this, what changes, and is any of it
destructive?*

That question has two layers, and today's tooling answers at most one.

The first layer is **what changes in the cluster**: which composed resources a
change adds, modifies, or removes. `crossplane composition render` answers "what
does this composition produce", and since the CLI moved to the high-fidelity
render engine ([one-pager-render-engine]) it produces exactly what the real
reconciler would. But render alone is not a preview: it does not know what is on
the cluster now, so it cannot tell you what would *change*.
[crossplane-diff][crossplane-diff] closes that gap. It renders XRs through the
same engine the CLI uses, resolves the extra resources functions require by
iterating renders against the live cluster, dry-run-applies the results
server-side to capture defaulting and webhook mutation, and diffs against live
state, including composition impact analysis (`crossplane-diff comp`: find every
XR using a changed Composition and diff them all). It is good, proven work. It
is also a separate binary in crossplane-contrib, with its own release train and
CI, that most Crossplane users never discover.

The second layer is **what changes in the cloud**. A cluster-layer diff shows
that `spec.forProvider.engineVersion` changed; it cannot show that Terraform
could only make that change by *destroying and recreating the database*. In
Crossplane that case is worse than it sounds: providers never destroy an
external resource to update it, so applying such a change does not recreate
anything, it wedges. The provider refuses the update, the MR sits unsynced, and
a human has to intervene. Replacement, computed values, provider defaults, and
custom diff logic are provider knowledge. A companion one-pager in
crossplane/upjet proposes exposing that knowledge: a `PlanService` gRPC protocol
that upjet providers serve from an `internal plan-server` subcommand, computing
the provider's own Terraform diff from a desired resource and its live state,
statelessly, with no cloud credentials and no cluster access. What that proposal
deliberately leaves to the client is orchestration: something has to figure out
which providers a change involves, run their plan servers, route resources to
them, and render the results.

The CLI is that client. It already does exactly this dance for functions:
`crossplane composition render` pulls function packages, runs them as local Docker
containers bound to ephemeral localhost ports, and drives them over gRPC.
And with the project workflow now in the CLI (`crossplane project
init|build|run`), the CLI knows a project's provider dependencies, so it
knows which plan servers to run without being told.

## Goals

- `crossplane project simulate`: preview what a project change (Composition,
  embedded function, or XR) would do to a live control plane and to the
  cloud behind it, end to end.
- `crossplane resource simulate`: preview what applying one or more managed
  resources would do, directly.
- Preview by rendering, not guessing: re-render each XR with the local
  changes, feeding in its observed composed resources and the required
  resources its functions consult, then diff the result against live state.
  crossplane-diff already does this well, so build on it rather than
  reimplement it, and work with its maintainers toward one tool in the CLI
  instead of two side by side.
- Degrade gracefully: a resource whose provider serves no plan (a native
  provider, or an upjet provider that predates the plan service) still
  appears in the preview with a client-side diff, clearly marked as
  approximate, rather than failing the run or silently dropping out.

It is not a goal to refresh cloud state at plan time (the plan protocol diffs
against the provider's last observation; see the upjet one-pager), to define
how non-upjet providers compute plans (the protocol is provider-agnostic;
implementations can follow), or to preview emergent runtime behavior across
many controllers (that remains the domain of control-plane-level simulation).

## Proposal

Add `simulate` as a verb in the CLI's noun-first command tree, in two
places, sharing one engine.

### `crossplane resource simulate`

The direct path: given YAML for one or more MRs, show what applying them
would do.

```console
$ crossplane resource simulate -f resources.yaml
[~] Key.kms.aws.upbound.io my-key (update)
    spec:
      forProvider:
        ~ deletionWindowInDays: "10" -> "7"

[-/+] Instance.rds.aws.upbound.io prod-db (replace)
    spec:
      forProvider:
        ~ engineVersion: "14.7" -> "15.4"  # forces replacement

Plan: 0 to add, 1 to change, 0 to destroy, 1 requires replacement.

Warning: 1 resource has fields that force replacement: engineVersion.
Crossplane providers never destroy and recreate to apply a change.
Applying this leaves the resource unsynced until it is replaced manually.
```

The summary deliberately deviates from Terraform's arithmetic. Terraform
books a replace as one add plus one destroy, because that is what it will do.
A Crossplane provider will do neither, so replaces get their own count
instead of inflating actions that will not happen.

For each input resource the CLI fetches the live MR from the cluster if one
exists (its `status.atProvider` and external-name annotation are the prior
state), sends desired and live to the routed plan server, and renders the
response. A resource with no live counterpart plans as a create "plan
before apply".

### Previewing a Provider Upgrade

Provider upgrades fall out of the same command with no new machinery,
because the plan server *is* the provider version being asked about. To
preview an upgrade, run the new version's image as the plan server and
hand it the fleet:

```console
$ crossplane resource simulate --all --provider-images xpkg.example.org/provider-aws-s3:v2.1.0
Found 214 managed resources in groups served by provider-aws-s3.

[-/+] Bucket.s3.aws.upbound.io legacy-logs (replace)
      ...

Plan: 0 to add, 3 to change, 0 to destroy, 1 requires replacement.
210 resources are unchanged under the new provider version.
```

With `--all` the CLI enumerates instead of reading files: it asks the plan
server which API groups it serves (`GetInfo` again), lists every managed
resource of those kinds on the cluster, and sends each one with its own
spec as desired state and its own live resource as prior state. The
question each plan answers is "does the new provider version consider this
resource up to date, exactly as it stands?". Anything that is not a no-op
is a finding: a changed default, a schema migration, a new replacement
rule, a validation the next reconcile would trip over. Replaces and new
error diagnostics are the highest signal, because those are the resources
that wedge right after the upgrade.

Because the plan server never refreshes, previewing the whole fleet costs
zero cloud API calls and needs no cloud credentials: the CLI reads the
cluster with the user's kubeconfig and everything else happens locally.
That makes "plan the fleet before every provider bump" cheap enough to run
in CI on every renovate PR that touches a provider version.

### `crossplane project simulate`

The end-to-end path, for the project workflow the CLI now owns:

```console
$ crossplane project simulate
Found 2 compositions in project.
Found 5 XRs on the cluster using them.

XR: XNetwork.example.org prod-network
  [~] SecurityGroupRule.ec2.aws.upbound.io prod-network-ssh (update)
      ...
Plan: 1 to add, 3 to change, 0 to destroy.
```

The flow:

1. **Discover.** Parse the project file, find the Compositions under the
   project's API paths, and list the XRs on the target cluster whose type
   each Composition composes. `--namespace` and `--name` scope the run.
2. **Prepare.** Build the project's embedded functions locally, exactly as
   `project run` does, so the simulation reflects the code in the working
   tree. Resolve the project's provider dependencies and start their plan
   servers (below).
3. **Render.** For each XR, fetch its current composed resources from the
   cluster and re-render it with the *local* Composition and functions,
   feeding the observed resources in, the same high-fidelity render path
   render and crossplane-diff already share. Functions that require extra
   resources get them the way crossplane-diff resolves them today:
   iteratively, fetching what the pipeline requests from the live cluster
   and re-rendering until requirements stabilize, with an iteration cap.
   (When Crossplane records required-resource references on XRs,
   [crossplane/crossplane#7351][issue-7351], discovery collapses to a
   single read; the loop is the interim.)
4. **Diff the cluster layer.** Compare rendered against live composed
   resources with crossplane-diff's calculator, including its server-side
   dry-run so defaulting does not masquerade as change, and its removal
   detection so resources the new Composition no longer produces show as
   deletes.
5. **Plan the cloud layer.** For every rendered MR, merge in the live MR's
   external-name and status, route it to a plan server by API group, and get
   the provider's verdict: no-op, update, or the replace nobody spotted in
   review.
6. **Report.** Per-XR diffs with per-resource actions, a
   `Plan: N to add, M to change, K to destroy` summary with replaces
   counted on their own, and `-o json|yaml`
   emitting the structured results for CI and PR automation.

Composition changes, embedded function changes, and XR changes all reduce to
this one flow, because they all reduce to "re-render with local inputs, then
ask the providers what the delta means".

One class of matched XRs needs special honesty: those pinned to an older
CompositionRevision (`compositionUpdatePolicy: Manual`). Applying the
changed Composition does nothing to them, not now, and not until someone
advances their revision. Simulating them as if apply reached them would
overstate the blast radius; dropping them would understate it and break the
rule that nothing disappears from the preview. So they print as deferred by
default, without a diff:

```console
XR: XNetwork.example.org staging-network (deferred)
    Pinned to composition revision 6; apply changes nothing until the
    revision advances. Rerun with --include-pinned to preview that moment.
```

With `--include-pinned` they simulate like every other XR, labelled as the
preview of the revision advance rather than of the apply, and counted
separately in the summary. crossplane-diff reached the same conclusion from
the other direction: its composition diffing excludes Manual-policy
composites by default behind an `--include-manual` flag. This keeps that
behavior and adds the visible deferred line, so a pinned XR is a stated
fact instead of a silent omission.

The command shape follows the CLI's kong conventions, and every flag falls
out of the flow above:

```go
type simulateCmd struct {
	ProjectFile    string        `short:"f" help:"The project definition file."`
	Namespace      string        `help:"Simulate only XRs in this namespace."`
	Name           string        `help:"Simulate only the XR with this name."`
	ProviderImages []string      `help:"Override the provider images to run as plan servers."`
	IncludePinned  bool          `help:"Also simulate XRs pinned to older composition revisions."`
	MaxIterations  uint          `default:"10" help:"Cap on requirement resolution passes per XR."`
	Timeout        time.Duration `default:"2m" help:"Per XR render and plan timeout."`
	MaxConcurrency uint          `default:"8" help:"XRs simulated concurrently."`
	Output         string        `short:"o" enum:"diff,json,yaml" default:"diff" help:"Output format."`
}
```

`crossplane resource simulate` shares the plan server, timeout, and output
flags, and takes its resources from `-f` files instead of project
discovery. Both commands enter the tree with the CLI's `maturity:"alpha"`
tag and graduate the way every other command does.

### Running Plan Servers Like Render Runs Functions

The CLI treats provider packages the way render treats function packages: as
images it can run locally and talk to over gRPC.

From the project's `dependsOn` (or an explicit `--provider-images` override,
for `resource simulate` outside a project), the CLI starts each provider
image with its `internal plan-server` entrypoint as a Docker container bound to an
ephemeral localhost port, following the same runtime conventions render
established for functions (pull policy, named-network support for
containerized CI, cleanup on exit). It then calls each server's `GetInfo`
RPC, which returns the API groups the server can plan
(`s3.aws.upbound.io`, `kms.aws.upbound.io`, ...), and builds a routing table
from the answer. Routing by declared capability rather than image-name
convention means family providers coexist and a resource with no route is
detected up front, not by a failed RPC.

Because plan servers are stateless and credential-free, all state travels
in the request. There is nothing to configure: no ProviderConfig, no cloud
credentials, no reach into the cluster from the container. The
CLI fetches live state with the user's own kubeconfig and RBAC; the plan
server only ever sees what the user could already read.

### From Response to Printed Diff

What a plan server returns is deliberately close to what the CLI prints.
Every response carries the action, the field changes with current and
desired values, replacement information, and provider diagnostics, and
every field path arrives as a CRD path
`spec.forProvider.deletionWindowInDays`.
The server also redacts sensitive values and marks computed ones.

Before sending, the CLI has to answer which live resource a rendered one
corresponds to, and names are not enough: a re-render can generate fresh
names for resources a Composition names dynamically. The CLI matches by the
`crossplane.io/composition-resource-name` annotation, the stable identity a
Composition assigns to each resource it composes, and falls back to group,
kind, and name where the annotation is absent. A match contributes the live
resource's external-name annotation and status to the request, so the
provider diffs against the cloud resource the MR actually tracks. The
inverse case matters just as much: a live composed resource with no
rendered counterpart means the change would remove it, and it enters the
plan as a delete instead of silently disappearing from the preview.

Printing then follows one rule: show users their own manifest, annotated.
Each resource prints as YAML in CRD terms with changed fields marked inline
(`~ field: "old" -> "new"`), sensitive values stay redacted, computed
values print as `(known after apply)`, and fields that force replacement
carry that warning on the line where the user's eye already is. Provider
diagnostics print under the resource they belong to. Actions map to the
summary symbols (`[+]` create, `[~]` update, `[-/+]` replace, `[-]` delete,
`[=]` no-op, `[?]` approximate fallback). The same data serializes as
`-o json|yaml`, one typed result per resource, so a pull-request bot can
gate on "any replace anywhere" without parsing console output.

### When There Is No Plan Server

Not every provider is an upjet provider, and not every upjet provider will
serve plans on day one. Simulate treats planning as a capability it detects,
not a requirement:

* If a resource routes to a plan server, its diff is provider-computed and
  accurate: replacement detection, known-after-apply values, sensitive-value
  redaction.
* If it does not, the CLI falls back to a client-side comparison of desired
  against live spec, rendered with a distinct marker (`[?]`) and a closing
  warning that the result is approximate: no replacement detection, no
  computed values.

A mixed control plane gets a complete preview that is honest about which
parts are provider-grade, and every provider that adopts the plan protocol
upgrades its slice of the output with no CLI changes. The protocol itself is
provider-agnostic (desired and live in, action and field changes out), so a
native provider can serve it with its own diff logic and be
indistinguishable from the upjet path.

### Building on crossplane-diff, Not Beside It

Step 4 above is crossplane-diff's core, and I propose we consume it rather
than rewrite it and go further: bring crossplane-diff into the CLI as the
foundation of simulate, with its maintainers' agreement and involvement.

The convergence is already happening from both sides. crossplane-diff
switched to driving the CLI's render engine so its output matches real
reconciliation; it reuses the CLI's loader and validation; its recent work
has been upstream PRs to make the CLI a better host for it
([crossplane/cli#159][cli-159], [crossplane/cli#96][cli-96],
[crossplane/crossplane#7452][xp-7452]). What remains separate is packaging:
a second binary, a second release train, a second CI matrix, and a
discoverability cliff, the users who most need a diff before applying are
the least likely to know a contrib repo exists.

Concretely, I propose we converge directly, together: not a wholesale
transplant of the repo, but a joint pass with its maintainers over what
simulate and a first-class diff experience actually need, moving those
pieces into crossplane/cli as shared internals, and leaving behind what the
standalone packaging forced them to build.

* **What moves.** The requirements resolver (the iterative render loop that
  fetches what functions ask for from the live cluster), the diff
  calculator (rendered against live, including removal detection and nested
  XRs), the server-side dry-run that keeps defaulting and webhook mutation
  out of the diff, and the composition impact discovery behind
  `crossplane-diff comp`. These become internal packages that `simulate`
  and a first-class diff command share.
* **What gets simpler in the move.** Living outside the CLI forced
  indirection the code no longer needs once it lives inside. The render
  seam it uses to drive the CLI's engine from another binary collapses into
  a direct call, and its loading and validation glue merges with the CLI's
  own instead of wrapping it. The move is the moment to shed that
  scaffolding, not to carry it along.
* **What it becomes.** One simulate surface, not parallel commands. What
  `xr` and `comp` do today is this same engine fed from different inputs:
  `comp` is `project simulate` with the changed Compositions arriving as
  files instead of being discovered from a project, and `xr` is the same
  preview driven by a changed XR file with the Compositions coming from the
  cluster. They become input modes of the simulate commands (`-f` for
  changed XRs or Compositions, project discovery by default, `--namespace`
  and `--name` to scope the cluster side), and both gain the plan layer as
  their upgrade. Whether thin aliases remain for existing crossplane-diff
  invocations is part of the command-naming discussion that belongs to the
  review. The existing maintainers stay code owners of what they built, the
  standalone repo enters maintenance with a pointer forward, and every
  Crossplane user gets the preview by default: one binary, one release
  train, one CI matrix.

Whether and how this lands is the maintainers' call as much as ours. That is
why they're proposed reviewers of this document, and why the migration
mechanics (import paths, command naming, the deprecation window for the
standalone binary) belong to that review rather than to this proposal. And
if the review lands somewhere short of a move, say on consuming
crossplane-diff as a shared module, the essential part survives: simulate
builds on their work, and the ecosystem stops growing second
implementations.

### Predictability

Simulate's cost scales with what the user asks to preview, not with the size
of the control plane. Per XR: the same render work `crossplane render` does
(plus requirement-resolution iterations, capped), one server-side dry-run
per composed resource, and one gRPC plan call per rendered MR, each of
which is a pure in-process schema diff on the server, no cloud calls.
Plan-server startup is the dominant fixed cost, a few seconds per distinct
provider image, paid once per run and torn down after. Nothing simulate does
writes to the cluster or the cloud: reads plus dry-runs on one side, a
credential-free diff on the other.

## Alternatives Considered

### Clone-Based Simulation

Clone the control plane, isolate it from external systems, apply the change,
and diff the clones. Considered previously as a platform feature; it is heavy
(clone primitives, provider isolation, doubled storage) and its accuracy has
a ceiling: a disconnected clone cannot say what providers would do, and a
connected one reintroduces credentials and doubles cloud traffic. The
per-resource plan answers the common case with none of that. Clones remain
the right tool for the questions simulate does not ask (emergent behavior of
many controllers reconciling together), and nothing here precludes them.

### A Server-Side Simulation API

Put simulation in the control plane: one API that takes a change set and
returns everything affected. This inverts the CLI's working model: render
and diff both succeed precisely because they run *before* and *outside* the
control plane, on the code in your working tree. A server-side API also
cannot cover the pre-deployment cases (nothing is installed yet; the provider
version being previewed is not running), needs RBAC and lifecycle answers up
front, and pushes orchestration into the server. Client-side orchestration
of per-resource primitives keeps the pieces simple; a server-side rollup can
be layered on later if a real need emerges.

[crossplane-diff]: https://github.com/crossplane-contrib/crossplane-diff
[one-pager-render-engine]: https://github.com/crossplane/crossplane/blob/main/design/one-pager-render-engine.md
[issue-6857]: https://github.com/crossplane/crossplane/issues/6857
[issue-7351]: https://github.com/crossplane/crossplane/issues/7351
[cli-159]: https://github.com/crossplane/cli/issues/159
[cli-96]: https://github.com/crossplane/cli/issues/96
[xp-7452]: https://github.com/crossplane/crossplane/issues/7452
