# crossplane-diff integration tests, ported

This package ports crossplane-diff's integration tests (origin/main 6a140b6:
`cmd/diff/diff_integration_test.go`, `diff_it_utils_test.go`, `main_test.go`
and the `testdata` tree) case for case. The case tables are verbatim; only
the harness wiring differs.

## How a case runs

Every case is CLI-style arguments plus an expected exit code and expected
output, funneled through one function: `runSimulate` in
`simulate_harness_test.go`. The harness wires this repository's ported
diff engine (`internal/diff/diffprocessor`) the way crossplane-diff's
`comp` and `xr` commands wire it — same options, same load path, same
`DetermineExitCode` — and reports the exit code the command would exit
with.

When `crossplane project simulate` grows the surface these cases need,
`runSimulate`'s body is replaced with a call to the real command. No case
table changes.

## Skips

Cases are never dropped. A case the current simulate surface cannot
express is ported in full and skipped with a greppable reason:

- `gap:<feature>` — the simulate command lacks the feature. Enable the
  cases by implementing the feature and removing the gap from
  `simulateGap`.
- `env:<reason>` — the environment lacks something. The render engine
  runs functions as Docker containers (no Docker daemon skips the case),
  and envtest needs `KUBEBUILDER_ASSETS` pointing at kube-apiserver/etcd
  binaries.

A small number of skips come from crossplane-diff itself (`skip: true`
in the source tables, with the upstream reason carried verbatim).

## Running

```sh
# Full suite (Docker + envtest assets required):
CROSSPLANE_CRDS_DIR=<crossplane v2.4.1 cluster/crds> \
KUBEBUILDER_ASSETS=$(setup-envtest use -p path) \
go test -tags integration -v ./internal/diff/integration/

# One case:
go test -tags integration -run 'TestCompDiffIntegration/CompositionChangeImpactsXRs' ./internal/diff/integration/
```

The suite is behind the `integration` build tag, so `go test ./...` and
`nix run .#test` do not compile it. `nix run .#integration-test` runs it
with `CROSSPLANE_CRDS_DIR` (the pinned Crossplane v2.4.1 CRDs from
`nix/crossplane-crds.nix`) and `KUBEBUILDER_ASSETS` set for you. Without
Docker or envtest assets, the cases skip with an `env:` reason rather
than fail.
