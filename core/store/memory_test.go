package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/tonyrosario/setpoint/core/api"
)

func testResource(name string) *api.Resource {
	return &api.Resource{
		Kind:     "container",
		Name:     name,
		Metadata: api.Metadata{Actor: "tester"},
		Spec:     json.RawMessage(`{"image":"nginx:alpine"}`),
	}
}

func TestPutGetRoundTrip(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()

	if err := m.Put(ctx, testResource("web")); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := m.Get(ctx, "container", "web")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Metadata.Actor != "tester" {
		t.Errorf("actor = %q, want tester", got.Metadata.Actor)
	}
	if got.Metadata.CreatedAt.IsZero() || got.Metadata.UpdatedAt.IsZero() {
		t.Error("timestamps not set on put")
	}
}

func TestGetNotFound(t *testing.T) {
	m := NewMemory()
	if _, err := m.Get(context.Background(), "container", "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestPutPreservesStatusAndCreatedAt(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()

	m.Put(ctx, testResource("web"))
	first, _ := m.Get(ctx, "container", "web")

	status := api.Status{Phase: api.PhaseReady, Ready: true}
	if err := m.UpdateStatus(ctx, "container", "web", status); err != nil {
		t.Fatalf("update status: %v", err)
	}

	// Re-applying Spec must not clobber Status (ADR-0004 ownership rule)
	// or CreatedAt.
	m.Put(ctx, testResource("web"))
	got, _ := m.Get(ctx, "container", "web")
	if !got.Status.Ready {
		t.Error("re-put clobbered status")
	}
	if !got.Metadata.CreatedAt.Equal(first.Metadata.CreatedAt) {
		t.Error("re-put changed CreatedAt")
	}
}

func TestUpdateStatusNotFound(t *testing.T) {
	m := NewMemory()
	err := m.UpdateStatus(context.Background(), "container", "nope", api.Status{})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestListByKind(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	m.Put(ctx, testResource("a"))
	m.Put(ctx, testResource("b"))
	other := testResource("c")
	other.Kind = "network"
	m.Put(ctx, other)

	containers, _ := m.List(ctx, "container")
	if len(containers) != 2 {
		t.Errorf("container list = %d items, want 2", len(containers))
	}
	all, _ := m.List(ctx, "")
	if len(all) != 3 {
		t.Errorf("full list = %d items, want 3", len(all))
	}
}

func TestDelete(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	m.Put(ctx, testResource("web"))

	if err := m.Delete(ctx, "container", "web"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := m.Get(ctx, "container", "web"); !errors.Is(err, ErrNotFound) {
		t.Fatal("resource still present after delete")
	}
	if err := m.Delete(ctx, "container", "web"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete err = %v, want ErrNotFound", err)
	}
}

func TestUpdateStatusCopiesObserved(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	m.Put(ctx, testResource("web"))

	observed := map[string]string{"containerId": "abc123"}
	if err := m.UpdateStatus(ctx, "container", "web", api.Status{Observed: observed}); err != nil {
		t.Fatalf("update status: %v", err)
	}

	// Mutating the caller's map after the call must not touch stored state.
	observed["containerId"] = "TAMPERED"

	got, _ := m.Get(ctx, "container", "web")
	if got.Status.Observed["containerId"] != "abc123" {
		t.Errorf("stored Observed aliased caller's map: got %q", got.Status.Observed["containerId"])
	}
}

func TestMarkForDeletion(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	m.Put(ctx, testResource("web"))

	if err := m.MarkForDeletion(ctx, "container", "web"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	got, _ := m.Get(ctx, "container", "web")
	if !got.IsMarkedForDeletion() {
		t.Fatal("resource not marked for deletion")
	}
	first := got.Metadata.DeletedAt

	// Marking again is idempotent — the timestamp must not move.
	if err := m.MarkForDeletion(ctx, "container", "web"); err != nil {
		t.Fatalf("second mark: %v", err)
	}
	got, _ = m.Get(ctx, "container", "web")
	if !got.Metadata.DeletedAt.Equal(first) {
		t.Error("second mark moved DeletedAt")
	}
}

func TestMarkForDeletionNotFound(t *testing.T) {
	m := NewMemory()
	if err := m.MarkForDeletion(context.Background(), "container", "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestCopiesAreIsolated(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	m.Put(ctx, testResource("web"))

	got, _ := m.Get(ctx, "container", "web")
	got.Spec[0] = 'X' // mutate the returned copy

	again, _ := m.Get(ctx, "container", "web")
	if again.Spec[0] == 'X' {
		t.Error("mutating a returned resource changed stored state")
	}
}
