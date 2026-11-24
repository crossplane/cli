These flags tune the diff:

- `--ignore-paths` leaves paths out of the diff.
- `--function-credentials` passes credentials to the functions the render runs:
  a YAML file, or the top-level YAML files of a directory, that must contain
  only Secrets.
- `--max-nested-depth` limits nested composite resource recursion.
- `--max-iterations` caps the render passes per composite resource that
  resolve the resources its functions require.
- `--dry-run-on` picks which resources the server-side dry run verifies:
  `existing` only checks resources already in the cluster; `all`, the default,
  also dry-run creates added resources, so their diffs include server-side
  defaulting and admission. `all` needs the `create` verb and falls back per
  resource without it.
- `--field-manager` and `--server-side` say how the input composite resources
  and claims are applied, as `kubectl apply`'s flags of the same names do, so
  the diff shows the fields that applying them would remove. Unset, they're
  inferred from each one's `managedFields`: the applier is its only field
  manager with an `Apply` entry, other than Crossplane's, or client-side apply
  if it has only a `kubectl.kubernetes.io/last-applied-configuration`
  annotation. With more than one candidate, or an imperative writer (a
  manager, other than Crossplane's, that wrote it with an `Update`, such as
  `kubectl create` or `edit` or a controller, on an object without that
  annotation), the command warns that dropped fields may not be shown
  removed. `--server-side` alone picks the only server-side applier, unless
  an imperative writer wrote it; `--server-side --field-manager M`
  server-side applies as M; `--server-side=false` is client-side apply;
  `--field-manager M` alone is server-side apply as M if M has an `Apply`
  entry, otherwise client-side. An explicit `--field-manager` is used even
  when an imperative writer wrote the resource.
  They only affect the composite resources and claims `xr simulate` takes as
  input, never composed resources.
- `--eventual-state` shows the state after all reconciliation cycles complete.
- `--max-recv-message-size` raises the function gRPC message limit, in MB.
- `--no-color` and `--compact` change how `-o diff` prints.
