# Kubernetes Work-Queue Machinery: A Technical Brief for Bespoke Control Planes

> Research brief for the Control Plane Core's work queue (M0–M1). Produced by a research subagent on 2026-07-03.

## 1. The Work Queue's Core Guarantees

client-go's workqueue (`k8s.io/client-go/util/workqueue`) maintains two internal sets per queue: a **dirty set** (items pending processing) and a **processing set** (items currently held by a worker). These two sets enforce three invariants:

- **Deduplication.** If a key is enqueued while it is already in the dirty set (not yet dequeued), the duplicate is silently dropped. If it is enqueued while in the *processing* set, it is added to the dirty set so it will be re-processed exactly once after `Done()` is called — not twice concurrently.
- **No concurrent processing of the same key.** The "stingy" property means a key cannot be held by two goroutines simultaneously, even with N workers pulling from one queue.
- **Keys, not objects or events.** Event handlers push `kind/namespace/name` strings (or typed `Request` structs) rather than the full object or the diff. Workers then read the *current* state from the cache before reconciling.

The last point is load-bearing for level-triggered correctness. A level-triggered system reacts to *current state*, not to the sequence of transitions. If five rapid writes to the same object produce five events, a naive event-driven system would attempt five reconciles with progressively staler snapshots. The key-only queue collapses those five events into one entry; the worker fetches the latest state and runs reconcile once. Even if a watch event is lost entirely (e.g., during a reconnect), the key has either already been re-enqueued by a later event or will be picked up by resync. The queue makes convergence a property of the *system*, not of event delivery reliability.

## 2. Rate Limiting and Backoff

`DefaultControllerRateLimiter()` composes two strategies under a `MaxOfRateLimiter` (the worst-case delay wins):

1. **`ItemExponentialFailureRateLimiter`** — per-item exponential backoff: `delay = base × 2^(failures)`. Defaults: base **5 ms**, max **1 000 s**. Each item has an independent failure counter.
2. **`BucketRateLimiter`** — overall token bucket: **10 req/s**, burst **100**. Caps aggregate requeue throughput across all items.

The three enqueue methods differ semantically:

| Method | When used | Rate-limiter applied? |
|---|---|---|
| `AddRateLimited(key)` | error or `Requeue: true` returned | Yes — per-item backoff + bucket |
| `AddAfter(key, d)` | `RequeueAfter: d` returned | No — exact delay, bypass limiter |
| `Add(key)` | watch event fires fresh | No — immediate |

`Forget(key)` resets the per-item failure counter in the rate limiter. Call it on success *before* `Done()`. Without it, a key that failed 10 times and then succeeded will begin its next failure sequence from the 11th backoff slot — several hundred seconds — rather than from base delay.

Per-item backoff beats a global retry loop because failure isolation is independent: a key crashing repeatedly does not inflate the wait for a key failing for the first time. It also avoids thundering herds: a global loop would batch-requeue every failed item at the same interval, creating periodic spike loads.

## 3. Resync

Informer resync does **not** re-list from the API server. It replays objects already in the local cache as synthetic `UpdateFunc` events on a configurable timer (controller-runtime default: **10 hours**). Each synthetic event enqueues the key, causing every known resource to be reconciled even if no real change occurred.

Why this is the safety net: watches are not perfectly reliable. A watch stream that reconnects after a network partition may miss events. Any key that missed its event is corrected no later than the next resync sweep, because the reconciler always reads current state, not the missed diff.

For a **poll-based control plane** (no informers, no watches), the observation poll *is* the resync. A periodic goroutine that lists all resources from the Store and enqueues each key is functionally identical to informer resync — in fact, it is stronger, because every poll cycle is a full reconcile opportunity, not just a cache replay. There is no need for a separate resync timer; the polling interval serves both purposes. A 30–60 second poll interval is typical for infrastructure-facing controllers, shorter (5–10 s) for internal coordination.

## 4. Requeue-After Patterns

Two distinct outcomes from a reconciler:

**Return an error** when something failed unexpectedly: an API call to an external substrate threw a 500, a write to the Store failed, a required secret could not be read. The queue calls `AddRateLimited`, applying exponential backoff. This is appropriate because the failure is likely transient but timing is unknown; let the rate limiter choose the next attempt.

**Return `Result{RequeueAfter: d}` with nil error** when the reconciler completed successfully but knows it needs to run again in *d* seconds — a dependency is not ready yet, an external resource is still converging, or a status field resolves via a later observation cycle. Crucially, controller-runtime calls `Forget()` and then `AddAfter(d)`, resetting any accumulated backoff. Nothing went wrong; we are simply waiting.

For a References-resolve-through-Status pattern: if `Spec.dbRef` points to a `Database` whose `Status.endpoint` is empty, return `RequeueAfter: 10s` (not an error — the database is just converging). If the Store lookup itself panics or returns an unexpected error, return that error so backoff applies.

## 5. Worker Pools and Graceful Shutdown

The standard pattern: one queue per controller kind, N goroutines (the `MaxConcurrentReconciles` knob, default 1) pulling from it:

```go
for i := 0; i < workers; i++ {
    go func() {
        for {
            key, shutdown := q.Get()
            if shutdown { return }
            err := reconcile(ctx, key)
            if err != nil { q.AddRateLimited(key) } else { q.Forget(key) }
            q.Done(key)
        }
    }()
}
```

Shutdown proceeds via context cancellation. The manager cancels the root context; reconcilers check `ctx.Done()` at expensive checkpoints and return. After context cancellation, `q.ShutDown()` is called: new items are rejected, `Get()` on an empty queue returns `(zero, true)` (shutdown=true), workers exit. `ShutDownWithDrain()` is the stricter variant — it blocks until every outstanding `Done()` is called, giving in-flight reconciles time to finish.

Reconcilers must be **idempotent** regardless of shutdown strategy. A SIGKILL, OOM kill, or crash can terminate a worker mid-reconcile with no cleanup path. On restart, the key will be re-enqueued (via resync or re-watch) and reconcile will run again from scratch. Any partially-applied external mutation must be safe to re-apply or must be detectable via status reads.

## 6. Minimal Bespoke Design Sketch

Given all of the above, the smallest honest implementation for a poll-driven, non-Kubernetes control plane:

```go
// RateLimiter tracks per-item failure state.
type RateLimiter[K comparable] interface {
    When(key K) time.Duration  // next delay for this key
    Forget(key K)              // reset on success
    Failures(key K) int
}

// WorkQueue is the core primitive per controller kind.
type WorkQueue[K comparable] interface {
    Enqueue(key K)                        // immediate, no rate limit
    EnqueueAfter(key K, d time.Duration)  // exact delay, bypass limiter
    EnqueueRateLimited(key K)             // failure path: apply rate limiter
    Forget(key K)                         // reset rate limiter state
    Get(ctx context.Context) (K, bool)    // blocks; bool=shutdown
    Done(key K)                           // release processing slot
    Shutdown()
}

// WorkerLoop runs N goroutines against one queue.
func WorkerLoop[K comparable](ctx context.Context, q WorkQueue[K],
    reconcile func(context.Context, K) error, n int) {
    var wg sync.WaitGroup
    for range n {
        wg.Add(1)
        go func() {
            defer wg.Done()
            for {
                key, shutdown := q.Get(ctx)
                if shutdown { return }
                if err := reconcile(ctx, key); err != nil {
                    q.EnqueueRateLimited(key)
                } else {
                    q.Forget(key)
                }
                q.Done(key)
            }
        }()
    }
    <-ctx.Done()
    q.Shutdown()
    wg.Wait()
}
```

**What to skip:** informer list-watch machinery (your Store poll replaces it), leader-election (add later if you scale horizontally), the delaying-queue heap (you can implement `EnqueueAfter` with a single `time.AfterFunc` per key initially). **What is load-bearing and must not be skipped:** the dirty+processing set semantics (without them, concurrent workers will double-process keys and race on status writes), per-item backoff tracking (without it, a single broken resource will hammer external APIs at full speed), and `Forget()` on success (without it, one past failure will impose a minutes-long penalty on every future reconcile of that key).

---

**Sources:**
- [Rate Limiting in controller-runtime and client-go — Daniel Mangum](https://danielmangum.com/posts/controller-runtime-client-go-rate-limiting/)
- [client-go workqueue — pkg.go.dev](https://pkg.go.dev/k8s.io/client-go/util/workqueue)
- [client-go default_rate_limiters.go — GitHub](https://github.com/kubernetes/client-go/blob/master/util/workqueue/default_rate_limiters.go)
- [client-go rate_limiting_queue.go — GitHub](https://github.com/kubernetes/client-go/blob/master/util/workqueue/rate_limiting_queue.go)
- [Error Back-off with Controller Runtime — stuartleeks.com](https://stuartleeks.com/posts/error-back-off-with-controller-runtime/)
- [controller-runtime reconcile.go — GitHub](https://github.com/kubernetes-sigs/controller-runtime/blob/main/pkg/reconcile/reconcile.go)
- [Why resync default is 10 hours — controller-runtime issue #521](https://github.com/kubernetes-sigs/controller-runtime/issues/521)
- [How do Kubernetes Operators Handle Concurrency — DEV Community](https://dev.to/sklarsa/how-do-kubernetes-operators-handle-concurrency-47n5)
- [Work Queue — openshift/kubernetes-client-go DeepWiki](https://deepwiki.com/openshift/kubernetes-client-go/7.2-work-queue)
- [Kubernetes Reconcile Loop Explained — golinuxcloud.com](https://www.golinuxcloud.com/kubernetes-reconcile-loop-explained/)
