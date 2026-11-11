# xr simulate integration tests

`integration_test.go` runs `TestDiffIntegration`: crossplane-diff's xr cases, run as
`crossplane xr simulate -f <XR files>` against an envtest cluster. A case keeps its
crossplane-diff args; `simharness`'s `RunIntegrationTest` translates them
into the command's flags and runs it. Fixtures only this suite uses are in
`testdata/diff`; those shared with another suite are in
`internal/testutils/integration/testdata/diff` (and `crds`).

The skips, how to run the suite and its coverage are in
`cmd/crossplane/project/testdata/README.md`. Run only this suite with
`go test -tags integration -run TestDiffIntegration ./cmd/crossplane/xr/`.
