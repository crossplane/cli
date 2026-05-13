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

package xpkg

import (
	"context"
	"net/http"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// RemoteFetcher implements a local (non-Kubernetes) xpkg.Fetcher. Pull secret
// arguments are accepted but ignored since there is no Kubernetes API to
// resolve them against in the CLI context.
type RemoteFetcher struct {
	keychain  authn.Keychain
	transport http.RoundTripper
	userAgent string
}

// RemoteFetcherOption configures a RemoteFetcher.
type RemoteFetcherOption func(*RemoteFetcher)

// WithKeychain sets the authn.Keychain used to authenticate registry
// requests. Defaults to authn.DefaultKeychain.
func WithKeychain(k authn.Keychain) RemoteFetcherOption {
	return func(f *RemoteFetcher) { f.keychain = k }
}

// WithUserAgent sets the User-Agent header sent on registry requests.
func WithUserAgent(ua string) RemoteFetcherOption {
	return func(f *RemoteFetcher) { f.userAgent = ua }
}

// WithTransport sets the http.RoundTripper used for registry requests.
// Defaults to remote.DefaultTransport.
func WithTransport(t http.RoundTripper) RemoteFetcherOption {
	return func(f *RemoteFetcher) { f.transport = t }
}

// NewRemoteFetcher returns a RemoteFetcher with the given options applied.
func NewRemoteFetcher(opts ...RemoteFetcherOption) *RemoteFetcher {
	f := &RemoteFetcher{
		keychain:  authn.DefaultKeychain,
		transport: remote.DefaultTransport,
	}
	for _, o := range opts {
		o(f)
	}
	return f
}

// Fetch retrieves a package image from the registry.
func (f *RemoteFetcher) Fetch(ctx context.Context, ref name.Reference, _ ...string) (v1.Image, error) {
	return remote.Image(ref, f.commonOpts(ctx)...)
}

// Head retrieves an image descriptor, falling back to a GET if the registry
// rejects HEAD.
func (f *RemoteFetcher) Head(ctx context.Context, ref name.Reference, _ ...string) (*v1.Descriptor, error) {
	d, err := remote.Head(ref, f.commonOpts(ctx)...)
	if err == nil && d != nil {
		return d, nil
	}

	rd, gerr := remote.Get(ref, f.commonOpts(ctx)...)
	if gerr != nil {
		if err != nil {
			return nil, err
		}
		return nil, gerr
	}

	return &rd.Descriptor, nil
}

// Tags lists tags for a package source.
func (f *RemoteFetcher) Tags(ctx context.Context, ref name.Reference, _ ...string) ([]string, error) {
	return remote.List(ref.Context(), f.commonOpts(ctx)...)
}

func (f *RemoteFetcher) commonOpts(ctx context.Context) []remote.Option {
	return []remote.Option{
		remote.WithAuthFromKeychain(f.keychain),
		remote.WithTransport(f.transport),
		remote.WithContext(ctx),
		remote.WithUserAgent(f.userAgent),
	}
}
