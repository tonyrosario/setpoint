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
func (r *Reconciler) reconcile(ctx context.Context, res *api.Resource) {
	status := r.converge(ctx, res)
	status.ObservedAt = time.Now().UTC()
	if err := r.store.UpdateStatus(ctx, res.Kind, res.Name, status); err != nil && err != store.ErrNotFound {
		r.log.Error("update status", "kind", res.Kind, "name", res.Name, "error", err)
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

	if !obs.Exists {
		r.log.Info("creating", "kind", res.Kind, "name", res.Name)
		if err := p.Create(ctx, res); err != nil {
			return api.Status{Phase: api.PhaseError, Message: fmt.Sprintf("create: %v", err)}
		}
		obs, err = p.Observe(ctx, res)
		if err != nil {
			return api.Status{Phase: api.PhaseCreating, Message: fmt.Sprintf("created; observe: %v", err)}
		}
	}

	status := api.Status{
		Ready:    obs.Ready,
		Message:  obs.Message,
		Observed: obs.Details,
	}
	if obs.Ready {
		status.Phase = api.PhaseReady
	} else {
		status.Phase = api.PhaseCreating
	}
	return status
}
