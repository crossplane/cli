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

package integration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestSelectServers(t *testing.T) {
	const store = "/Users/u/Library/Application Support/io.kubebuilder.envtest/k8s/1.32.0-darwin-arm64"

	dirs := []string{store, "/usr/local/kubebuilder/bin"}
	envtest := "etcd --data-dir=" + filepath.Join(os.TempDir(), "k8s_test_framework_123") + " --listen-peer-urls=x"
	apiserver := "kube-apiserver --cert-dir=" + filepath.Join(os.TempDir(), "k8s_test_framework_456")

	// ReapOrphanedEnvtest passes only PPID-1 processes named like a server; the executable path and the command
	// line's envtest data or cert dir decide the rest.
	candidates := []candidate{
		{exe: store + "/kube-apiserver", cmdline: apiserver},                                         // orphaned envtest apiserver
		{exe: store + "/etcd", cmdline: envtest},                                                     // orphaned envtest etcd
		{exe: "/usr/local/kubebuilder/bin/etcd", cmdline: envtest},                                   // orphaned envtest etcd (envtest's default dir)
		{exe: store + "/../1.32.0-darwin-arm64/etcd", cmdline: envtest},                              // unclean path to an envtest binary
		{exe: "/opt/homebrew/bin/etcd", cmdline: envtest},                                            // a real etcd daemon
		{exe: "/usr/local/bin/kube-apiserver", cmdline: envtest},                                     // a real apiserver
		{exe: store + "/kubectl", cmdline: envtest},                                                  // a non-server envtest binary
		{exe: "/usr/local/kubebuilder/bin-x/etcd", cmdline: envtest},                                 // a sibling dir sharing a prefix
		{exe: store + "/sub/etcd", cmdline: envtest},                                                 // nested below an envtest dir
		{exe: "etcd", cmdline: envtest},                                                              // a bare name, not a path
		{exe: "", cmdline: envtest},                                                                  // unknown executable
		{exe: store + "/etcd", cmdline: "etcd --data-dir=/var/lib/etcd"},                             // a system etcd in an envtest dir
		{exe: store + "/kube-apiserver", cmdline: "kube-apiserver --cert-dir=/k8s_test_framework_1"}, // not under TempDir
		{exe: store + "/etcd"},                                                                       // unknown command line
	}

	selected := selectServers(candidates, dirs)

	got := make([]string, 0, len(selected))
	for _, c := range selected {
		got = append(got, c.exe)
	}

	want := []string{
		store + "/kube-apiserver",
		store + "/etcd",
		"/usr/local/kubebuilder/bin/etcd",
		store + "/../1.32.0-darwin-arm64/etcd",
	}

	if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("selectServers() -want +got:\n%s", diff)
	}
}
