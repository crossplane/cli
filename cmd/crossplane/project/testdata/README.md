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
render engine runs functions as Docker containers, so no Docker daemon
skips the case. Each case starts its cluster with
`internal/testutils/integration`'s `Start`, which fails the case, rather
than skipping it, when `KUBEBUILDER_ASSETS` or `CROSSPLANE_CRDS_DIR` is
unset.

## Running

```sh
# Full suite (Docker + envtest assets required):
CROSSPLANE_CRDS_DIR=<crossplane cluster/crds at go.mod's crossplane/apis version> \
KUBEBUILDER_ASSETS=$(setup-envtest use -p path) \
go test -tags integration -v ./cmd/crossplane/project/

# One case:
go test -tags integration -run 'TestProjectSimulateIntegration/PlainProject' ./cmd/crossplane/project/
```

The suite is behind the `integration` build tag, so `go test ./...` and
`nix run .#test` do not compile it. `nix run .#integration-test` runs it
with `CROSSPLANE_CRDS_DIR` (the Crossplane CRDs `nix/crossplane-crds.nix`
pins) and `KUBEBUILDER_ASSETS` set for you. Without Docker, the cases skip
with an `env:` reason rather than fail.

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
