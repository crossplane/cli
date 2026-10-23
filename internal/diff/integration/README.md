# crossplane-diff integration tests, ported

This package ports crossplane-diff's integration tests (origin/main 6a140b6:
`cmd/diff/diff_integration_test.go`, `diff_it_utils_test.go`, `main_test.go`
and the `testdata` tree) case for case. The case tables are verbatim; only
the harness wiring differs.

## How a case runs

Every case is crossplane-diff CLI-style arguments plus an expected exit
code and expected output, funneled through one function: `runSimulate` in
`simulate_harness_test.go`. It translates the arguments into
`crossplane project simulate` arguments (input files as `-f`, a single
`--resource` as `--name`, `--include-manual` as `--include-pinned`,
`--analyze-unchanged` as `--analyze-on=always`) and runs the real command
through kong. `project.SimulateHooks` points the command at the case's
envtest cluster in place of a kubeconfig, so the cases run in parallel. Composition cases run simulate's Composition-file
mode, XR cases its composite resource mode.

## Skips

Cases are never dropped. A case the current simulate surface cannot
express is ported in full and skipped with a greppable reason:

- `gap:<feature>` — the simulate command lacks the feature, or behaves
  differently from crossplane-diff. Enable the cases by implementing the
  feature and removing the gap from `simulateGap`.
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

## Coverage

`nix run .#integration-test` writes `coverage-integration.txt`
(`-covermode=count -coverpkg=./...`); CI uploads it to Codecov with flag
`integration`, next to the unit tests' `unittests` flag, and Codecov
merges them per commit. To merge locally with the unit profile from the
sandboxed check:

```sh
nix build .#checks.$(nix eval --raw --impure --expr builtins.currentSystem).test
{ cat result/coverage.txt; tail -n +2 coverage-integration.txt; } > coverage-merged.txt
go tool cover -func=coverage-merged.txt | tail -1
```
