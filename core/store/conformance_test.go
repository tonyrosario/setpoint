package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/tonyrosario/setpoint/core/api"
)

// storeFactory returns a fresh, empty Store for one subtest. Cleanup (closing
// handles, removing temp files) is the factory's responsibility via t.Cleanup.
type storeFactory func(t *testing.T) Store

// RunStoreConformance is the behavioral spec every Store implementation must
// satisfy (ADR-0004). Both Memory and SQLite run it, which is what "Store
// contract proven" means for M2 and what keeps the two from drifting.
func RunStoreConformance(t *testing.T, newStore storeFactory) {
	t.Helper()

	// get fails the test cleanly instead of letting a buggy implementation's
	// (nil, err) return panic on the next dereference — the conformance suite
	// should diagnose a broken Store, not crash on it.
	get := func(t *testing.T, m Store, name string) *api.Resource {
		t.Helper()
		got, err := m.Get(context.Background(), "container", name)
		if err != nil {
			t.Fatalf("get %q: %v", name, err)
		}
		return got
	}
	// mustPut asserts Put succeeds before the test relies on the stored state.
	mustPut := func(t *testing.T, m Store, res *api.Resource) {
		t.Helper()
		if err := m.Put(context.Background(), res); err != nil {
			t.Fatalf("put %q: %v", res.Name, err)
		}
	}

	t.Run("PutGetRoundTrip", func(t *testing.T) {
		m := newStore(t)

		mustPut(t, m, testResource("web"))
		got := get(t, m, "web")
		if got.Metadata.Actor != "tester" {
			t.Errorf("actor = %q, want tester", got.Metadata.Actor)
		}
		if got.Metadata.CreatedAt.IsZero() || got.Metadata.UpdatedAt.IsZero() {
			t.Error("timestamps not set on put")
		}
		if string(got.Spec) != `{"image":"nginx:alpine"}` {
			t.Errorf("spec = %s, want the stored document", got.Spec)
		}
	})

	t.Run("GetNotFound", func(t *testing.T) {
		m := newStore(t)
		if _, err := m.Get(context.Background(), "container", "nope"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("PutPreservesStatusAndCreatedAt", func(t *testing.T) {
		m := newStore(t)
		ctx := context.Background()

		mustPut(t, m, testResource("web"))
		first := get(t, m, "web")

		status := api.Status{Phase: api.PhaseReady, Ready: true}
		if err := m.UpdateStatus(ctx, "container", "web", status); err != nil {
			t.Fatalf("update status: %v", err)
		}

		// Re-applying Spec must not clobber Status (ADR-0004 ownership rule)
		// or CreatedAt.
		mustPut(t, m, testResource("web"))
		got := get(t, m, "web")
		if !got.Status.Ready {
			t.Error("re-put clobbered status")
		}
		if !got.Metadata.CreatedAt.Equal(first.Metadata.CreatedAt) {
			t.Error("re-put changed CreatedAt")
		}
	})

	t.Run("UpdateStatusNotFound", func(t *testing.T) {
		m := newStore(t)
		err := m.UpdateStatus(context.Background(), "container", "nope", api.Status{})
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("ListByKind", func(t *testing.T) {
		m := newStore(t)
		ctx := context.Background()
		mustPut(t, m, testResource("a"))
		mustPut(t, m, testResource("b"))
		other := testResource("c")
		other.Kind = "network"
		mustPut(t, m, other)

		containers, _ := m.List(ctx, "container")
		if len(containers) != 2 {
			t.Errorf("container list = %d items, want 2", len(containers))
		}
		all, _ := m.List(ctx, "")
		if len(all) != 3 {
			t.Errorf("full list = %d items, want 3", len(all))
		}
	})

	t.Run("ListEmpty", func(t *testing.T) {
		m := newStore(t)
		all, err := m.List(context.Background(), "")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(all) != 0 {
			t.Errorf("empty store list = %d items, want 0", len(all))
		}
	})

	t.Run("Delete", func(t *testing.T) {
		m := newStore(t)
		ctx := context.Background()
		mustPut(t, m, testResource("web"))

		if err := m.Delete(ctx, "container", "web"); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if _, err := m.Get(ctx, "container", "web"); !errors.Is(err, ErrNotFound) {
			t.Fatal("resource still present after delete")
		}
		if err := m.Delete(ctx, "container", "web"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("second delete err = %v, want ErrNotFound", err)
		}
	})

	t.Run("UpdateStatusCopiesObserved", func(t *testing.T) {
		m := newStore(t)
		ctx := context.Background()
		mustPut(t, m, testResource("web"))

		observed := map[string]string{"containerId": "abc123"}
		if err := m.UpdateStatus(ctx, "container", "web", api.Status{Observed: observed}); err != nil {
			t.Fatalf("update status: %v", err)
		}

		// Mutating the caller's map after the call must not touch stored state.
		observed["containerId"] = "TAMPERED"

		got := get(t, m, "web")
		if got.Status.Observed["containerId"] != "abc123" {
			t.Errorf("stored Observed aliased caller's map: got %q", got.Status.Observed["containerId"])
		}
	})

	t.Run("References", func(t *testing.T) {
		m := newStore(t)
		res := testResource("web")
		res.References = map[string]api.Reference{
			"db": {Kind: "container", Name: "postgres", Field: "address"},
		}
		mustPut(t, m, res)
		got := get(t, m, "web")
		ref, ok := got.References["db"]
		if !ok {
			t.Fatal("references not persisted")
		}
		if ref.Name != "postgres" || ref.Field != "address" {
			t.Errorf("reference = %+v, want the stored envelope", ref)
		}
	})

	t.Run("MarkForDeletion", func(t *testing.T) {
		m := newStore(t)
		ctx := context.Background()
		mustPut(t, m, testResource("web"))

		if err := m.MarkForDeletion(ctx, "container", "web"); err != nil {
			t.Fatalf("mark: %v", err)
		}
		got := get(t, m, "web")
		if !got.IsMarkedForDeletion() {
			t.Fatal("resource not marked for deletion")
		}
		first := got.Metadata.DeletedAt

		// Marking again is idempotent — the timestamp must not move.
		if err := m.MarkForDeletion(ctx, "container", "web"); err != nil {
			t.Fatalf("second mark: %v", err)
		}
		got = get(t, m, "web")
		if !got.Metadata.DeletedAt.Equal(first) {
			t.Error("second mark moved DeletedAt")
		}
	})

	t.Run("PutPreservesDeletionMark", func(t *testing.T) {
		m := newStore(t)
		ctx := context.Background()
		mustPut(t, m, testResource("web"))
		if err := m.MarkForDeletion(ctx, "container", "web"); err != nil {
			t.Fatalf("mark: %v", err)
		}

		// Re-applying the Spec must NOT clear the deletion mark — otherwise the
		// reconciler would resurrect a resource already being torn down.
		mustPut(t, m, testResource("web"))

		got := get(t, m, "web")
		if !got.IsMarkedForDeletion() {
			t.Error("re-applying Spec cleared the deletion mark")
		}
	})

	t.Run("MarkForDeletionNotFound", func(t *testing.T) {
		m := newStore(t)
		if err := m.MarkForDeletion(context.Background(), "container", "nope"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("CopiesAreIsolated", func(t *testing.T) {
		m := newStore(t)
		mustPut(t, m, testResource("web"))

		got := get(t, m, "web")
		got.Spec[0] = 'X' // mutate the returned copy

		again := get(t, m, "web")
		if again.Spec[0] == 'X' {
			t.Error("mutating a returned resource changed stored state")
		}
	})

	t.Run("ConcurrentAccess", func(t *testing.T) {
		m := newStore(t)
		ctx := context.Background()

		const workers = 16
		var wg sync.WaitGroup
		wg.Add(workers)
		for i := 0; i < workers; i++ {
			go func(i int) {
				defer wg.Done()
				name := fmt.Sprintf("res-%d", i)
				res := testResource(name)
				if err := m.Put(ctx, res); err != nil {
					t.Errorf("put %s: %v", name, err)
					return
				}
				status := api.Status{Phase: api.PhaseReady, Ready: true, Observed: map[string]string{"id": name}}
				if err := m.UpdateStatus(ctx, "container", name, status); err != nil {
					t.Errorf("update %s: %v", name, err)
					return
				}
				if _, err := m.Get(ctx, "container", name); err != nil {
					t.Errorf("get %s: %v", name, err)
				}
				if _, err := m.List(ctx, "container"); err != nil {
					t.Errorf("list during %s: %v", name, err)
				}
			}(i)
		}
		wg.Wait()

		all, _ := m.List(ctx, "container")
		if len(all) != workers {
			t.Errorf("after concurrent writes: %d resources, want %d", len(all), workers)
		}
	})
}

func testResource(name string) *api.Resource {
	return &api.Resource{
		Kind:     "container",
		Name:     name,
		Metadata: api.Metadata{Actor: "tester"},
		Spec:     json.RawMessage(`{"image":"nginx:alpine"}`),
	}
}
