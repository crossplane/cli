# Linting XRDs with the Crossplane CLI

* Owner: Erik Miller (@GenerikUser)
* Reviewers: TBD
* Status: Draft

## Background

A composite resource definition (XRD) is an API. Once users create XRs
against it, its schema is hard to change. Crossplane already tells you whether
an XRD's schema is *valid*. `crossplane resource validate` checks resources
against it, and since [#301][cli-301] the CLI reports a schema that is not
structural, which Kubernetes would reject. Nothing tells you whether the
schema is *good*: whether it follows the API design practice the Crossplane
maintainers recommend, and whether this version breaks the last one.

[Issue #69][cli-69] asks for that second check, a linter for XRDs. The
practices it draws on are tracked in [crossplane/docs#564][docs-564]:
- status fields that do not reject what they observe
- arrays of objects that have a key
- enums instead of booleans
- the server-side apply list markers
- descriptions on every field

A follow-up comment on the issue adds a diff mode that flags breaking changes
between two versions of an XRD, with [openshift/crd-schema-checker][crd-checker]
as prior art. [kubernetes-sigs/crdify][crdify] is the SIG-owned tool for the
same job on CRDs. It has per-check severity, reads the old CRD from a file, a
git ref or a cluster, and ships its comparators as a Go library that this
proposal builds on. A maintainer called #301's structural check "a first step"
toward the linter. That sets the split this proposal keeps: hard validity
problems are errors that validation owns, and best-practice findings are a
lint layer above them.

Platform teams also differ on which practices they care about. One team bans
booleans, another does not; one requires a description on every field. A
fixed set of checks would either be too noisy for some teams or too quiet for
others. ESLint and [typescript-eslint][ts-eslint] solved the same problem for
JavaScript: a set of rules, each with a severity and options, configured per
repository, suppressible inline, and extendable with rules of your own. That
model fits XRDs well. Its runtime plugin loading does not, because Go has no
equivalent of `require`.

## Goals

- `crossplane xrd lint`: check XRDs against a set of built-in rules and report
  findings with a rule name, severity, location and docs link.
- Make every rule configurable per repository: severity (`off`, `warn`,
  `error`) and options, set in a config file and suppressible inline.
- Let users add rules of their own, written as CEL expressions in that config
  file, with no compiling and no plugins.
- Detect breaking changes between two versions of an XRD, as a category of
  rules that runs when the old version is supplied, and between the served
  versions of a single XRD.
- Be quiet by default. With no config file, only the rules guarding against
  things that actually misbehave in Crossplane fail the run. One design
  preference, `xrd/no-bools`, warns without failing.
- Fit CI: machine-readable output, meaningful exit codes, a warning budget.

It is not a goal to lint Compositions, functions, packages or projects; this
proposal covers XRDs only. Nor is it a goal to fix findings automatically.
Rules may suggest a fix as text, and `--fix` can come later. It is not a goal
to load third-party rules written in Go at runtime, or to offer presets or
shareable configs. Re-implementing validity checks is also out of scope:
structural-schema and CEL cost checks stay with validation.

## Proposal

Add `lint` as a verb under the existing `xrd` noun, gated as alpha
(`maturity:"alpha"`). It is built from three pieces:
- a registry of built-in rules, each with its own default severity
- a config file that overrides those defaults and declares custom CEL rules
- one engine that walks each XRD's schemas, runs the rules, applies
  suppressions and renders findings

### `crossplane xrd lint`

```console
$ crossplane xrd lint apis/
apis/network/definition.yaml
  xnetworks.example.org v1alpha1
    41:9   error  spec.subnets: array has no x-kubernetes-list-type   xrd/lists-need-list-type
    77:13  error  status.cidr: status field must not set pattern      xrd/status-no-value-validation
    52:11  warn   spec.public: prefer an enum to a boolean            xrd/no-bools

2 errors, 1 warning
```

```
crossplane xrd lint [flags] <files-or-dirs>...

  --old=SOURCE          Previous version of the XRD(s). Enables xrd-diff/* rules.
  --config=PATH         Lint config file. Default: search upward for .crossplane-lint.yaml.
  -o, --output=FORMAT   text | json | yaml (default text)
  --max-warnings=N      Fail if there are more than N warnings (default -1, unlimited).
```

**Loading inputs**
- Lint has its own small `yaml.v3` loader. It handles files, directories,
  stdin and multi-document YAML, and keeps the filename, line numbers and
  comments that findings and suppressions need. (proposed)
- The shared `cmd/crossplane/common/load` loader decodes straight to
  unstructured objects and discards those positions, so it is left unchanged.
- Objects that are not `CompositeResourceDefinition`s are skipped.
- Both `apiextensions.crossplane.io/v1` and `v2` XRDs are in scope.
  (proposed)

**Old versions** (proposed)
- `--old` takes a source, using the URI forms crdify uses, plus one for
  packages:
  - a plain path, or `file://<path>`: files and directories, as for inputs
  - `git://<ref>?path=<path>`: the XRDs at a ref of the current repository,
    for example the base branch in CI. `path` may name a file or a directory.
  - `kube://[<name>][?context=<context>]`: the XRDs installed in a cluster.
    The kubeconfig is found the way `kubectl` finds it, and `context` picks
    one of its contexts; without it, the current context is used. With a
    name, only that XRD is loaded; without one, every XRD in the cluster is.
  - `xpkg://<ref>`: the XRDs in a Configuration package in a registry, fetched
    the way the CLI already fetches packages
- crdify's own loaders return CRDs, and its `kube://` loads one CRD by name,
  so lint has its own loaders for these schemes. It keeps crdify's syntax,
  so `git://main?path=apis/` and `kube://xnetworks.example.org` mean the same
  thing in both tools.
- Line numbers are only needed for the new XRDs, which findings point at, so
  old sources need no position information.

**Validity first**
- Before any lint rule runs, lint applies the existing structural check from
  [#301][cli-301] to each XRD. (proposed)
- An XRD that is invalid or not structural gets an `error` finding that
  cannot be disabled, and the remaining rules are skipped for that XRD.

**Findings**
- Each finding carries: rule, severity, file, line, XRD name, version, field
  path, message, docs URL, and an optional suggested fix.
- Built-in rules always have a docs URL. A custom rule has one only if its
  config sets `docsURL`; otherwise the field is left empty.
- Text output groups findings by file and XRD.
- json and yaml output emit `{summary: {errors, warnings}, findings: [...]}`.
  They follow the `Renderer`/`RendererFor(format)` pattern that `resource
  validate` uses in `pkg/validate/output`.

**Exit codes**
- The command exits non-zero when any of these is true:
  - there is at least one `error` finding
  - warnings exceed `--max-warnings`
  - the config is invalid
  - an input cannot be read
  - a rule fails to evaluate (see [Runtime errors](#custom-rules-in-cel))
- Otherwise it exits zero.

### Configuration

The CLI searches upward from the working directory for
`.crossplane-lint.yaml`; `--config` overrides the search. With no file, the
defaults apply. The file holds only overrides and custom rules:

```yaml
apiVersion: lint.crossplane.io/v1alpha1
kind: LintConfig
rules:
  xrd/no-bools: error
  xrd/require-description: error
  xrd/string-max-length: {severity: warn, options: {max: 1024}}
  xrd/status-no-value-validation: off
customRules:
  local/no-unbounded-strings:
    severity: warn
    match: property
    expr: "self.type != 'string' || has(self.maxLength)"
    message: "String fields should set maxLength."
```

**Rule values**
- A rule value is either a bare severity, or an object with `severity` and
  `options` keys: `{severity: warn, options: {max: 1024}}`.
- ESLint's array form, a list of severity then options, is not accepted. A
  rule value that is a YAML sequence is a config error.
- Options are validated against a schema that the rule declares.

**Strict parsing**
- The lint config is decoded with `yaml.v3`, the same library as the input
  loader, with `KnownFields(true)`, so an unknown key is an error. (proposed)
- `yaml.v3` also rejects a duplicate key within `rules` or `customRules`, so
  the same rule cannot be configured twice in one file. A test covers this.

**Rule names**
- Custom rule names take the form `<prefix>/<name>`, with both parts in
  lower-case kebab case, for example `acme/no-unbounded-strings`.
- The prefix is free-form, typically a team or organisation name, or `local`.
- The `xrd/`, `xrd-diff/` and `lint/` prefixes are reserved. `lint/` carries
  the linter's own findings, such as `lint/rule-error`. (proposed)

**Name checks**
- Beyond what the decoder catches, two explicit checks make a config error:
  - a custom rule whose name uses a reserved prefix, or matches a built-in
    rule's name
  - a `rules:` entry that names no known rule, built-in or custom, so typos
    surface instead of silently doing nothing
- A `rules:` entry may name a custom rule, which overrides the severity the
  custom rule declares.

**Header**
- The `apiVersion`/`kind` header follows the CLI's other versioned files.
  (proposed)

There are deliberately no presets. ESLint needs `recommended` and `strict`
because it has hundreds of rules. With a few dozen, a default severity per
rule says the same thing more plainly, and a team's config reads as a list of
the ways it differs from the default.

### Built-in Rules

Every rule walks each served version's `openAPIV3Schema`. The test for "error
by default" is whether breaking the rule makes something misbehave in
Crossplane, not merely whether the API is weaker.

**On by default, as `error`.** Four rules guard against real misbehaviour:

- `xrd/status-fields-optional`: no `required` anywhere under `status`.
  Status is written in pieces, by Crossplane and by functions; a required
  field can cause those writes to be rejected.
- `xrd/status-no-value-validation`: no `minimum`, `maximum`, `pattern`,
  `enum`, length or item limits under `status`. Types are fine. Same reason:
  an observed value should never be rejected.
- `xrd/lists-need-list-type`: every array declares an explicit
  `x-kubernetes-list-type`; `atomic`, `set` and `map` all pass. This makes
  merge behaviour under server-side apply a deliberate choice rather than a
  default nobody chose.
- `xrd/served-versions-compatible`: the served versions of one XRD are
  compatible with each other. All served versions of an XRD are views of the
  same stored objects, so an incompatible difference between two served
  versions, such as a field whose type differs, misbehaves for whoever uses
  the other version. The rule needs no `--old`. (proposed)
  - It pairs versions the way crdify's served-version validator does, then
    compares each pair with crdify's `CompareVersions`. Served versions are
    sorted in Kubernetes version order (`v1alpha1` < `v1beta1` < `v1`), and
    every pair is compared, older to newer, not just neighbours.
  - Each pair runs through the same comparators, with the same options, as
    the `xrd-diff/` rules below, including the unhandled-change check. Each
    finding names the comparator and the version pair, for example
    `v1alpha1 -> v1`.
  - Every incompatible pair is reported, whether or not `--old` is given and
    whether or not the XRD configures a conversion webhook. The rule has no
    options of its own; comparator options come from the `xrd-diff/` rules.

**On by default, as `warn`.** One design preference is common enough to
surface by default without failing the run:

| Rule | Checks | Options |
|---|---|---|
| `xrd/no-bools` | No `type: boolean`; prefer an enum. | |

**Off by default.** Eight rules are design preferences that teams opt into:

| Rule | Checks | Options |
|---|---|---|
| `xrd/array-of-objects-needs-key` | Arrays of objects are keyed (`map` list type with keys). | |
| `xrd/no-floats` | No `type: number`; use integers or strings. | |
| `xrd/no-maps` | No open `additionalProperties` maps; prefer keyed lists. | |
| `xrd/no-preserve-unknown-fields` | No `x-kubernetes-preserve-unknown-fields: true`. | |
| `xrd/no-phase` | No `status.phase`; prefer conditions. | |
| `xrd/require-description` | Every property has a `description`. | `ignoreStatus` |
| `xrd/string-max-length` | String fields set `maxLength`. | `max` (proposed) |
| `xrd/version-naming` | Version names follow `v1alpha1` → `v1beta1` → `v1`. | |

The rule names follow [kube-api-linter][kal], which enforces the same
Kubernetes API conventions on Go types, so that one vocabulary covers both.

Some candidates are left out on purpose. Detecting a renamed field, or
deciding that an object should have been a discriminated union, takes
guesswork that a built-in rule should not ship. Teams that want them can
write them as CEL rules.

### Breaking Changes

Breaking-change checks are not a separate mode. They are a category of rules,
`xrd-diff/`, that runs only when `--old` supplies the previous version of
each XRD. Old and new XRDs are paired by `metadata.name`. A new XRD with no
old counterpart has no diff rules applied. An old
`apiextensions.crossplane.io/v1` XRD may be paired with a new `v2` XRD, so a
migration between the two can be checked. (proposed)

**Versions**
- Version-level rules run over the union of version names in the old and new
  XRD. A version that was served in the old XRD and is missing from the new
  one, or is no longer served, is reported by `xrd-diff/no-version-removal`.
  The finding attaches to the new XRD's `spec.versions`.
- Property-level rules then compare each version served in both the old and
  the new XRD with the version of the same name.

**Comparators**

The property comparisons are built on [crdify's validations
package][crdify-validations] rather than comparators of our own. Its
`CompareProperties` takes two `JSONSchemaProps` directly, so an XRD version's
`openAPIV3Schema` can be compared without rendering a CRD. (proposed)

| Rule | Checks |
|---|---|
| `xrd-diff/no-version-removal` | A served version is gone, or is no longer served. |
| `xrd-diff/scope` | The XRD's `scope` changed (`v2` XRDs). |
| `xrd-diff/no-field-removal` | A field in the old schema is missing from the new one. |
| `xrd-diff/type` | A field's `type` changed. |
| `xrd-diff/enum` | An `enum` value was removed, or (by option) added. |
| `xrd-diff/required` | A field became required, or a new field is required. |
| `xrd-diff/default` | A `default` was added, removed or changed. |
| `xrd-diff/maximum`, `max-length`, ... | An upper bound was added or tightened. |
| `xrd-diff/minimum`, `min-length`, ... | A lower bound was added or tightened. |
| `xrd-diff/pattern` | A `pattern` was added or changed, or (by option) removed. |
| `xrd-diff/nullable` | `nullable` changed, depending on its options. |
| `xrd-diff/description` | A `description` changed. Off by default. |
| `xrd-diff/unhandled` | Any other change that no comparator above understands. |

- Each crdify comparator becomes one rule, named after the comparator in
  kebab case. A comparator's own configuration, such as `enum`'s
  `additionPolicy`, becomes that rule's options.
- `xrd-diff/no-field-removal` maps to crdify's `existingFieldRemoval`, and
  `xrd-diff/scope` to its `scope` check.
  `xrd-diff/no-version-removal` is lint's own, because crdify's nearest
  check covers stored versions, which an XRD file does not record.
- `xrd-diff/unhandled` uses crdify's unhandled-change detection. Any change
  that no other comparator handles is flagged, so a kind of change nobody
  anticipated is reported instead of passing silently. That is safer than a
  fixed list of forbidden changes.
- crdify's comparator set also reaches changes that would otherwise need
  custom rules, such as a changed `default`, a tightened `maxLength` or a new
  `pattern`.

These default to `error` once `--old` is given, except
`xrd-diff/description`, which is off. Teams can change them like any other
rule. (proposed) Because they share the config, the suppression and the
output, a CI job can run one command against the base branch's XRDs to get
both kinds of finding.

### Suppressing Findings

There is one suppression mechanism, a YAML comment:

```yaml
properties:
  # crossplane-lint-disable-next-line xrd/no-bools -- kept for v1 compatibility
  enabled:
    type: boolean
```

**What it covers**
- The comment suppresses the named rules on the schema node that starts on
  the next line, and on everything nested beneath that node.
- Several rules can be named, separated by commas.
- The `-- reason` part is optional.

**Findings with no single node** (proposed)
- Some findings have no node of their own: a field removed by a diff, a
  version-level rule, or a custom rule with `match: xrd`.
- These attach to the nearest ancestor node that still exists in the new
  XRD. XRD-level findings attach to the document root.
- A `disable-next-line` comment above that node suppresses them.

**Unused comments**
- A comment that suppresses nothing is itself reported, as a warning from
  `lint/unused-disable`.
- This keeps stale suppressions from accumulating. (proposed)

### Custom Rules in CEL

Kubernetes users already write CEL in `x-kubernetes-validations`, and the CLI
already depends on `cel-go` through validation. That makes CEL the natural
language for rules that live in a repository. It is the closest analogue to
ESLint's local rules that needs no compiling and no runtime code loading.

```yaml
customRules:
  <prefix>/<name>:
    severity: off | warn | error
    match: property | xrd | diff
    filter: <optional CEL evaluating to bool; the rule applies only where true>
    expr: <CEL evaluating to bool; true passes>
    message: <shown when expr is false>
    description: <optional>
    docsURL: <optional; copied into each finding>
```

**Match targets**

There are three `match` values. The variables in scope depend on which one a
rule uses. (proposed)

`property` runs on every schema property of every served version. Its
variables:
- `self`: the property's full schema subtree, including nested properties
  and items
- `path`: its data path
- `inStatus`: whether it sits under `status`
- `required`: whether its parent lists it as required
- `xrd`: the whole XRD

`xrd` runs on each XRD once. Its only variable is `self`: the whole XRD.

`diff` runs only with `--old`. It runs on every property path present in
either version, including the schema root, for each version name served in
both the old and the new XRD. Its variables:
- `old`, `new`: the matching property's schema subtree in each version,
  either of which may be absent
- `path`, `inStatus`: as for `property`
- `oldRequired`, `newRequired`: whether each side's parent lists it as
  required
- `oldXrd`, `newXrd`: the whole XRDs

**Typing** (proposed)
- `self`, `xrd`, `old`, `new`, `oldXrd` and `newXrd` are dynamic maps decoded
  from the YAML. Fields are read as `self.maxLength`, and presence is tested
  with `has(self.maxLength)`.
- A key that is not a valid CEL identifier, or is a reserved word, is read
  with index syntax, for example `self.properties['namespace']`.
- Integral YAML numbers decode as `int`. All other numbers decode as `double`.
- `path` is the property's data path within the resource: property names
  joined by dots from the schema root, with `[*]` for array items, for
  example `spec.subnets[*].cidr`. The schema root itself has the path `""`.
- In a `diff` rule, a property missing from one version is bound to `null`.
  `has()` only tests a field, not a bare variable, so absence is tested with
  `old == null` or `new == null`.
- `oldRequired` and `newRequired` are `false` when that side is absent.

**Filter and expression**
- `filter` decides whether the rule applies at all, and `expr` runs only
  where `filter` is true. Omitting `filter` means the rule applies
  everywhere its `match` reaches.
- Where a value could be absent, guard it in `filter` and keep `expr` to the
  check itself, as the examples below do.

**Environment** (proposed)
- The CEL environment includes the Kubernetes CEL libraries from
  [`k8s.io/apiserver/pkg/cel/library`][k8s-cel-lib]: regex, lists, URLs,
  quantity and the rest. Custom rules get the same functions users already
  know from `x-kubernetes-validations`.
- The environment is built from `k8s.io/apiserver/pkg/cel/environment` at
  the compatibility version of the Kubernetes libraries the CLI vendors, so
  the set of functions moves with the API server's, not ahead of it.
- Every evaluation runs under a cost limit, set with cel-go's `CostLimit`
  program option to the API server's per-expression limit for validation
  rules (`PerCallLimit`, 1,000,000). An evaluation that exceeds it stops and
  is a runtime error, below.
- The limit is enforced at runtime only. The API server can estimate cost
  before running a rule because a CRD schema bounds every list and string;
  `self` here is a dynamic map with no bounds, so an estimate would reject
  any comprehension, such as `all()` in example e.

**Compilation**
- `filter` and `expr` both compile once, when the config is loaded.
- A compile error in either, or either one not evaluating to bool, is a
  config error.

**Firing**
- A `property` rule fires once for each property where `filter` is true and
  `expr` is false.
- An `xrd` rule fires at most once per XRD.
- A `diff` rule fires once for each paired property path where `filter` is
  true and `expr` is false. It attaches to the property in the new XRD, or
  to its nearest surviving ancestor when the property was removed.
- The schema root has the path `""` and is one of the paired paths, so
  XRD-level comparisons can use `filter: "path == ''"` together with
  `oldXrd` and `newXrd`. Such a rule fires at most once per version pair.

**Runtime errors** (proposed)
- `filter` or `expr` can fail while it is being evaluated, for example on a
  missing key or by exceeding the cost limit. A built-in rule can fail too,
  for example if a crdify comparator returns an error.
- Lint then reports an `error` finding with rule ID `lint/rule-error` that
  names the failing rule, and the whole run fails with a non-zero exit.
- This holds whatever the failing rule's own severity: a `warn` rule that
  cannot be evaluated fails the run just as an `error` rule does. A rule
  that did not run gives no answer, and lint does not pass on a missing
  answer.
- `lint/rule-error` cannot be disabled, lowered or suppressed inline.
- The run never crashes: lint still evaluates the other rules and reports
  all their findings alongside the rule errors.

Custom rules share everything else with built-in rules: severity overrides,
suppression, output and exit codes.

### Custom Rule Examples

Each block below can be pasted under `customRules:` in
`.crossplane-lint.yaml`. Each is followed by a schema fragment it would flag.
Longer expressions use YAML folded scalars (`>-`), which avoids quoting and
escaping problems.

**a. String fields must set `maxLength`** (`property`)

```yaml
  acme/string-max-length:
    severity: warn
    match: property
    filter: "has(self.type) && self.type == 'string'"
    expr: "has(self.maxLength)"
    message: "String fields must set maxLength."
```

```yaml
# flagged: spec.region
spec:
  type: object
  properties:
    region:
      type: string
```

**b. Integer fields must set both `minimum` and `maximum`** (`property`)

```yaml
  acme/integer-bounds:
    severity: warn
    match: property
    filter: "has(self.type) && self.type == 'integer'"
    expr: "has(self.minimum) && has(self.maximum)"
    message: "Integer fields must set both minimum and maximum."
```

```yaml
# flagged: spec.replicas (no maximum)
replicas:
  type: integer
  minimum: 1
```

**c. `*Ref` and `*Name` fields under `spec` must be strings** (`property`, uses `path`)

```yaml
  acme/ref-and-name-are-strings:
    severity: error
    match: property
    filter: "path.matches(r'^spec\\..*(Ref|Name)$')"
    expr: "has(self.type) && self.type == 'string'"
    message: "Fields ending in Ref or Name under spec must be strings."
```

In the double-quoted YAML string above, `\\.` reaches CEL as `\.`, and the raw
CEL string `r'...'` passes it to the regular expression unchanged.

```yaml
# flagged: spec.networkRef
spec:
  type: object
  properties:
    networkRef:
      type: object
      properties:
        id:
          type: string
```

**d. `spec.group` must end with the organisation's domain** (`xrd`)

```yaml
  acme/group-domain:
    severity: error
    match: xrd
    expr: "self.spec.group.endsWith('.example.org')"
    message: "XRD groups must end with .example.org."
```

```yaml
# flagged
apiVersion: apiextensions.crossplane.io/v2
kind: CompositeResourceDefinition
metadata:
  name: xnetworks.platform.acme.io
spec:
  group: platform.acme.io
```

**e. Every served version has a description at the schema root** (`xrd`)

```yaml
  acme/version-root-description:
    severity: warn
    match: xrd
    expr: >-
      self.spec.versions.all(v, !v.served ||
        (has(v.schema) && has(v.schema.openAPIV3Schema) &&
         has(v.schema.openAPIV3Schema.description)))
    message: "Every served version needs a description on its schema root."
```

```yaml
# flagged: v1alpha1 is served and its root has no description
spec:
  versions:
    - name: v1alpha1
      served: true
      referenceable: true
      schema:
        openAPIV3Schema:
          type: object
```

The built-in `xrd-diff/` rules already cover changes to a field's `default`,
bounds, `pattern` and other constraints, so the `diff` examples below cover
what they do not: Crossplane-specific XRD fields, and a narrower policy than a
built-in rule applies.

**f. An XRD's claim names must not change** (`diff`, XRD-level)

```yaml
  acme/claim-names-unchanged:
    severity: error
    match: diff
    filter: >-
      path == '' && has(oldXrd.spec.claimNames)
    expr: >-
      has(newXrd.spec.claimNames) &&
      newXrd.spec.claimNames.kind == oldXrd.spec.claimNames.kind &&
      newXrd.spec.claimNames.plural == oldXrd.spec.claimNames.plural
    message: "Claim names must not be removed or renamed; existing claims would be orphaned."
```

```text
# old                              # new: flagged at the document root
claimNames:                        claimNames:
  kind: Network                      kind: NetworkClaim
  plural: networks                   plural: networkclaims
```

Because the schema root is one of the paired paths, `path == ''` makes the
rule run once per version pair rather than once per property.

**g. A field's description must not be removed** (`diff`)

`xrd-diff/description` flags any change to a description, which is why it is
off by default. A team that only minds descriptions disappearing can say so:

```yaml
  acme/description-not-removed:
    severity: warn
    match: diff
    filter: >-
      old != null && new != null &&
      has(old.description) && old.description != ''
    expr: "has(new.description) && new.description != ''"
    message: "A field's description must not be removed."
```

```text
# old                              # new: flagged at spec.region
region:                            region:
  type: string                       type: string
  description: The cloud region.
```

### Rules in Go

Built-in rules are Go types in an internal package, `internal/xrdlint`.
(proposed) An exported package would be a stability promise made by an alpha
command, so the API stays internal until the command reaches beta. At that
point it can move to `pkg/xrdlint`, so that someone who needs a rule CEL
cannot express can build their own binary, the pattern golangci-lint uses for
module plugins. Until then, such a rule is contributed as a built-in.

```go
type Meta struct {
    ID              string // "xrd/no-bools"
    Description     string
    DocsURL         string
    DefaultSeverity Severity // Off, Warn, Error
    Options         *apiextensionsv1.JSONSchemaProps // nil if the rule takes none
}

// SchemaRule runs on one XRD.
type SchemaRule interface {
    Meta() Meta
    Check(ctx *Context, xrd *XRD)
}

// DiffRule runs on an old/new pair.
type DiffRule interface {
    Meta() Meta
    Compare(ctx *Context, old, new *XRD)
}

func (c *Context) Options(into any) error
func (c *Context) Report(path FieldPath, message string, opts ...ReportOption)
```

**Engine**
- A shared walker visits every schema property and hands rules its path, its
  node, and whether it sits under `status`.
- Most rules are a few lines on top of the walker.

**Positions**
- Lint's own loader parses inputs into `yaml.v3` nodes alongside the
  decoded object.
- A map from field path to node supplies line numbers.
- The same map finds the comments used for suppression. (proposed)

**Testing**
- Rules are tested table-driven, in the spirit of ESLint's `RuleTester`, with
  `xrdlinttest.Run(t, rule, cases)`. The helper lives in
  `internal/xrdlint/xrdlinttest` and moves with the rule package at beta.
  (proposed)
- Each case is a YAML fixture plus the findings it should produce: rule ID,
  field path and, optionally, the message.
- Diff rules take a pair of fixtures.
- The `xrd-diff/` rules backed by crdify are tested on XRD fixtures too, so
  the mapping from XRD schemas to crdify's comparators is covered, not just
  crdify itself.
- Every rule has at least one passing case and one failing case.
- The command is tested end to end on a small fixture directory, covering
  config discovery, exit codes and each output format.

## Alternatives Considered

### Fold Linting into `resource validate`

`resource validate` already loads XRDs, so a linter could be added there as
`--lint`. But validation answers a yes-or-no question against the API
server's rules, while linting is opinion that teams configure. Mixing the two
would blur which failures are real rejections and which are preferences. #301
already drew that line by making structural problems hard errors, and keeping
lint in its own command preserves it.

### Presets and Shareable Configs

ESLint's `extends: [recommended, strict]` was the starting point. Two
problems made per-rule defaults the better choice:
- The names imply nesting. `strict` reads as a superset of `recommended`,
  but the rules actually split by consequence (breaks Crossplane, or does
  not), not by degree.
- With a few dozen rules, a preset adds a layer of indirection without
  saving much configuration.

Presets can be added later if the rule count grows or teams want to share
configs across repositories.

### Runtime Plugins in Go, WASM or as Packages

ESLint loads plugins with `require`. Go's equivalents are each costly:
- `plugin` is fragile across builds.
- Exec and WASM plugins need a protocol and a distribution story.
- Packaging rules as xpkgs, the way functions are packaged, is the most
  Crossplane-native option, but it is a large surface for a first version.

CEL rules in config cover the common case. Once the Go package is exported
at beta, it covers the rest. A plugin mechanism can follow if real demand
appears.

### Our Own Breaking-Change Comparators

The first draft of this proposal defined five `xrd-diff/` rules with
comparators written for lint. crdify already has a larger, tested set, and
its unhandled-change check catches the changes a fixed list misses. Building
on it means less code to maintain and one set of answers across CRDs and
XRDs. crdify is pre-1.0, so lint pins its version, and its comparator names
and options become lint's rule names and options.

### Rego Instead of CEL

Rego is more expressive and familiar to policy teams. But CEL is already a
CLI dependency, and Kubernetes users already write it in XRDs. It is also
non-Turing-complete and cheap to evaluate. A second policy language would
add a dependency to serve a minority of rules.

[cli-69]: https://github.com/crossplane/cli/issues/69
[cli-301]: https://github.com/crossplane/cli/pull/301
[docs-564]: https://github.com/crossplane/docs/issues/564
[crd-checker]: https://github.com/openshift/crd-schema-checker
[ts-eslint]: https://typescript-eslint.io
[kal]: https://github.com/kubernetes-sigs/kube-api-linter
[crdify]: https://github.com/kubernetes-sigs/crdify
[crdify-validations]: https://github.com/kubernetes-sigs/crdify/tree/main/pkg/validations
[k8s-cel-lib]: https://pkg.go.dev/k8s.io/apiserver/pkg/cel/library
