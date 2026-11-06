These flags choose which composite resources a Composition change reaches, and
how the change prints:

- `--namespace` (`-n`) simulates only composite resources in one namespace.
- `--name` takes `[namespace/]name` and simulates only that composite
  resource. It's repeatable or comma-separated. A bare name pairs with
  `--namespace`, or is cluster scoped without it.
- `--include-pinned` renders every pinned composite resource against the new
  Composition. By default `simulate` skips composite resources with a `Manual`
  composition update policy, because they don't adopt a new Composition
  revision on their own. A pinned composite whose revision calls a function
  the run changes is still simulated, rendered against the revision it's
  pinned to.
- `--analyze-on` sets the smallest Composition change that triggers impact
  analysis: `spec-change` only when the Composition spec changes;
  `any-change`, the default, also when only its metadata changes, which still
  creates a new CompositionRevision that composites re-point to; `always` even
  when the Composition is identical, for a pre-edit convergence baseline.
- `--minimize-composition` collapses each changed Composition to one line in
  `-o diff` output. Errors and Compositions with no changes still print in
  full.

When a run reaches both a claim and its composite resource, the change is
reported under the claim. If the claim is skipped because it uses the Manual
update policy but its composite resource is pinned to a revision that calls a
changed function, the change is reported under the composite resource.
