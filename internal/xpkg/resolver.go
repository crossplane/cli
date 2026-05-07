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
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/google/go-containerregistry/pkg/name"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"
)

// Resolver translates a CLI-style package reference into a fully qualified OCI
// ref, returning the resolved semantic version or tag where applicable.
//
// It handles four shapes:
//
//   - pkg@digest          → returned unchanged, version=""
//   - pkg:<exact-tag>     → returned unchanged, version=<exact-tag>
//   - pkg:<constraint>    → tags listed via ListVersions, highest match wins
//
// Opaque (non-semver, non-constraint) tags are returned unchanged with
// version=tag.
type Resolver struct {
	client xpkg.Client
}

// NewResolver returns a Resolver backed by client.
func NewResolver(client xpkg.Client) *Resolver {
	return &Resolver{client: client}
}

// Resolve returns the resolved reference and the exact version tag extracted
// from it (empty for digest refs and bare sources).
func (r *Resolver) Resolve(ctx context.Context, ref string) (name.Reference, string, error) {
	// If the ref is specified by digest it doesn't need to be resolved. Return
	// it verbatim.
	if dgst, err := name.NewDigest(ref, name.StrictValidation); err == nil {
		return dgst, "", nil
	}

	// The registry part of a ref can contain a colon (for a port number), so
	// look for the *last* colon, which should separate the repository from the
	// tag.
	parts := strings.Split(ref, ":")
	if len(parts) < 2 {
		return nil, "", errors.Errorf("ref %s is missing a version", ref)
	}

	tagOrConstraint := parts[len(parts)-1]
	repo, err := name.NewRepository(strings.Join(parts[:len(parts)-1], ":"), name.StrictValidation)
	if err != nil {
		return nil, "", errors.Wrapf(err, "ref %s has an invalid repository", ref)
	}

	sc, err := semver.NewConstraint(tagOrConstraint)
	if err != nil {
		// Not a constraint - treat as an opaque tag.
		tag, err := name.NewTag(ref, name.StrictValidation)
		if err != nil {
			return nil, "", errors.Wrapf(err, "invalid ref %s", ref)
		}
		return tag, tag.TagStr(), nil
	}

	tags, err := r.client.ListVersions(ctx, repo.Name())
	if err != nil {
		return nil, "", errors.Wrapf(err, "cannot list versions for %s", repo)
	}
	v := highestSatisfying(tags, sc)
	if v == "" {
		return nil, "", errors.Errorf("cannot find version to satisfy constraint %s", sc)
	}

	return repo.Tag(v), v, nil
}

// highestSatisfying returns the original-form string of the highest
// version in tags that satisfies c, or "" if none matches.
func highestSatisfying(tags []string, c *semver.Constraints) string {
	vs := make(semver.Collection, 0, len(tags))
	for _, t := range tags {
		v, err := semver.NewVersion(t)
		if err != nil {
			continue
		}
		vs = append(vs, v)
	}

	sort.Sort(sort.Reverse(vs))

	for _, v := range vs {
		if c.Check(v) {
			return v.Original()
		}
	}

	return ""
}
