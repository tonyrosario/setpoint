package reconcile

import (
	"sync"
	"time"
)

// This file implements the honest work queue that replaces the naive
// single-worker sweep (M0 slice 4, per docs/research/workqueue-backoff-patterns.md).
//
// The two load-bearing properties, both borrowed from client-go's workqueue:
//
//  1. Deduplication + no-concurrent-same-key. A key enqueued while already
//     pending is dropped; a key enqueued while being processed is re-run
//     exactly once after Done; the same key is never held by two workers at
//     once. This is what makes level-triggering correct under load — many
//     rapid writes to one resource collapse into one reconcile against
//     current state.
//  2. Per-item exponential backoff. A failing key backs off independently,
//     so one broken resource never starves or hammers the others, and a past
//     failure doesn't penalize future reconciles once Forget resets it.

// rateLimiter decides how long a failing key waits before its next attempt.
type rateLimiter[K comparable] interface {
	when(key K) time.Duration // next delay for this key (advances the counter)
	forget(key K)             // reset the counter (call on success)
	failures(key K) int       // current failure count (for tests/introspection)
}

// exponentialRateLimiter is per-item exponential backoff: delay = base·2^failures,
// capped at max. Mirrors client-go's ItemExponentialFailureRateLimiter defaults.
type exponentialRateLimiter[K comparable] struct {
	mu    sync.Mutex
	fails map[K]int
	base  time.Duration
	max   time.Duration
}

func newExponentialRateLimiter[K comparable](base, max time.Duration) *exponentialRateLimiter[K] {
	return &exponentialRateLimiter[K]{fails: make(map[K]int), base: base, max: max}
}

func (r *exponentialRateLimiter[K]) when(key K) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()

	exp := r.fails[key]
	r.fails[key] = exp + 1

	// Cap the shift to avoid Duration overflow; anything past ~20 doublings
	// is well beyond max anyway.
	if exp > 20 {
		return r.max
	}
	backoff := r.base << exp
	if backoff <= 0 || backoff > r.max {
		return r.max
	}
	return backoff
}

func (r *exponentialRateLimiter[K]) forget(key K) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.fails, key)
}

func (r *exponentialRateLimiter[K]) failures(key K) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fails[key]
}

// workQueue is a keyed, deduplicating work queue with per-item rate limiting.
type workQueue[K comparable] struct {
	mu           sync.Mutex
	cond         *sync.Cond
	ready        []K            // FIFO of keys ready to process now
	dirty        map[K]struct{} // keys queued (ready, or re-marked while processing)
	processing   map[K]struct{} // keys currently held by a worker
	limiter      rateLimiter[K]
	shuttingDown bool
}

func newWorkQueue[K comparable](limiter rateLimiter[K]) *workQueue[K] {
	q := &workQueue[K]{
		dirty:      make(map[K]struct{}),
		processing: make(map[K]struct{}),
		limiter:    limiter,
	}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// enqueue adds a key to process as soon as a worker is free. A no-op if the
// key is already dirty; if the key is being processed it is marked dirty so
// it re-runs once after Done.
func (q *workQueue[K]) enqueue(key K) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.shuttingDown {
		return
	}
	if _, ok := q.dirty[key]; ok {
		return
	}
	q.dirty[key] = struct{}{}
	if _, ok := q.processing[key]; ok {
		return // already running; Done will re-add because it's now dirty
	}
	q.ready = append(q.ready, key)
	q.cond.Signal()
}

// enqueueAfter adds a key after delay d. Used for "not ready yet, check again
// soon" — the requeue-after seam (deletion confirmation in M0; dependency
// readiness in M1). Does not touch the rate limiter.
func (q *workQueue[K]) enqueueAfter(key K, d time.Duration) {
	if d <= 0 {
		q.enqueue(key)
		return
	}
	time.AfterFunc(d, func() { q.enqueue(key) })
}

// enqueueRateLimited adds a key after its current per-item backoff. Used for
// unexpected failures — the delay grows each time until forget is called.
func (q *workQueue[K]) enqueueRateLimited(key K) {
	q.enqueueAfter(key, q.limiter.when(key))
}

// forget resets a key's failure backoff. Call on a successful reconcile.
func (q *workQueue[K]) forget(key K) {
	q.limiter.forget(key)
}

// get blocks until a key is ready or the queue shuts down. The returned key
// is marked processing; the caller MUST call done(key) when finished.
func (q *workQueue[K]) get() (key K, shutdown bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	for len(q.ready) == 0 && !q.shuttingDown {
		q.cond.Wait()
	}
	if len(q.ready) == 0 {
		var zero K
		return zero, true
	}
	key = q.ready[0]
	q.ready = q.ready[1:]
	q.processing[key] = struct{}{}
	delete(q.dirty, key)
	return key, false
}

// done releases a key. If it was re-marked dirty while processing, it becomes
// ready again so the newer state gets reconciled.
func (q *workQueue[K]) done(key K) {
	q.mu.Lock()
	defer q.mu.Unlock()

	delete(q.processing, key)
	if _, ok := q.dirty[key]; ok {
		q.ready = append(q.ready, key)
		q.cond.Signal()
	}
}

// shutDown wakes all waiting workers so they exit. Pending delayed re-adds
// become no-ops (enqueue checks shuttingDown).
func (q *workQueue[K]) shutDown() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.shuttingDown = true
	q.cond.Broadcast()
}

// depth reports the number of ready keys (for tests/introspection).
func (q *workQueue[K]) depth() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.ready)
}
