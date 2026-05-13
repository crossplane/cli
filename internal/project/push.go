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

package project

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"golang.org/x/sync/errgroup"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	devv1alpha1 "github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/internal/async"
)

// Pusher is able to push a set of packages built from a project to a registry.
type Pusher interface {
	// Push pushes a set of packages built from a project to a registry and
	// returns the tag to which the configuration package was pushed.
	Push(ctx context.Context, project *devv1alpha1.Project, imgMap ImageTagMap, opts ...PushOption) (name.Tag, error)
}

// PusherOption configures a pusher.
type PusherOption func(p *realPusher)

// PushWithTransport sets the HTTP transport to be used by the pusher.
func PushWithTransport(t http.RoundTripper) PusherOption {
	return func(p *realPusher) {
		p.transport = t
	}
}

// PushWithMaxConcurrency sets the maximum concurrency for pushing packages.
func PushWithMaxConcurrency(n uint) PusherOption {
	return func(p *realPusher) {
		p.maxConcurrency = n
	}
}

// PushWithAuthKeychain provides a registry credential source to be used by the
// push.
func PushWithAuthKeychain(kc authn.Keychain) PusherOption {
	return func(p *realPusher) {
		p.keychain = kc
	}
}

// PushOption configures a push.
type PushOption func(o *pushOptions)

type pushOptions struct {
	eventChan async.EventChannel
	tag       string
}

// PushWithEventChannel provides a channel to which progress updates will be
// written during the push. It is the caller's responsibility to manage the
// lifecycle of this channel.
func PushWithEventChannel(ch async.EventChannel) PushOption {
	return func(o *pushOptions) {
		o.eventChan = ch
	}
}

// PushWithTag sets the tag to be used for the pushed packages.
func PushWithTag(tag string) PushOption {
	return func(o *pushOptions) {
		o.tag = tag
	}
}

type realPusher struct {
	keychain       authn.Keychain
	transport      http.RoundTripper
	maxConcurrency uint
}

// Push implements the Pusher interface.
func (p *realPusher) Push(ctx context.Context, project *devv1alpha1.Project, imgMap ImageTagMap, opts ...PushOption) (name.Tag, error) {
	os := &pushOptions{
		tag: fmt.Sprintf("v0.0.0-%d", time.Now().Unix()),
	}
	for _, opt := range opts {
		opt(os)
	}

	imgTag, err := name.NewTag(fmt.Sprintf("%s:%s", project.Spec.Repository, os.tag), name.StrictValidation)
	if err != nil {
		return imgTag, errors.Wrap(err, "failed to construct image tag")
	}

	cfgImage, fnImages, err := SortImages(imgMap, project.Spec.Repository)
	if err != nil {
		return imgTag, err
	}

	// Push all the function packages in parallel.
	eg, egCtx := errgroup.WithContext(ctx)
	sem := make(chan struct{}, p.maxConcurrency)
	for repo, images := range fnImages {
		eg.Go(func() error {
			sem <- struct{}{}
			defer func() {
				<-sem
			}()

			stage := fmt.Sprintf("Pushing function package %s", repo)
			os.eventChan.SendEvent(stage, async.EventStatusStarted)

			tag := repo.Tag(os.tag)
			if err := p.pushIndex(egCtx, tag, images...); err != nil {
				os.eventChan.SendEvent(stage, async.EventStatusFailure)
				return errors.Wrapf(err, "failed to push function %q", repo)
			}
			os.eventChan.SendEvent(stage, async.EventStatusSuccess)
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		return imgTag, err
	}

	// Once the functions are pushed, push the configuration package.
	stage := fmt.Sprintf("Pushing configuration image %s", imgTag)
	os.eventChan.SendEvent(stage, async.EventStatusStarted)
	if err := p.pushImage(ctx, imgTag, cfgImage); err != nil {
		os.eventChan.SendEvent(stage, async.EventStatusFailure)
		return imgTag, errors.Wrap(err, "failed to push configuration package")
	}
	os.eventChan.SendEvent(stage, async.EventStatusSuccess)

	return imgTag, nil
}

func (p *realPusher) pushIndex(ctx context.Context, tag name.Tag, imgs ...v1.Image) error {
	// Build an index. This is a little superfluous if there's only one image
	// (single architecture), but we generate configuration dependencies on
	// embedded functions assuming there's an index, so we push an index
	// regardless of whether we really need one.
	idx, imgs, err := BuildIndex(imgs...)
	if err != nil {
		return err
	}

	// Push the images by digest.
	repo := tag.Repository
	for _, img := range imgs {
		dgst, err := img.Digest()
		if err != nil {
			return err
		}
		if err := p.pushImage(ctx, repo.Digest(dgst.String()), img); err != nil {
			return err
		}
	}

	// Tag the function the same as the configuration. The configuration depends
	// on it by digest, so this isn't necessary for things to work correctly,
	// but it makes the user experience more intuitive.
	return remote.WriteIndex(tag, idx,
		remote.WithAuthFromKeychain(p.keychain),
		remote.WithContext(ctx),
		remote.WithTransport(p.transport),
	)
}

func (p *realPusher) pushImage(ctx context.Context, ref name.Reference, img v1.Image) error {
	img, err := AnnotateImage(img)
	if err != nil {
		return err
	}

	return remote.Write(ref, img,
		remote.WithAuthFromKeychain(p.keychain),
		remote.WithContext(ctx),
		remote.WithTransport(p.transport),
	)
}

// NewPusher returns a new project Pusher.
func NewPusher(opts ...PusherOption) Pusher {
	p := &realPusher{
		transport:      http.DefaultTransport,
		maxConcurrency: 8,
		keychain:       authn.DefaultKeychain,
	}

	for _, opt := range opts {
		opt(p)
	}

	return p
}
