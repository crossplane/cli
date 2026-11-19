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

package common

import (
	"github.com/crossplane/cli/v2/internal/dependency"
	"github.com/crossplane/cli/v2/internal/git"
)

// GitFlags configures authentication and connectivity for git-based
// dependencies. It is embedded in every command that resolves project
// dependencies, so users can pass the same settings across the CLI.
type GitFlags struct {
	GitUsername        string `default:"x-access-token"               env:"CROSSPLANE_GIT_USERNAME"                                                                                              help:"Username for git HTTPS authentication."`
	GitToken           string `env:"CROSSPLANE_GIT_TOKEN"             help:"Token for git HTTPS authentication."`
	GitProxy           string `env:"CROSSPLANE_GIT_PROXY"             help:"Proxy URL to tunnel git operations through, for example http://proxy:8080. Supports HTTP CONNECT for SSH tunneling."`
	GitInsecureHostKey bool   `env:"CROSSPLANE_GIT_INSECURE_HOST_KEY" help:"Skip SSH host key verification for git operations. Only use if you understand the MITM risks."`
}

// ManagerOptions returns the dependency manager options the flags configure.
func (f GitFlags) ManagerOptions() []dependency.ManagerOption {
	var opts []dependency.ManagerOption
	if f.GitToken != "" {
		opts = append(opts, dependency.WithGitAuthProvider(&git.HTTPSAuthProvider{
			Username: f.GitUsername,
			Password: f.GitToken,
		}))
	}
	if f.GitProxy != "" {
		opts = append(opts, dependency.WithGitProxyURL(f.GitProxy))
	}
	if f.GitInsecureHostKey {
		opts = append(opts, dependency.WithGitInsecureHostKey(true))
	}
	return opts
}
