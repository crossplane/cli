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

package tree

import (
	"context"

	"github.com/alecthomas/kong"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	crossplanemeta "github.com/crossplane/crossplane-runtime/v2/pkg/meta"

	"github.com/crossplane/cli/v2/cmd/crossplane/internal"
	"github.com/crossplane/cli/v2/cmd/crossplane/tree/internal/printer"
	"github.com/crossplane/cli/v2/pkg/resource"

	_ "embed"
)

//go:embed help/suspend.md
var helpSuspendDetail string

// SuspendCmd pauses reconciliation of a Crossplane resource.
type SuspendCmd struct {
	TraceCmd

	Cascade bool `help:"Recursively apply to the resource tree." name:"cascade"`
}

// Help returns help message for the suspend command.
func (c *SuspendCmd) Help() string {
	return helpSuspendDetail
}

// Run runs the suspend command.
func (c *SuspendCmd) Run(k *kong.Context, logger logging.Logger) error {
	ctx := context.Background()
	logger = logger.WithValues("Resource", c.Resource, "Name", c.Name)

	// Init new printer
	p, err := printer.New(c.Output)
	if err != nil {
		return errors.Wrap(err, errInitPrinter)
	}

	logger.Debug("Built printer", "output", c.Output)

	clientconfig, client, rmapper, err := c.setupKubeClient(logger)
	if err != nil {
		return err
	}

	res, name, err := c.getResourceAndName()
	if err != nil {
		return errors.Wrap(err, errInvalidResourceAndName)
	}

	mapping, err := internal.MappingFor(rmapper, res)
	if err != nil {
		return errors.Wrap(err, errGetMapping)
	}

	// Get Resource object. Contains k8s resource and all its children, also as Resource.
	rootRef := &v1.ObjectReference{
		Kind:       mapping.GroupVersionKind.Kind,
		APIVersion: mapping.GroupVersionKind.GroupVersion().String(),
		Name:       name,
	}

	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		namespace := c.Namespace
		if namespace == "" {
			namespace, _, err = clientconfig.Namespace()
			if err != nil {
				return errors.Wrap(err, errKubeNamespace)
			}
		}

		logger.Debug("Requested resource is namespaced", "namespace", namespace)
		rootRef.Namespace = namespace
	}
	// If no name is provided, we should print a list of resources.
	shouldPrintAsList := name == ""

	logger.Debug("Getting resource tree", "rootRef", rootRef.String())
	var resourceList *resource.ResourceList
	if shouldPrintAsList {
		// If no name is provided, we list all resources of the kind.
		logger.Debug("No name provided, listing all resources of the kind")
		resourceList = resource.ListResources(ctx, client, rootRef)
	} else {
		// If a name is provided, we get the specific resource.
		logger.Debug("Name provided, getting specific resource", "name", name)
		res := resource.GetResource(ctx, client, rootRef)
		resourceList = &resource.ResourceList{
			Items: []*resource.Resource{res},
			Error: res.Error,
		}
	}

	// We should just surface any error getting the root resource immediately.
	nameDisplay := name
	if nameDisplay == "" {
		nameDisplay = "<all>"
	}
	if err := resourceList.Error; err != nil {
		return errors.Wrapf(err, errFmtGetResource, mapping.GroupVersionKind.Kind, nameDisplay, rootRef.Namespace)
	}

	if c.Cascade {
		for i := range resourceList.Items {
			root := resourceList.Items[i]
			itemKind, itemName, itemNamespace := root.Unstructured.GetKind(), root.Unstructured.GetName(), root.Unstructured.GetNamespace()
			root, err = c.getResourceTree(ctx, root, mapping, client, logger)
			if err != nil {
				logger.Debug(errGetResource, "error", err)
				return errors.Wrapf(err, errFmtGetResourceTree, itemKind, itemName, itemNamespace)
			}

			logger.Debug("Got resource tree", "root", root)

			resourceList.Items[i] = root
		}
	}

	if err := c.applyAnnotation(ctx, client, resourceList.Items); err != nil {
		return err
	}

	// Watch mode for a single resource
	if c.Watch && !shouldPrintAsList && len(resourceList.Items) > 0 {
		root := resourceList.Items[0]
		return c.watchResourceTree(ctx, k, logger, client, root, mapping, p)
	}

	return printResources(k.Stdout, p, resourceList, shouldPrintAsList, c.Watch)
}

func (c *SuspendCmd) applyAnnotation(ctx context.Context, client client.Client, resources []*resource.Resource) error {
	for i := range resources {
		paused := "true"
		if err := patchAnnotations(ctx, client, &resources[i].Unstructured, map[string]*string{
			crossplanemeta.AnnotationKeyReconciliationPaused: &paused,
		}); err != nil {
			return err
		}

		if err := c.applyAnnotation(ctx, client, resources[i].Children); err != nil {
			return err
		}
	}

	return nil
}
