package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/tonyrosario/setpoint/core/api"
	"github.com/tonyrosario/setpoint/core/provider"
	"github.com/tonyrosario/setpoint/core/store"
)

// fakeProvider scripts Observe/Create/Update/Delete behavior for one kind.
type fakeProvider struct {
	kind       string
	exists     bool
	ready      bool
	upToDate   bool
	observeErr error
	createErr  error
	updateErr  error
	deleteErr  error
	created    int
	updated    int
	deleted    int
	// sawSpec records the Spec bytes the provider was last handed — how
	// tests observe reference substitution (ADR-0012).
	sawSpec json.RawMessage
}

func (f *fakeProvider) Kinds() []string { return []string{f.kind} }

func (f *fakeProvider) Observe(ctx context.Context, res *api.Resource) (provider.Observation, error) {
	f.sawSpec = res.Spec
	if f.observeErr != nil {
		return provider.Observation{}, f.observeErr
	}
	return provider.Observation{
		Exists:   f.exists,
		Ready:    f.ready,
		UpToDate: f.upToDate,
		Details:  map[string]string{"fake": "yes"},
	}, nil
}

func (f *fakeProvider) Create(ctx context.Context, res *api.Resource) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.created++
	f.exists = true
	f.ready = true
	f.upToDate = true
	return nil
}

func (f *fakeProvider) Update(ctx context.Context, res *api.Resource) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	f.updated++
	f.exists = true
	f.ready = true
	f.upToDate = true
	return nil
}

func (f *fakeProvider) Delete(ctx context.Context, res *api.Resource) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleted++
	f.exists = false
	f.ready = false
	return nil
}

func newTest(p provider.Provider) (*Reconciler, *store.Memory) {
	st := store.NewMemory()
	log := slog.New(slog.DiscardHandler)
	rec := New(st, []provider.Provider{p}, time.Hour, log)
	return rec, st
}

// reconcileAll synchronously reconciles every stored resource once. It
// exercises the reconcile logic directly, without the worker pool — the queue
// and workers have their own tests in queue_test.go.
func reconcileAll(rec *Reconciler) {
	resources, _ := rec.store.List(context.Background(), "")
	for _, res := range resources {
		_ = rec.reconcile(context.Background(), res)
	}
}

func putContainer(t *testing.T, st store.Store, name string) {
	t.Helper()
	err := st.Put(context.Background(), &api.Resource{
		Kind: "container",
		Name: name,
		Spec: json.RawMessage(`{"image":"nginx"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func status(t *testing.T, st store.Store, name string) api.Status {
	t.Helper()
	res, err := st.Get(context.Background(), "container", name)
	if err != nil {
		t.Fatal(err)
	}
	return res.Status
}

func TestCreatesWhenAbsent(t *testing.T) {
	fake := &fakeProvider{kind: "container"}
	rec, st := newTest(fake)
	putContainer(t, st, "web")

	reconcileAll(rec)

	if fake.created != 1 {
		t.Fatalf("created = %d, want 1", fake.created)
	}
	got := status(t, st, "web")
	if !got.Ready || got.Phase != api.PhaseReady {
		t.Errorf("status = %+v, want Ready", got)
	}
	if got.ObservedAt.IsZero() {
		t.Error("ObservedAt not set")
	}
}

func TestNoOpWhenPresent(t *testing.T) {
	fake := &fakeProvider{kind: "container", exists: true, ready: true, upToDate: true}
	rec, st := newTest(fake)
	putContainer(t, st, "web")

	reconcileAll(rec)
	reconcileAll(rec)

	if fake.created != 0 || fake.updated != 0 {
		t.Fatalf("created=%d updated=%d, want 0/0 (level-triggered no-op)", fake.created, fake.updated)
	}
	if got := status(t, st, "web"); !got.Ready {
		t.Errorf("status = %+v, want Ready", got)
	}
}

func TestExistsButNotReady(t *testing.T) {
	// Right spec (upToDate), but not running yet — e.g. still starting.
	fake := &fakeProvider{kind: "container", exists: true, ready: false, upToDate: true}
	rec, st := newTest(fake)
	putContainer(t, st, "web")

	reconcileAll(rec)

	got := status(t, st, "web")
	if got.Ready || got.Phase != api.PhaseCreating {
		t.Errorf("status = %+v, want not-ready Creating", got)
	}
	if fake.updated != 0 {
		t.Errorf("updated = %d, want 0 (up-to-date container must not be recreated)", fake.updated)
	}
}

func TestUpdatesWhenNotUpToDate(t *testing.T) {
	// Container exists and runs, but does not match Spec → must be updated.
	fake := &fakeProvider{kind: "container", exists: true, ready: true, upToDate: false}
	rec, st := newTest(fake)
	putContainer(t, st, "web")

	reconcileAll(rec)

	if fake.updated != 1 {
		t.Fatalf("updated = %d, want 1", fake.updated)
	}
	if fake.created != 0 {
		t.Errorf("created = %d, want 0 (existing container is updated, not created)", fake.created)
	}
	got := status(t, st, "web")
	if !got.Ready || got.Phase != api.PhaseReady {
		t.Errorf("status = %+v, want Ready after update", got)
	}
}

func TestUpdateErrorSetsErrorPhase(t *testing.T) {
	fake := &fakeProvider{kind: "container", exists: true, ready: true, upToDate: false,
		updateErr: errors.New("recreate failed")}
	rec, st := newTest(fake)
	putContainer(t, st, "web")

	reconcileAll(rec)

	if got := status(t, st, "web"); got.Phase != api.PhaseError {
		t.Errorf("status = %+v, want Error", got)
	}
}

func TestObserveErrorSetsErrorPhase(t *testing.T) {
	fake := &fakeProvider{kind: "container", observeErr: errors.New("daemon down")}
	rec, st := newTest(fake)
	putContainer(t, st, "web")

	reconcileAll(rec)

	got := status(t, st, "web")
	if got.Phase != api.PhaseError || got.Ready {
		t.Errorf("status = %+v, want Error", got)
	}
}

func TestCreateErrorSetsErrorPhase(t *testing.T) {
	fake := &fakeProvider{kind: "container", createErr: errors.New("no such image")}
	rec, st := newTest(fake)
	putContainer(t, st, "web")

	reconcileAll(rec)

	got := status(t, st, "web")
	if got.Phase != api.PhaseError {
		t.Errorf("status = %+v, want Error", got)
	}
}

func TestUnknownKindSetsErrorPhase(t *testing.T) {
	fake := &fakeProvider{kind: "container"}
	rec, st := newTest(fake)
	err := st.Put(context.Background(), &api.Resource{
		Kind: "network",
		Name: "net1",
		Spec: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	reconcileAll(rec)

	res, _ := st.Get(context.Background(), "network", "net1")
	if res.Status.Phase != api.PhaseError {
		t.Errorf("status = %+v, want Error for unregistered kind", res.Status)
	}
}

func markDeleted(t *testing.T, st store.Store, name string) {
	t.Helper()
	if err := st.MarkForDeletion(context.Background(), "container", name); err != nil {
		t.Fatal(err)
	}
}

func TestDeletionRemovesSubstrateThenResource(t *testing.T) {
	fake := &fakeProvider{kind: "container", exists: true, ready: true}
	rec, st := newTest(fake)
	putContainer(t, st, "web")
	markDeleted(t, st, "web")

	// First pass: substrate object exists → Delete it, stay in store.
	reconcileAll(rec)
	if fake.deleted != 1 {
		t.Fatalf("deleted = %d, want 1", fake.deleted)
	}
	if _, err := st.Get(context.Background(), "container", "web"); err != nil {
		t.Fatalf("resource gone too early: %v", err)
	}

	// Second pass: substrate clean → resource removed from store.
	reconcileAll(rec)
	if _, err := st.Get(context.Background(), "container", "web"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("resource still present after substrate clean: %v", err)
	}
}

func TestDeletionMarkSkipsConverge(t *testing.T) {
	// A marked resource whose substrate object is already gone must NOT be
	// recreated — it must be removed.
	fake := &fakeProvider{kind: "container", exists: false}
	rec, st := newTest(fake)
	putContainer(t, st, "web")
	markDeleted(t, st, "web")

	reconcileAll(rec)

	if fake.created != 0 {
		t.Errorf("created = %d, want 0 (marked resource must not be recreated)", fake.created)
	}
	if _, err := st.Get(context.Background(), "container", "web"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("resource with no substrate object should be removed immediately: %v", err)
	}
}

func TestDeletionOfUnknownKindRemovesResource(t *testing.T) {
	fake := &fakeProvider{kind: "container"}
	rec, st := newTest(fake)
	err := st.Put(context.Background(), &api.Resource{
		Kind: "network", Name: "net1", Spec: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MarkForDeletion(context.Background(), "network", "net1"); err != nil {
		t.Fatal(err)
	}

	reconcileAll(rec)

	if _, err := st.Get(context.Background(), "network", "net1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("marked resource of unmanaged kind should be dropped: %v", err)
	}
}

func TestProcessRequeuesWithBackoffOnError(t *testing.T) {
	// A reconcile that keeps failing must accumulate per-item backoff.
	fake := &fakeProvider{kind: "container", observeErr: errors.New("daemon down")}
	rec, st := newTest(fake)
	putContainer(t, st, "web")

	key := resourceKey{Kind: "container", Name: "web"}
	rec.queue.enqueue(key)
	got, _ := rec.queue.get()
	rec.process(context.Background(), got) // fails → enqueueRateLimited

	if f := rec.queue.limiter.failures(key); f == 0 {
		t.Fatal("expected backoff failure count to advance after a failed reconcile")
	}
	if got := status(t, st, "web"); got.Phase != api.PhaseError {
		t.Errorf("status = %+v, want Error", got)
	}
}

func TestProcessForgetsOnSuccess(t *testing.T) {
	fake := &fakeProvider{kind: "container"} // absent → create → ready
	rec, st := newTest(fake)
	putContainer(t, st, "web")
	key := resourceKey{Kind: "container", Name: "web"}

	// Pre-load some failures, then a successful process must reset them.
	rec.queue.limiter.when(key)
	rec.queue.limiter.when(key)

	rec.queue.enqueue(key)
	got, _ := rec.queue.get()
	rec.process(context.Background(), got)

	if f := rec.queue.limiter.failures(key); f != 0 {
		t.Fatalf("failures after successful process = %d, want 0 (forget)", f)
	}
}

func TestProcessForgetsVanishedResource(t *testing.T) {
	fake := &fakeProvider{kind: "container"}
	rec, _ := newTest(fake)
	// Key for a resource that isn't in the store.
	key := resourceKey{Kind: "container", Name: "ghost"}
	rec.queue.enqueue(key)
	got, _ := rec.queue.get()
	rec.process(context.Background(), got) // Get → ErrNotFound → forget, no create

	if fake.created != 0 {
		t.Errorf("created = %d, want 0 (no resource to reconcile)", fake.created)
	}
}

func TestRunGracefulShutdown(t *testing.T) {
	fake := &fakeProvider{kind: "container", exists: true, ready: true, upToDate: true}
	rec, st := newTest(fake)
	putContainer(t, st, "web")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { rec.Run(ctx); close(done) }()

	rec.Nudge()
	cancel() // cancel while running

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}
}

// blockingProvider blocks in Observe until its context is cancelled, standing
// in for a slow external substrate. enterOnce guards the entered signal since
// the queue may drain a re-marked key a second time during shutdown.
type blockingProvider struct {
	kind      string
	entered   chan struct{}
	enterOnce sync.Once
}

func (b *blockingProvider) Kinds() []string { return []string{b.kind} }
func (b *blockingProvider) Observe(ctx context.Context, res *api.Resource) (provider.Observation, error) {
	b.enterOnce.Do(func() { close(b.entered) })
	<-ctx.Done() // block until the reconcile context is cancelled
	return provider.Observation{}, ctx.Err()
}
func (b *blockingProvider) Create(context.Context, *api.Resource) error { return nil }
func (b *blockingProvider) Update(context.Context, *api.Resource) error { return nil }
func (b *blockingProvider) Delete(context.Context, *api.Resource) error { return nil }

func TestShutdownCancelsInFlightReconcile(t *testing.T) {
	fake := &blockingProvider{kind: "container", entered: make(chan struct{})}
	rec, st := newTest(fake)
	putContainer(t, st, "web")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { rec.Run(ctx); close(done) }()

	rec.Nudge()
	<-fake.entered // a worker is now blocked inside Observe

	cancel() // must unblock the in-flight reconcile via its context

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run hung: in-flight reconcile was not cancelled on shutdown")
	}
}

func TestNudgeTriggersSweep(t *testing.T) {
	fake := &fakeProvider{kind: "container"}
	rec, st := newTest(fake)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { rec.Run(ctx); close(done) }()

	putContainer(t, st, "web")
	rec.Nudge()

	deadline := time.After(2 * time.Second)
	for {
		if status(t, st, "web").Ready {
			break
		}
		select {
		case <-deadline:
			t.Fatal("nudge did not trigger reconcile within 2s")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done
}
