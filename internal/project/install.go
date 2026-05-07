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
	"encoding/json"
	"math"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/google/go-containerregistry/pkg/name"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"

	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
	xpkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"
	xpkgv1beta1 "github.com/crossplane/crossplane/apis/v2/pkg/v1beta1"
)

// InstallConfiguration installs a Configuration package on the target control
// plane and waits for it and all its dependencies to become healthy.
func InstallConfiguration(ctx context.Context, cl client.Client, cfgName string, tag name.Tag, logger logging.Logger) error {
	pkgSource := tag.String()
	cfg := &xpkgv1.Configuration{
		TypeMeta: metav1.TypeMeta{
			APIVersion: xpkgv1.SchemeGroupVersion.String(),
			Kind:       xpkgv1.ConfigurationKind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: cfgName,
		},
		Spec: xpkgv1.ConfigurationSpec{
			PackageSpec: xpkgv1.PackageSpec{
				Package: pkgSource,
			},
		},
	}

	logger.Debug("Installing configuration package")

	err := retryWithBackoff(ctx, 2*time.Second, func(ctx context.Context) (bool, error) {
		//nolint:staticcheck // TODO(adamwg): Migrate to cl.Apply.
		err := cl.Patch(ctx, cfg, client.Apply, client.ForceOwnership, client.FieldOwner("crossplane-cli"))
		if err != nil {
			if isRetryableServerError(err) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})
	if err != nil {
		return err
	}

	logger.Debug("Waiting for packages to be ready")
	return waitForPackagesReady(ctx, cl, cfg)
}

func isRetryableServerError(err error) bool {
	if apierrors.IsTimeout(err) ||
		apierrors.IsInternalError(err) ||
		apierrors.IsServerTimeout(err) {
		return true
	}

	var statusErr *apierrors.StatusError
	if errors.As(err, &statusErr) {
		reason := statusErr.ErrStatus.Reason
		if reason == metav1.StatusReasonServiceUnavailable {
			return true
		}
	}

	return false
}

func waitForPackagesReady(ctx context.Context, cl client.Client, cfg *xpkgv1.Configuration) error {
	nn := types.NamespacedName{
		Name: "lock",
	}
	var lock xpkgv1beta1.Lock

	return retryWithBackoff(ctx, 5*time.Second, func(ctx context.Context) (bool, error) {
		cfgRev, revFound, err := getCurrentRevision(ctx, cl, cfg)
		if err != nil {
			return false, err
		}
		if !revFound {
			return false, nil
		}

		if cfgRev.GetSource() != cfg.GetSource() {
			return false, nil
		}

		if !packageHasHealthyConditions(cfgRev) {
			return false, nil
		}

		if err := cl.Get(ctx, nn, &lock); err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, errors.Wrap(err, "failed to get lock")
		}

		var cfgPkg *xpkgv1beta1.LockPackage
		for _, pkg := range lock.Packages {
			if pkg.Name == cfgRev.Name {
				cfgPkg = &pkg
				break
			}
		}
		if cfgPkg == nil {
			return false, nil
		}

		healthy, err := allDepsHealthy(ctx, cl, lock, *cfgPkg)
		if err != nil {
			return false, err
		}

		return healthy, nil
	})
}

func getCurrentRevision(ctx context.Context, cl client.Client, cfg *xpkgv1.Configuration) (*xpkgv1.ConfigurationRevision, bool, error) {
	cfgNN := types.NamespacedName{
		Name: cfg.Name,
	}
	if err := cl.Get(ctx, cfgNN, cfg); err != nil {
		return nil, false, errors.Wrap(err, "failed to get configuration")
	}

	if cfg.Status.CurrentRevision == "" {
		return nil, false, nil
	}

	revNN := types.NamespacedName{
		Name: cfg.Status.CurrentRevision,
	}
	var cfgRev xpkgv1.ConfigurationRevision
	if err := cl.Get(ctx, revNN, &cfgRev); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, errors.Wrap(err, "failed to get configuration revision")
	}

	return &cfgRev, true, nil
}

func allDepsHealthy(ctx context.Context, cl client.Client, lock xpkgv1beta1.Lock, pkg xpkgv1beta1.LockPackage) (bool, error) {
	for _, dep := range pkg.Dependencies {
		depPkg, found := lookupLockPackage(lock.Packages, dep.Package, dep.Constraints)
		if !found {
			return false, nil
		}
		healthy, err := packageIsHealthy(ctx, cl, depPkg)
		if err != nil {
			return false, err
		}
		if !healthy {
			return false, nil
		}
	}

	return true, nil
}

func lookupLockPackage(pkgs []xpkgv1beta1.LockPackage, source, constraint string) (xpkgv1beta1.LockPackage, bool) {
	for _, pkg := range pkgs {
		if !sourcesEqual(pkg.Source, source) {
			continue
		}

		vc, err := semver.NewConstraint(constraint)
		if err != nil {
			if pkg.Version == constraint {
				return pkg, true
			}
		}
		pv, err := semver.NewVersion(pkg.Version)
		if err != nil {
			continue
		}
		if vc.Check(pv) {
			return pkg, true
		}
	}
	return xpkgv1beta1.LockPackage{}, false
}

func sourcesEqual(a, b string) bool {
	ra, err := name.NewRepository(a, name.StrictValidation)
	if err != nil {
		return false
	}
	rb, err := name.NewRepository(b, name.StrictValidation)
	if err != nil {
		return false
	}

	return ra.String() == rb.String()
}

func packageIsHealthy(ctx context.Context, cl client.Client, lpkg xpkgv1beta1.LockPackage) (bool, error) {
	var pkg xpkgv1.PackageRevision

	if lpkg.Kind != nil {
		switch *lpkg.Kind {
		case xpkgv1.ConfigurationKind:
			pkg = &xpkgv1.ConfigurationRevision{}
		case xpkgv1.ProviderKind:
			pkg = &xpkgv1.ProviderRevision{}
		case xpkgv1.FunctionKind:
			pkg = &xpkgv1.FunctionRevision{}
		}
	}

	if lpkg.Type != nil {
		switch *lpkg.Type {
		case xpkgv1beta1.ConfigurationPackageType:
			pkg = &xpkgv1.ConfigurationRevision{}
		case xpkgv1beta1.ProviderPackageType:
			pkg = &xpkgv1.ProviderRevision{}
		case xpkgv1beta1.FunctionPackageType:
			pkg = &xpkgv1.FunctionRevision{}
		}
	}

	err := cl.Get(ctx, types.NamespacedName{Name: lpkg.Name}, pkg)
	if err != nil {
		return false, err
	}

	return packageHasHealthyConditions(pkg), nil
}

func packageHasHealthyConditions(pkg xpkgv1.PackageRevision) bool {
	v1Healthy := resource.IsConditionTrue(pkg.GetCondition(xpv2.TypeHealthy))
	v2Healthy := resource.IsConditionTrue(pkg.GetCondition(xpkgv1.TypeRevisionHealthy))

	if _, ok := pkg.(xpkgv1.PackageRevisionWithRuntime); ok {
		v2Healthy = v2Healthy && resource.IsConditionTrue(pkg.GetCondition(xpkgv1.TypeRuntimeHealthy))
	}

	return v1Healthy || v2Healthy
}

// ApplyResources installs arbitrary resources to the target control plane.
func ApplyResources(ctx context.Context, cl client.Client, resources []runtime.RawExtension) error {
	for _, raw := range resources {
		if len(raw.Raw) == 0 {
			return errors.New("encountered an invalid or empty raw resource")
		}

		obj := &unstructured.Unstructured{}
		if err := json.Unmarshal(raw.Raw, obj); err != nil {
			return errors.Wrap(err, "failed to unmarshal resource")
		}

		if err := retryWithBackoff(ctx, 2*time.Second, func(ctx context.Context) (bool, error) {
			//nolint:staticcheck // TODO(adamwg): Migrate to cl.Apply.
			err := cl.Patch(ctx, obj, client.Apply, client.ForceOwnership, client.FieldOwner("crossplane-cli"))
			if err != nil {
				if isPermanentError(err) {
					return false, err
				}
				return false, nil
			}
			return true, nil
		}); err != nil {
			return errors.Wrapf(err, "failed to apply resource %s/%s",
				obj.GetKind(), obj.GetName())
		}
	}
	return nil
}

func isPermanentError(err error) bool {
	if apierrors.IsBadRequest(err) ||
		apierrors.IsInvalid(err) ||
		apierrors.IsMethodNotSupported(err) ||
		apierrors.IsNotAcceptable(err) ||
		apierrors.IsUnsupportedMediaType(err) ||
		apierrors.IsUnauthorized(err) ||
		apierrors.IsForbidden(err) ||
		apierrors.IsRequestEntityTooLargeError(err) {
		return true
	}

	return false
}

func retryWithBackoff(ctx context.Context, maxWait time.Duration, fn func(ctx context.Context) (bool, error)) error {
	backoff := wait.Backoff{
		Duration: 500 * time.Millisecond,
		Factor:   2.0,
		Jitter:   0.1,
		Cap:      maxWait,
		Steps:    math.MaxInt32,
	}

	for {
		done, err := fn(ctx)
		if err != nil {
			return err
		}
		if done {
			return nil
		}

		sleep := backoff.Step()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sleep):
		}
	}
}
