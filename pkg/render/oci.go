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

package render

import (
	"context"

	"github.com/Masterminds/semver/v3"
	"github.com/google/go-containerregistry/pkg/name"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"
)

// CheckOCIFunctionSupport returns an error if the supplied engine renders with
// a version of Crossplane that doesn't support pipeline steps that reference
// functions by OCI reference.
func CheckOCIFunctionSupport(ctx context.Context, e Engine, log logging.Logger) error {
	minVersion := semver.MustParse("v2.5.0")

	raw, err := e.Version(ctx)
	if err != nil {
		return errors.Wrap(err, "cannot determine whether the render engine supports functions referenced by OCI reference")
	}

	v, err := semver.NewVersion(raw)
	if err != nil {
		log.Debug("Cannot parse render engine version", "version", raw, "error", err)
		return nil
	}

	if v.LessThan(minVersion) {
		return errors.Errorf("referencing functions by OCI ref requires Crossplane %s or later; %s selected", minVersion, raw)
	}

	return nil
}

// OCIRefsToFunctions creates synthetic Function resources for the given OCI
// refs, such that render will use them to satisfy OCI ref pipeline steps.
func OCIRefsToFunctions(refs []string) ([]pkgv1.Function, error) {
	fns := make([]pkgv1.Function, len(refs))

	for i, ref := range refs {
		name, err := normalizeOCIRef(ref)
		if err != nil {
			return nil, errors.Wrapf(err, "cannot normalize OCI ref %s to name function", ref)
		}

		fns[i].SetGroupVersionKind(pkgv1.FunctionGroupVersionKind)
		fns[i].SetName(name)
		fns[i].Spec.Package = ref
	}

	return fns, nil
}

func normalizeOCIRef(ref string) (string, error) {
	d, err := name.NewDigest(ref, name.StrictValidation)
	if err != nil {
		return "", err
	}

	return d.Context().Digest(d.DigestStr()).String(), nil
}
