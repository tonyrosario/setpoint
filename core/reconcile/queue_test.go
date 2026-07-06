package reconcile

import (
	"testing"
	"time"
)

func newIntQueue() *workQueue[int] {
	return newWorkQueue[int](newExponentialRateLimiter[int](time.Millisecond, time.Second))
}

func TestQueueDedupsWhilePending(t *testing.T) {
	q := newIntQueue()
	q.enqueue(1)
	q.enqueue(1)
	q.enqueue(1)
	if q.depth() != 1 {
		t.Fatalf("depth = %d, want 1 (duplicates collapse)", q.depth())
	}
}

func TestQueueFIFOOrder(t *testing.T) {
	q := newIntQueue()
	q.enqueue(1)
	q.enqueue(2)
	q.enqueue(3)

	for _, want := range []int{1, 2, 3} {
		got, shutdown := q.get()
		if shutdown {
			t.Fatal("unexpected shutdown")
		}
		if got != want {
			t.Fatalf("got %d, want %d", got, want)
		}
		q.done(got)
	}
}

func TestQueueReEnqueueWhileProcessingRunsOnceAfterDone(t *testing.T) {
	q := newIntQueue()
	q.enqueue(1)

	// Take key 1 out to "process" it.
	got, _ := q.get()
	if got != 1 {
		t.Fatalf("got %d, want 1", got)
	}
	// While it is processing, it is re-enqueued (e.g. a new write arrived).
	q.enqueue(1)
	// It must NOT be immediately available — only one worker may hold key 1.
	if q.depth() != 0 {
		t.Fatalf("depth = %d, want 0 while key is processing", q.depth())
	}
	// After done, it becomes ready again exactly once.
	q.done(1)
	if q.depth() != 1 {
		t.Fatalf("depth = %d, want 1 after done re-adds the dirty key", q.depth())
	}
	got, _ = q.get()
	q.done(got)
	if q.depth() != 0 {
		t.Fatalf("depth = %d, want 0 (only re-run once)", q.depth())
	}
}

func TestQueueGetUnblocksOnShutdown(t *testing.T) {
	q := newIntQueue()
	done := make(chan bool, 1)
	go func() {
		_, shutdown := q.get() // blocks — queue is empty
		done <- shutdown
	}()

	// Give the goroutine a moment to block, then shut down.
	time.Sleep(20 * time.Millisecond)
	q.shutDown()

	select {
	case shutdown := <-done:
		if !shutdown {
			t.Fatal("get returned shutdown=false after shutDown")
		}
	case <-time.After(time.Second):
		t.Fatal("get did not unblock on shutDown")
	}
}

func TestQueueNoEnqueueAfterShutdown(t *testing.T) {
	q := newIntQueue()
	q.shutDown()
	q.enqueue(1)
	if q.depth() != 0 {
		t.Fatalf("depth = %d, want 0 (enqueue after shutdown is a no-op)", q.depth())
	}
}

func TestExponentialBackoffGrows(t *testing.T) {
	r := newExponentialRateLimiter[int](time.Millisecond, time.Second)

	prev := time.Duration(0)
	for i := 0; i < 6; i++ {
		d := r.when(1)
		if d <= prev {
			t.Fatalf("attempt %d: delay %v did not grow beyond %v", i, d, prev)
		}
		prev = d
	}
	// A different key is independent — starts at base.
	if got := r.when(2); got != time.Millisecond {
		t.Errorf("independent key delay = %v, want base %v", got, time.Millisecond)
	}
}

func TestExponentialBackoffCapsAtMax(t *testing.T) {
	r := newExponentialRateLimiter[int](time.Millisecond, 10*time.Millisecond)
	var last time.Duration
	for i := 0; i < 30; i++ {
		last = r.when(1)
	}
	if last != 10*time.Millisecond {
		t.Fatalf("delay = %v, want capped at max 10ms", last)
	}
}

func TestForgetResetsBackoff(t *testing.T) {
	r := newExponentialRateLimiter[int](time.Millisecond, time.Second)
	r.when(1)
	r.when(1)
	r.when(1)
	if r.failures(1) == 0 {
		t.Fatal("expected accumulated failures")
	}
	r.forget(1)
	if got := r.failures(1); got != 0 {
		t.Fatalf("failures after forget = %d, want 0", got)
	}
	// Next delay starts from base again.
	if got := r.when(1); got != time.Millisecond {
		t.Errorf("delay after forget = %v, want base %v", got, time.Millisecond)
	}
}
