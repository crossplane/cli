// Package crossplane contains interfaces and implementations for clients that talk to Kubernetes about Crossplane
// primitives, often by consuming clients from the kubernetes package.
package crossplane

import (
	"context"

	un "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	"github.com/crossplane/cli/v2/internal/diff/client/core"
	"github.com/crossplane/cli/v2/internal/diff/client/kubernetes"
)

// Crossplane API group and kind constants used across the client package.
const (
	// CrossplaneAPIExtGroup is the Crossplane API extensions group.
	CrossplaneAPIExtGroup = "apiextensions.crossplane.io"
	// CrossplaneAPIExtGroupV1 is the v1 group/version for Crossplane API extensions.
	CrossplaneAPIExtGroupV1 = "apiextensions.crossplane.io/v1"
	// CrossplaneAPIExtGroupV2 is the v2 group/version for Crossplane API extensions.
	CrossplaneAPIExtGroupV2 = "apiextensions.crossplane.io/v2"
	// CrossplanePkgGroup is the Crossplane packages group.
	CrossplanePkgGroup = "pkg.crossplane.io"
	// CompositionKind is the kind for Compositions.
	CompositionKind = "Composition"
	// CompositionRevisionKind is the kind for CompositionRevisions.
	CompositionRevisionKind = "CompositionRevision"
	// FunctionKind is the kind for Crossplane Functions.
	FunctionKind = "Function"

	// updatePolicyAutomatic is the Automatic compositionUpdatePolicy value (also the default when
	// unset), mirroring Crossplane's CompositionUpdatePolicy.
	updatePolicyAutomatic = "Automatic"
	// updatePolicyManual is the Manual compositionUpdatePolicy value.
	updatePolicyManual = "Manual"
)

// Initialize initializes all the clients in this bundle.
func (c *Clients) Initialize(ctx context.Context, logger logging.Logger) error {
	return core.InitializeClients(ctx, logger,
		// definition client before composition client since it's a dependency
		c.Definition,
		c.Composition,
		c.Function,
		c.ResourceTree,
	)
}

// Clients is an aggregation of all of our Crossplane clients, used to pass them as a bundle,
// typically for initialization where the consumer can select which ones they need.
type Clients struct {
	Composition  CompositionClient
	Credential   CredentialClient
	Definition   DefinitionClient
	Function     FunctionClient
	ResourceTree ResourceTreeClient
}

func listMatchingResources(ctx context.Context, client kubernetes.ResourceClient, gvks []schema.GroupVersionKind, namespace string) ([]*un.Unstructured, error) {
	// List all matching resources for the given GVKs
	var envConfigs []*un.Unstructured

	for _, gvk := range gvks {
		ecs, err := client.ListResources(ctx, gvk, namespace)
		if err != nil {
			return nil, errors.Wrapf(err, "cannot list items for GVK %s", gvk.String())
		}

		envConfigs = append(envConfigs, ecs...)
	}

	return envConfigs, nil
}
