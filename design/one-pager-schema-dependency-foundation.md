# A Foundation for Schema Generation and Dependency Management

* Owner: Cyrill Näf (@BigGold1310)
* Reviewers: Adam Wolfe-Gordon (@adamwg), Steven Borrelli (@stevendborrelli)
* Status: Proposed

## Summary

Schema generation is wired independently into seven commands. Each wiring
drops something another one remembers, so four filed issues and one unfiled
correctness gap all trace back to the same missing piece: there is no single
implementation to fix once.

This document proposes that piece — a `Resolver` (what does the project
depend on?) and an `Engine` (turn that into files), reachable only through
one constructor, writing one versioned lock:

| Issue | Root cause | Closed by |
|---|---|---|
| [#215][issue-215] — `update-cache` skips `apis/` | Two generation paths; only `build` calls both | One pass over a full `ResolvedSet` |
| [#428][issue-428] — schemas go stale | Additive writes into `schemas/`; lock is `map[sourceID]version` | Whole-group regeneration + atomic swap; grouped lock |
| [#242][issue-242] — `render` ignores `spec.schemas.languages` | `render` builds its own generator list | `project.NewEngine` is the only source of an `Engine` |
| [#370][issue-370] — `--git-token` only on `update-cache` | Each command copies its own fetch path | The shared resolver owns git auth |
| *(unfiled)* pins move on unrelated builds | `addPackage` re-resolves every constraint on every run | `FromLock` vs. `ResolveConstraints` |

**What this does not change.** Only what each command calls underneath
changes; everything here moves layers at most:

* Command signatures, flags, output and exit codes; no new commands.
* The xpkg protocol, the global per-user package cache, and registry auth
  ([`xpkg/fetcher.go`][fetcher-go]).
* `generator.Interface`, apart from the additive `schemaMerger` seam no
  generator here implements.
* Transitive resolution: `ResolveRef` and the dependency walk keep their
  semantics.
* The project-file schema, apart from an additive `spec.schemas.mode`.

## Background

The CLI generates language-specific schemas (Go, Python, KCL, JSON) from the
project's own `apis/` directory and from its dependencies (xpkg packages,
git-hosted CRDs, HTTP, Kubernetes OpenAPI). Seven commands reach that
machinery, each its own way:

| Command | What it does today | Own issue |
|---|---|---|
| `dependency add` | `AddDependency` → `addPackage`, for the one new package ([`manager.go:315`][dep-manager]) | — |
| `dependency update-cache` | `RefreshAll` over every declared dependency — never `apis/` ([`manager.go:346`][dep-manager]) | #215 |
| `project build` / `run` | `AddAll`, then a *separate* `schemaManager.Generate` for `apis/` ([`build.go:269-282`][build-go]) | the pin gap |
| `function generate` | Its own call into the schema manager | — |
| `composition generate` | A third copy of the add-a-dependency wiring, for `function-auto-ready` ([`generate.go:95-117`][comp-gen]) | — |
| `composition render` / `operation render` | Own wiring, `generator.AllLanguages` with no filter applied ([`render/xr/cmd.go:477`][render-xr]) | #242 |
| `dependency clean-cache` | `Manager.Clean()` removes `schemas/`; `CleanPackages` removes the xpkg cache unless `--keep-packages` ([`manager.go:390`][dep-manager]) | — |

**#428** — every row above writes into the shared `schemas/<lang>/` tree with
`CopyFilesBetweenFs` ([`manager.go:131`][schema-manager]), and nothing
removes what a source no longer produces. The lock ([`lock.go`][lock-go])
records only `map[sourceID]version`, so "the tree matches what the project
declares" is indistinguishable from "the tree is whatever the last few runs
left behind." Real projects (modelplane) now `rm -rf schemas/` before every
build. It also blocks TypeScript ([#170][issue-170]) outright: that
generator emits one package describing the whole run, which cannot coexist
with last-writer-wins.

**#370** — `--git-token` / `--git-username` exist only on `update-cache`
([`cache.go:47`][cache-go]); every other row carries its own fetch path.

Two PRs already attempt this: [PR #435][pr-435] and [PR #430][pr-430]. Both
reach the same diagnosis — generation must be one pass over every known
source, not N independent ones — by conflicting routes, so picking either
as-is means inheriting its tradeoff (see Alternatives Considered). This
document proposes a third path: a foundation both PRs' ideas rebuild on top
of, incrementally.

## Constraints and goals

Two constraints shape everything below, and are referred to by name
throughout:

1. **No breakage.** Nothing that works today changes behavior in the PR that
   lands this foundation. The new engine sits underneath the old interface;
   later PRs migrate callers one at a time. Measurably: zero required action
   for existing users and CI on upgrade.
2. **No second foundational rewrite.** This is the dependency-management
   architecture for the next several years, so it has to accommodate what is
   already visible — run-level generators (TypeScript [#170][issue-170],
   Rust [#374][issue-374]), resource-level filtering, and a future
   `crossplane schema` surface — without being reshaped to fit them.

Within those, the goals: one engine invoked once per run from a single
resolved view of every source; one resolver answering "what does this
project depend on" reproducibly, so a pin moves only when asked; one
configuration surface consulted uniformly, `render` and `function generate`
included; and git auth owned by the resolver.

**Non-goals**: shipping a `crossplane schema` command group, and
implementing TypeScript or Rust merge support — only the interface contract
those need is fixed here.

## Proposal

Every command does the same two things, each today in its own way: turn what
`crossplane-project.yaml` declares (plus whatever the lock already pins)
into a fully resolved set of sources, then turn that set into files under
`schemas/`. This gives both steps one shared implementation, split into four
layers.

| Layer | Owns | Entry points |
|---|---|---|
| 3 — commands | the seven above, plus `dependency remove` | signatures unchanged |
| 2 — declaration | every project-file mutation | `projectfile.Upsert(dep)`, `Remove(ref)` |
| 1 — resolver | what the project depends on | `FromLock`, `ResolveConstraints` |
| 0 — engine | turning a resolved set into files | `Generate`, `Verify`, `Clean` |

Each layer calls only the one beneath it, and no layer below layer 3 knows which
command invoked it.

`Source` is kept exactly as it is today in
[`internal/schemas/manager`][source-go]: its five constructors already cover
every origin the CLI supports — xpkg, git, HTTP, k8s-OpenAPI, and the
project's own filesystem — each reporting its origin through its `ID()`
prefix. A `ResolvedSet` is those dependencies plus the one `apis/` directory
`Spec.Paths.APIs` names. Because `Resources()` fetches on first call and
memoizes ([`source.go:214`][source-go]), a `ResolvedSet` is pins and
identities until the engine asks for a filesystem, and the engine generates
one group at a time rather than materializing every source at once.

**Nothing generates until a full `ResolvedSet` exists.** That one rule is
the structural fix for #215 and #428: there is no "dependencies pass" and
separate "apis pass" that only some commands run both halves of.

### Where the code goes

No new top-level package for its own sake. The shared vocabulary stays where
`Source` already lives; the new work is one `engine` package underneath it
plus new methods on `dependency` above it.

```
internal/
  schemas/
    manager/                 # unchanged home of the shared vocabulary
      source.go                #   unchanged
      resolved_set.go          #   NEW: ResolvedSet
      lock.go                  #   REWRITTEN: exported, versioned, grouped (lockVersion 2)
    engine/                   # NEW — Layer 0
      engine.go                #   Engine: Generate, Verify, Clean (the only exports)
      group.go                 #   source groups, fingerprinting, lock comparison (unexported)
      swap.go                  #   staging tree + atomic rename into schemas/ (unexported)
  dependency/                 # Layer 1 — today's Manager becomes Resolver
    resolver.go                #   NEW: Resolver, FromLock, ResolveConstraints
    manager.go                  #   kept: ResolveRef, transitive walk, Source construction
                                #   MOVED OUT: upsertDependency (manager.go:429)
  git/flags.go                 # NEW (from PR #435): shared git.HTTPSAuthFlags
  project/
    engine.go                   #   NEW: project.NewEngine
    projectfile/                #   + Upsert(dep), Remove(ref) — upsertDependency lands here,
                                #     so Layer 2 owns all project-file mutation
apis/dev/v1alpha1/
  project_types.go              # + ProjectSchemas.Mode and a nil-safe GetMode(), alongside
                                #   today's GetLanguages(): Spec.Schemas is a pointer, so
                                #   "legacy is implicit" depends on the getter
cmd/crossplane/**               # call-site changes only
```

### Interfaces

```go
// internal/schemas/manager — unchanged Source, new ResolvedSet.

type Source interface {
    ID() string                                       // stable lock key, prefixed by origin:
                                                      //   xpkg://, git://, http://, k8s://, fs://
    Version(ctx context.Context) (string, error)      // the pin: an OCI digest, a commit SHA, or apis/'s content hash
    Resources(ctx context.Context) (afero.Fs, error)  // the filesystem the generator reads from
    Type() SourceType                                 // crd or openapi — picks GenerateFromCRD vs GenerateFromOpenAPI
}

type ResolvedSet struct {
    Dependencies []Source  // sorted by ID() — see Two groups, below
    APIs         Source    // one directory: Spec.Paths.APIs is a single path
}

// internal/dependency — Layer 1.

type Resolver interface {
    // FromLock resolves declared against the pins already in lock. It never
    // contacts a registry or remote to *pick* a version — only to fetch an
    // exact pin that isn't in the per-user package cache yet.
    FromLock(ctx context.Context, declared []v1alpha1.Dependency, lock *manager.Lock) (manager.ResolvedSet, error)

    // ResolveConstraints resolves declared against upstream, picking the
    // highest version satisfying every constraint, and returns the new pins.
    // Only the commands whose job is to move a pin call it.
    ResolveConstraints(ctx context.Context, declared []v1alpha1.Dependency, lock *manager.Lock) (manager.ResolvedSet, *manager.Lock, error)
}

// internal/schemas/engine — Layer 0.

type Config struct {
    Languages []string
    // ApiGroups []string // added by a later PR, see Roadmap
}

type Engine interface {
    // Generate materializes set into schemas/, skipping groups the lock
    // says are still fresh. Drift is fixed silently.
    Generate(ctx context.Context, set manager.ResolvedSet, cfg Config, lock *manager.Lock) (*manager.Lock, error)

    // Verify reports the drift Generate would otherwise fix, writing
    // nothing. No command calls it here (see Strictness); it exists now
    // because retrofitting a comparison into an engine built without one is
    // the expensive version.
    Verify(ctx context.Context, set manager.ResolvedSet, cfg Config, lock *manager.Lock) (Drift, error)

    // Clean removes the generated schemas tree, and with it the lock inside it.
    Clean() error
}

// GroupDrift names one group ("dependencies" or "apis") and why Generate
// would rewrite it: a changed source, a changed language set, a hand-edited file.
type Drift struct{ Groups []GroupDrift }
type GroupDrift struct {
    Group  string
    Reason string
}

// internal/project — the one constructor every command calls. It bakes
// proj.Spec.Schemas and auth into what it returns, so no caller can build a
// Resolver or Engine without them, and it errors on an unsupported language
// here, once, rather than inside each generator.

func NewEngine(projFS afero.Fs, proj *v1alpha1.Project, auth git.AuthProvider) (dependency.Resolver, engine.Engine, error)
```

`manager` stays the lowest of the three and imports neither `dependency` nor
`engine`, which is why `ResolvedSet` and `Lock` live there: both layers above
need the same vocabulary, and only `manager` can hold it without the two
importing each other through it.

### What each command calls

Every row below starts the same way — `project.NewEngine(projFS, proj,
auth)`, the only source of a `Resolver` or an `Engine`.

| Command | After this foundation |
|---|---|
| `dependency add` | `Upsert`, `ResolveConstraints`, `Generate`. Same visible result, no leftover or duplicate models |
| `dependency remove` *(new)* | `Remove`, `FromLock`, `Generate`. The removed package's models disappear through the group swap, not a cleanup step someone must remember |
| `dependency update-cache` | `ResolveConstraints` for every dependency, then `Generate` over **both** groups — #215 closes because there is only one pass |
| `dependency clean-cache` | Unchanged behavior, with `Engine.Clean()` replacing `Manager.Clean()`: still one `RemoveAll` over `schemas/`, which takes the lock inside it. `--keep-packages` keeps its meaning |
| `project build` / `run` | `FromLock`, then one `Generate` over both groups. Bootstraps via a single `ResolveConstraints` only when no lock exists; never re-resolves a pinned constraint again |
| `function generate` | `FromLock` + `Generate`, like everything else |
| `composition generate` | `Upsert` + `ResolveConstraints` + `Generate` — the same three calls as `dependency add`, which is what it is doing |
| `composition render` / `operation render` | `FromLock` + `Generate` with the project's actual `Config` — #242 closes here |

For any one command that is a change inside the handler, not to its
signature:

```go
// project build, today (internal/project/build.go, abbreviated):
if err := manager.AddAll(ctx, declared); err != nil { ... }           // dependencies pass
if err := schemaManager.Generate(ctx, apisSource); err != nil { ... }  // separate apis pass

// project build, after:
resolver, engine, err := project.NewEngine(projFS, proj, auth)
set, err := resolver.FromLock(ctx, declared, lock)
lock, err = engine.Generate(ctx, set, cfg, lock)
```

### Three decisions worth arguing about

**One constructor.** `project.NewEngine` is the only way to get a `Resolver`
or an `Engine`, and it bakes in `proj.Spec.Schemas` and git auth. #242 and
#370 are each a command building its own wiring and dropping a shared
parameter ([`cache.go:76-93`][cache-go]); one constructor leaves no wiring
for an eighth command to drop anything from.

**Two resolver methods, not a `refresh bool`.** A command gains the ability
to bump a project to a newer upstream version only by wiring in
`ResolveConstraints`, which reads in a diff as "why does this now talk to
the registry?" — where a flag flipped three screens away reads as nothing.
That closes the unfiled gap: `addPackage` ([`manager.go:195`][dep-manager])
re-resolves every semver constraint on *every* `project build`, so a
provider matching `>=1.2.0` can silently become `1.5.0` on a build that
touched nothing else.

**Two groups, swapped atomically.** Dependencies and `apis/` are one group
each, and a group is always generated whole. The engine first compares the
group's inputs against what the lock recorded — each source's pin and hash,
the `Config`, the generator fingerprint. All unchanged: the tree on disk is
current, skip the group. Otherwise regenerate it whole into
`schemas/.staging/` and rename that over `schemas/<group>` (PR #430's swap
mechanics). Staging exists only between generation and the rename, and sits
next to its destination because a rename cannot cross filesystems; a crashed
run is the only way to see it, and `Engine.Clean()` sweeps it with the rest
of `schemas/`. Sources are compared in `ID()` order, so the same inputs
always produce the same verdict.

Generating whole groups is what closes #428: whatever a removed source, a
renamed kind, or a dropped language used to produce is simply absent from
the new tree, with no manual `clean-cache`. And because the last step is a
rename, a run that fails halfway leaves `schemas/` as it was.

### The lock

Replace [`lock.go`][lock-go]'s `map[string]string` with a versioned, grouped
lock, following PR #430's shape. This is what this PR writes:

```json
{
  "lockVersion": 2,
  "packages": {},
  "generatorFingerprint": "<generator image refs + options>",
  "config": { "languages": ["go", "python"], "apiGroups": null },
  "groups": {
    "dependencies": {
      "sources": {
        "xpkg://provider-aws-s3": {
          "pin": "sha256:...",
          "hash": "sha256:...",
          "resources": {
            "Bucket.v1beta1.s3.aws.crossplane.io": { "hash": "sha256:..." }
          }
        }
      }
    },
    "apis": {
      "sources": {
        "fs://apis": {
          "hash": "sha256:...",
          "resources": {
            "XExample.v1alpha1.example.org": { "hash": "sha256:..." }
          }
        }
      }
    }
  }
}
```

| Field | Question it answers |
|---|---|
| `lockVersion` | Which shape is this file in? Lets a future format change be detected and migrated rather than silently misread. |
| `packages` | Always empty. The downgrade shim — see Compatibility below. |
| `generatorFingerprint` | Generator image references + options, deliberately **not** the CLI version. When *this* moves, every group is stale even though no source changed. |
| `config` | Which `Config` was this generated with? A `languages` change is itself drift. Because the object mirrors the `Config` struct, a later predicate is a new field here — never a new top-level sibling, never a `lockVersion` bump, as long as its absence means "no filter". |

Keeping the CLI version out of the fingerprint is what stops a release from
staling every group in every project. The tradeoff: the KCL and Python
images are pinned in CLI source ([`kcl.go:54`][kcl-gen],
[`python.go:51`][python-gen]), so bumping either moves the fingerprint,
while a change to the in-process Go or JSON generator does not and surfaces
only on the next source change.

Each source carries both `pin` and `hash`: `pin` is what `FromLock` fetches
(a digest or commit SHA, never a constraint), `hash` is what
`Generate`/`Verify` compare against to catch a hand-edited file or a locally
modified `apis/`. A dependency has both; `apis/` has only `hash`, having no
upstream pin.

`resources` is keyed by GVK (`Kind.version.group`) rather than by output
path, replacing the flat per-file map PR #435 sketched. Output file structure
is a per-language implementation detail, whereas a GVK is the meaningful unit
— and belongs to exactly one source, so attribution for merged run-level
output (one npm package, one crate) falls out for free instead of needing
separate "which sources fed this file" bookkeeping. Per-resource hashes
don't change regeneration granularity, since a changed source's own `hash`
still invalidates its whole group; they let `Verify` report *which* resource
drifted, and let a later `apiGroups` filter be reasoned about straight from
the lock without re-fetching a source to find out what it holds.

They are also the lock's only size driver — a thousand-resource provider
adds a thousand entries. Because granularity does not depend on them, they
can be made optional later as a field-level change, the same way `outputs`
is added below.

**Room to grow.** Each resource later gains an `outputs` map naming the file
that GVK landed in per language — `"outputs": { "go":
"schemas/go/provider-aws-s3/bucket_types.go" }` alongside its `hash`.
A new field in an existing object: no `lockVersion` bump, no change for
existing readers. Deliberately **not** in this PR — `generator.Interface`
takes and returns a whole filesystem ([`interface.go:32`][generator-go]), so
no GVK goes in and no mapping comes back.

**Compatibility, in both directions.**

* *New CLI, old lock.* A missing, unversioned, or malformed lock counts as
  stale: bootstrap via `ResolveConstraints` once, never a build failure.
  That covers a corrupted lock too — there is nothing to repair by hand.
* *Old CLI, new lock.* Today's reader decodes into `lock{Packages
  map[string]string}` ([`lock.go:23`][lock-go]) then writes
  `l.Packages[id] = version` ([`manager.go:206`][schema-manager]), so a file
  with no `packages` key decodes to a nil map and panics on the next write.
  This PR therefore keeps writing a vestigial `"packages": {}`, which an old
  CLI reads as an empty lock and regenerates from — correct, if slower than
  it needs to be. That keeps one filename and one `lockVersion` story; the
  vestigial key goes away with the rest of the legacy surface in Roadmap
  step 7.

### Strictness

`mode` gates *generation* drift only — whether `Generate` fixes a mismatch
silently or `Verify` fails closed. It does not gate *resolution*, which is
fixed per command, so the pin guarantee holds in both modes.

* **`legacy`** (default, and implicit for every project today, including
  those with no lock and those with no `spec.schemas` block, since
  `GetMode()` returns `legacy` for a nil `ProjectSchemas`). Commands call
  `Generate`; drift regenerates silently, as today. The only observable
  changes are #215, #242 and #428.
* **`strict`** (opt-in via `spec.schemas.mode: strict`). Commands call
  `Verify`; drift fails the build with an actionable message (PR #435's
  model).

## Roadmap

None of this ships in this PR. Each step is listed to show the foundation
needs no further architectural change to support it.

| # | Step | Why the foundation already fits it |
|---|---|---|
| 1 | **This document** — engine, resolver, lock v2, all seven commands remapped, `legacy` only | — |
| 2 | **Git auth per command**: embed `git.HTTPSAuthFlags` into `dependency add`, then `build`, `run`, `render` | Closes #370 one independently revertable command at a time |
| 3 | **`strict` mode, opt-in**: `build`/`run` call `Verify` and fail closed | `Verify` already exists; `legacy` stays the default |
| 4 | **Resource-level filtering**: `spec.schemas.apiGroups` as a second `Config` predicate | The lock's `config` mirrors `Config`, so this is a new field in an existing object |
| 5 | **A `crossplane schema update\|generate\|status\|clean` group** | A thin surface over an engine already running underneath everything. Its `status` is the first consumer of per-resource data, so this PR also populates `resources[].outputs` |
| 6 | **Default `strict` for new projects** (project-file schema version bump) | `legacy` and step 5's deprecated side effects stay through a stated window |
| 7 | **Remove `legacy`, the deprecated side effects, and the vestigial `packages` key** | The only step allowed to break anyone — by then scheduled and documented, not bundled into a foundation |

Steps 2–4, 6 and 7 are Layer-3 changes only: a command calls `Verify` where
it called `Generate`, or a new file under `cmd/crossplane/schema/` calls the
same five methods under a new name. Step 5 is the one exception, changing
`generator.Interface` for the reason given under The lock.

**The one seam none of them needs.** The engine's group-assembly step
accepts a `schemaMerger` capability (PR #430's `MergeGeneratedSchemas`): a
generator whose output describes an entire run — TypeScript's npm package,
Rust's crate — implements it, and the engine merges parts instead of letting
the last source win. No generator here implements it; the seam exists so
that #170 and #374 become feature PRs against this engine rather than
reasons to reshape it.

## Alternatives Considered

**Land PR #435 as-is.** Reaches the end state fastest: new command surface,
per-file integrity checking, deterministic resolution, git auth everywhere,
one PR. Rejected as the foundation because it ships six breaking changes at
once with no deprecation window — generation removed from `dependency add`,
`update-cache`, `clean-cache` and `function generate`, plus `build`/`run`
failing where they used to succeed silently. Every CI pipeline relying on
implicit regeneration breaks on upgrade day, failing "no breakage" outright.
Its ideas are what this foundation adopts; what is rejected is adopting them
as a replacement rather than as a foundation.

**Land PR #430 as-is.** The non-breaking posture is right and its
whole-group swap is the core of this engine. Rejected as sufficient on its
own because it stops short of "no second foundational rewrite": no git-auth
plumbing for #370, resolution still re-resolved every run rather than
pinned, and a lock format with no slot for the per-resource tracking
`strict` mode needs — so a second lock migration later. This foundation
takes its swap mechanics directly and adds the resolver, the lock extension
point, and the strictness gate. Its output cache is left out: the lock
comparison already skips unchanged groups, and adding a cache later is an
unexported change inside `group.go` and `swap.go`.

**Patch each issue at its own call site.** Add `--git-token` to `dependency
add`; make `update-cache` also call `Generate`; pass the languages into
`render`; patch the additive write in `manager.go`. Each patch is small and
would plausibly close its one issue. Rejected because it *is* the status
quo's failure mode rather than a fix for it: there are four issues across
seven call sites precisely because there was never one shared implementation
to patch once, and the next command added reintroduces the same class of bug
in an eighth place.

[issue-215]: https://github.com/crossplane/cli/issues/215
[issue-428]: https://github.com/crossplane/cli/issues/428
[issue-242]: https://github.com/crossplane/cli/issues/242
[issue-370]: https://github.com/crossplane/cli/issues/370
[issue-170]: https://github.com/crossplane/cli/issues/170
[issue-374]: https://github.com/crossplane/cli/issues/374
[pr-435]: https://github.com/crossplane/cli/pull/435
[pr-430]: https://github.com/crossplane/cli/pull/430
[dep-manager]: https://github.com/crossplane/cli/blob/main/internal/dependency/manager.go
[build-go]: https://github.com/crossplane/cli/blob/main/internal/project/build.go
[schema-manager]: https://github.com/crossplane/cli/blob/main/internal/schemas/manager/manager.go
[cache-go]: https://github.com/crossplane/cli/blob/main/cmd/crossplane/dependency/cache.go
[comp-gen]: https://github.com/crossplane/cli/blob/main/cmd/crossplane/composition/generate.go
[render-xr]: https://github.com/crossplane/cli/blob/main/cmd/crossplane/render/xr/cmd.go
[generator-go]: https://github.com/crossplane/cli/blob/main/internal/schemas/generator/interface.go
[go-gen]: https://github.com/crossplane/cli/blob/main/internal/schemas/generator/go.go
[json-gen]: https://github.com/crossplane/cli/blob/main/internal/schemas/generator/json.go
[kcl-gen]: https://github.com/crossplane/cli/blob/main/internal/schemas/generator/kcl.go
[python-gen]: https://github.com/crossplane/cli/blob/main/internal/schemas/generator/python.go
[fetcher-go]: https://github.com/crossplane/cli/blob/main/internal/xpkg/fetcher.go
[source-go]: https://github.com/crossplane/cli/blob/main/internal/schemas/manager/source.go
[lock-go]: https://github.com/crossplane/cli/blob/main/internal/schemas/manager/lock.go
