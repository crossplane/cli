package kubernetes

import (
	"context"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	"github.com/crossplane/cli/v2/internal/diff/client/core"
)

// TypeConverter provides conversion between Kubernetes types.
type TypeConverter interface {
	// GVKToGVR converts a GroupVersionKind to a GroupVersionResource
	GVKToGVR(ctx context.Context, gvk schema.GroupVersionKind) (schema.GroupVersionResource, error)

	// GetResourceNameForGVK returns the resource name for a given GVK
	GetResourceNameForGVK(ctx context.Context, gvk schema.GroupVersionKind) (string, error)
}

// DefaultTypeConverter implements TypeConverter.
type DefaultTypeConverter struct {
	mapper meta.RESTMapper
	logger logging.Logger
}

// NewTypeConverter creates a new DefaultTypeConverter.
func NewTypeConverter(clients *core.Clients, logger logging.Logger) TypeConverter {
	return &DefaultTypeConverter{
		mapper: clients.Mapper,
		logger: logger,
	}
}

// GVKToGVR converts a GroupVersionKind to a GroupVersionResource.
func (c *DefaultTypeConverter) GVKToGVR(_ context.Context, gvk schema.GroupVersionKind) (schema.GroupVersionResource, error) {
	mapping, err := c.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		c.logger.Debug("Failed to map GVK to GVR", "gvk", gvk.String(), "error", err)
		return schema.GroupVersionResource{}, errors.Wrapf(err, "cannot find resource for %s", gvk.String())
	}

	return mapping.Resource, nil
}

// GetResourceNameForGVK returns the resource name for a given GVK.
func (c *DefaultTypeConverter) GetResourceNameForGVK(ctx context.Context, gvk schema.GroupVersionKind) (string, error) {
	gvr, err := c.GVKToGVR(ctx, gvk)
	if err != nil {
		return "", err
	}

	return gvr.Resource, nil
}
