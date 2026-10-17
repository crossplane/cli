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

// Package containerruntime runs Crossplane package images (Functions,
// Providers) as local Docker containers serving a gRPC API. It carries the
// lifecycle render and simulate share: find-or-create a container by name,
// start it, resolve the address to reach it on, and clean it up per a
// cleanup policy, labeling every container so leftovers can be identified
// and swept.
package containerruntime

import (
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/rand"
)

// Cleanup specifies what to do with a package's container once its run is
// done.
type Cleanup string

const (
	// CleanupStop stops the container, waiting up to StopGracePeriod for it
	// to exit on SIGTERM before Docker kills it.
	CleanupStop Cleanup = "Stop"

	// CleanupRemove stops the container, waiting up to StopGracePeriod for
	// it to exit on SIGTERM, then force removes it. The container is removed
	// even if the graceful stop fails.
	CleanupRemove Cleanup = "Remove"

	// CleanupOrphan leaves the container running, so a later run can find it
	// by name and reuse it without paying startup.
	CleanupOrphan Cleanup = "Orphan"

	// CleanupDefault is the policy callers use when none is specified.
	CleanupDefault = CleanupRemove
)

// PullPolicy controls how a package's runtime image is pulled by Docker.
type PullPolicy string

const (
	// PullAlways always pulls the image.
	PullAlways PullPolicy = "Always"

	// PullNever never pulls the image.
	PullNever PullPolicy = "Never"

	// PullIfNotPresent pulls the image if it's not present.
	PullIfNotPresent PullPolicy = "IfNotPresent"

	// PullDefault is the policy used when none is specified.
	PullDefault = PullIfNotPresent
)

const (
	// StopGracePeriod is how long the Stop and Remove cleanup policies wait
	// for a container to exit on SIGTERM before Docker kills it.
	StopGracePeriod = 3 * time.Second

	// StopTimeout bounds the ContainerStop request: the grace period plus
	// time for Docker to kill the container.
	StopTimeout = StopGracePeriod + 2*time.Second

	// RemoveTimeout bounds the ContainerRemove request. It's separate from
	// StopTimeout so a stop that hangs can't leave removal without time to
	// run.
	RemoveTimeout = 10 * time.Second
)

// Label key suffixes appended to a caller's annotation domain (e.g.
// "render.crossplane.io" becomes "render.crossplane.io/managed-by"), so each
// command keeps its documented label keys while the labeling logic lives in
// one place.
const (
	labelSuffixManagedBy = "/managed-by"
	labelSuffixSession   = "/session"
	labelSuffixCleanup   = "/cleanup"

	// LabelValueManagedBy is the value of every <domain>/managed-by label.
	LabelValueManagedBy = "crossplane-cli"
)

// SessionID returns an identifier unique to this process. It is the session
// ID used for Docker resources when the caller doesn't supply one.
//
//nolint:gochecknoglobals // Must be stable for the life of the process.
var SessionID = sync.OnceValue(func() string { return rand.String(12) })

// ManagedLabels returns the labels marking a Docker resource as managed by
// the crossplane CLI under the given annotation domain:
// <domain>/managed-by and <domain>/session. An empty session falls back to
// the per-process SessionID.
func ManagedLabels(domain, session string) map[string]string {
	if session == "" {
		session = SessionID()
	}
	return map[string]string{
		domain + labelSuffixManagedBy: LabelValueManagedBy,
		domain + labelSuffixSession:   session,
	}
}

// ContainerLabels returns ManagedLabels plus the effective cleanup policy
// recorded as <domain>/cleanup, the full label set for a package container.
func ContainerLabels(domain, session string, cleanup Cleanup) map[string]string {
	l := ManagedLabels(domain, session)
	l[domain+labelSuffixCleanup] = string(cleanup)
	return l
}
