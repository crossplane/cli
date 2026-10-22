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

// Package client bundles the Kubernetes and Crossplane clients used by the
// diff engine.
package client

import (
	"context"

	"k8s.io/client-go/rest"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	"github.com/crossplane/cli/v2/internal/diff/client/core"
	xp "github.com/crossplane/cli/v2/internal/diff/client/crossplane"
	k8 "github.com/crossplane/cli/v2/internal/diff/client/kubernetes"
)

// Clients holds the Kubernetes and Crossplane clients used by the diff engine.
type Clients struct {
	K8sClients k8.Clients
	XpClients  xp.Clients
}

// New creates the Kubernetes and Crossplane clients for the given config.
func New(config *rest.Config, logger logging.Logger) (*Clients, error) {
	coreClients, err := core.NewClients(config)
	if err != nil {
		// error is already well-decorated
		return nil, err
	}

	tc := k8.NewTypeConverter(coreClients, logger)

	k8c := k8.Clients{
		Type:     tc,
		Access:   k8.NewAccessClient(coreClients, tc, logger),
		Apply:    k8.NewApplyClient(coreClients, tc, logger),
		Resource: k8.NewResourceClient(coreClients, tc, logger),
		Schema:   k8.NewSchemaClient(coreClients, tc, logger),
	}

	defClient := xp.NewDefinitionClient(k8c.Resource, tc, logger)

	xpc := xp.Clients{
		Composition:  xp.NewCompositionClient(k8c.Resource, defClient, logger),
		Credential:   xp.NewCredentialClient(k8c.Resource, logger),
		Definition:   defClient,
		Environment:  xp.NewEnvironmentClient(k8c.Resource, logger),
		Function:     xp.NewFunctionClient(k8c.Resource, logger),
		ResourceTree: xp.NewResourceTreeClient(coreClients.Tree, logger),
	}

	return &Clients{
		K8sClients: k8c,
		XpClients:  xpc,
	}, nil
}

// Initialize initializes all clients.
func (c *Clients) Initialize(ctx context.Context, logger logging.Logger) error {
	if err := c.XpClients.Initialize(ctx, logger); err != nil {
		return errors.Wrap(err, "cannot initialize Crossplane client")
	}

	return nil
}
