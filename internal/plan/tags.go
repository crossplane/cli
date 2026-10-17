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

package plan

import (
	"slices"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	xpresource "github.com/crossplane/crossplane-runtime/v2/pkg/resource"
)

// externalTagKeys returns the tags Crossplane providers inject into every
// managed resource they own, identifying the managing kind, name, and provider
// config. Upjet providers write them into the resource's tags (or labels)
// field on the cluster through an initializer, before the first reconcile.
func externalTagKeys() []string {
	return []string{
		xpresource.ExternalResourceTagKeyKind,
		xpresource.ExternalResourceTagKeyName,
		xpresource.ExternalResourceTagKeyProvider,
	}
}

// MergeExternalTags copies Crossplane's external tags from the live resource
// into the desired one, wherever the live spec carries all of them in one map
// (the initializer's signature). Applying a manifest without these tags never
// removes them, because the provider's initializer re-adds them before
// reconciling; without this merge every simulation of such a resource would
// report their removal. The diff server does not run initializers, so the
// merge happens here, on the client.
func MergeExternalTags(desired, live *unstructured.Unstructured) {
	if desired == nil || live == nil {
		return
	}
	liveSpec, ok, _ := unstructured.NestedMap(live.Object, "spec")
	if !ok {
		return
	}

	var found [][]string
	findExternalTagMaps(liveSpec, []string{"spec"}, &found)
	for _, path := range found {
		liveTags, ok, _ := unstructured.NestedMap(live.Object, path...)
		if !ok {
			continue
		}
		for _, key := range externalTagKeys() {
			v, ok := liveTags[key].(string)
			if !ok {
				continue
			}
			// Best effort: a desired tree too malformed to set the field on
			// will fail planning on its own terms.
			_ = unstructured.SetNestedField(desired.Object, v, append(slices.Clone(path), key)...)
		}
	}
}

// findExternalTagMaps collects the paths of all maps under node that contain
// every external tag key.
func findExternalTagMaps(node map[string]any, path []string, found *[][]string) {
	for k, v := range node {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		p := append(slices.Clone(path), k)
		if hasAllExternalTagKeys(m) {
			*found = append(*found, p)
			continue
		}
		findExternalTagMaps(m, p, found)
	}
}

func hasAllExternalTagKeys(m map[string]any) bool {
	for _, key := range externalTagKeys() {
		if _, ok := m[key].(string); !ok {
			return false
		}
	}
	return true
}
