package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/tonyrosario/setpoint/core/api"
	"github.com/tonyrosario/setpoint/core/provider"
	"github.com/tonyrosario/setpoint/core/store"
)

// fakeProvider scripts Observe/Create/Delete behavior for one kind.
type fakeProvider struct {
	kind       string
	exists     bool
	ready      bool
	observeErr error
	createErr  error
	deleteErr  error
	created    int
	deleted    int
}

func (f *fakeProvider) Kinds() []string { return []string{f.kind} }

func (f *fakeProvider) Observe(ctx context.Context, res *api.Resource) (provider.Observation, error) {
	if f.observeErr != nil {
		return provider.Observation{}, f.observeErr
	}
	return provider.Observation{
		Exists:  f.exists,
		Ready:   f.ready,
		Details: map[string]string{"fake": "yes"},
	}, nil
}

func (f *fakeProvider) Create(ctx context.Context, res *api.Resource) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.created++
	f.exists = true
	f.ready = true
	return nil
}

func (f *fakeProvider) Update(ctx context.Context, res *api.Resource) error { return nil }

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

	rec.sweep(context.Background())

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
	fake := &fakeProvider{kind: "container", exists: true, ready: true}
	rec, st := newTest(fake)
	putContainer(t, st, "web")

	rec.sweep(context.Background())
	rec.sweep(context.Background())

	if fake.created != 0 {
		t.Fatalf("created = %d, want 0 (level-triggered no-op)", fake.created)
	}
	if got := status(t, st, "web"); !got.Ready {
		t.Errorf("status = %+v, want Ready", got)
	}
}

func TestExistsButNotReady(t *testing.T) {
	fake := &fakeProvider{kind: "container", exists: true, ready: false}
	rec, st := newTest(fake)
	putContainer(t, st, "web")

	rec.sweep(context.Background())

	got := status(t, st, "web")
	if got.Ready || got.Phase != api.PhaseCreating {
		t.Errorf("status = %+v, want not-ready Creating", got)
	}
}

func TestObserveErrorSetsErrorPhase(t *testing.T) {
	fake := &fakeProvider{kind: "container", observeErr: errors.New("daemon down")}
	rec, st := newTest(fake)
	putContainer(t, st, "web")

	rec.sweep(context.Background())

	got := status(t, st, "web")
	if got.Phase != api.PhaseError || got.Ready {
		t.Errorf("status = %+v, want Error", got)
	}
}

func TestCreateErrorSetsErrorPhase(t *testing.T) {
	fake := &fakeProvider{kind: "container", createErr: errors.New("no such image")}
	rec, st := newTest(fake)
	putContainer(t, st, "web")

	rec.sweep(context.Background())

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

	rec.sweep(context.Background())

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
	rec.sweep(context.Background())
	if fake.deleted != 1 {
		t.Fatalf("deleted = %d, want 1", fake.deleted)
	}
	if _, err := st.Get(context.Background(), "container", "web"); err != nil {
		t.Fatalf("resource gone too early: %v", err)
	}

	// Second pass: substrate clean → resource removed from store.
	rec.sweep(context.Background())
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

	rec.sweep(context.Background())

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

	rec.sweep(context.Background())

	if _, err := st.Get(context.Background(), "network", "net1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("marked resource of unmanaged kind should be dropped: %v", err)
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
