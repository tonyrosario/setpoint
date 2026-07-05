// Package reconcile drives convergence: compare every resource's Spec to
// what its Provider observes in the Substrate, and act to close the gap
// (ADR-0002, level-triggered).
//
// This is the deliberately naive M0-slice-1 loop: one worker sweeps all
// resources on a timer (or when nudged by a write). It reads current state
// and converges — it never depends on having seen an event. The honest
// work queue (dedup, per-item backoff, Forget) replaces the internals in a
// later slice without changing this package's contract.
package reconcile

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/tonyrosario/setpoint/core/api"
	"github.com/tonyrosario/setpoint/core/provider"
	"github.com/tonyrosario/setpoint/core/store"
)

// Reconciler sweeps all stored resources and converges each through its
// kind's Provider.
type Reconciler struct {
	store     store.Store
	providers map[string]provider.Provider
	interval  time.Duration
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
	return &Reconciler{
		store:     s,
		providers: byKind,
		interval:  interval,
		nudge:     make(chan struct{}, 1),
		log:       log,
	}
}

// Nudge asks for a sweep soon. Non-blocking; a pending nudge is enough.
func (r *Reconciler) Nudge() {
	select {
	case r.nudge <- struct{}{}:
	default:
	}
}

// Run sweeps until ctx is cancelled. The periodic sweep is the safety net
// that makes level-triggering honest: every resource is reconciled on every
// pass regardless of what events were or weren't seen.
func (r *Reconciler) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	r.sweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.sweep(ctx)
		case <-r.nudge:
			r.sweep(ctx)
		}
	}
}

func (r *Reconciler) sweep(ctx context.Context) {
	resources, err := r.store.List(ctx, "")
	if err != nil {
		r.log.Error("list resources", "error", err)
		return
	}
	for _, res := range resources {
		if ctx.Err() != nil {
			return
		}
		r.reconcile(ctx, res)
	}
}

// reconcile converges a single resource and records what it saw in Status.
// A resource marked for deletion takes the removal path instead of the
// convergence path — otherwise we would recreate the very object we are
// trying to delete.
func (r *Reconciler) reconcile(ctx context.Context, res *api.Resource) {
	if res.IsMarkedForDeletion() {
		r.reconcileDeletion(ctx, res)
		return
	}
	status := r.converge(ctx, res)
	r.setStatus(ctx, res, status)
}

// reconcileDeletion drives a marked resource to removal: delete the
// Substrate object, then hard-remove the resource from the Store once the
// Substrate is clean. Idempotent — a resource whose Substrate object is
// already gone (removed out-of-band, or on an earlier pass) is simply
// removed from the Store.
func (r *Reconciler) reconcileDeletion(ctx context.Context, res *api.Resource) {
	p, ok := r.providers[res.Kind]
	if !ok {
		// Nothing can exist in a Substrate we don't manage; drop it.
		r.removeFromStore(ctx, res)
		return
	}

	obs, err := p.Observe(ctx, res)
	if err != nil {
		r.setStatus(ctx, res, api.Status{Phase: api.PhaseDeleting, Message: fmt.Sprintf("observe: %v", err)})
		return
	}

	if obs.Exists {
		r.log.Info("deleting", "kind", res.Kind, "name", res.Name)
		if err := p.Delete(ctx, res); err != nil {
			r.setStatus(ctx, res, api.Status{Phase: api.PhaseDeleting, Message: fmt.Sprintf("delete: %v", err)})
			return
		}
		// Substrate delete initiated; confirm removal on the next pass.
		r.setStatus(ctx, res, api.Status{Phase: api.PhaseDeleting, Message: "removing from substrate"})
		r.Nudge()
		return
	}

	// Substrate is clean — remove the resource itself.
	r.removeFromStore(ctx, res)
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

func (r *Reconciler) converge(ctx context.Context, res *api.Resource) api.Status {
	p, ok := r.providers[res.Kind]
	if !ok {
		return api.Status{
			Phase:   api.PhaseError,
			Message: fmt.Sprintf("no provider registered for kind %q", res.Kind),
		}
	}

	obs, err := p.Observe(ctx, res)
	if err != nil {
		return api.Status{Phase: api.PhaseError, Message: fmt.Sprintf("observe: %v", err)}
	}

	switch {
	case !obs.Exists:
		r.log.Info("creating", "kind", res.Kind, "name", res.Name)
		if err := p.Create(ctx, res); err != nil {
			return api.Status{Phase: api.PhaseError, Message: fmt.Sprintf("create: %v", err)}
		}
		obs, err = p.Observe(ctx, res)
		if err != nil {
			return api.Status{Phase: api.PhaseCreating, Message: fmt.Sprintf("created; observe: %v", err)}
		}
	case !obs.UpToDate:
		// The Substrate object exists but no longer matches Spec; converge
		// it (for Docker, delete-and-recreate).
		r.log.Info("updating", "kind", res.Kind, "name", res.Name)
		if err := p.Update(ctx, res); err != nil {
			return api.Status{Phase: api.PhaseError, Message: fmt.Sprintf("update: %v", err)}
		}
		obs, err = p.Observe(ctx, res)
		if err != nil {
			return api.Status{Phase: api.PhaseCreating, Message: fmt.Sprintf("updated; observe: %v", err)}
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
	return status
}
