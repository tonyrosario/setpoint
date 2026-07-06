// Package reconcile drives convergence: compare every resource's Spec to
// what its Provider observes in the Substrate, and act to close the gap
// (ADR-0002, level-triggered).
//
// Reconcilers run off an honest work queue (M0 slice 4): a keyed,
// deduplicating queue with per-item exponential backoff, drained by a pool
// of workers. A periodic resync enqueues every key — the poll IS the resync
// safety net (docs/research/workqueue-backoff-patterns.md §3), since we have
// no watch protocol. Reconciles must be idempotent: a worker can be killed
// mid-reconcile and the key re-run from scratch.
package reconcile

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/tonyrosario/setpoint/core/api"
	"github.com/tonyrosario/setpoint/core/provider"
	"github.com/tonyrosario/setpoint/core/store"
)

const (
	defaultWorkers = 2
	// backoff bounds mirror client-go's controller defaults.
	backoffBase = 5 * time.Millisecond
	backoffMax  = 1000 * time.Second
	// deletionRequeue is how soon to re-check a resource whose Substrate
	// object we just asked to be removed (the requeue-after seam).
	deletionRequeue = 500 * time.Millisecond
)

// resourceKey identifies a resource in the queue. Keys, not objects, are
// queued — the worker reads current state from the Store before reconciling.
type resourceKey struct {
	Kind string
	Name string
}

func keyOf(res *api.Resource) resourceKey {
	return resourceKey{Kind: res.Kind, Name: res.Name}
}

// Reconciler converges stored resources toward their Spec via a worker pool
// draining a rate-limited work queue.
type Reconciler struct {
	store     store.Store
	providers map[string]provider.Provider
	queue     *workQueue[resourceKey]
	interval  time.Duration
	workers   int
	nudge     chan struct{}
	log       *slog.Logger
}

// New builds a Reconciler. Providers are indexed by every kind they own.
func New(s store.Store, providers []provider.Provider, interval time.Duration, log *slog.Logger) *Reconciler {
	byKind := make(map[string]provider.Provider)
	for _, p := range providers {
		for _, kind := range p.Kinds() {
			byKind[kind] = p
		}
	}
	limiter := newExponentialRateLimiter[resourceKey](backoffBase, backoffMax)
	return &Reconciler{
		store:     s,
		providers: byKind,
		queue:     newWorkQueue[resourceKey](limiter),
		interval:  interval,
		workers:   defaultWorkers,
		nudge:     make(chan struct{}, 1),
		log:       log,
	}
}

// Nudge asks for a resync soon. Non-blocking; a pending nudge is enough. The
// server calls this after every accepted write, but convergence never
// depends on the nudge arriving — the periodic resync is the safety net.
func (r *Reconciler) Nudge() {
	select {
	case r.nudge <- struct{}{}:
	default:
	}
}

// Run starts the worker pool and resync loop, blocking until ctx is
// cancelled and all workers have drained.
func (r *Reconciler) Run(ctx context.Context) {
	// Bridge context cancellation to the queue so blocked workers wake.
	go func() {
		<-ctx.Done()
		r.queue.shutDown()
	}()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		r.resyncLoop(ctx)
	}()
	for i := 0; i < r.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.worker()
		}()
	}
	wg.Wait()
}

// resyncLoop enqueues every resource on a timer and on nudge. Each cycle is a
// full reconcile opportunity — the safety net that catches out-of-band drift
// and any missed signal.
func (r *Reconciler) resyncLoop(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	r.resync(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.resync(ctx)
		case <-r.nudge:
			r.resync(ctx)
		}
	}
}

func (r *Reconciler) resync(ctx context.Context) {
	resources, err := r.store.List(ctx, "")
	if err != nil {
		r.log.Error("list resources", "error", err)
		return
	}
	for _, res := range resources {
		r.queue.enqueue(keyOf(res))
	}
}

// worker pulls keys and reconciles until the queue shuts down.
func (r *Reconciler) worker() {
	for {
		key, shutdown := r.queue.get()
		if shutdown {
			return
		}
		r.process(key)
	}
}

// process reconciles one key: read current state, reconcile, then either
// forget (success) or requeue with backoff (failure). A key whose resource
// has vanished from the Store is simply forgotten.
func (r *Reconciler) process(key resourceKey) {
	defer r.queue.done(key)

	ctx := context.Background()
	res, err := r.store.Get(ctx, key.Kind, key.Name)
	if err == store.ErrNotFound {
		r.queue.forget(key) // resource gone; nothing to reconcile
		return
	}
	if err != nil {
		r.log.Error("get resource", "kind", key.Kind, "name", key.Name, "error", err)
		r.queue.enqueueRateLimited(key)
		return
	}

	if err := r.reconcile(ctx, res); err != nil {
		r.queue.enqueueRateLimited(key)
		return
	}
	r.queue.forget(key)
}

// reconcile converges a single resource and records what it saw in Status.
// A resource marked for deletion takes the removal path instead of the
// convergence path — otherwise we would recreate the very object we are
// trying to delete. Returns a non-nil error only for unexpected failures the
// caller should retry with backoff.
func (r *Reconciler) reconcile(ctx context.Context, res *api.Resource) error {
	if res.IsMarkedForDeletion() {
		return r.reconcileDeletion(ctx, res)
	}
	status, err := r.converge(ctx, res)
	r.setStatus(ctx, res, status)
	return err
}

// reconcileDeletion drives a marked resource to removal: delete the
// Substrate object, then hard-remove the resource from the Store once the
// Substrate is clean. Idempotent — a resource whose Substrate object is
// already gone (removed out-of-band, or on an earlier pass) is simply
// removed from the Store.
func (r *Reconciler) reconcileDeletion(ctx context.Context, res *api.Resource) error {
	p, ok := r.providers[res.Kind]
	if !ok {
		// Nothing can exist in a Substrate we don't manage; drop it.
		r.removeFromStore(ctx, res)
		return nil
	}

	obs, err := p.Observe(ctx, res)
	if err != nil {
		r.setStatus(ctx, res, api.Status{Phase: api.PhaseDeleting, Message: fmt.Sprintf("observe: %v", err)})
		return err
	}

	if obs.Exists {
		r.log.Info("deleting", "kind", res.Kind, "name", res.Name)
		if err := p.Delete(ctx, res); err != nil {
			r.setStatus(ctx, res, api.Status{Phase: api.PhaseDeleting, Message: fmt.Sprintf("delete: %v", err)})
			return err
		}
		// Substrate delete initiated (not a failure); confirm removal soon.
		r.setStatus(ctx, res, api.Status{Phase: api.PhaseDeleting, Message: "removing from substrate"})
		r.queue.enqueueAfter(keyOf(res), deletionRequeue)
		return nil
	}

	// Substrate is clean — remove the resource itself.
	r.removeFromStore(ctx, res)
	return nil
}

func (r *Reconciler) setStatus(ctx context.Context, res *api.Resource, status api.Status) {
	status.ObservedAt = time.Now().UTC()
	if err := r.store.UpdateStatus(ctx, res.Kind, res.Name, status); err != nil && err != store.ErrNotFound {
		r.log.Error("update status", "kind", res.Kind, "name", res.Name, "error", err)
	}
}

func (r *Reconciler) removeFromStore(ctx context.Context, res *api.Resource) {
	switch err := r.store.Delete(ctx, res.Kind, res.Name); {
	case err == nil:
		r.log.Info("deleted", "kind", res.Kind, "name", res.Name)
	case err == store.ErrNotFound:
		// Already absent (removed out-of-band or on a prior pass); nothing to log.
	default:
		r.log.Error("delete resource", "kind", res.Kind, "name", res.Name, "error", err)
	}
}

// converge returns the Status to record plus an error the caller should
// retry with backoff. A missing provider yields an Error status but no
// retryable error — retrying can't register a provider, and the resync will
// re-check anyway.
func (r *Reconciler) converge(ctx context.Context, res *api.Resource) (api.Status, error) {
	p, ok := r.providers[res.Kind]
	if !ok {
		return api.Status{
			Phase:   api.PhaseError,
			Message: fmt.Sprintf("no provider registered for kind %q", res.Kind),
		}, nil
	}

	obs, err := p.Observe(ctx, res)
	if err != nil {
		return api.Status{Phase: api.PhaseError, Message: fmt.Sprintf("observe: %v", err)}, err
	}

	switch {
	case !obs.Exists:
		r.log.Info("creating", "kind", res.Kind, "name", res.Name)
		if err := p.Create(ctx, res); err != nil {
			return api.Status{Phase: api.PhaseError, Message: fmt.Sprintf("create: %v", err)}, err
		}
		obs, err = p.Observe(ctx, res)
		if err != nil {
			return api.Status{Phase: api.PhaseCreating, Message: fmt.Sprintf("created; observe: %v", err)}, err
		}
	case !obs.UpToDate:
		// The Substrate object exists but no longer matches Spec; converge
		// it (for Docker, delete-and-recreate).
		r.log.Info("updating", "kind", res.Kind, "name", res.Name)
		if err := p.Update(ctx, res); err != nil {
			return api.Status{Phase: api.PhaseError, Message: fmt.Sprintf("update: %v", err)}, err
		}
		obs, err = p.Observe(ctx, res)
		if err != nil {
			return api.Status{Phase: api.PhaseCreating, Message: fmt.Sprintf("updated; observe: %v", err)}, err
		}
	}

	// Ready means the object both exists-and-runs and matches Spec.
	ready := obs.Ready && obs.UpToDate
	status := api.Status{
		Ready:    ready,
		Message:  obs.Message,
		Observed: obs.Details,
	}
	if ready {
		status.Phase = api.PhaseReady
	} else {
		status.Phase = api.PhaseCreating
	}
	return status, nil
}
