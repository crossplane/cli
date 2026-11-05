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

package containerruntime

import (
	"context"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
)

// A Run ties the Docker resources one process creates (package containers,
// the render container and the render network) to that process, and reclaims
// the ones left behind by runs that died without cleaning up (SIGKILL, OOM, a
// second Ctrl+C, a CI runner tearing down the job).
//
// Ownership is carried by labels. A run's ID is the session ID its resources
// are labelled with: <domain>/session=<run ID> alongside
// <domain>/managed-by=crossplane-cli, under either DomainRender or
// DomainSimulate. A run's resources are exactly the ones carrying both labels
// of one domain with its ID, so a container or network that lacks either is
// never touched.
//
// Liveness is a lease: a run holds an exclusive lock on "<run ID>.lock" in the
// lease directory for as long as it has resources. The operating system drops
// the lock when the process exits however it exits, so a lease file whose lock
// can be taken belongs to a dead run. That needs no process table, cannot be
// fooled by PID reuse, and keeps concurrent runs from touching each other's
// resources. A lease that is not in this directory (another user, another
// container sharing the Docker socket) is never seen, so its resources are
// never touched.
//
// Reclaiming a dead run finishes what it would have done, following each
// container's recorded <domain>/cleanup label: Orphan, or a value this
// version does not know, is left alone, Stop is stopped, and Remove, or no
// cleanup label, is force removed. Its networks are removed after its
// containers. Reclaiming talks to the daemon this run connects to, so if
// DOCKER_HOST changed since a dead run, its lease is retired but its
// resources on the other daemon are left behind.
//
// A Run is not safe for concurrent use; its owner serializes access.
type Run struct {
	id        string
	dir       string
	leased    bool
	log       logging.Logger
	newClient func() (RunClient, error)

	// acquired is set once Acquire has run.
	acquired bool
	// lease is the open, locked lease file while the run holds it; nil when
	// the run is unleased, leasing failed, or the lease was released.
	lease *os.File
}

const (
	// runIDLength is the length of a run ID NewSessionID generates: 16
	// lowercase hex characters (64 random bits), so a session label carrying
	// one cannot belong to anything but that run.
	runIDLength = 16

	leaseExt = ".lock"

	// tempLeaseExt ends the name of a lease file before acquireLease renames
	// it into place.
	tempLeaseExt = ".tmp"

	// staleTempLeaseAge is how old a temporary lease file must be before a
	// reaper removes it: far longer than acquireLease holds one.
	staleTempLeaseAge = time.Minute

	// reapTimeout bounds reclaiming dead runs' resources, and sweeping a run's
	// own networks, so a slow Docker daemon cannot hold up a run. It covers all
	// the dead runs one Acquire reclaims together, and each Stop container can
	// take StopGracePeriod, so a few dead runs can run out of it. A run that is
	// not fully reclaimed in time keeps its lease, and the next run finishes it.
	reapTimeout = 10 * time.Second
)

// errLeaseUnsupported is lock's error on a platform without run leases.
var errLeaseUnsupported = errors.New("run leases are not supported on this platform")

// RunClient is the subset of the Docker client a Run uses to reclaim
// containers and networks.
type RunClient interface {
	CleanupClient
	ContainerList(ctx context.Context, options client.ContainerListOptions) (client.ContainerListResult, error)
	NetworkList(ctx context.Context, options client.NetworkListOptions) (client.NetworkListResult, error)
	NetworkRemove(ctx context.Context, networkID string, options client.NetworkRemoveOptions) (client.NetworkRemoveResult, error)
}

var _ RunClient = (*client.Client)(nil)

// A RunOption configures a Run.
type RunOption func(*Run)

// WithRunClient makes the Run talk to Docker through a client from newClient,
// instead of one configured from the environment. The Run closes each client
// newClient returns, if it is an io.Closer, once done with it.
func WithRunClient(newClient func() (RunClient, error)) RunOption {
	return func(r *Run) {
		r.newClient = newClient
	}
}

// DefaultLeaseDir is where runs keep their leases: a per-user directory, so
// one user's runs never judge another's. It returns "" when the user has no
// cache directory, which disables leasing and reaping.
func DefaultLeaseDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}

	return filepath.Join(dir, "crossplane", "runs")
}

// NewRun returns a Run with the given ID that keeps its lease in dir. An
// empty dir disables both leasing and reaping. leased says whether id is one
// NewSessionID generated for this run alone; only such a run takes a lease,
// because two processes sharing a supplied ID would rename over each other's
// lease. NewRun does no I/O.
func NewRun(dir, id string, leased bool, log logging.Logger, opts ...RunOption) *Run {
	if log == nil {
		log = logging.NewNopLogger()
	}

	r := &Run{id: id, dir: dir, leased: leased, log: log, newClient: newRunClient}
	for _, o := range opts {
		o(r)
	}

	return r
}

// ID is the run's ID: the session label value that makes a container or
// network this run's.
func (r *Run) ID() string {
	return r.id
}

// Acquire takes the run's lease, when it is leased, then reclaims what every
// dead run in the lease directory left behind, bounded by reapTimeout. Call
// it before creating any Docker resource. Acquire is idempotent: only the
// first call does anything.
//
// The returned error is a lease or reap failure, for the caller to warn
// about. It never means the run cannot go on: an unleased run's resources
// only risk leaking if it dies, and a dead run that could not be reclaimed is
// retried by the next run.
func (r *Run) Acquire(ctx context.Context) error {
	if r.acquired {
		return nil
	}

	r.acquired = true

	if r.dir == "" {
		return nil
	}

	var errs []error

	if r.leased {
		f, err := r.acquireLease()
		switch {
		case errors.Is(err, errLeaseUnsupported):
			// Not a failure to warn about on every run: the platform has no
			// leases, so no run is ever reclaimed, as before leases existed.
			r.log.Debug("Run leases are not supported on this platform; this run's Docker resources will not be reclaimed if it dies")
		case err != nil:
			errs = append(errs, errors.Wrap(err, "cannot lease this run's Docker resources; they will not be reclaimed if it dies"))
		}

		r.lease = f
	}

	if err := r.reapDead(ctx); err != nil {
		errs = append(errs, errors.Wrap(err, "cannot clean up Docker resources left behind by an earlier run; the next run will retry"))
	}

	return errors.Join(errs...)
}

// Release removes the run's leftover session networks under sweepDomain,
// unless it is empty, then gives up the run's lease. The lease file is kept,
// so the next run reclaims what is left, when clean is false or a network
// could not be removed. clean reports whether the caller removed everything
// else the run created. Release is a no-op on the lease of an unleased run,
// and safe to call more than once.
func (r *Run) Release(ctx context.Context, clean bool, sweepDomain string) error {
	var err error
	if sweepDomain != "" {
		err = r.sweepNetworks(ctx, sweepDomain)
	}

	releaseLease(r.lease, leasePath(r.dir, r.id), clean && err == nil)
	r.lease = nil

	return err
}

// acquireLease takes the run's lease, refusing an ID that is not one
// NewSessionID generates: reapers skip such lease files, so it would protect
// nothing and never be reclaimed.
func (r *Run) acquireLease() (*os.File, error) {
	if !validRunID(r.id) {
		return nil, errors.Errorf("run ID %q is not %d lowercase hex characters", r.id, runIDLength)
	}

	return acquireLease(r.dir, r.id)
}

// sweepNetworks removes every network labelled with the run's session under
// domain. As in reclaim, a network that one of the run's kept containers
// (Orphan, or a cleanup policy this version does not know) is still attached
// to is left, and is not an error.
func (r *Run) sweepNetworks(ctx context.Context, domain string) error {
	cli, err := r.newClient()
	if err != nil {
		return errors.Wrap(err, "cannot remove this run's Docker networks")
	}
	defer closeClient(cli, r.log)

	ctx, cancel := context.WithTimeout(ctx, reapTimeout)
	defer cancel()

	kept := map[string]bool{}

	var errs []error

	res, err := cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: sessionFilter(domain, r.id)})
	if err != nil {
		// Still try the networks. The list failure is reported either way,
		// so it keeps the lease and the next run retries.
		errs = append(errs, errors.Wrap(err, "cannot list Docker containers"))
	}

	for _, c := range res.Items {
		if keeps(Cleanup(c.Labels[domain+labelSuffixCleanup])) {
			keep(kept, c)
		}
	}

	errs = append(errs, r.removeNetworks(ctx, cli, r.id, kept, domain))

	return errors.Wrap(errors.Join(errs...), "cannot remove this run's Docker networks")
}

// deadLease is a lease this run took from a dead run.
type deadLease struct {
	id   string
	path string
	f    *os.File
}

// reapDead reclaims the resources of every run whose lease is in r.dir but
// held by no process. With no dead lease it makes no Docker call.
func (r *Run) reapDead(ctx context.Context) error {
	// Not filepath.Glob: it would read glob syntax in r.dir as a pattern.
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return nil // Best effort: a missing or unreadable dir has no leases to reap.
	}

	var dead []deadLease

	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}

		if isTempLease(e.Name()) {
			removeStaleTempLease(filepath.Join(r.dir, e.Name()))
			continue
		}

		id, ok := strings.CutSuffix(e.Name(), leaseExt)
		if !ok || id == r.id || !validRunID(id) {
			continue
		}

		p := filepath.Join(r.dir, e.Name())

		if f, ok := tryLease(p); ok {
			dead = append(dead, deadLease{id: id, path: p, f: f})
		}
	}

	if len(dead) == 0 {
		return nil
	}

	cli, err := r.newClient()
	if err != nil {
		// NOTE(jcogilvie): a Docker-less machine that once left a lease retries this
		// on every run. Drop such leases after N failures if that shows up.
		for _, l := range dead {
			releaseLease(l.f, l.path, false)
		}

		return err
	}
	defer closeClient(cli, r.log)

	ctx, cancel := context.WithTimeout(ctx, reapTimeout)
	defer cancel()

	var errs []error

	for _, l := range dead {
		err := r.reclaim(ctx, cli, l.id)
		if err != nil {
			errs = append(errs, errors.Wrapf(err, "run %s", l.id))
		} else {
			r.log.Debug("Reclaimed Docker resources left by a dead run", "run", l.id)
		}

		releaseLease(l.f, l.path, err == nil)
	}

	return errors.Join(errs...)
}

// reclaim finishes what dead run id would have done to its containers, per
// each one's cleanup label, then removes its networks. It reports whether the
// run is fully reclaimed: a network that a kept container is attached to does
// not count against it.
func (r *Run) reclaim(ctx context.Context, cli RunClient, id string) error {
	grace := int(StopGracePeriod / time.Second)

	// kept holds the names of the networks kept containers are attached to.
	kept := map[string]bool{}

	var errs []error

	for _, d := range []string{DomainRender, DomainSimulate} {
		res, err := cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: sessionFilter(d, id)})
		if err != nil {
			errs = append(errs, errors.Wrap(err, "cannot list Docker containers"))
			continue
		}

		for _, c := range res.Items {
			switch cl := Cleanup(c.Labels[d+labelSuffixCleanup]); {
			case keeps(cl):
				if cl != CleanupOrphan {
					r.log.Debug("Leaving Docker container with an unknown cleanup policy", "container", c.ID, "cleanup", cl, "run", id)
				}
				keep(kept, c)
			case cl == CleanupStop:
				if err := stopContainer(ctx, cli, c.ID, client.ContainerStopOptions{Timeout: &grace}); err != nil && !errdefs.IsNotFound(err) {
					errs = append(errs, errors.Wrapf(err, "cannot stop Docker container %s", c.ID))
				}
			default:
				// Remove, or no cleanup label: the render container, which
				// the dead run would have removed. Force stops it if running.
				if err := removeContainer(ctx, cli, c.ID); err != nil && !errdefs.IsNotFound(err) {
					errs = append(errs, errors.Wrapf(err, "cannot remove Docker container %s", c.ID))
				}
			}
		}
	}

	if len(errs) > 0 {
		// A container still attached would make removing its network fail,
		// so networks wait for every container to be finished.
		return errors.Join(errs...)
	}

	return r.removeNetworks(ctx, cli, id, kept, DomainRender, DomainSimulate)
}

// keeps reports whether cleanup policy cl leaves a container running: Orphan,
// or a policy this version does not know, perhaps from a newer one, which may
// have asked to keep it. No policy (no cleanup label) is not kept.
func keeps(cl Cleanup) bool {
	return cl != CleanupStop && cl != CleanupRemove && cl != ""
}

// keep records the networks kept container c is attached to.
func keep(kept map[string]bool, c container.Summary) {
	if c.NetworkSettings == nil {
		return
	}

	for n := range c.NetworkSettings.Networks {
		kept[n] = true
	}
}

// removeNetworks removes every network of run id under domains. A run that
// used no Docker render engine, or a network the user supplied, has none. A
// network named in kept is left with the kept container attached to it, even
// if that container is stopped: Docker would remove it then, and the
// container could not start again without it.
func (r *Run) removeNetworks(ctx context.Context, cli RunClient, id string, kept map[string]bool, domains ...string) error {
	var errs []error

	for _, d := range domains {
		res, err := cli.NetworkList(ctx, client.NetworkListOptions{Filters: sessionFilter(d, id)})
		if err != nil {
			errs = append(errs, errors.Wrap(err, "cannot list Docker networks"))
			continue
		}

		for _, n := range res.Items {
			if kept[n.Name] {
				r.log.Debug("Leaving Docker network attached to a kept container", "network", n.Name, "run", id)
				continue
			}

			_, err := cli.NetworkRemove(ctx, n.ID, client.NetworkRemoveOptions{})

			switch {
			case err == nil, errdefs.IsNotFound(err):
			default:
				errs = append(errs, errors.Wrapf(err, "cannot remove Docker network %s", n.Name))
			}
		}
	}

	return errors.Join(errs...)
}

// newRunClient returns a Docker client configured from the environment.
func newRunClient() (RunClient, error) {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, errors.Wrap(err, "cannot create Docker client using environment variables")
	}

	return cli, nil
}

// closeClient closes cli if it holds a connection.
func closeClient(cli RunClient, log logging.Logger) {
	c, ok := cli.(io.Closer)
	if !ok {
		return
	}

	if err := c.Close(); err != nil {
		log.Debug("Error closing Docker client", "error", err)
	}
}

// sessionFilter matches what run id created under domain. Docker requires
// every label filter to match, so a resource is matched only if it carries
// both labels.
func sessionFilter(domain, id string) client.Filters {
	return make(client.Filters).Add("label",
		domain+labelSuffixSession+"="+id,
		domain+labelSuffixManagedBy+"="+LabelValueManagedBy)
}

func validRunID(id string) bool {
	if len(id) != runIDLength {
		return false
	}

	_, err := hex.DecodeString(id)

	return err == nil && strings.ToLower(id) == id
}

func leasePath(dir, id string) string {
	return filepath.Join(dir, id+leaseExt)
}

// acquireLease creates and locks run id's lease file. The file is locked
// under a temporary name and renamed into place, so a reaper never sees a
// lease file that is not yet locked.
func acquireLease(dir, id string) (*os.File, error) {
	if !leasesSupported {
		return nil, errLeaseUnsupported
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, errors.Wrap(err, "cannot create lease directory")
	}

	f, err := os.CreateTemp(dir, id+"-*"+tempLeaseExt)
	if err != nil {
		return nil, errors.Wrap(err, "cannot create lease file")
	}

	if err := lock(f); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())

		return nil, errors.Wrap(err, "cannot lock lease file")
	}

	if err := os.Rename(f.Name(), leasePath(dir, id)); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())

		return nil, errors.Wrap(err, "cannot publish lease file")
	}

	return f, nil
}

// isTempLease reports whether name is a lease file acquireLease has not yet
// renamed into place: "<run ID>-<random>.tmp".
func isTempLease(name string) bool {
	rest, ok := strings.CutSuffix(name, tempLeaseExt)
	if !ok {
		return false
	}

	id, _, ok := strings.Cut(rest, "-")

	return ok && validRunID(id)
}

// removeStaleTempLease removes a temporary lease file that a run which died
// in acquireLease, between creating and renaming it, left behind. A file
// younger than staleTempLeaseAge may be another run's, about to be locked and
// renamed, so it is left; so is one whose lock is held.
func removeStaleTempLease(path string) {
	fi, err := os.Stat(path)
	if err != nil || time.Since(fi.ModTime()) < staleTempLeaseAge {
		return
	}

	if f, ok := tryLease(path); ok {
		releaseLease(f, path, true)
	}
}

// tryLease takes the lease at path if no process holds it, i.e. if its run is
// dead. A lease that another reaper reclaimed and removed between our open
// and our lock is no longer at path, so it is not taken: each dead run is
// reclaimed by one reaper.
func tryLease(path string) (*os.File, bool) {
	f, err := os.OpenFile(path, os.O_RDWR, 0) //nolint:gosec // The path is a lease file in our own lease directory.
	if err != nil {
		return nil, false
	}

	if err := lock(f); err != nil {
		_ = f.Close()
		return nil, false
	}

	held, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, false
	}

	if now, err := os.Stat(path); err != nil || !os.SameFile(held, now) {
		_ = f.Close()
		return nil, false
	}

	return f, true
}

// releaseLease gives up a lease, removing its file first when the run's
// resources are all gone, so no one can take a lease that no longer protects
// anything. A nil lease (unleased, or leasing failed) is a no-op.
func releaseLease(f *os.File, path string, remove bool) {
	if f == nil {
		return
	}

	if remove {
		_ = os.Remove(path)
	}

	_ = f.Close()
}
