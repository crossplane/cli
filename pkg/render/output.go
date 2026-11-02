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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/fieldpath"

	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
)

func conditionTime() metav1.Time {
	return metav1.NewTime(time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC))
}

// ReplaceConditionTimestamps replaces the lastTransitionTime timestamps in any
// of the argument's conditions with a static timestamp (the same one used by
// the old CLI renderer). This makes render output stable across runs despite
// the render engine producing real timestamps. It is a no-op if the argument
// does not have a status or has a status without conditions.
func ReplaceConditionTimestamps(o runtime.Object) error {
	u, ok := o.(*unstructured.Unstructured)
	if !ok {
		data, err := runtime.DefaultUnstructuredConverter.ToUnstructured(o)
		if err != nil {
			return errors.Wrap(err, "cannot convert object to unstructured")
		}
		u = &unstructured.Unstructured{Object: data}
	}

	fp := fieldpath.Pave(u.Object)

	// This function is a no-op for objects with no status, or whose status does
	// not have conditions.
	var cs xpv2.ConditionedStatus
	if err := fp.GetValueInto("status", &cs); err != nil {
		return nil //nolint:nilerr // See comment above.
	}

	for i := range cs.Conditions {
		cs.Conditions[i].LastTransitionTime = conditionTime()
	}

	if err := fp.SetValue("status.conditions", cs.Conditions); err != nil {
		return errors.Wrap(err, "cannot set condition timestamps")
	}

	return nil
}
