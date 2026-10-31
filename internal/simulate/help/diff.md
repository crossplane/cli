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
- `--eventual-state` shows the state after all reconciliation cycles complete.
- `--max-recv-message-size` raises the function gRPC message limit, in MB.
- `--no-color` and `--compact` change how `-o diff` prints.
