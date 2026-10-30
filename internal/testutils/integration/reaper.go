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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/shirou/gopsutil/v4/process"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// isServer reports whether name is that of a long-running server envtest starts.
func isServer(name string) bool {
	return name == "kube-apiserver" || name == "etcd"
}

// candidate is an orphaned process named like an envtest server.
type candidate struct {
	proc *process.Process
	// exe is the absolute path of the process's executable.
	exe string
	// cmdline is the process's command line.
	cmdline string
}

// ReapOrphanedEnvtest SIGKILLs every envtest server orphaned by an earlier run, and reports through logf what it
// killed and what it could not. Call it from TestMain, before m.Run.
//
// envtest runs kube-apiserver and etcd as children of the test binary, and only a deferred Environment.Stop ends
// them. When the binary dies without running its defers (the -timeout panic, a kill, a Ctrl+C), they are reparented
// to PID 1 (or to a subreaper, in which case they are not reaped) and run until someone kills them, piling up across
// runs (crossplane-contrib/crossplane-diff#524). A process whose parent is PID 1 has lost the test binary that
// started it, so it cannot belong to a live run.
// ReapOrphanedEnvtest is best-effort: it never fails, and it skips any process it cannot inspect.
func ReapOrphanedEnvtest(logf func(format string, args ...any)) {
	pids, err := process.Pids()
	if err != nil {
		logf("envtest reaper: cannot list processes: %v", err)
		return
	}

	// Look up the executable only of orphans named like an envtest server.
	var candidates []candidate

	for _, pid := range pids {
		// Not process.NewProcess, which first checks the PID exists and fetches its creation time: most of the
		// scan's cost, for nothing Kill uses. A PID that has exited meanwhile fails Ppid and is skipped.
		p := &process.Process{Pid: pid}

		if ppid, err := p.Ppid(); err != nil || ppid != 1 {
			continue
		}

		if name, err := p.Name(); err != nil || !isServer(name) {
			continue
		}

		exe, err := p.Exe()
		if err != nil {
			continue
		}

		if cmdline, err := p.Cmdline(); err == nil {
			candidates = append(candidates, candidate{proc: p, exe: exe, cmdline: cmdline})
		}
	}

	var killed []string

	for _, c := range selectServers(candidates, binaryDirs()) {
		if err := c.proc.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			logf("envtest reaper: cannot kill %s (pid %d): %v", c.exe, c.proc.Pid, err)
			continue
		}

		killed = append(killed, fmt.Sprintf("%s (pid %d)", filepath.Base(c.exe), c.proc.Pid))
	}

	if len(killed) > 0 {
		logf("envtest reaper: killed %d envtest server(s) orphaned by an earlier run: %s",
			len(killed), strings.Join(killed, ", "))
	}
}

// selectServers returns the candidates whose executable is an envtest server directly in one of dirs, and whose
// command line carries envtest's fingerprint (hasEnvtestDir), so a system etcd or kube-apiserver in a shared dir
// that KUBEBUILDER_ASSETS names is spared.
func selectServers(candidates []candidate, dirs []string) []candidate {
	var selected []candidate

	for _, c := range candidates {
		exe := filepath.Clean(c.exe)
		if filepath.IsAbs(exe) && isServer(filepath.Base(exe)) && slices.Contains(dirs, filepath.Dir(exe)) &&
			hasEnvtestDir(c.cmdline) {
			selected = append(selected, c)
		}
	}

	return selected
}

// hasEnvtestDir reports whether cmdline passes etcd a --data-dir, or kube-apiserver a --cert-dir, that envtest
// created. envtest creates them when they are unset, as Start leaves them: controller-runtime's process.go uses
// os.MkdirTemp("", "k8s_test_framework_").
func hasEnvtestDir(cmdline string) bool {
	envtestDirPrefix := filepath.Join(os.TempDir(), "k8s_test_framework_")

	for arg := range strings.FieldsSeq(cmdline) {
		for _, flag := range []string{"--data-dir=", "--cert-dir="} {
			if dir, ok := strings.CutPrefix(arg, flag); ok && strings.HasPrefix(dir, envtestDirPrefix) {
				return true
			}
		}
	}

	return false
}

// binaryDirs returns every directory a test run could have started envtest's servers from: KUBEBUILDER_ASSETS, each
// version in setup-envtest's store, and envtest's built-in default, /usr/local/kubebuilder/bin. All are
// included, not only the one envtest would use now, because an orphan may come from a run configured differently.
// Each is also included with its symlinks resolved, as the process table may report the resolved path.
func binaryDirs() []string {
	dirs := []string{"/usr/local/kubebuilder/bin"}

	if v := os.Getenv("KUBEBUILDER_ASSETS"); v != "" {
		dirs = append(dirs, filepath.Clean(v))
	}

	if store, err := envtest.SetupEnvtestDefaultBinaryAssetsDirectory(); err == nil {
		versions, _ := os.ReadDir(store) // A missing store has no versions.
		for _, v := range versions {
			dirs = append(dirs, filepath.Join(store, v.Name()))
		}
	}

	for _, d := range dirs {
		if r, err := filepath.EvalSymlinks(d); err == nil && r != d {
			dirs = append(dirs, r)
		}
	}

	return dirs
}
