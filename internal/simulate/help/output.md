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
  doesn't recognize. That status is the simulated outcome. Separately,
  `health` reports the live object's state before apply: `health.state` is
  `failing` (its Synced condition is False), `notReady` (its Ready condition
  is False), `paused` (it carries the `crossplane.io/paused: "true"`
  annotation, whatever its conditions), or `healthy`, with the `synced` and
  `ready` conditions it was read from. A claim's `health` is its backing
  composite resource's. A composite or plan has no `health` when there is no
  live object, as for a create, or, for a composite, when it was not
  simulated (`error`, `skipped`, `deferred`). Each composite also lists under
  `unhealthy` the composed resources that are not healthy and have no plan of
  their own, each once. That includes nested composites and what they compose;
  an entry a nested composite composes names it under `via`. `summary.health`
  counts the composites that are not healthy (`unhealthyComposites`), and the
  other resources that are `failing`, `notReady` or `paused`, each once.
- `diff` prints a line diff of each changed resource's manifest. Every
  Secret value, and any value a provider marks sensitive, reads
  `(sensitive value)`; a value the provider computes on apply reads
  `(known after apply)`; and a field that forces replacement ends
  `# forces replacement`.

The `--timeout` flag, 2m by default, bounds the time spent simulating one
composite resource.
