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

package git

// HTTPSAuthFlags are the flags for git HTTPS authentication. Embed it into a
// command's Kong flag struct with the `embed:""` tag, then pass AuthProvider to
// the dependency manager.
type HTTPSAuthFlags struct {
	GitToken    string `env:"CROSSPLANE_GIT_TOKEN" help:"Token for git HTTPS authentication."`
	GitUsername string `default:"x-access-token"   env:"CROSSPLANE_GIT_USERNAME"              help:"Username for git HTTPS authentication."`
}

// AuthProvider returns the auth provider configured by the flags.
func (f HTTPSAuthFlags) AuthProvider() AuthProvider {
	return &HTTPSAuthProvider{Username: f.GitUsername, Password: f.GitToken}
}
