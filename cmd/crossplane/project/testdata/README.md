# Simulate integration tests

The `integration*_test.go` files in `cmd/crossplane/project` run
`crossplane project simulate -f <project file>` against fixture projects
under `testdata/project`, each against its own envtest cluster seeded from
`testdata/comp/resources`. `TestDiffSingleResourceConcurrent` drives the
diff processor directly with `testdata/diff`.

## How a case runs

A case copies its project fixture to a temp dir and runs the real command
through kong with `runSimulateArgvWith` in
`integration_simulate_harness_test.go`. `simulate.Hooks` points the
command at the case's envtest cluster in place of a kubeconfig, so the
cases run in parallel.

## Skips

A case the environment cannot run skips with an `env:<reason>`: the
render engine runs functions as Docker containers (no Docker daemon skips
the case), and envtest needs `KUBEBUILDER_ASSETS` pointing at
kube-apiserver/etcd binaries.

## Running

```sh
# Full suite (Docker + envtest assets required):
CROSSPLANE_CRDS_DIR=<crossplane v2.4.1 cluster/crds> \
KUBEBUILDER_ASSETS=$(setup-envtest use -p path) \
go test -tags integration -v ./cmd/crossplane/project/

# One case:
go test -tags integration -run 'TestProjectSimulateIntegration/PlainProject' ./cmd/crossplane/project/
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
