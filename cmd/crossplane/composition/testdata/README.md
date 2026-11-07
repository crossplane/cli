# composition simulate integration tests

`integration_test.go` runs `TestCompDiffIntegration`: crossplane-diff's composition cases, run as
`crossplane composition simulate -f <Composition files>` against an envtest cluster. A case keeps its
crossplane-diff args; `simharness`'s `RunIntegrationTest` translates them
into the command's flags and runs it. Fixtures only this suite uses are in
`testdata/comp`; those shared with another suite are in
`internal/testutils/integration/testdata/comp` (and `crds`).

The skips, how to run the suite and its coverage are in
`cmd/crossplane/project/testdata/README.md`. Run only this suite with
`go test -tags integration -run TestCompDiffIntegration ./cmd/crossplane/composition/`.
