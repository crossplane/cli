The `-o` flag picks the output format:

- `plan`, the default, prints each change as a field-level plan, grouped under
  the composite resource it belongs to. When the run simulates a changed
  Composition, the Composition prints first, with whether applying it creates
  a new Composition revision.
- `json` and `yaml` print the same plan as a structured result, for CI and
  other tools. Each plan carries the objects it compares, as `before` (the
  live object, absent for a create) and `after` (the rendered object, absent
  for a delete), and neither when the cluster diff found the resource
  unchanged, such as drift only the provider plan found. Both are without
  status, server-managed metadata and ignored paths, and with Secret values
  redacted. For a resource the provider planned,
  `after` is the manifest sent to it; values the provider computes appear only
  in the changes.
  Each composite has a `status`: `changed`, `unchanged`, `error`, `deferred`,
  `skipped`, or `unknown` when the engine reports a status this version
  doesn't recognize.
- `diff` prints a line diff of each changed resource's manifest. Every
  Secret value, and any value a provider marks sensitive, reads
  `(sensitive value)`; a value the provider computes on apply reads
  `(known after apply)`; and a field that forces replacement ends
  `# forces replacement`.

The `--timeout` flag, 2m by default, bounds the time spent simulating one
composite resource.
