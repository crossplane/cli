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

// Package git contains functions to interact with repos.
package git

import (
	"strings"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/go-git/go-git/v5/storage"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

// CloneOptions configure for git actions.
type CloneOptions struct {
	Repo      string
	RefName   string
	Directory string
	Path      string // Optional path for sparse checkout
}

// AuthProvider wraps a specific auth method.
type AuthProvider interface {
	GetAuthMethod() (transport.AuthMethod, error)
}

// HTTPSAuthProvider provides authentication for HTTPS repositories.
type HTTPSAuthProvider struct {
	Username string
	Password string
}

// GetAuthMethod returns the HTTP BasicAuth transport method.
func (a *HTTPSAuthProvider) GetAuthMethod() (transport.AuthMethod, error) {
	if a.Username != "" || a.Password != "" {
		return &http.BasicAuth{Username: a.Username, Password: a.Password}, nil
	}
	// Return nil authenticator to allow anonymous cloning.
	return nil, nil
}

// SSHAuthProvider provides authentication for SSH repositories.
type SSHAuthProvider struct {
	Username       string
	PrivateKeyPath string
	Passphrase     string
}

// GetAuthMethod returns the SSH PublicKey transport method.
func (a *SSHAuthProvider) GetAuthMethod() (transport.AuthMethod, error) {
	username := a.Username
	if username == "" {
		username = "git"
	}

	authMethod, err := ssh.NewPublicKeysFromFile(username, a.PrivateKeyPath, a.Passphrase)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create SSH public key auth method for user %q", username)
	}

	return authMethod, nil
}

// SSHAgentAuthProvider provides authentication using the SSH agent.
type SSHAgentAuthProvider struct {
	Username string
}

// GetAuthMethod returns the SSH agent auth method.
func (a *SSHAgentAuthProvider) GetAuthMethod() (transport.AuthMethod, error) {
	username := a.Username
	if username == "" {
		username = "git"
	}
	return ssh.NewSSHAgentAuth(username)
}

// CompositeAuthProvider tries multiple auth providers in order until one succeeds.
type CompositeAuthProvider struct {
	Providers []AuthProvider
}

// GetAuthMethod returns the first successful auth method.
func (c *CompositeAuthProvider) GetAuthMethod() (transport.AuthMethod, error) {
	var lastErr error
	for _, p := range c.Providers {
		method, err := p.GetAuthMethod()
		if err == nil {
			return method, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("no auth providers configured")
}

// Cloner can clone git repositories with (optional) authentication.
type Cloner interface {
	CloneRepository(store storage.Storer, fs billy.Filesystem, auth AuthProvider, opts CloneOptions) (*plumbing.Reference, error)
}

// DefaultCloner is the default implementation of Cloner.
type DefaultCloner struct{}

// CheckSHA1Hash checks if a string is a valid git SHA hash (40 hex characters).
func CheckSHA1Hash(ref string) bool {
	if len(ref) != 40 {
		return false
	}

	for _, c := range ref {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}

	return true
}

func extractBranchName(ref string) string {
	if strings.HasPrefix(ref, "refs/heads/") {
		return ref[11:]
	}
	if ref != "" && !strings.HasPrefix(ref, "refs/") {
		return ref
	}
	return "main"
}

func handleSHACheckout(repoObj *git.Repository, authMethod transport.AuthMethod, sha string, sparsePath string) error {
	err := repoObj.Fetch(&git.FetchOptions{
		Auth: authMethod,
		RefSpecs: []config.RefSpec{
			config.RefSpec("+refs/heads/*:refs/remotes/origin/*"),
		},
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return errors.Wrap(err, "failed to fetch refs")
	}

	worktree, err := repoObj.Worktree()
	if err != nil {
		return errors.Wrap(err, "failed to get worktree")
	}

	checkoutOpts := &git.CheckoutOptions{
		Hash: plumbing.NewHash(sha),
	}
	if sparsePath != "" {
		checkoutOpts.SparseCheckoutDirectories = []string{sparsePath}
	}

	if err := worktree.Checkout(checkoutOpts); err != nil {
		if sparsePath != "" {
			return errors.Wrapf(err, "failed to sparse checkout commit %s with path %q", sha, sparsePath)
		}
		return errors.Wrapf(err, "failed to checkout commit %s", sha)
	}

	return nil
}

// CloneRepository clones a git repository using the provided CloneOptions and AuthProvider.
func (dc *DefaultCloner) CloneRepository(store storage.Storer, fs billy.Filesystem, auth AuthProvider, opts CloneOptions) (*plumbing.Reference, error) {
	authMethod, err := auth.GetAuthMethod()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get authentication method")
	}

	isTag := strings.HasPrefix(opts.RefName, "refs/tags/")

	refToCheck := strings.TrimPrefix(opts.RefName, "refs/heads/")
	isSHA := CheckSHA1Hash(refToCheck)

	cloneOptions := &git.CloneOptions{
		URL:        opts.Repo,
		Depth:      1,
		Auth:       authMethod,
		NoCheckout: opts.Path != "",
		Tags:       git.NoTags,
	}

	if !isSHA {
		cloneOptions.ReferenceName = plumbing.ReferenceName(opts.RefName)
		cloneOptions.SingleBranch = !isTag
	}

	repoObj, err := git.Clone(store, fs, cloneOptions)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to clone repository from %q", opts.Repo)
	}

	if isSHA {
		if err := handleSHACheckout(repoObj, authMethod, refToCheck, opts.Path); err != nil {
			return nil, err
		}
	}

	if opts.Path != "" && !isSHA {
		worktree, err := repoObj.Worktree()
		if err != nil {
			return nil, errors.Wrap(err, "failed to get worktree")
		}

		var checkoutRef plumbing.ReferenceName
		switch {
		case isTag:
			checkoutRef = plumbing.ReferenceName(opts.RefName)
		default:
			branchName := extractBranchName(opts.RefName)
			checkoutRef = plumbing.ReferenceName("refs/remotes/origin/" + branchName)
		}

		checkoutOptions := &git.CheckoutOptions{
			Branch:                    checkoutRef,
			SparseCheckoutDirectories: []string{opts.Path},
		}

		if err := worktree.Checkout(checkoutOptions); err != nil {
			return nil, errors.Wrapf(err, "failed to sparse checkout path %q", opts.Path)
		}
	}

	ref, err := repoObj.Head()
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get repository's HEAD from %q", opts.Repo)
	}
	return ref, nil
}
