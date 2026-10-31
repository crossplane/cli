/*
Copyright 2026 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package integration provides fixtures for integration tests that run Crossplane CLI commands against an
// envtest API server (a real kube-apiserver and etcd, with no controllers).
//
// Start starts a cluster serving Crossplane's CRDs and the XR and claim CRDs generated from a test's XRDs, and
// sets up the test's manifests as Crossplane would have left them. ReapOrphanedEnvtest, called from TestMain,
// kills envtest servers an earlier run left behind.
//
// Start and the fixtures behind it build only with the integration tag, as do the tests that use them, since they
// need envtest's binaries. Run them with `nix run .#integration-test`, which provides those binaries and
// Crossplane's CRDs at the version go.mod pins; Start fails, rather than skips, a test run without them.
// CRDsForXRD, PinnedCrossplaneCRDsDir and ReapOrphanedEnvtest are untagged, so their own tests run with the unit tests.
//
// Integration tests are any test files with `//go:build integration`. The integration-test app runs
// `go test -tags integration` on every package containing one, so unit tests in those packages also run there.
package integration
