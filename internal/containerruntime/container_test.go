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

package containerruntime_test

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/moby/moby/api/types/container"

	cr "github.com/crossplane/cli/v2/internal/containerruntime"
)

const (
	ctrName  = "fn"
	ctrNet   = "render-net"
	thisRun  = "this-session"
	otherRun = "other-session"
)

// existing is a render container named ctrName on ctrNet. An empty cleanup
// means it has no cleanup label, as from an older CLI.
type existing struct {
	id      string
	running bool
	session string
	cleanup cr.Cleanup
}

func (e existing) summary(domain string) container.Summary {
	state, labels := exited, cr.ManagedLabels(domain, e.session)
	if e.running {
		state = running
	}
	if e.cleanup != "" {
		labels = cr.ContainerLabels(domain, e.session, e.cleanup)
	}

	c := ctr(e.id, state, labels, ctrNet)
	c.Names = []string{"/" + ctrName}

	return c
}

// TestContainerNamedReuse covers which container found by name Start reuses,
// and what Stop then leaves of it. A container Start creates has ID new-fn.
func TestContainerNamedReuse(t *testing.T) {
	type args struct {
		noDomain bool
		// domain is the Container's Domain. Empty means DomainRender.
		domain   string
		cleanup  cr.Cleanup // this run's policy
		existing *existing
		// raced is created under the name by another run between Start's
		// lookup and its create.
		raced *existing
	}
	type want struct {
		// errContains are substrings of Start's error. Empty means no error.
		errContains []string
		// errExcludes are substrings Start's error must not contain.
		errExcludes []string
		containers  map[string]container.ContainerState
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"NotFoundIsCreated": {
			reason: "With no container by the name, Start creates one, and Stop applies this run's policy.",
			args:   args{cleanup: cr.CleanupStop},
			want:   want{containers: map[string]container.ContainerState{"new-fn": exited}},
		},
		"RunningOrphanIsSharedAndLeftRunning": {
			reason: "A running Orphan container is shared, and this run never stops or removes it, though its own policy is Remove.",
			args:   args{cleanup: cr.CleanupRemove, existing: &existing{id: "old", running: true, session: otherRun, cleanup: cr.CleanupOrphan}},
			want:   want{containers: map[string]container.ContainerState{"old": running}},
		},
		"RunningUnknownPolicyIsSharedAndLeftRunning": {
			reason: "A running container with a policy this version doesn't know counts as Orphan, so it's shared and left running.",
			args:   args{cleanup: cr.CleanupRemove, existing: &existing{id: "old", running: true, session: otherRun, cleanup: cr.Cleanup("Keep")}},
			want:   want{containers: map[string]container.ContainerState{"old": running}},
		},
		"RunningSameSessionIsShared": {
			reason: "A running Remove container of this run's session is this run's own, so it's shared and finished under this run's policy.",
			args:   args{cleanup: cr.CleanupStop, existing: &existing{id: "old", running: true, session: thisRun, cleanup: cr.CleanupRemove}},
			want:   want{containers: map[string]container.ContainerState{"old": exited}},
		},
		"RunningOfOtherSessionIsRefused": {
			reason: "A running Remove container of another session would be removed under this run, so Start refuses it, saying why and what to do, and leaves it alone.",
			args:   args{cleanup: cr.CleanupRemove, existing: &existing{id: "old", running: true, session: otherRun, cleanup: cr.CleanupRemove}},
			want: want{
				errContains: []string{`"fn"`, otherRun, "another run", "remove it when it exits", `"Orphan"`, "runtime-docker-name", "docker rm -f fn"},
				containers:  map[string]container.ContainerState{"old": running},
			},
		},
		"RefusalOutsideRenderGivesNoFunctionHint": {
			reason: "Simulate names only Orphan diff servers, so a refusal there points at removing the leftover, not at render's Function annotations.",
			args:   args{domain: cr.DomainSimulate, cleanup: cr.CleanupOrphan, existing: &existing{id: "old", running: true, session: otherRun, cleanup: cr.CleanupRemove}},
			want: want{
				errContains: []string{`"fn"`, otherRun, "another run", "older CLI", "docker rm -f fn"},
				errExcludes: []string{"Functions", "runtime-docker-name"},
				containers:  map[string]container.ContainerState{"old": running},
			},
		},
		"RunningWithoutCleanupLabelIsReused": {
			reason: "A running container from before render recorded cleanup policies is reused under this run's policy, as before.",
			args:   args{cleanup: cr.CleanupStop, existing: &existing{id: "old", running: true, session: otherRun}},
			want:   want{containers: map[string]container.ContainerState{"old": exited}},
		},
		"StoppedIsRestartedAndKeptPerItsLabel": {
			reason: "A stopped container is restarted and taken over, and its Orphan label keeps it running past this Remove run.",
			args:   args{cleanup: cr.CleanupRemove, existing: &existing{id: "old", session: otherRun, cleanup: cr.CleanupOrphan}},
			want:   want{containers: map[string]container.ContainerState{"old": running}},
		},
		"StoppedIsRestartedAndKeptPerThisRun": {
			reason: "A stopped container is restarted and taken over, and this Stop run keeps it longer than its Remove label asks.",
			args:   args{cleanup: cr.CleanupStop, existing: &existing{id: "old", session: otherRun, cleanup: cr.CleanupRemove}},
			want:   want{containers: map[string]container.ContainerState{"old": exited}},
		},
		"NameConflictIsRefusedByTheRules": {
			reason: "When another run creates the name between lookup and create, Start judges that run's container by the same rules, refusing a Remove one rather than returning Docker's conflict.",
			args:   args{cleanup: cr.CleanupRemove, raced: &existing{id: "raced", running: true, session: otherRun, cleanup: cr.CleanupRemove}},
			want: want{
				errContains: []string{otherRun, "another run", "docker rm -f fn"},
				containers:  map[string]container.ContainerState{"raced": running},
			},
		},
		"NoDomainReusesAnything": {
			reason: "Without a Domain (the diff server manager), any container with the name is reused under this run's policy, as before.",
			args:   args{noDomain: true, cleanup: cr.CleanupStop, existing: &existing{id: "old", running: true, session: otherRun, cleanup: cr.CleanupRemove}},
			want:   want{containers: map[string]container.ContainerState{"old": exited}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			domain := tc.args.domain
			if domain == "" {
				domain = cr.DomainRender
			}

			h := &host{}
			if tc.args.existing != nil {
				h.containers = append(h.containers, tc.args.existing.summary(domain))
			}
			if tc.args.raced != nil {
				c := tc.args.raced.summary(domain)
				h.raced = &c
			}

			c := &cr.Container{
				Image:      "xpkg.crossplane.io/example/fn:v1",
				Name:       ctrName,
				Domain:     domain,
				Port:       9443,
				Labels:     cr.ContainerLabels(domain, thisRun, tc.args.cleanup),
				Network:    ctrNet,
				PullPolicy: cr.PullNever,
				Client:     h.client(),
			}
			if tc.args.noDomain {
				c.Domain = ""
			}

			r, err := c.Start(t.Context())
			if err == nil {
				err = r.Stop(t.Context(), tc.args.cleanup)
			}

			if len(tc.want.errContains) == 0 && err != nil {
				t.Errorf("\n%s\nStart or Stop: unexpected error: %v", tc.reason, err)
			}
			for _, s := range tc.want.errContains {
				if err == nil || !strings.Contains(err.Error(), s) {
					t.Errorf("\n%s\nStart(...): want an error containing %q, got %v", tc.reason, s, err)
				}
			}
			for _, s := range tc.want.errExcludes {
				if err != nil && strings.Contains(err.Error(), s) {
					t.Errorf("\n%s\nStart(...): want an error without %q, got %v", tc.reason, s, err)
				}
			}

			got, _ := h.left()
			if diff := cmp.Diff(tc.want.containers, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("\n%s\ncontainers left: -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

// TestRunningStopSharedContainer covers two handles to one container, as when
// two Functions share a name: the second to stop it finds it removed, or its
// removal still in progress, which is not a failure.
func TestRunningStopSharedContainer(t *testing.T) {
	cases := map[string]struct {
		reason  string
		cleanup cr.Cleanup
		// removing makes the second handle's removal conflict, as Docker
		// refuses one while another is in progress.
		removing bool
	}{
		"RemoveFindsItRemoved": {
			reason:  "The second Remove handle finds the container gone.",
			cleanup: cr.CleanupRemove,
		},
		"RemoveFindsItBeingRemoved": {
			reason:   "The second Remove handle finds the first still removing the container.",
			cleanup:  cr.CleanupRemove,
			removing: true,
		},
		"StopFindsItRemoved": {
			reason:  "A Stop handle finds the container removed by a Remove handle.",
			cleanup: cr.CleanupStop,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := &host{}
			c := &cr.Container{
				Image:      "xpkg.crossplane.io/example/fn:v1",
				Name:       ctrName,
				Domain:     cr.DomainRender,
				Port:       9443,
				Labels:     cr.ContainerLabels(cr.DomainRender, thisRun, cr.CleanupRemove),
				Network:    ctrNet,
				PullPolicy: cr.PullNever,
				Client:     h.client(),
			}

			first, err := c.Start(t.Context())
			if err != nil {
				t.Fatalf("Start(...): unexpected error: %v", err)
			}
			second, err := c.Start(t.Context())
			if err != nil {
				t.Fatalf("Start(...): unexpected error: %v", err)
			}

			if tc.removing {
				h.removing = first.ID
			} else if err := first.Stop(t.Context(), cr.CleanupRemove); err != nil {
				t.Fatalf("Stop(...): unexpected error: %v", err)
			}

			if err := second.Stop(t.Context(), tc.cleanup); err != nil {
				t.Errorf("\n%s\nStop(...): want no error, got %v", tc.reason, err)
			}
		})
	}
}

// TestRunningStopTakenOver covers a container that stopped while the run that
// started it was still running, e.g. because its process exited, and that
// another run then took over: the first run's Stop leaves it to that run.
func TestRunningStopTakenOver(t *testing.T) {
	for _, cleanup := range []cr.Cleanup{cr.CleanupStop, cr.CleanupRemove} {
		t.Run(string(cleanup), func(t *testing.T) {
			h := &host{}
			newContainer := func(session string) *cr.Container {
				return &cr.Container{
					Image:      "xpkg.crossplane.io/example/fn:v1",
					Name:       ctrName,
					Domain:     cr.DomainRender,
					Port:       9443,
					Labels:     cr.ContainerLabels(cr.DomainRender, session, cleanup),
					Network:    ctrNet,
					PullPolicy: cr.PullNever,
					Client:     h.client(),
				}
			}

			creator, err := newContainer(thisRun).Start(t.Context())
			if err != nil {
				t.Fatalf("Start(...): unexpected error: %v", err)
			}
			h.containers[0].State = exited

			taker, err := newContainer(otherRun).Start(t.Context())
			if err != nil {
				t.Fatalf("Start(...): unexpected error: %v", err)
			}

			if err := creator.Stop(t.Context(), cleanup); err != nil {
				t.Fatalf("Stop(...): unexpected error: %v", err)
			}
			if got, _ := h.left(); got["new-"+ctrName] != running {
				t.Errorf("creator's Stop(...): want the taken-over container left running, got %v", got)
			}

			if err := taker.Stop(t.Context(), cleanup); err != nil {
				t.Fatalf("Stop(...): unexpected error: %v", err)
			}
			want := map[string]container.ContainerState{"new-" + ctrName: exited}
			if cleanup == cr.CleanupRemove {
				want = nil
			}
			got, _ := h.left()
			if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("taker's Stop(...): containers left, -want, +got:\n%s", diff)
			}
		})
	}
}
