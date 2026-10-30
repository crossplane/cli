The `xrd lint` command checks CompositeResourceDefinitions (XRDs) against a set
of rules for good API design, and reports each finding with its rule, severity
and location.

Before any rule runs, each XRD gets the same structural check `xrd convert`
applies. An XRD that fails it is reported as `lint/invalid-xrd`, and no other
rule runs on it.

Rules on by default:

* `xrd/status-fields-optional` (error): no field under `status` is required.
* `xrd/status-no-value-validation` (error): no minimum, maximum, pattern,
  enum, length or item limits under `status`.
* `xrd/lists-need-list-type` (error): every array sets
  `x-kubernetes-list-type`.
* `xrd/no-bools` (warn): no `type: boolean`; prefer an enum.

The report is always written before the command exits. The exit status is:

* 0: no error findings, and warnings within `--max-warnings`.
* 1: lint could not do its job: the config is invalid, an input cannot be
  read, the inputs hold no XRDs, or a rule failed to run (`lint/rule-error`).
  The reason is printed to stderr.
* 2: an input XRD is invalid or not structural (`lint/invalid-xrd`).
* 3: lint ran and found problems: an error finding, or more warnings than
  `--max-warnings` allows.

When several apply, the lowest non-zero status wins. Statuses 2 and 3 print
nothing beyond the report.

## Configuration

The command searches upward from the working directory for
`.crossplane-lint.yaml`; `--lint-config` overrides the search. The file holds
only overrides of each rule's default severity (`off`, `warn` or `error`):

```yaml
apiVersion: lint.crossplane.io/v1alpha1
kind: LintConfig
rules:
  xrd/no-bools:
    severity: error
  xrd/lists-need-list-type:
    severity: warn
```

A rule value is always an object with a required `severity` and, for a rule
that takes them, `options`. A bare severity such as `xrd/no-bools: error`, a
list, unknown keys and unknown rule names are errors.

## Suppressing findings

A comment suppresses the named rules on the schema node that starts on the
next line, and on everything beneath it. A comment that suppresses nothing is
reported as `lint/unused-disable`.

```yaml
properties:
  # crossplane-lint-disable-next-line xrd/no-bools -- kept for v1 compatibility
  enabled:
    type: boolean
```

## Examples

Lint every XRD under a directory:

```shell
crossplane xrd lint apis/
```

Lint an XRD from stdin and print the findings as JSON:

```shell
cat definition.yaml | crossplane xrd lint - -o json
```

Fail CI on any warning:

```shell
crossplane xrd lint apis/ --max-warnings=0
```
